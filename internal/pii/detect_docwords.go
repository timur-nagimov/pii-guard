package pii

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Здесь лежит общая основа словесных детекторов документа — тех, что
// опираются не на форму числа, а на слова: орган выдачи паспорта
// (detect_issuer.go), гражданство (detect_citizenship.go) и серия
// водительского удостоверения старого образца (detect_driver_letters.go).
// У всех трёх общий приём: значение ищется относительно якорного слова и
// ограничивается по словам, поэтому вспомогательные функции с приставкой dw
// собраны здесь — правка любой из них задевает сразу три детектора.

// dwMaxStemTail ограничивает хвост слова после основы. Без ограничения
// короткая основа «инди» поймала бы слово «индивидуальный».
const dwMaxStemTail = 4

// dwShortAnchorRunes — длина, до которой якорь считается коротким. Короткий
// якорь должен совпасть со словом целиком, иначе «ву» найдётся внутри слова
// «вузов»; длинный достаточно найти как начало слова, чтобы поймать словоформы.
const dwShortAnchorRunes = 3

// dwSeparatorRunes — знаки, которые могут стоять между якорем и значением.
const dwSeparatorRunes = " \t\r\n:-–—«\"'№.=>|()[]"

// dwWordGapRunes — знаки, допустимые между словами одного значения. Запятая
// сюда не входит: она разделяет разные сведения в анкете.
const dwWordGapRunes = " \t.-"

// dwIsLetter сообщает, что руна — буква кириллицы или латиницы.
func dwIsLetter(r rune) bool {
	return unicode.Is(unicode.Cyrillic, r) || unicode.Is(unicode.Latin, r)
}

// dwIsWordRune сообщает, что руна продолжает слово. Своя проверка нужна
// потому, что граница слова в регулярных выражениях Go работает только с
// латиницей и для кириллицы неприменима.
func dwIsWordRune(r rune) bool {
	return dwIsLetter(r) || unicode.IsDigit(r)
}

// dwRuneAt возвращает руну в указанном смещении или ноль за границами строки.
func dwRuneAt(s string, i int) rune {
	if i < 0 || i >= len(s) {
		return 0
	}
	r, _ := firstRune(s[i:])
	return r
}

// dwRuneBefore возвращает руну перед смещением или ноль в начале строки.
// Декодирование идёт с конца, а не проходом с начала: проход делал вызов
// квадратичным по длине текста и ронял пропускную способность на длинных
// документах в разы.
func dwRuneBefore(s string, i int) rune {
	if i <= 0 || i > len(s) {
		return 0
	}
	r, _ := decodeLastRuneBefore(s, i)
	return r
}

// dwHasWordBounds сообщает, что участок отделён от соседних слов. Справа
// достаточно отсутствия буквы: запись «в/у77АА123456» слитная, и требование
// небуквенной границы справа потеряло бы якорь.
func dwHasWordBounds(s string, start, end int) bool {
	return !dwIsWordRune(dwRuneBefore(s, start)) && !dwIsLetter(dwRuneAt(s, end))
}

// dwContainsWord ищет слово целиком, с границами с обеих сторон.
func dwContainsWord(hay, needle string) bool {
	for from := 0; from < len(hay); {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		if dwHasWordBounds(hay, i, i+len(needle)) {
			return true
		}
		from = i + 1
	}
	return false
}

// dwContainsAnchor ищет якорь: короткий — словом целиком, длинный — как
// начало слова, чтобы «водительск» поймал и «водительское», и «водительских».
func dwContainsAnchor(hay, anchor string) bool {
	if utf8.RuneCountInString(anchor) <= dwShortAnchorRunes {
		return dwContainsWord(hay, anchor)
	}
	for from := 0; from < len(hay); {
		i := strings.Index(hay[from:], anchor)
		if i < 0 {
			return false
		}
		i += from
		if !dwIsWordRune(dwRuneBefore(hay, i)) {
			return true
		}
		from = i + 1
	}
	return false
}

// dwAnyAnchor возвращает первый якорь из списка, найденный в строке.
func dwAnyAnchor(hay string, anchors []string) (string, bool) {
	for _, a := range anchors {
		if a != "" && dwContainsAnchor(hay, a) {
			return a, true
		}
	}
	return "", false
}

// dwMatchLongestAnchor подбирает самый длинный якорь, начинающийся в данном
// смещении. Длинный выигрывает у короткого, иначе «выдано» разберётся как
// «выдан» с лишней буквой в значении.
//
// Сравнение идёт по свёрнутой строке, где латинские омоглифы заменены на
// кириллические: «грaждaнcтво» с латинскими буквами совпадает с якорем
// «гражданство». Возвращается длина совпадения в байтах исходной строки.
// anchorSet — список якорей, подготовленный один раз: свёрнутые строки и
// набор первых букв. Подбор якоря вызывается на каждом слове текста, и перебор
// всего списка с повторным сворачиванием на каждом слове был виден на горячем
// пути: добавление семи якорей замедлило маскирование на шестую часть.
type anchorSet struct {
	folded []string
	first  map[rune]bool
}

// newAnchorSet готовит якоря к подбору.
func newAnchorSet(anchors []string) *anchorSet {
	set := &anchorSet{
		folded: make([]string, 0, len(anchors)),
		first:  make(map[rune]bool, len(anchors)),
	}
	for _, a := range anchors {
		na := FoldHomoglyphs(a)
		if na == "" {
			continue
		}
		set.folded = append(set.folded, na)
		r, _ := firstRune(na)
		set.first[r] = true
	}
	return set
}

// Возвращается длина совпадения В ИСХОДНОЙ строке, а не длина самого якоря:
// сворачивание омоглифов приравнивает однобайтовую латинскую букву
// двухбайтовой кириллической, поэтому длины расходятся. Пока наружу отдавалась
// длина якоря, в тексте «Выдaвший оргaн: Отделом внутренних дел» значение
// искали на два байта не там, и орган выдачи не находился совсем.
func dwMatchLongestAnchor(s string, at int, set *anchorSet) (int, bool) {
	// Первая буква слова отсекает почти все слова текста до перебора списка.
	r, _ := firstRune(s[at:])
	if !set.first[foldRune(r)] {
		return 0, false
	}
	bestLen := 0
	for _, na := range set.folded {
		matched, n := dwFoldPrefixLen(s[at:], na)
		if !matched || n <= bestLen {
			continue
		}
		if dwIsWordRune(dwRuneAt(s, at+n)) {
			continue
		}
		bestLen = n
	}
	return bestLen, bestLen > 0
}

// dwFoldPrefixLen сообщает, что свёрнутая строка начинается с префикса, и
// возвращает длину совпадения в байтах исходной строки.
func dwFoldPrefixLen(s, prefix string) (bool, int) {
	si, pi := 0, 0
	for pi < len(prefix) {
		if si >= len(s) {
			return false, 0
		}
		sr, ssize := firstRune(s[si:])
		pr, psize := firstRune(prefix[pi:])
		if foldRune(sr) != pr {
			return false, 0
		}
		si += ssize
		pi += psize
	}
	return true, si
}

// foldRune приводит латинский омоглиф к кириллице того же начертания.
func foldRune(r rune) rune {
	if c, ok := homoglyphCyr[r]; ok {
		return c
	}
	return r
}

// dwInList сообщает, что слово есть в списке целиком. Слово и элементы списка
// сворачиваются по омоглифам: латинская «о» и кириллическая «о» считаются
// одной буквой.
func dwInList(word string, list []string) bool {
	norm := FoldHomoglyphs(word)
	for _, w := range list {
		if norm == FoldHomoglyphs(w) {
			return true
		}
	}
	return false
}

// dwHasStem сообщает, что слово начинается с одной из основ и хвост после
// основы короткий. Так одна основа покрывает все падежные формы названия.
// Слово и основа сворачиваются по омоглифам, поэтому «Pocсия» с латинской
// «о» совпадает с основой «росси», а латинская основа «russia» — со словом
// «russiа» с кириллической «а».
//
// Если точного совпадения нет, допускается одна опечатка в основе: «Азерайджан»
// совпадает с основой «азербайджан». Опечатка принимается только у длинных
// слов, чтобы не ловить случайные совпадения.
func dwHasStem(word string, stems []string) bool {
	norm := FoldHomoglyphs(word)
	wl := utf8.RuneCountInString(norm)
	for _, st := range stems {
		nst := FoldHomoglyphs(st)
		if !strings.HasPrefix(norm, nst) {
			continue
		}
		if wl-utf8.RuneCountInString(nst) <= dwMaxStemTail {
			return true
		}
	}
	// Опечатка в основе: одна вставка, удаление или замена руны. Только для
	// достаточно длинных слов и основ, чтобы не ловить случайные совпадения.
	if wl < 6 {
		return false
	}
	for _, st := range stems {
		nst := FoldHomoglyphs(st)
		if utf8.RuneCountInString(nst) < 6 {
			continue
		}
		if nearlyEqual(norm, nst) {
			return true
		}
	}
	return false
}

// dwSkipSeparators пропускает знаки между якорем и значением. Один перевод
// строки допускается: в анкетах подпись поля и значение часто стоят на
// разных строках.
func dwSkipSeparators(s string, from int) (int, bool) {
	const maxRunes = 8
	i, newlines := from, 0
	for n := 0; n < maxRunes && i < len(s); n++ {
		r, size := firstRune(s[i:])
		if size == 0 {
			break
		}
		if r == '\n' {
			newlines++
			if newlines > 1 {
				return 0, false
			}
			i += size
			continue
		}
		if !strings.ContainsRune(dwSeparatorRunes, r) {
			return i, true
		}
		i += size
	}
	return 0, false
}

// dwNextWord находит следующее слово из букв, разрешая между словами только
// короткий промежуток из пробелов, точек и дефисов.
func dwNextWord(s string, from, limit int) (int, int, bool) {
	i, gaps := from, 0
	for i < limit {
		r, size := firstRune(s[i:])
		if size == 0 || dwIsLetter(r) {
			break
		}
		if gaps >= 2 || !strings.ContainsRune(dwWordGapRunes, r) {
			return 0, 0, false
		}
		gaps++
		i += size
	}
	start := i
	for i < limit {
		r, size := firstRune(s[i:])
		if size == 0 || !dwIsLetter(r) {
			break
		}
		i += size
	}
	if start == i {
		return 0, 0, false
	}
	return start, i, true
}

// dwPrevWord находит слово слева от смещения в тех же правилах промежутка.
func dwPrevWord(s string, lo, from int) (int, int, bool) {
	i, gaps := from, 0
	for i > lo {
		r, size := decodeLastRuneBefore(s, i)
		if size == 0 || dwIsLetter(r) {
			break
		}
		if gaps >= 2 || !strings.ContainsRune(dwWordGapRunes, r) {
			return 0, 0, false
		}
		gaps++
		i -= size
	}
	end := i
	for i > lo {
		r, size := decodeLastRuneBefore(s, i)
		if size == 0 || !dwIsLetter(r) {
			break
		}
		i -= size
	}
	if i == end {
		return 0, 0, false
	}
	return i, end, true
}

// dwWordBefore возвращает слово, стоящее вплотную слева от смещения.
func dwWordBefore(s string, i int) string {
	end := i
	for i > 0 {
		r, size := decodeLastRuneBefore(s, i)
		if size == 0 || !dwIsLetter(r) {
			break
		}
		i -= size
	}
	return s[i:end]
}

// dwOverlaps сообщает, что диапазон пересекается с уже найденным фрагментом.
func dwOverlaps(spans []Span, start, end int) bool {
	for _, s := range spans {
		if s.Start < end && start < s.End {
			return true
		}
	}
	return false
}

// dwFoldHomoglyph приводит латинские буквы-двойники к кириллице. В документах
// серию часто набирают латиницей: «77 AA 123456» неотличимо на вид от «77 АА».
func dwFoldHomoglyph(r rune) rune {
	switch r {
	case 'a':
		return 'а'
	case 'b':
		return 'в'
	case 'c':
		return 'с'
	case 'e':
		return 'е'
	case 'h':
		return 'н'
	case 'k':
		return 'к'
	case 'm':
		return 'м'
	case 'o':
		return 'о'
	case 'p':
		return 'р'
	case 't':
		return 'т'
	case 'x':
		return 'х'
	case 'y':
		return 'у'
	default:
		return r
	}
}
