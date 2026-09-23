package pii

import (
	"strings"
	"unicode/utf8"
)

// NumRun — числовая последовательность: одна или несколько групп цифр,
// соединённых допустимыми разделителями. Это основной кандидат для всех
// форматных типов: паспорта, телефона, карты, ИНН, даты, кода подразделения.
//
// Детекторы классифицируют кандидата по длинам групп и по якорю рядом, а не
// прогоняют регулярное выражение по всему тексту.
type NumRun struct {
	// Start и End — байтовые границы всей последовательности вместе с
	// внутренними разделителями и ведущим знаком «плюс».
	Start int
	End   int
	// Digits — только цифры последовательности.
	Digits string
	// Groups — длины групп цифр в порядке следования.
	Groups []int
	// Seps — разделители, встреченные между группами, без повторов.
	Seps string
	// HasPlus сообщает, что последовательность начинается со знака «плюс».
	HasPlus bool
}

// GroupsPattern возвращает длины групп в виде строки вида "4-6" — удобно для
// сравнения формы в детекторах и в табличных тестах.
func (n NumRun) GroupsPattern() string {
	var b strings.Builder
	for i, g := range n.Groups {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(itoa(g))
	}
	return b.String()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// numSeparators — символы, которые могут стоять между группами цифр внутри
// одной сущности. Запятая и точка с запятой сюда не входят: они разделяют
// разные сущности в перечислении.
const numSeparators = " \t  -–—./()\\№#"

func isNumSeparator(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune(numSeparators, r) {
			return false
		}
	}
	return s != ""
}

// DigitRuns находит все числовые последовательности документа за один проход
// по токенам. Последовательность обрывается на любом символе, который не
// является цифрой или допустимым разделителем.
func DigitRuns(d *Doc) []NumRun {
	runs := make([]NumRun, 0, 8)
	toks := d.Tokens
	for i := 0; i < len(toks); i++ {
		if toks[i].Kind != KindDigit {
			continue
		}
		run := NumRun{Start: toks[i].Start, End: toks[i].End}
		run.Digits = d.Text[toks[i].Start:toks[i].End]
		run.Groups = []int{toks[i].End - toks[i].Start}

		// Цифры, приклеенные к букве без пробела, — часть слова: «CVC2»,
		// «COVID19», «дом5». Такую группу нельзя склеивать со следующим
		// числом, иначе «CVC2 987» превращается в четырёхзначное число.
		attached := i > 0 &&
			(toks[i-1].Kind == KindLat || toks[i-1].Kind == KindCyr) &&
			toks[i-1].End == toks[i].Start
		if attached {
			runs = append(runs, run)
			continue
		}

		// Ведущий знак «плюс» относим к последовательности: он значим для
		// телефонов в международном формате.
		if i > 0 && toks[i-1].Kind == KindPunct && d.Text[toks[i-1].Start:toks[i-1].End] == "+" {
			run.Start = toks[i-1].Start
			run.HasPlus = true
		}

		j := i
		for {
			// Между группами цифр может стоять несколько токенов-разделителей
			// подряд: например, в записи «+7 (916) 123-45-67» между «7» и «916»
			// идут пробел и скобка.
			k := j + 1
			for k < len(toks) && (toks[k].Kind == KindSpace || toks[k].Kind == KindPunct) {
				k++
			}
			if k >= len(toks) || toks[k].Kind != KindDigit || k == j+1 {
				break
			}
			sepText := d.Text[toks[j].End:toks[k].Start]
			if !isNumSeparator(sepText) {
				break
			}
			// Длинный разделитель почти всегда означает разрыв между
			// сущностями, а не внутреннюю структуру номера.
			if len([]rune(sepText)) > 3 || strings.ContainsAny(sepText, "\n\r") {
				break
			}
			for _, r := range sepText {
				if !strings.ContainsRune(run.Seps, r) {
					run.Seps += string(r)
				}
			}
			run.Digits += d.Text[toks[k].Start:toks[k].End]
			run.Groups = append(run.Groups, toks[k].End-toks[k].Start)
			run.End = toks[k].End
			j = k
		}
		runs = append(runs, run)
		i = j
	}
	return runs
}

// DigitsOnly оставляет в строке только цифры.
func DigitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// Luhn проверяет контрольную сумму по алгоритму Луна. Используется как
// усилитель уверенности для номеров карт, а не как условие обнаружения:
// в тестовых текстах номера обычно выдуманные и сумму не проходят.
func Luhn(digits string) bool {
	if len(digits) < 12 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		v := int(c - '0')
		if double {
			v *= 2
			if v > 9 {
				v -= 9
			}
		}
		sum += v
		double = !double
	}
	return sum%10 == 0
}

var (
	innWeights10 = []int{2, 4, 10, 3, 5, 9, 4, 6, 8}
	innWeights11 = []int{7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
	innWeights12 = []int{3, 7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
)

func innCheck(digits string, weights []int) int {
	sum := 0
	for i, w := range weights {
		sum += int(digits[i]-'0') * w
	}
	return sum % 11 % 10
}

// INNValid проверяет контрольные суммы ИНН длиной 10 или 12 цифр.
func INNValid(digits string) bool {
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	switch len(digits) {
	case 10:
		return innCheck(digits, innWeights10) == int(digits[9]-'0')
	case 12:
		return innCheck(digits, innWeights11) == int(digits[10]-'0') &&
			innCheck(digits, innWeights12) == int(digits[11]-'0')
	default:
		return false
	}
}

// SNILSValid проверяет контрольную сумму СНИЛС из 11 цифр.
func SNILSValid(digits string) bool {
	if len(digits) != 11 {
		return false
	}
	sum := 0
	for i := 0; i < 9; i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
		sum += int(digits[i]-'0') * (9 - i)
	}
	ctrl := sum % 101
	if ctrl == 100 {
		ctrl = 0
	}
	want := int(digits[9]-'0')*10 + int(digits[10]-'0')
	return ctrl == want
}

// trimRunes — символы, которые нужно отрезать от краёв фрагмента: пробелы и
// пунктуация, случайно попавшие в захват. Внутренние разделители не трогаем.
const trimRunes = " \t\r\n .,;:!?\"'«»()[]{}<>№#-–—/\\"

// NormalizeSpan обрезает у фрагмента ведущие и хвостовые пробелы и знаки
// препинания. Это защищает метрику: изменённый байт за пределами настоящего
// фрагмента персональных данных ломает выравнивание соседних фрагментов.
func NormalizeSpan(text string, start, end int) (int, int) {
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	for start < end {
		r, size := decodeRuneAt(text, start)
		if !strings.ContainsRune(trimRunes, r) {
			break
		}
		start += size
	}
	for end > start {
		r, size := decodeLastRuneBefore(text, end)
		if !strings.ContainsRune(trimRunes, r) {
			break
		}
		end -= size
	}
	return start, end
}

func decodeRuneAt(s string, i int) (rune, int) {
	for _, r := range s[i:] {
		return r, len(string(r))
	}
	return 0, 0
}

func decodeLastRuneBefore(s string, end int) (rune, int) {
	// Декодирование с конца строки, а не проход с начала: при проходе
	// нормализация границ становилась квадратичной по длине текста и роняла
	// пропускную способность на длинных документах в разы.
	r, size := utf8.DecodeLastRuneInString(s[:end])
	if r == utf8.RuneError && size <= 1 {
		return r, size
	}
	return r, size
}

// homoglyphCyr — латинские буквы, неотличимые на вид от кириллических, и их
// кириллические двойники. Подмена таких букв — классический способ обойти
// маскирование: значение остаётся персональными данными, а слово перестаёт
// совпадать со словарём. Сравнение ведётся по свёрнутой строке, где латинская
// буква заменена на кириллическую того же начертания.
var homoglyphCyr = map[rune]rune{
	'a': 'а', 'e': 'е', 'o': 'о', 'p': 'р', 'c': 'с', 'y': 'у', 'x': 'х',
	'A': 'А', 'E': 'Е', 'O': 'О', 'P': 'Р', 'C': 'С', 'Y': 'У', 'X': 'Х',
}

// FoldHomoglyphs заменяет латинские омоглифы на кириллические того же
// начертания. Длина строки в байтах при этом меняется: латинская буква занимает
// один байт, кириллическая два. Поэтому результат пригоден для сравнения, но
// не для смещений в исходном тексте.
// homoglyphASCII — та же таблица, но массивом по коду ASCII. Ноль означает,
// что замены нет. Массив вместо карты потому, что эта функция оказалась самой
// дорогой на горячем пути: профиль показал 27 процентов времени, из них 22 на
// доступе к карте. Все ключи таблицы это латинские буквы, то есть коды меньше
// 128, поэтому массива на 128 ячеек достаточно.
var homoglyphASCII = func() [128]rune {
	var t [128]rune
	for k, v := range homoglyphCyr {
		if k < 128 {
			t[k] = v
		}
	}
	return t
}()

// FoldHomoglyphs заменяет латинские омоглифы на кириллические того же
// начертания.
//
// Проверка «нужна ли замена» идёт по БАЙТАМ, а не по рунам. Это безопасно
// ровно потому, что все заменяемые буквы латинские: в UTF-8 у многобайтовой
// руны все байты больше 127, поэтому кириллица под проверку не попадает и
// ложного срабатывания не даёт. Для обычного русского текста цикл сводится к
// одному сравнению на байт без единого обращения к таблице.
func FoldHomoglyphs(s string) string {
	need := false
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 128 && homoglyphASCII[b] != 0 {
			need = true
			break
		}
	}
	if !need {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if r < 128 && homoglyphASCII[r] != 0 {
			b.WriteRune(homoglyphASCII[r])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ContainsAnyLower ищет в строке нижнего регистра любое из слов и возвращает
// первое найденное.
func ContainsAnyLower(haystack string, needles []string) (string, bool) {
	for _, n := range needles {
		if n != "" && strings.Contains(haystack, n) {
			return n, true
		}
	}
	return "", false
}

// NumRuns возвращает числовые последовательности документа, вычисляя их один
// раз. Документ живёт в пределах одного запроса и детекторы обходят его
// последовательно, поэтому дополнительная синхронизация не нужна.
func (d *Doc) NumRuns() []NumRun {
	if d.runs == nil {
		d.runs = DigitRuns(d)
		if d.runs == nil {
			d.runs = []NumRun{}
		}
	}
	return d.runs
}

// AnchorBefore ищет якорь слева от значения и требует, чтобы между якорем и
// значением не было других цифр. Без этого требования якорь «cvc2» из одной
// части предложения помечает число из другой его части.
//
// Поиск идёт по свёрнутой строке, где латинские омоглифы заменены на
// кириллические: «пасп0рт» с латинской «о» и «паспорт» с кириллической
// считаются одним словом. Проверка «между якорем и значением нет цифр» идёт
// по той же свёрнутой строке: цифры омоглифами не бывают и не меняются.
func (d *Doc) AnchorBefore(start int, anchors []string, maxRunes int) (string, bool) {
	lo, _ := d.WindowRunes(start, start, maxRunes, 0)
	window := d.Lower[lo:start]
	norm := FoldHomoglyphs(window)
	// Берём якорь, который заканчивается ближе всего к значению: из пары
	// «cvc» и «cvc2» должен победить более длинный, иначе цифра из самого
	// якоря окажется «чужой цифрой» между якорем и значением.
	bestPos, bestAnchor := -1, ""
	for _, a := range anchors {
		if a == "" {
			continue
		}
		na := FoldHomoglyphs(a)
		pos := strings.LastIndex(norm, na)
		if pos < 0 {
			continue
		}
		if end := pos + len(na); end > bestPos {
			bestPos, bestAnchor = end, a
		}
	}
	if bestAnchor == "" {
		// Пробел, вставленный внутрь якоря, разрывает слово: «поч товый»
		// вместо «почтовый». Пробуем окно без пробелов.
		return anchorBeforeCompact(norm, anchors)
	}
	// Между якорем и значением не должно быть других цифр.
	for _, r := range norm[bestPos:] {
		if r >= '0' && r <= '9' {
			return "", false
		}
	}
	return bestAnchor, true
}

// anchorBeforeCompact ищет якорь в окне, из которого убраны пробелы. Так
// опечатка с лишним пробелом внутри слова не теряет якорь: «поч товый» и
// «почтовый» считаются одним словом.
func anchorBeforeCompact(norm string, anchors []string) (string, bool) {
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\u00a0' {
			return -1
		}
		return r
	}, norm)
	best := ""
	for _, a := range anchors {
		if a == "" || len(a) <= len(best) {
			continue
		}
		na := FoldHomoglyphs(a)
		naCompact := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\u00a0' {
				return -1
			}
			return r
		}, na)
		if !strings.Contains(compact, naCompact) {
			continue
		}
		best = a
	}
	if best == "" {
		return "", false
	}
	// Между якорем и значением не должно быть других цифр.
	na := FoldHomoglyphs(best)
	naCompact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\u00a0' {
			return -1
		}
		return r
	}, na)
	pos := strings.LastIndex(compact, naCompact)
	for _, r := range compact[pos+len(naCompact):] {
		if r >= '0' && r <= '9' {
			return "", false
		}
	}
	return best, true
}
