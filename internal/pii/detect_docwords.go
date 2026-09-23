package pii

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Здесь собраны детекторы, которые опираются не на форму числа, а на
// слова документа: орган выдачи паспорта, гражданство и серия водительского
// удостоверения старого образца. У всех трёх общий приём: значение ищется
// относительно якорного слова и ограничивается по словам, поэтому для них
// вынесены общие вспомогательные функции с приставкой dw.

// dwMaxStemTail ограничивает хвост слова после основы. Без ограничения
// короткая основа «инди» поймала бы слово «индивидуальный».
const dwMaxStemTail = 4

// dwShortAnchorRunes — длина, до которой якорь считается коротким. Короткий
// якорь должен совпасть со словом целиком, иначе «ву» найдётся внутри слова
// «вузов»; длинный достаточно найти как начало слова, чтобы поймать словоформы.
const dwShortAnchorRunes = 3

// dwSeparatorRunes — знаки, которые могут стоять между якорем и значением.
// Неразрывный пробел из выгрузок учитывается наравне с обычным.
const dwSeparatorRunes = " \t\r\n\u00a0:-–—«\"'№.=>|()[]"

// dwWordGapRunes — знаки, допустимые между словами одного значения. Запятая
// сюда не входит: она разделяет разные сведения в анкете. Неразрывный пробел
// из выгрузок не рвёт значение.
const dwWordGapRunes = " \t\u00a0.-"

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

// dwNextWordValue находит следующее слово значения, разрешая между словами
// перевод строки. В анкетах значение гражданства часто переносится по строкам:
// «Республика\nБеларусь» или слово разбито внутри («Арм\nения»). Обычный
// dwNextWord перевод строки не пропускает, поэтому для значения гражданства
// нужен отдельный проход.
func dwNextWordValue(s string, from, limit int) (int, int, bool) {
	i, gaps := from, 0
	for i < limit {
		r, size := firstRune(s[i:])
		if size == 0 || dwIsLetter(r) {
			break
		}
		if gaps >= 2 || !strings.ContainsRune(dwWordGapRunes, r) && r != '\n' {
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

// --- орган, выдавший документ ---

// issuerAnchors — слова, после которых в документе стоит название органа.
// Сам якорь в маскируемый фрагмент не входит.
var issuerAnchors = []string{
	"кем выдан", "орган выдачи", "выдавший орган", "выдан", "выдано", "выдана",
	"выданный", "выданная", "выданного", "выданным", "выдавший", "выдавшим",
	"выдавшего", "issued by",
	// Косвенные падежи и канцелярские обороты: в анкетах пишут
	// «наименование органа выдачи», «подразделение выдачи», и без этих
	// форм якорь не находился вовсе.
	"наименование органа выдачи", "органа выдачи", "подразделение выдачи",
	"подразделением выдачи", "орган, выдавший", "кем выдано", "кем выдана",
}

// issuerAgencyWords — ведомственные сокращения. Без такого признака внутри
// фрагмента «выдан кредит» и «выдана справка» органом выдачи не считаются.
var issuerAgencyWords = []string{
	"мвд", "гумвд", "умвд", "омвд", "оумвд", "оуфмс", "уфмс", "фмс", "овд",
	"ровд", "оувд", "увд", "гувд", "овм", "оувм", "тп", "мрэо", "мро",
}

// issuerAgencyPhrases — развёрнутые названия органов. Хранятся основами,
// чтобы одинаково ловить «отдел внутренних дел» и «отделом внутренних дел».
var issuerAgencyPhrases = []string{
	"внутренних дел", "вопросам миграции", "паспортный стол", "паспортным столом",
	"паспортного стола", "отделение полиции", "отделением полиции",
	"отдел полиции", "отделом полиции", "миграционной службы", "миграционная служба",
	"территориальный пункт", "территориальным пунктом", "миграционным пунктом",
}

// issuerPrefixWords — служебные слова перед сокращением, которые относятся к
// названию органа: «ГУ МВД», «Отделом УФМС».
var issuerPrefixWords = []string{
	"гу", "ту", "мо", "мп", "главное", "главным", "отдел", "отделом", "отделение",
	"отделением", "отд", "управление", "управлением", "территориальный",
	"территориальным", "пункт", "пунктом",
}

// issuerStopWords — слова, на которых сведения об органе заканчиваются.
var issuerStopWords = []string{
	"код подразделения", "код подр", "к/п", "дата выдачи", "зарегистрирован",
	"проживающ", "проживает", "место рождения", "место жительства", "адрес",
	"снилс", "телефон", "номер паспорта", "серия",
}

// issuerAbbrev — сокращения длиной от четырёх букв, после которых точка не
// заканчивает предложение: «по Респ. Татарстан».
var issuerAbbrev = []string{"респ", "терр", "адм", "упр", "авт"}

// issuerGeoWords — слова, с которых начинается уточнение места внутри
// названия органа. После них запятая не заканчивает название: запись
// «по г. Тюмень, район Заводской» — один орган, а не два сведения.
var issuerGeoWords = []string{
	"район", "района", "районе", "районам", "г", "гор", "город", "города",
	"обл", "область", "области", "край", "крае", "края", "респ", "республика",
	"республике", "округ", "округе",
}

// issuerMaxRunes ограничивает длину названия органа. Без ограничения ошибка
// поиска стоп-признака утащила бы в маску весь абзац.
const issuerMaxRunes = 120

// issuerDetector находит орган, выдавший документ. Ищет двумя путями: после
// слова «выдан» и по самому ведомственному сокращению в паспортном контексте.
type issuerDetector struct{}

// NewIssuerDetector создаёт детектор органа выдачи.
func NewIssuerDetector() Detector { return issuerDetector{} }

// Types перечисляет типы, которые находит детектор.
func (issuerDetector) Types() []Type { return []Type{TypeIssuer} }

// Detect ищет название органа выдачи.
func (issuerDetector) Detect(d *Doc) []Span {
	out := issuerByAnchor(d)
	return append(out, issuerByAgency(d, out)...)
}

// issuerByAnchor собирает названия органа, стоящие после слова «выдан».
func issuerByAnchor(d *Doc) []Span {
	var out []Span
	for _, tok := range d.Tokens {
		if tok.Kind != KindCyr && tok.Kind != KindLat {
			continue
		}
		if dwIsWordRune(dwRuneBefore(d.Lower, tok.Start)) {
			continue
		}
		anchorLen, ok := dwMatchLongestAnchor(d.Lower, tok.Start, issuerAnchorSet)
		if !ok {
			continue
		}
		start, end, ok := issuerValueAfter(d, tok.Start+anchorLen)
		if !ok || dwOverlaps(out, start, end) {
			continue
		}
		out = append(out, Span{Start: start, End: end, Type: TypeIssuer, Conf: ConfHigh, Reason: "issuer:anchor"})
	}
	return out
}

// issuerValueAfter вычисляет границы названия органа, начинающегося сразу за
// якорем, и проверяет, что внутри есть ведомственный признак.
func issuerValueAfter(d *Doc, from int) (int, int, bool) {
	start, ok := dwSkipSeparators(d.Lower, from)
	if !ok {
		return 0, 0, false
	}
	_, lineEnd := d.LineBounds(start)
	start, end := NormalizeSpan(d.Text, start, issuerValueEnd(d, start, lineEnd))
	if start >= end || !issuerHasAgency(d.Lower[start:end]) {
		return 0, 0, false
	}
	return start, end, true
}

// issuerByAgency находит орган, названный без слова «выдан»: в паспортном
// контексте строка «ОУФМС России по г. Москве» сама по себе является органом.
func issuerByAgency(d *Doc, found []Span) []Span {
	var out []Span
	for _, tok := range d.Tokens {
		if tok.Kind != KindCyr || !dwInList(d.Lower[tok.Start:tok.End], issuerAgencyWords) {
			continue
		}
		start := issuerExtendLeft(d, tok.Start)
		_, lineEnd := d.LineBounds(start)
		start, end := NormalizeSpan(d.Text, start, issuerValueEnd(d, start, lineEnd))
		if start >= end || dwOverlaps(found, start, end) || dwOverlaps(out, start, end) {
			continue
		}
		if _, ok := d.FindAnchor(start, end, anchorsPassport, anchorWindow, anchorWindow/2); !ok {
			continue
		}
		out = append(out, Span{Start: start, End: end, Type: TypeIssuer, Conf: ConfAnchored, Reason: "issuer:agency_context"})
	}
	return out
}

// issuerExtendLeft добавляет к фрагменту служебные слова слева: в записи
// «ГУ МВД России» сокращение начинается не с найденного слова.
func issuerExtendLeft(d *Doc, start int) int {
	lineLo, _ := d.LineBounds(start)
	pos := start
	for n := 0; n < 2; n++ {
		ws, we, ok := dwPrevWord(d.Lower, lineLo, pos)
		if !ok || !dwInList(d.Lower[ws:we], issuerPrefixWords) {
			break
		}
		pos = ws
	}
	return pos
}

// issuerValueEnd ищет конец названия органа: стоп-слово, дату, запятую,
// точку с запятой или конец предложения. Дальше границы строки не уходит.
func issuerValueEnd(d *Doc, start, lineEnd int) int {
	if _, hi := d.WindowRunes(start, start, 0, issuerMaxRunes); hi < lineEnd {
		lineEnd = hi
	}
	for i := start; i < lineEnd; {
		if issuerStopsAt(d.Lower, i, lineEnd) {
			return i
		}
		_, size := firstRune(d.Lower[i:])
		if size == 0 {
			break
		}
		i += size
	}
	return lineEnd
}

// issuerStopsAt сообщает, что в данном смещении начинается стоп-признак:
// знак препинания или слово, после которого идут уже другие сведения.
func issuerStopsAt(low string, i, lineEnd int) bool {
	if stop, decided := issuerPunctStops(low, i, lineEnd); decided {
		return stop
	}
	if dwIsWordRune(dwRuneBefore(low, i)) {
		return false
	}
	for _, w := range issuerStopWords {
		if strings.HasPrefix(low[i:], w) {
			return true
		}
	}
	return false
}

// issuerPunctStops разбирает знак в текущем смещении. Второе значение
// сообщает, что знак разобран и проверять стоп-слова уже не нужно.
//
// Скобка и тире с пробелом слева заканчивают название: за ними идёт
// пояснение вида «(заявка принята)» или продолжение предложения.
func issuerPunctStops(low string, i, lineEnd int) (bool, bool) {
	r, _ := firstRune(low[i:])
	switch r {
	case ';', '(', ')':
		return true, true
	case ',':
		return !issuerGeoContinues(low, i, lineEnd), true
	case '.':
		return issuerSentenceEnd(low, i), true
	case '-', '–', '—':
		return unicode.IsSpace(dwRuneBefore(low, i)), true
	}
	if r >= '0' && r <= '9' {
		return issuerDigitStops(low, i, lineEnd), true
	}
	return false, false
}

// issuerGeoContinues сообщает, что после запятой идёт уточнение места, а не
// новые сведения. Без этого название обрывалось на запятой и район города в
// маску не попадал.
//
// Проверяются два слова: уточнение бывает как с признаком впереди («район
// Заводской»), так и с признаком позади («Московская область»).
func issuerGeoContinues(low string, comma, lineEnd int) bool {
	pos := comma + 1
	for n := 0; n < 2; n++ {
		ws, we, ok := dwNextWord(low, pos, lineEnd)
		if !ok {
			return false
		}
		if issuerIsGeoWord(low, ws, we) {
			return true
		}
		pos = we
	}
	return false
}

// issuerIsGeoWord сообщает, что слово обозначает единицу административного
// деления.
func issuerIsGeoWord(low string, ws, we int) bool {
	word := low[ws:we]
	// Сокращение «р-н» распадается при разборе на слово из одной буквы,
	// поэтому хвост проверяется отдельно.
	if word == "р" {
		return strings.HasPrefix(low[we:], "-н")
	}
	return dwInList(word, issuerGeoWords)
}

// issuerDigitStops сообщает, что число похоже на дату, год или код, а не на
// номер отделения. Номер вида «ОП № 3» остаётся частью названия.
func issuerDigitStops(low string, i, lineEnd int) bool {
	j := i
	for j < lineEnd && low[j] >= '0' && low[j] <= '9' {
		j++
	}
	if j-i >= 4 {
		return true
	}
	return j+1 < lineEnd && strings.ContainsRune("./-", rune(low[j])) &&
		low[j+1] >= '0' && low[j+1] <= '9'
}

// issuerSentenceEnd отличает конец предложения от точки в сокращении: после
// «г.» и «Респ.» название продолжается.
func issuerSentenceEnd(low string, i int) bool {
	if next := dwRuneAt(low, i+1); next != 0 && !unicode.IsSpace(next) {
		return false
	}
	word := dwWordBefore(low, i)
	if utf8.RuneCountInString(word) < 4 {
		return false
	}
	return !dwInList(word, issuerAbbrev)
}

// issuerHasAgency проверяет ведомственный признак внутри фрагмента. Вариант
// без точек нужен для записей вида «О.У.Ф.М.С.».
func issuerHasAgency(frag string) bool {
	// Ведомственный признак ищется по свёрнутому тексту: в распознанных
	// сканах пишут «внутpенних дел» с латинской «p», и признак не находился,
	// а без него весь фрагмент отбрасывался. Смещения здесь не нужны — ответ
	// булев, поэтому обычное сворачивание годится.
	if hasASCIIHomoglyph(frag) {
		frag = FoldHomoglyphs(frag)
	}
	for _, w := range issuerAgencyWords {
		if dwContainsWord(frag, w) {
			return true
		}
	}
	compact := strings.ReplaceAll(frag, ".", "")
	for _, w := range issuerAgencyWords {
		if dwContainsWord(compact, w) {
			return true
		}
	}
	_, ok := ContainsAnyLower(frag, issuerAgencyPhrases)
	return ok
}

// --- гражданство ---

// citizenshipAnchors — слова, после которых стоит гражданство. Без якоря
// название страны гражданством не считается: это может быть место поездки.
var citizenshipAnchors = []string{
	"гражданство", "гражданства", "гражданстве", "гражданин", "гражданина",
	"гражданину", "гражданином", "гражданка", "гражданки", "гражданке",
	"гражданкой", "подданство", "подданства", "подданный", "подданная",
	"гражданская принадлежность", "гражданской принадлежности",
	"гражданскую принадлежность", "гражданской принадлежностью",
	"citizenship", "nationality",
}

// citizenshipQualifiers — слова, которые входят в значение гражданства, но
// сами гражданством не являются: «Республика», «Федерация», «Штаты». Сюда же
// отнесены «гражданин» и «подданный»: в записи «гражданство: гражданин
// России» эталонное значение начинается именно с этого слова.
var citizenshipQualifiers = []string{
	"республика", "республики", "республику", "республике", "респ",
	"федерация", "федерации", "федерацию", "народная", "народной",
	"демократическая", "исламская", "королевство", "королевства",
	"соединенные", "соединённые", "штаты", "южная", "северная", "новая",
	"гражданин", "гражданина", "гражданка", "гражданки", "подданный",
	"подданная", "подданного", "citizen", "national", "и",
	"republic", "federation", "united", "states",
}

// citizenshipFillerWords — служебные слова между якорем и значением:
// «гражданство по паспорту», «гражданство клиента». В маску они не попадают,
// но и значение за ними терять нельзя.
var citizenshipFillerWords = []string{
	"по", "паспорту", "паспорта", "документу", "документам", "клиента",
	"клиенту", "заявителя", "заявителю", "страна", "страны", "of",
	"владельца", "владельцу", "счёта", "счета", "счет",
}

// citizenshipMaxFillers — сколько служебных слов допускается между якорем и
// значением. Двух хватает на запись «гражданство по паспорту».
const citizenshipMaxFillers = 2

// citizenshipStems — основы названий стран и прилагательных-демонимов.
// Основа покрывает все падежные формы, а ограничение хвоста не даёт ей
// зацепить постороннее слово.
var citizenshipStems = []string{
	"росси", "российск", "россиян", "рф", "казахстан", "казахстанск", "казахск",
	"казах", "беларус", "белорус", "узбекистан", "узбекск", "узбек", "армени",
	"армянск", "армян", "киргиз", "кыргыз", "кыргызстан", "таджикистан",
	"таджикск", "таджик", "украин", "азербайджан", "грузи", "грузинск",
	"молдав", "молдов", "туркмен", "туркменистан", "латви", "латышск", "литв", "литовск",
	"эстон", "израил", "израильск", "герман", "немецк", "турци", "турецк",
	"кита", "китайск", "вьетнам", "сша", "америк", "американск", "инди",
	"индийск", "серб", "польш", "польск", "поляк", "финлянд", "финск", "итал",
	"франци", "французск", "испани", "испанск", "великобритани", "британск",
	"англи", "английск", "абхаз", "осети", "монгол", "афган", "сири", "иран",
	"ирак", "египет", "египетск", "канад", "бразил", "япон", "коре",
	"швейцар", "швеци", "шведск", "норвеги", "нидерланд", "бельги", "австри",
	"австрал", "чехи", "чешск", "словак", "словени", "болгари", "болгарск",
	"румыни", "венгри", "греци", "греческ", "португал", "иностранн", "двойн",
	"russia", "russian", "kazakh", "belarus", "ukrain", "uzbek", "armenia",
	"georgia", "germany", "turkey", "china", "india", "usa", "ru", "rf",
}

// citizenshipMaxWords — сколько слов может занимать значение гражданства:
// «гражданин Соединённых Штатов Америки» — самый длинный ожидаемый случай.
const citizenshipMaxWords = 4

// citizenshipMaxRunes ограничивает длину значения гражданства.
const citizenshipMaxRunes = 60

// citizenshipDetector находит гражданство, указанное после якорного слова.
type citizenshipDetector struct{}

// NewCitizenshipDetector создаёт детектор гражданства.
func NewCitizenshipDetector() Detector { return citizenshipDetector{} }

// Types перечисляет типы, которые находит детектор.
func (citizenshipDetector) Types() []Type { return []Type{TypeCitizenship} }

// Detect ищет значение гражданства после якоря. Маскируется только значение,
// само слово «гражданство» остаётся в тексте. Значение может стоять и перед
// якорем через тире: «Россия — гражданство клиента».
func (citizenshipDetector) Detect(d *Doc) []Span {
	var out []Span
	for _, tok := range d.Tokens {
		if tok.Kind != KindCyr && tok.Kind != KindLat {
			continue
		}
		if dwIsWordRune(dwRuneBefore(d.Lower, tok.Start)) {
			continue
		}
		anchorLen, ok := dwMatchLongestAnchor(d.Lower, tok.Start, citizenshipAnchorSet)
		if !ok {
			continue
		}
		start, end, ok := citizenshipValue(d, tok.Start+anchorLen)
		if ok && !dwOverlaps(out, start, end) {
			out = append(out, Span{Start: start, End: end, Type: TypeCitizenship, Conf: ConfHigh, Reason: "citizenship:anchor"})
		}
		start, end, ok = citizenshipValueBefore(d, tok.Start)
		if ok && !dwOverlaps(out, start, end) {
			out = append(out, Span{Start: start, End: end, Type: TypeCitizenship, Conf: ConfHigh, Reason: "citizenship:anchor-before"})
		}
	}
	return out
}

// citizenshipValueBefore вычисляет границы названия страны или демонима перед
// якорем через тире: «Россия — гражданство клиента». Значение принимается
// только если перед ним стоит тире, иначе «Россия» в новостях без якоря
// гражданства стала бы гражданством.
func citizenshipValueBefore(d *Doc, from int) (int, int, bool) {
	low := d.Lower
	pos, ok := citizenshipDashBefore(low, from)
	if !ok {
		return 0, 0, false
	}
	// Набираем слова значения назад.
	lineLo, _ := d.LineBounds(from)
	start, end, matched := pos, pos, false
	for n := 0; n < citizenshipMaxWords; n++ {
		ws, we, ok := dwPrevWord(low, lineLo, pos)
		if !ok {
			break
		}
		isStem, ok := citizenshipWordCandidate(low[ws:we])
		if !ok {
			break
		}
		matched = matched || isStem
		if end == pos {
			end = we
		}
		start, pos = ws, ws
	}
	if !matched {
		return 0, 0, false
	}
	start, end = NormalizeSpan(d.Text, start, end)
	if start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// citizenshipDashBefore пропускает пробелы и тире между якорем и значением и
// возвращает позицию сразу после тире. Если перед якорем тире нет, значение
// не принимается: «Россия» в новостях без якоря гражданства не гражданство.
func citizenshipDashBefore(low string, from int) (int, bool) {
	pos := from
	for pos > 0 {
		r, size := decodeLastRuneBefore(low, pos)
		if size == 0 {
			return 0, false
		}
		if r == ' ' || r == '\t' || r == '\u00a0' {
			pos -= size
			continue
		}
		if r == '-' || r == '–' || r == '—' {
			return pos - size, true
		}
		return 0, false
	}
	return 0, false
}

// citizenshipWordCandidate сообщает, является ли слово названием страны или
// демонимом (по основе) либо служебным словом рядом с ним. Возвращает признак
// основы и признак того, что слово вообще подходит для значения гражданства.
func citizenshipWordCandidate(word string) (isStem, ok bool) {
	isStem = dwHasStem(word, citizenshipStems)
	if !isStem && !dwInList(word, citizenshipQualifiers) {
		return false, false
	}
	return isStem, true
}

// citizenshipValue вычисляет границы названия страны или демонима после якоря.
func citizenshipValue(d *Doc, from int) (int, int, bool) {
	start, ok := dwSkipSeparators(d.Lower, from)
	if !ok {
		return 0, 0, false
	}
	// Значение гражданства в анкетах переносится по строкам, поэтому предел
	// берётся по окну, а не по концу строки.
	_, lineEnd := d.WindowRunes(start, start, 0, citizenshipMaxRunes)
	start = citizenshipSkipFillers(d.Lower, start, lineEnd)
	end, ok := citizenshipWordsEnd(d.Lower, start, lineEnd)
	if !ok {
		return 0, 0, false
	}
	start, end = NormalizeSpan(d.Text, start, end)
	if start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// citizenshipSkipFillers пропускает служебные слова между якорем и значением.
// Без этого запись «гражданство по паспорту: Россия» терялась целиком: сразу
// за якорем стоит предлог, а не название страны.
func citizenshipSkipFillers(low string, start, lineEnd int) int {
	pos := start
	for n := 0; n < citizenshipMaxFillers; n++ {
		ws, we, ok := dwNextWordValue(low, pos, lineEnd)
		if !ok || ws != pos || !dwInList(low[ws:we], citizenshipFillerWords) {
			return pos
		}
		next, ok := dwSkipSeparators(low, we)
		if !ok {
			return pos
		}
		pos = next
	}
	return pos
}

// citizenshipWordsEnd набирает слова значения, пока они остаются частью
// названия страны. Хотя бы одно слово обязано быть названием: из одного
// уточняющего слова «Республика» гражданство не следует.
func citizenshipWordsEnd(low string, start, limit int) (int, bool) {
	pos, end, matched := start, 0, false
	for n := 0; n < citizenshipMaxWords; n++ {
		ws, we, ok := dwNextWordValue(low, pos, limit)
		if !ok {
			break
		}
		word := low[ws:we]
		isStem := dwHasStem(word, citizenshipStems)
		if !isStem && !dwInList(word, citizenshipQualifiers) {
			// Слово может быть разбито переводом строки: «Арм\nения».
			// Склеиваем его со следующим словом и проверяем основу.
			ws2, we2, ok2 := dwNextWordValue(low, we, limit)
			if ok2 && dwHasStem(low[ws:we]+low[ws2:we2], citizenshipStems) {
				matched = true
				end, pos = we2, we2
				continue
			}
			break
		}
		matched = matched || isStem
		end, pos = we, we
	}
	if !matched {
		return 0, false
	}
	return end, true
}

// --- водительское удостоверение старого образца ---

// driverLettersAnchors — якоря водительского удостоверения. Короткие «в/у» и
// «ву» ищутся словом целиком, длинные — как начало слова.
var driverLettersAnchors = []string{
	"в/у", "ву", "водительск", "удостоверение водителя", "удостоверения водителя",
	"права", "правами", "driver", "driving", "license", "licence",
}

// driverSepRunes — знаки, допустимые между группами номера удостоверения.
const driverSepRunes = " \t-–—.№/"

// driverStandardLetters — буквы, которые применяются в сериях российских
// документов. Совпадение с этим набором позволяет маскировать номер даже без
// якоря рядом.
const driverStandardLetters = "авекмнорстух"

// driverLettersDetector находит водительское удостоверение старого образца
// вида «77 АА 123456». Форма из десяти цифр разбирается детектором форматных
// типов, здесь она намеренно не повторяется.
type driverLettersDetector struct{}

// NewDriverLicenseLettersDetector создаёт детектор удостоверения с буквенной
// серией.
func NewDriverLicenseLettersDetector() Detector { return driverLettersDetector{} }

// Types перечисляет типы, которые находит детектор.
func (driverLettersDetector) Types() []Type { return []Type{TypeDriverLicense} }

// Detect ищет номера вида «код региона, серия из двух букв, шесть цифр».
func (driverLettersDetector) Detect(d *Doc) []Span {
	var out []Span
	for i := range d.Tokens {
		tok := d.Tokens[i]
		if tok.Kind != KindDigit || tok.End-tok.Start != 2 {
			continue
		}
		s, ok := driverLettersSpan(d, i)
		if !ok || dwOverlaps(out, s.Start, s.End) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// driverLettersSpan проверяет форму номера и назначает уверенность: с якорем
// рядом — высокую, без якоря — только для стандартных букв серии.
func driverLettersSpan(d *Doc, i int) (Span, bool) {
	last, series, ok := driverLettersParts(d, i)
	if !ok {
		return Span{}, false
	}
	start, end := NormalizeSpan(d.Text, d.Tokens[i].Start, d.Tokens[last].End)
	if start >= end {
		return Span{}, false
	}
	window := d.LowerWindow(start, end, anchorWindow, anchorWindow/2)
	if _, found := dwAnyAnchor(window, driverLettersAnchors); found {
		return Span{Start: start, End: end, Type: TypeDriverLicense, Conf: ConfHigh, Reason: "driver:letters_anchor"}, true
	}
	if !driverStandardSeries(series) {
		return Span{}, false
	}
	return Span{Start: start, End: end, Type: TypeDriverLicense, Conf: ConfAnchored, Reason: "driver:letters_shape"}, true
}

// driverLettersParts разбирает три части номера и возвращает индекс токена с
// номером и серию, приведённую к кириллице.
func driverLettersParts(d *Doc, i int) (int, string, bool) {
	toks := d.Tokens
	li, ok := driverNextPart(d, i)
	if !ok {
		return 0, "", false
	}
	last, series, ok := driverSeriesPart(d, li)
	if !ok {
		return 0, "", false
	}
	ni, ok := driverNextPart(d, last)
	if !ok || toks[ni].Kind != KindDigit || toks[ni].End-toks[ni].Start != 6 {
		return 0, "", false
	}
	// Буквы, приклеенные к номеру справа, означают, что это часть другого
	// обозначения, а не номер удостоверения.
	if ni+1 < len(toks) && toks[ni+1].Start == toks[ni].End && driverIsLetterToken(toks[ni+1]) {
		return 0, "", false
	}
	return ni, series, true
}

// driverSeriesPart собирает серию из букв. Серия может распасться на два
// токена, если её набрали вперемешку кириллицей и латиницей: «Aа».
func driverSeriesPart(d *Doc, li int) (int, string, bool) {
	toks := d.Tokens
	if !driverIsLetterToken(toks[li]) {
		return 0, "", false
	}
	last := li
	raw := d.Lower[toks[li].Start:toks[li].End]
	if utf8.RuneCountInString(raw) == 1 && li+1 < len(toks) &&
		toks[li+1].Start == toks[li].End && driverIsLetterToken(toks[li+1]) {
		last = li + 1
		raw += d.Lower[toks[last].Start:toks[last].End]
	}
	series, ok := driverSeries(raw)
	if !ok {
		return 0, "", false
	}
	return last, series, true
}

// driverIsLetterToken сообщает, что токен состоит из букв.
func driverIsLetterToken(t Token) bool {
	return t.Kind == KindCyr || t.Kind == KindLat
}

// driverNextPart находит следующую значащую часть номера, проверяя, что
// разделитель короткий, допустимый и не содержит перевода строки.
func driverNextPart(d *Doc, i int) (int, bool) {
	toks := d.Tokens
	j := i + 1
	for j < len(toks) && (toks[j].Kind == KindSpace || toks[j].Kind == KindPunct) {
		j++
	}
	if j >= len(toks) {
		return 0, false
	}
	sep := d.Text[toks[i].End:toks[j].Start]
	if utf8.RuneCountInString(sep) > 3 {
		return 0, false
	}
	for _, r := range sep {
		if !strings.ContainsRune(driverSepRunes, r) {
			return 0, false
		}
	}
	return j, true
}

// driverSeries приводит две буквы серии к кириллице, учитывая латинские
// двойники, и отвергает всё, что не является парой кириллических букв.
func driverSeries(raw string) (string, bool) {
	var b strings.Builder
	count := 0
	for _, r := range raw {
		f := dwFoldHomoglyph(r)
		if !unicode.Is(unicode.Cyrillic, f) {
			return "", false
		}
		b.WriteRune(f)
		count++
	}
	if count != 2 {
		return "", false
	}
	return b.String(), true
}

// driverStandardSeries сообщает, что обе буквы серии взяты из набора,
// применяемого в российских документах.
func driverStandardSeries(series string) bool {
	for _, r := range series {
		if !strings.ContainsRune(driverStandardLetters, r) {
			return false
		}
	}
	return true
}

// Якоря готовятся один раз при запуске: подбор идёт на каждом слове каждого
// текста, и повторная подготовка там недопустима.
var (
	issuerAnchorSet      = newAnchorSet(issuerAnchors)
	citizenshipAnchorSet = newAnchorSet(citizenshipAnchors)
)
