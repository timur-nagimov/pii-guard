package pii

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Здесь лежит разбор дат, записанных словами: словари названий месяцев и
// числительных, поиск месяца с одной опечаткой и сборка фрагмента вокруг
// слова-месяца. Часть вынесена из detect_date.go отдельно, потому что эта
// форма записи самостоятельна — опорой служит слово, а не число, обход идёт
// по токенам документа, и числовым датам эти словари не нужны вовсе.

// dateSpacers — знаки, которые допустимо встретить между частями даты,
// записанной словами.
// Кавычки входят в разделители: канцелярская запись «12» июля 2017 года
// ставит день в кавычки, и без них дата не собиралась вовсе. Сам знак в
// границы фрагмента не попадает — они начинаются с цифры дня.
const dateSpacers = " \t\u00a0-–—.,«»\"'"

// dateMonthWords — месяцы во всех падежных формах и английские названия.
// Карта создаётся один раз и дальше только читается, поэтому детектор
// остаётся безопасным для параллельного вызова.
var dateMonthWords = map[string]int{
	"январь": 1, "января": 1, "январе": 1, "январю": 1, "январём": 1, "январем": 1, "янв": 1,
	"февраль": 2, "февраля": 2, "феврале": 2, "февралю": 2, "февралём": 2, "февралем": 2, "фев": 2, "февр": 2,
	"март": 3, "марта": 3, "марте": 3, "марту": 3, "мартом": 3, "мар": 3,
	"апрель": 4, "апреля": 4, "апреле": 4, "апрелю": 4, "апрелем": 4, "апр": 4,
	"май": 5, "мая": 5, "мае": 5, "маю": 5, "маем": 5,
	"июнь": 6, "июня": 6, "июне": 6, "июню": 6, "июнем": 6,
	"июль": 7, "июля": 7, "июле": 7, "июлю": 7, "июлем": 7,
	"август": 8, "августа": 8, "августе": 8, "августу": 8, "августом": 8, "авг": 8,
	"сентябрь": 9, "сентября": 9, "сентябре": 9, "сентябрю": 9, "сентябрём": 9, "сен": 9, "сент": 9,
	"октябрь": 10, "октября": 10, "октябре": 10, "октябрю": 10, "октябрём": 10, "окт": 10,
	"ноябрь": 11, "ноября": 11, "ноябре": 11, "ноябрю": 11, "ноябрём": 11, "ноя": 11, "нояб": 11,
	"декабрь": 12, "декабря": 12, "декабре": 12, "декабрю": 12, "декабрём": 12, "дек": 12,

	"january": 1, "february": 2, "march": 3, "april": 4, "may": 5, "june": 6,
	"july": 7, "august": 8, "september": 9, "october": 10, "november": 11, "december": 12,
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "jun": 6, "jul": 7,
	"aug": 8, "sep": 9, "sept": 9, "oct": 10, "nov": 11, "dec": 12,
}

// Разбор месяца с опечаткой. Префикс нужен как дешёвый отбор кандидатов:
// перебирать весь словарь месяцев на каждом слове документа нельзя.
const (
	// dateMonthPrefixRunes — сколько первых букв месяца должны совпасть.
	dateMonthPrefixRunes = 3
	// dateMonthFuzzyMin — минимальная длина слова, которое разрешено считать
	// месяцем с опечаткой. Короткие слова вроде «март» слишком близки к
	// именам и к сокращениям, поэтому разбираются только точным совпадением.
	dateMonthFuzzyMin = 5
)

// dateMonthByPrefix — месяцы, сгруппированные по первым трём буквам. Списки
// отсортированы, поэтому при нескольких одинаково близких кандидатах выбор
// не зависит от порядка обхода карты.
var dateMonthByPrefix = dateBuildMonthPrefixes()

// dateBuildMonthPrefixes строит индекс месяцев по префиксу один раз при
// загрузке пакета. Дальше индекс только читается.
func dateBuildMonthPrefixes() map[string][]string {
	out := make(map[string][]string, len(dateMonthWords))
	for w := range dateMonthWords {
		if utf8.RuneCountInString(w) < dateMonthFuzzyMin {
			continue
		}
		p, ok := dateWordPrefix(w)
		if !ok {
			continue
		}
		out[p] = append(out[p], w)
	}
	for _, list := range out {
		sort.Strings(list)
	}
	return out
}

// dateWordPrefix возвращает первые буквы слова без выделения памяти.
func dateWordPrefix(w string) (string, bool) {
	n, i := 0, 0
	for i < len(w) && n < dateMonthPrefixRunes {
		_, size := utf8.DecodeRuneInString(w[i:])
		i += size
		n++
	}
	if n < dateMonthPrefixRunes {
		return "", false
	}
	return w[:i], true
}

// dateMonthFuzzy ищет месяц с одной опечаткой: перестановкой соседних букв,
// пропущенной, лишней или другой буквой. Тексты пишут люди, и «сентбяря» в
// анкете означает ровно тот же сентябрь.
func dateMonthFuzzy(word string) (int, bool) {
	if utf8.RuneCountInString(word) < dateMonthFuzzyMin {
		return 0, false
	}
	prefix, ok := dateWordPrefix(word)
	if !ok {
		return 0, false
	}
	for _, cand := range dateMonthByPrefix[prefix] {
		if dateNearWord(word, cand) {
			return dateMonthWords[cand], true
		}
	}
	return 0, false
}

// dateNearWord сообщает, что слова отличаются не больше чем на одну опечатку.
func dateNearWord(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	switch len(ra) - len(rb) {
	case 0:
		return dateOneReplace(ra, rb) || dateOneSwap(ra, rb)
	case 1:
		return dateOneSkip(ra, rb)
	case -1:
		return dateOneSkip(rb, ra)
	default:
		return false
	}
}

// dateOneReplace сообщает, что слова одной длины отличаются в одной букве.
func dateOneReplace(a, b []rune) bool {
	diff := 0
	for i := range a {
		if a[i] != b[i] {
			diff++
		}
	}
	return diff <= 1
}

// dateOneSwap сообщает, что слова отличаются перестановкой соседних букв.
func dateOneSwap(a, b []rune) bool {
	i := dateFirstDiff(a, b)
	if i < 0 || i+1 >= len(a) {
		return false
	}
	if a[i] != b[i+1] || a[i+1] != b[i] {
		return false
	}
	return dateOneReplace(a[i+2:], b[i+2:])
}

// dateOneSkip сообщает, что длинное слово превращается в короткое удалением
// одной буквы.
func dateOneSkip(long, short []rune) bool {
	i := dateFirstDiff(long[:len(short)], short)
	if i < 0 {
		return true
	}
	return dateSameRunes(long[i+1:], short[i:])
}

// dateFirstDiff возвращает позицию первой различающейся буквы или минус один.
func dateFirstDiff(a, b []rune) int {
	for i := range a {
		if i >= len(b) {
			return i
		}
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

// dateSameRunes сравнивает две последовательности букв.
func dateSameRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	return dateFirstDiff(a, b) < 0
}

// wordDates ищет даты, записанные словами. Опорой служит слово-месяц: оно
// однозначно, а числа вокруг него проверяются на правдоподобие.
func (dd dateDetector) wordDates(d *Doc) []Span {
	var out []Span
	for i, tok := range d.Tokens {
		if tok.Kind != KindCyr && tok.Kind != KindLat {
			continue
		}
		month, ok := dateMonth(d.Lower[tok.Start:tok.End])
		if !ok {
			continue
		}
		if s, ok := dd.dayFirstDate(d, i, month); ok {
			out = append(out, s)
			continue
		}
		if s, ok := dd.monthFirstDate(d, i, month); ok {
			out = append(out, s)
		}
	}
	return out
}

// dateMonth определяет номер месяца по слову: сначала точно, потом с
// допуском на одну опечатку.
func dateMonth(word string) (int, bool) {
	if m, ok := dateMonthWords[word]; ok {
		return m, true
	}
	return dateMonthFuzzy(word)
}

// dayFirstDate разбирает запись «12 марта 1985», «5-го марта 1990» и
// «двенадцатое марта тысяча девятьсот восемьдесят пятого года».
func (dd dateDetector) dayFirstDate(d *Doc, monthTok, month int) (Span, bool) {
	start, day, ok := dateDayBefore(d, monthTok)
	if !ok {
		return Span{}, false
	}
	end, year, ok := dateYearAfter(d, monthTok)
	if !ok {
		return Span{}, false
	}
	return dd.wordDateSpan(d, start, end, day, month, year)
}

// monthFirstDate разбирает английскую запись «March 12, 1985».
func (dd dateDetector) monthFirstDate(d *Doc, monthTok, month int) (Span, bool) {
	dayTok, ok := dateDayTokenAfter(d, monthTok)
	if !ok {
		return Span{}, false
	}
	day, err := strconv.Atoi(dateTokenText(d, dayTok))
	if err != nil {
		return Span{}, false
	}
	end, year, ok := dateYearAfter(d, dayTok)
	if !ok {
		return Span{}, false
	}
	return dd.wordDateSpan(d, d.Tokens[monthTok].Start, end, day, month, year)
}

// wordDateSpan проверяет правдоподобие даты словами и определяет её тип.
func (dd dateDetector) wordDateSpan(d *Doc, start, end, day, month, year int) (Span, bool) {
	if !dateValidDayMonthNum(day, month, year) {
		return Span{}, false
	}
	s, e := NormalizeSpan(d.Text, start, end)
	return dd.classify(d, s, e, "words")
}

// dateTokenText возвращает исходный текст токена.
func dateTokenText(d *Doc, i int) string {
	return d.Text[d.Tokens[i].Start:d.Tokens[i].End]
}

// dateDaySuffixes — окончания порядкового числительного перед месяцем:
// «5-го марта», «1-е мая».
var dateDaySuffixes = []string{"го", "ое", "ый", "е"}

// dateDayBefore определяет день слева от месяца: число «12», порядковое
// «5-го» или слово «двенадцатое». Возвращает начало фрагмента и день.
func dateDayBefore(d *Doc, monthTok int) (int, int, bool) {
	if j, ok := dateDayTokenBefore(d, monthTok); ok {
		if day, err := strconv.Atoi(dateTokenText(d, j)); err == nil {
			return d.Tokens[j].Start, day, true
		}
	}
	words, start, ok := dateWordsBefore(d, monthTok, dateDayWordsMax)
	if !ok {
		return 0, 0, false
	}
	day, ok := dateWordNumber(words)
	if !ok || day < 1 || day > dateMaxDay {
		return 0, 0, false
	}
	return start, day, true
}

// dateYearAfter определяет год справа от токена: число «1985» или слова
// «тысяча девятьсот восемьдесят пятого». Возвращает конец фрагмента и год.
func dateYearAfter(d *Doc, from int) (int, int, bool) {
	if j, ok := dateYearTokenAfter(d, from); ok {
		if year, ok := dateYearValue(dateTokenText(d, j)); ok {
			return d.Tokens[j].End, year, true
		}
	}
	words, end, ok := dateWordsAfter(d, from, dateYearWordsMax)
	if !ok {
		return 0, 0, false
	}
	year, ok := dateWordNumber(words)
	if !ok || !dateYearPlausible(year) {
		return 0, 0, false
	}
	return end, year, true
}

// dateDayTokenBefore ищет число дня слева от месяца.
func dateDayTokenBefore(d *Doc, monthTok int) (int, bool) {
	for j := monthTok - 1; j >= 0 && j >= monthTok-4; j-- {
		if d.Tokens[j].Kind != KindDigit {
			continue
		}
		if d.Tokens[j].End-d.Tokens[j].Start > 2 {
			return 0, false
		}
		if !dateIsDayGap(d.Lower[d.Tokens[j].End:d.Tokens[monthTok].Start]) {
			return 0, false
		}
		return j, true
	}
	return 0, false
}

// dateDayTokenAfter ищет число дня справа от месяца в английской записи.
func dateDayTokenAfter(d *Doc, monthTok int) (int, bool) {
	return dateDigitTokenAfter(d, monthTok, 2, 1, 2)
}

// dateYearTokenAfter ищет четырёхзначный год справа от указанного токена.
func dateYearTokenAfter(d *Doc, from int) (int, bool) {
	return dateDigitTokenAfter(d, from, 3, 4, 4)
}

// dateDigitTokenAfter ищет справа от токена from число длиной от minLen до
// maxLen, просматривая не больше span токенов. День и год ищутся одинаково и
// отличаются только глубиной просмотра и длиной числа, поэтому обход общий.
// Первое встреченное число решает исход: если оно не той длины или отделено
// не разделителем, записи нужного вида здесь уже нет.
func dateDigitTokenAfter(d *Doc, from, span, minLen, maxLen int) (int, bool) {
	for j := from + 1; j < len(d.Tokens) && j <= from+span; j++ {
		if d.Tokens[j].Kind != KindDigit {
			continue
		}
		if n := d.Tokens[j].End - d.Tokens[j].Start; n < minLen || n > maxLen {
			return 0, false
		}
		if !dateIsSpacerGap(d.Lower[d.Tokens[from].End:d.Tokens[j].Start]) {
			return 0, false
		}
		return j, true
	}
	return 0, false
}

// dateIsDayGap проверяет промежуток между числом дня и месяцем, допуская
// окончание порядкового числительного.
func dateIsDayGap(gap string) bool {
	if len([]rune(gap)) > 6 || strings.ContainsAny(gap, "\n\r") {
		return false
	}
	rest := gap
	for _, s := range dateDaySuffixes {
		rest = strings.ReplaceAll(rest, s, "")
	}
	return dateOnlySpacers(rest)
}

// dateIsSpacerGap проверяет промежуток между частями даты: там допустимы
// только пробелы и знаки препинания, и промежуток не пересекает строку.
func dateIsSpacerGap(gap string) bool {
	if len([]rune(gap)) > 4 || strings.ContainsAny(gap, "\n\r") {
		return false
	}
	return dateOnlySpacers(gap)
}

// dateOnlySpacers сообщает, что строка состоит только из допустимых
// разделителей. Цифр и букв в ней быть не может.
func dateOnlySpacers(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune(dateSpacers, r) {
			return false
		}
	}
	return true
}

// Сколько слов-числительных подряд разбирается для дня и для года.
// «двадцать первое» это два слова, «тысяча девятьсот восемьдесят пятого» —
// четыре, «две тысячи двадцать четвёртого» — тоже четыре.
const (
	dateDayWordsMax  = 2
	dateYearWordsMax = 4
)

// dateNumberWords — числительные, которыми записывают день и год: и
// количественные, и порядковые формы. Карта только читается.
var dateNumberWords = map[string]int{
	"первое": 1, "первого": 1, "первый": 1, "первая": 1, "первом": 1,
	"второе": 2, "второго": 2, "второй": 2, "вторая": 2,
	"третье": 3, "третьего": 3, "третий": 3, "третья": 3,
	"четвёртое": 4, "четвертое": 4, "четвёртого": 4, "четвертого": 4, "четвёртый": 4, "четвертый": 4,
	"пятое": 5, "пятого": 5, "пятый": 5, "пятая": 5,
	"шестое": 6, "шестого": 6, "шестой": 6,
	"седьмое": 7, "седьмого": 7, "седьмой": 7,
	"восьмое": 8, "восьмого": 8, "восьмой": 8,
	"девятое": 9, "девятого": 9, "девятый": 9,
	"десятое": 10, "десятого": 10, "десятый": 10,
	"одиннадцатое": 11, "одиннадцатого": 11,
	"двенадцатое": 12, "двенадцатого": 12,
	"тринадцатое": 13, "тринадцатого": 13,
	"четырнадцатое": 14, "четырнадцатого": 14,
	"пятнадцатое": 15, "пятнадцатого": 15,
	"шестнадцатое": 16, "шестнадцатого": 16,
	"семнадцатое": 17, "семнадцатого": 17,
	"восемнадцатое": 18, "восемнадцатого": 18,
	"девятнадцатое": 19, "девятнадцатого": 19,
	"двадцатое": 20, "двадцатого": 20, "двадцать": 20,
	"тридцатое": 30, "тридцатого": 30, "тридцать": 30,

	"сорок": 40, "сорокового": 40, "пятьдесят": 50, "пятидесятого": 50,
	"шестьдесят": 60, "шестидесятого": 60, "семьдесят": 70, "семидесятого": 70,
	"восемьдесят": 80, "восьмидесятого": 80, "девяносто": 90, "девяностого": 90,
	"сто": 100, "двести": 200, "триста": 300, "четыреста": 400, "пятьсот": 500,
	"шестьсот": 600, "семьсот": 700, "восемьсот": 800, "девятьсот": 900,
	"два": 2, "две": 2,
}

// dateThousandWords — слова-тысячи: они умножают накопленное слева число.
var dateThousandWords = map[string]bool{
	"тысяча": true, "тысячи": true, "тысяч": true, "тысячного": true,
}

// dateWordNumber складывает числительные в одно число: «тысяча девятьсот
// восемьдесят пятого» это 1985, «двадцать первого» это 21.
func dateWordNumber(words []string) (int, bool) {
	if len(words) == 0 {
		return 0, false
	}
	total, pending := 0, 0
	for _, w := range words {
		if dateThousandWords[w] {
			if pending == 0 {
				pending = 1
			}
			total += pending * 1000
			pending = 0
			continue
		}
		v, ok := dateNumberWords[w]
		if !ok {
			return 0, false
		}
		pending += v
	}
	return total + pending, true
}

// dateIsNumberWord сообщает, что слово входит в запись числа словами.
func dateIsNumberWord(w string) bool {
	if dateThousandWords[w] {
		return true
	}
	_, ok := dateNumberWords[w]
	return ok
}

// dateWordsBefore собирает подряд идущие слова-числительные слева от токена и
// возвращает их вместе с началом первого слова.
func dateWordsBefore(d *Doc, tok, maxWords int) ([]string, int, bool) {
	words := make([]string, 0, maxWords)
	start := 0
	for j := tok - 1; j >= 0 && len(words) < maxWords; j-- {
		w, ok := dateNumberWordAt(d, j)
		if !ok {
			break
		}
		if w == "" {
			continue
		}
		words = append(words, w)
		start = d.Tokens[j].Start
	}
	dateReverse(words)
	return words, start, len(words) > 0
}

// dateWordsAfter собирает подряд идущие слова-числительные справа от токена и
// возвращает их вместе с концом последнего слова.
func dateWordsAfter(d *Doc, tok, maxWords int) ([]string, int, bool) {
	words := make([]string, 0, maxWords)
	end := 0
	for j := tok + 1; j < len(d.Tokens) && len(words) < maxWords; j++ {
		w, ok := dateNumberWordAt(d, j)
		if !ok {
			break
		}
		if w == "" {
			continue
		}
		words = append(words, w)
		end = d.Tokens[j].End
	}
	return words, end, len(words) > 0
}

// dateNumberWordAt читает токен как часть числа словами. Пустая строка при
// истине означает разделитель, который можно пропустить.
func dateNumberWordAt(d *Doc, j int) (string, bool) {
	tok := d.Tokens[j]
	text := d.Lower[tok.Start:tok.End]
	if tok.Kind == KindSpace || tok.Kind == KindPunct {
		if !dateIsSpacerGap(text) {
			return "", false
		}
		return "", true
	}
	if tok.Kind != KindCyr || !dateIsNumberWord(text) {
		return "", false
	}
	return text, true
}

// dateReverse переворачивает список слов, собранный справа налево.
func dateReverse(words []string) {
	for i, j := 0, len(words)-1; i < j; i, j = i+1, j-1 {
		words[i], words[j] = words[j], words[i]
	}
}
