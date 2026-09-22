package pii

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Режимы детектора дат. Режим задаёт, что делать с правдоподобной датой, у
// которой нет явного якоря рождения или выдачи.
const (
	// DateModeAnchorOnly маскирует только даты с явным якорем. Самый
	// осторожный режим, нужен для текстов, где даты встречаются в бизнесовом
	// смысле чаще, чем в персональном.
	DateModeAnchorOnly = "anchor_only"
	// DateModePIIContext дополнительно маскирует дату, рядом с которой стоят
	// другие персональные данные. Режим по умолчанию: в проверочных текстах
	// дату рождения часто пишут без слов «дата рождения», просто в одном ряду
	// с фамилией и паспортом.
	DateModePIIContext = "pii_context"
	// DateModeAny маскирует любую правдоподобную дату. Нужен для текстов,
	// целиком состоящих из анкет.
	DateModeAny = "any"
)

// Якорные слова детектора дат. Все записаны в нижнем регистре, поиск идёт по
// Doc.Lower. Вместо полных словоформ используются основы, поэтому «родился»,
// «родилась», «родившийся», «рождения» и «рождена» покрываются двумя записями.
var (
	dateAnchorsBirth = []string{
		"родил", "родивш", "рожд", "урожен",
		"г.р.", "г. р.", "г/р", "д.р.", "д. р.", "д/р", "др.", "др ", "др:",
		"birth", "born", "dob",
	}
	// dateAnchorsBirthYear — более узкий набор для записи одного года без дня
	// и месяца. Широкие основы сюда не входят: любое четырёхзначное число
	// рядом со словом «родился» иначе стало бы годом рождения.
	dateAnchorsBirthYear = []string{
		"г.р.", "г. р.", "г/р", "года рождения", "год рождения",
		"году рождения", "рождения", "род.", "родил", "рожден", "рождён",
	}
	// dateAnchorsIssue — основа «выда» покрывает «выдан», «выдана», «выдал»,
	// «выдачи» и опечатку «выдаан», которая встречается в текстах жюри.
	dateAnchorsIssue = []string{"выда", "issued"}
	// dateAnchorsDoc — упоминание документа или органа, который его выдал.
	// Дата после такого упоминания почти всегда дата выдачи.
	dateAnchorsDoc = []string{
		"паспорт", "удостоверение", "свидетельство", "код подразделения",
		"увд", "овд", "мвд", "гувд", "уфмс", "фмс", "мфц",
	}
	// dateAnchorsPII — признаки того, что рядом идёт речь о конкретном
	// человеке и его данных.
	dateAnchorsPII = []string{
		"клиент", "заявител", "заёмщик", "заемщик", "плательщик", "держател",
		"анкет", "паспорт", "снилс", "инн ", "инн:", "телефон", "карта",
		"карты", "зарегистрирован", "прожива", "фио", "ф.и.о.", "гражданин",
		"гражданк", "абонент", "застрахован",
	}
	// datePatronymics — окончания отчеств. Отчество рядом с датой почти
	// наверняка означает анкету человека, а не бизнесовую дату.
	datePatronymics = []string{"ович", "евич", "ьич", "овна", "евна", "ична"}
	// dateNegAnchors — контексты, в которых дата не является персональными
	// данными и лишняя маска будет засчитана как ошибка.
	dateNegAnchors = []string{
		"срок действия", "действует до", "действителен до", "действительна до",
		"дата платежа", "дата операции", "дата договора", "дата заключения",
		"оплачен", "оплата", "оплаты", "списание", "зачисление",
		"отчётный период", "отчетный период", "курс на", "срок оплаты",
	}
)

// Окна поиска контекста вокруг даты. Все значения в рунах: байтовое окно для
// кириллицы вдвое короче задуманного и до якоря не достаёт.
const (
	// dateAnchorWindow — окно слева, в котором ищется явный якорь.
	dateAnchorWindow = 48
	// dateSuffixWindow — окно справа: якорь часто идёт после даты, как в
	// записи «05.03.1990 г.р.».
	dateSuffixWindow = 24
	// dateDocWindow — окно упоминания документа перед датой выдачи. Между
	// словом «паспорт» и датой помещаются серия, номер и название органа.
	dateDocWindow = 120
	// datePIIWindow — окно режима pii_context.
	datePIIWindow = 80
	// dateNegWindow — окно отрицательных якорей.
	dateNegWindow = 40
	// dateMinYear — нижняя граница правдоподобного года.
	dateMinYear = 1900
	// dateNeighbourDigitsMin — сколько цифр рядом с датой означают документ
	// человека. Десять цифр это серия с номером паспорта, карта, телефон
	// или ИНН, то есть анкета, а не деловая запись.
	dateNeighbourDigitsMin = 10
	// dateMaxDay — наибольший возможный день месяца.
	dateMaxDay = 31
	// dateCompactLen — длина даты, записанной одной группой цифр: «12031985».
	dateCompactLen = 8
)

// dateSeparators — знаки, которыми разделяют части числовой даты.
const dateSeparators = "./-–—"

// dateSpacers — знаки, которые допустимо встретить между частями даты,
// записанной словами.
const dateSpacers = " \t\u00a0-–—.,"

// dateDetector находит даты рождения и даты выдачи документов. Один тип
// фрагмента здесь разделяется на два по контексту, поэтому оба типа
// обслуживает один детектор: он читает окружение даты ровно один раз.
type dateDetector struct {
	mode string
}

// NewDateDetector создаёт детектор дат в заданном режиме. Неизвестное
// значение режима приводится к режиму по умолчанию: ошибка в конфигурации не
// должна выключать целый тип персональных данных.
func NewDateDetector(mode string) Detector {
	switch mode {
	case DateModeAnchorOnly, DateModeAny:
		return dateDetector{mode: mode}
	default:
		return dateDetector{mode: DateModePIIContext}
	}
}

// Types перечисляет типы, которые находит детектор.
func (dateDetector) Types() []Type { return []Type{TypeDOB, TypeIssueDate} }

// Detect ищет даты трёх видов: числовые, записанные словами и одиночный год
// рождения. Год разбирается последним, чтобы не дублировать год, уже вошедший
// в полную дату.
func (dd dateDetector) Detect(d *Doc) []Span {
	out := dd.numericDates(d)
	out = append(out, dd.wordDates(d)...)
	out = append(out, dd.birthYears(d, out)...)
	return out
}

// dateGroup — одна группа цифр внутри числовой последовательности вместе с её
// границами в тексте документа.
type dateGroup struct {
	start int
	end   int
}

// numericDates разбирает числовые кандидаты документа. Отдельного прохода по
// тексту нет: числовые последовательности уже посчитаны один раз для всех
// детекторов.
func (dd dateDetector) numericDates(d *Doc) []Span {
	var out []Span
	for _, run := range d.NumRuns() {
		// Ведущий плюс бывает только у телефона в международном формате.
		if run.HasPlus {
			continue
		}
		out = append(out, dd.runDates(d, run)...)
	}
	return out
}

// runDates ищет даты внутри одной числовой последовательности.
//
// Искать надо именно внутри: в строке «Белова К.А. 1977-12-17 75 52 040112»
// дата и номер паспорта склеиваются в одну последовательность, и целиком она
// датой не является. Тройки разбираются слева направо и не перекрываются,
// иначе хвост даты вместе с началом номера дал бы вторую «дату».
func (dd dateDetector) runDates(d *Doc, run NumRun) []Span {
	groups := dateSplitRun(d, run)
	if len(groups) == 1 {
		return dd.compactDate(d, groups[0])
	}
	var out []Span
	for i := 0; i+2 < len(groups); {
		if !dateTripleShape(d, groups[i:i+3]) {
			i++
			continue
		}
		start, end := NormalizeSpan(d.Text, groups[i].start, groups[i+2].end)
		if s, ok := dd.classify(d, start, end, "numeric"); ok {
			out = append(out, s)
		}
		i += 3
	}
	return out
}

// dateSplitRun разбирает числовую последовательность на группы цифр с их
// границами в тексте.
func dateSplitRun(d *Doc, run NumRun) []dateGroup {
	out := make([]dateGroup, 0, len(run.Groups))
	for i := run.Start; i < run.End; {
		if !dateIsDigit(d.Text[i]) {
			i++
			continue
		}
		j := i
		for j < run.End && dateIsDigit(d.Text[j]) {
			j++
		}
		out = append(out, dateGroup{start: i, end: j})
		i = j
	}
	return out
}

// dateIsDigit сообщает, что байт это десятичная цифра.
func dateIsDigit(b byte) bool { return b >= '0' && b <= '9' }

// dateTripleShape проверяет, что три соседние группы цифр образуют дату:
// промежутки одного вида, длины групп подходящие, число существует.
func dateTripleShape(d *Doc, g []dateGroup) bool {
	left, ok := dateGapKind(d.Text[g[0].end:g[1].start])
	if !ok {
		return false
	}
	right, ok := dateGapKind(d.Text[g[1].end:g[2].start])
	if !ok || left != right {
		return false
	}
	parts := []string{
		d.Text[g[0].start:g[0].end],
		d.Text[g[1].start:g[1].end],
		d.Text[g[2].start:g[2].end],
	}
	if !dateGroupLengths(parts, left == dateGapSpace) {
		return false
	}
	return datePlausibleParts(parts)
}

// dateGapSpace — вид промежутка, в котором знака-разделителя нет, только
// пробел.
const dateGapSpace = ' '

// dateGapKind определяет вид промежутка между группами цифр: знак-разделитель
// или только пробел. Вид возвращается наружу, потому что внутри одной даты
// промежутки не смешиваются: «12.03 1985» это две разные записи рядом.
func dateGapKind(gap string) (rune, bool) {
	kind := rune(dateGapSpace)
	for _, r := range gap {
		switch {
		case strings.ContainsRune(dateSeparators, r):
			if kind != dateGapSpace && kind != r {
				return 0, false
			}
			kind = r
		case r == ' ' || r == '\t' || r == '\u00a0':
		default:
			return 0, false
		}
	}
	return kind, true
}

// dateGroupLengths допускает четыре цифры года в начале или в конце, две
// цифры года в конце и одну или две цифры для дня и месяца.
//
// Для записи через один пробел требования строже: день и месяц с ведущим
// нулём, год из четырёх цифр. Иначе датой стала бы любая тройка коротких
// чисел в перечислении, а таких в банковских текстах много.
func dateGroupLengths(parts []string, spaced bool) bool {
	a, b, c := len(parts[0]), len(parts[1]), len(parts[2])
	if spaced {
		return a == 2 && b == 2 && c == 4 || a == 4 && b == 2 && c == 2
	}
	shortPair := a >= 1 && a <= 2 && b >= 1 && b <= 2
	switch {
	case a == 4:
		return b >= 1 && b <= 2 && c >= 1 && c <= 2
	case c == 4 || c == 2:
		return shortPair
	default:
		return false
	}
}

// datePlausibleParts проверяет, что из трёх чисел складывается существующая
// дата хотя бы в одном порядке. Разбирать, день это или месяц, не требуется:
// маскируется вся запись целиком.
func datePlausibleParts(parts []string) bool {
	if len(parts[0]) == 4 {
		year, ok := dateYearValue(parts[0])
		if !ok {
			return false
		}
		return dateValidDayMonth(parts[1], parts[2], year) || dateValidDayMonth(parts[2], parts[1], year)
	}
	year, ok := dateYearValue(parts[2])
	if !ok {
		return false
	}
	return dateValidDayMonth(parts[0], parts[1], year) || dateValidDayMonth(parts[1], parts[0], year)
}

// compactDate разбирает дату, записанную одной группой из восьми цифр:
// «12031985». От номера документа такая запись ничем не отличается, кроме
// соседнего слова, поэтому нужен явный якорь рождения или выдачи; упоминания
// документа рядом здесь недостаточно.
func (dd dateDetector) compactDate(d *Doc, g dateGroup) []Span {
	digits := d.Text[g.start:g.end]
	if len(digits) != dateCompactLen || !dateCompactPlausible(digits) {
		return nil
	}
	start, end := NormalizeSpan(d.Text, g.start, g.end)
	if start >= end || !dateWithinLine(d, start, end) {
		return nil
	}
	t, conf, rule, ok := dateExplicitAnchor(d, start, end)
	if !ok {
		return nil
	}
	return []Span{{Start: start, End: end, Type: t, Conf: conf, Reason: "date:compact+" + rule}}
}

// dateCompactPlausible проверяет восемь цифр в порядке «день месяц год» и в
// порядке «год месяц день».
func dateCompactPlausible(digits string) bool {
	dayFirst := []string{digits[0:2], digits[2:4], digits[4:8]}
	yearFirst := []string{digits[0:4], digits[4:6], digits[6:8]}
	return datePlausibleParts(dayFirst) || datePlausibleParts(yearFirst)
}

// dateYearValue разбирает год из двух или четырёх цифр и проверяет его
// правдоподобие. Двузначный год относим к прошлому веку, если в текущем веке
// он ещё не наступил.
func dateYearValue(s string) (int, bool) {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	if len(s) == 2 {
		if v <= time.Now().Year()%100 {
			v += 2000
		} else {
			v += 1900
		}
	}
	if !dateYearPlausible(v) {
		return 0, false
	}
	return v, true
}

// dateYearPlausible проверяет, что год лежит между началом двадцатого века и
// текущим годом.
func dateYearPlausible(year int) bool {
	return year >= dateMinYear && year <= time.Now().Year()
}

// dateValidDayMonth проверяет пару «день и месяц» с учётом длины месяца:
// тридцатое февраля датой не является.
func dateValidDayMonth(dayStr, monthStr string, year int) bool {
	day, err := strconv.Atoi(dayStr)
	if err != nil {
		return false
	}
	month, err := strconv.Atoi(monthStr)
	if err != nil {
		return false
	}
	return dateValidDayMonthNum(day, month, year)
}

// dateValidDayMonthNum проверяет уже разобранные день и месяц.
func dateValidDayMonthNum(day, month, year int) bool {
	if month < 1 || month > 12 {
		return false
	}
	return day >= 1 && day <= dateDaysInMonth(month, year)
}

// dateDaysInMonth возвращает число дней в месяце указанного года.
func dateDaysInMonth(month, year int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if dateIsLeap(year) {
			return 29
		}
		return 28
	default:
		return 0
	}
}

// dateIsLeap сообщает, високосный ли год по григорианскому правилу.
func dateIsLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

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
	for j := monthTok + 1; j < len(d.Tokens) && j <= monthTok+2; j++ {
		if d.Tokens[j].Kind != KindDigit {
			continue
		}
		if d.Tokens[j].End-d.Tokens[j].Start > 2 {
			return 0, false
		}
		if !dateIsSpacerGap(d.Lower[d.Tokens[monthTok].End:d.Tokens[j].Start]) {
			return 0, false
		}
		return j, true
	}
	return 0, false
}

// dateYearTokenAfter ищет четырёхзначный год справа от указанного токена.
func dateYearTokenAfter(d *Doc, from int) (int, bool) {
	for j := from + 1; j < len(d.Tokens) && j <= from+3; j++ {
		if d.Tokens[j].Kind != KindDigit {
			continue
		}
		if d.Tokens[j].End-d.Tokens[j].Start != 4 {
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

// birthYears находит запись года рождения без дня и месяца: «1985 г.р.»,
// «1985 года рождения», «род. 1985». Года, уже вошедшие в полную дату,
// пропускаются.
func (dd dateDetector) birthYears(d *Doc, found []Span) []Span {
	var out []Span
	for _, run := range d.NumRuns() {
		if run.HasPlus || len(run.Groups) != 1 || len(run.Digits) != 4 {
			continue
		}
		if _, ok := dateYearValue(run.Digits); !ok {
			continue
		}
		start, end := NormalizeSpan(d.Text, run.Start, run.End)
		if dateOverlapsAny(found, start, end) || dateOverlapsAny(out, start, end) {
			continue
		}
		if !dateWithinLine(d, start, end) {
			continue
		}
		if _, ok := dateAnchorDistance(d, start, end, dateAnchorsBirthYear, dateSuffixWindow, dateSuffixWindow); !ok {
			continue
		}
		out = append(out, Span{Start: start, End: end, Type: TypeDOB, Conf: ConfHigh, Reason: "date:year+birth_anchor"})
	}
	return out
}

// dateOverlapsAny сообщает, что диапазон пересекается с уже найденным.
func dateOverlapsAny(spans []Span, start, end int) bool {
	probe := Span{Start: start, End: end}
	for _, s := range spans {
		if s.Overlaps(probe) {
			return true
		}
	}
	return false
}

// classify определяет тип даты и уверенность по её окружению.
//
// Порядок правил важен: явный якорь сильнее отрицательного контекста. Иначе
// в записи «дата рождения 05.03.1990, оплата прошла» слово «оплата» гасило бы
// настоящую дату рождения.
func (dd dateDetector) classify(d *Doc, start, end int, shape string) (Span, bool) {
	if start >= end || !dateWithinLine(d, start, end) {
		return Span{}, false
	}
	if t, conf, rule, ok := dateByAnchor(d, start, end); ok {
		return Span{Start: start, End: end, Type: t, Conf: conf, Reason: "date:" + shape + "+" + rule}, true
	}
	if dateHasNegative(d, start, end) {
		return Span{}, false
	}
	t, conf, rule, ok := dd.byContext(d, start, end)
	if !ok {
		return Span{}, false
	}
	return Span{Start: start, End: end, Type: t, Conf: conf, Reason: "date:" + shape + "+" + rule}, true
}

// dateByAnchor разбирает дату по явному якорю, а при его отсутствии — по
// упоминанию документа неподалёку.
func dateByAnchor(d *Doc, start, end int) (Type, float64, string, bool) {
	if t, conf, rule, ok := dateExplicitAnchor(d, start, end); ok {
		return t, conf, rule, true
	}
	// Дата вскоре после упоминания документа или органа выдачи: между словом
	// «паспорт» и датой помещаются серия, номер и название органа.
	if _, ok := ContainsAnyLower(d.LowerWindow(start, start, dateDocWindow, 0), dateAnchorsDoc); ok {
		return TypeIssueDate, ConfAnchored, "document_context", true
	}
	return "", 0, "", false
}

// dateExplicitAnchor выбирает между якорем рождения и якорем выдачи. Из двух
// побеждает тот, что стоит ближе: в анкете «паспорт выдан ..., 05.03.1990
// г.р.» оба якоря попадают в одно окно, и решает расстояние.
func dateExplicitAnchor(d *Doc, start, end int) (Type, float64, string, bool) {
	birth, hasBirth := dateAnchorDistance(d, start, end, dateAnchorsBirth, dateAnchorWindow, dateSuffixWindow)
	issue, hasIssue := dateAnchorDistance(d, start, end, dateAnchorsIssue, dateAnchorWindow, dateSuffixWindow)
	switch {
	case hasBirth && (!hasIssue || birth <= issue):
		return TypeDOB, ConfHigh, "birth_anchor", true
	case hasIssue:
		return TypeIssueDate, ConfHigh, "issue_anchor", true
	default:
		return "", 0, "", false
	}
}

// byContext решает судьбу даты без якоря по режиму детектора.
func (dd dateDetector) byContext(d *Doc, start, end int) (Type, float64, string, bool) {
	switch dd.mode {
	case DateModeAnchorOnly:
		return "", 0, "", false
	case DateModeAny:
		return TypeDOB, ConfMedium, "any", true
	default:
		if dateHasPIIContext(d, start, end) {
			return TypeDOB, ConfMedium, "pii_context", true
		}
		return "", 0, "", false
	}
}

// dateAnchorDistance возвращает расстояние в байтах до ближайшего якоря слева
// или справа от даты. Расстояние нужно, чтобы выбрать между якорем рождения и
// якорем выдачи, когда в предложении есть оба.
func dateAnchorDistance(d *Doc, start, end int, anchors []string, before, after int) (int, bool) {
	best := -1
	lo, _ := d.WindowRunes(start, start, before, 0)
	left := d.Lower[lo:start]
	for _, a := range anchors {
		pos := strings.LastIndex(left, a)
		if pos < 0 {
			continue
		}
		if dist := len(left) - (pos + len(a)); best < 0 || dist < best {
			best = dist
		}
	}
	_, hi := d.WindowRunes(end, end, 0, after)
	right := d.Lower[end:hi]
	for _, a := range anchors {
		pos := strings.Index(right, a)
		if pos < 0 {
			continue
		}
		if best < 0 || pos < best {
			best = pos
		}
	}
	return best, best >= 0
}

// dateHasNegative сообщает, что рядом стоит слово, при котором дата не
// является персональными данными.
func dateHasNegative(d *Doc, start, end int) bool {
	_, ok := ContainsAnyLower(d.LowerWindow(start, end, dateNegWindow, dateSuffixWindow), dateNegAnchors)
	return ok
}

// dateHasPIIContext сообщает, что рядом с датой есть другие персональные
// данные: банковский якорь, отчество или цифры документа.
func dateHasPIIContext(d *Doc, start, end int) bool {
	lo, hi := d.WindowRunes(start, end, datePIIWindow, datePIIWindow)
	window := d.Lower[lo:hi]
	if _, ok := ContainsAnyLower(window, dateAnchorsPII); ok {
		return true
	}
	if _, ok := ContainsAnyLower(window, datePatronymics); ok {
		return true
	}
	return dateNeighbourDigits(d, start, end, lo, hi) >= dateNeighbourDigitsMin
}

// dateNeighbourDigits считает цифры соседних чисел в окне, не считая цифры
// самой даты. Серия и номер документа рядом с датой это анкета человека, и не
// важно, разорваны они словом «номер» или нет.
func dateNeighbourDigits(d *Doc, start, end, lo, hi int) int {
	total := 0
	for _, run := range d.NumRuns() {
		if run.End <= lo || run.Start >= hi {
			continue
		}
		total += dateDigitsOutside(d, run, start, end)
	}
	return total
}

// dateDigitsOutside считает цифры последовательности за пределами фрагмента.
// Отбрасывать последовательность целиком нельзя: в записи «195.12.1.20» дата
// и остаток адреса лежат в одной последовательности.
func dateDigitsOutside(d *Doc, run NumRun, start, end int) int {
	n := 0
	for i := run.Start; i < run.End; i++ {
		if !dateIsDigit(d.Text[i]) || (i >= start && i < end) {
			continue
		}
		n++
	}
	return n
}

// dateWithinLine требует, чтобы фрагмент не пересекал перевод строки.
func dateWithinLine(d *Doc, start, end int) bool {
	lo, hi := d.LineBounds(start)
	return start >= lo && end <= hi
}
