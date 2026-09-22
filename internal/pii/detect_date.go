package pii

import (
	"strconv"
	"strings"
	"time"
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
// «родилась», «родившийся», «рождения» и «рождена» покрываются тремя записями.
var (
	dateAnchorsBirth = []string{
		"родил", "родивш", "рожден", "рождён", "рожда", "урожен",
		"г.р.", "г. р.", "г/р", "д.р.", "д. р.", "д/р", "др.", "др ", "др:",
		"birth", "born",
	}
	// dateAnchorsBirthYear — более узкий набор для записи одного года без дня
	// и месяца. Широкие основы сюда не входят: любое четырёхзначное число
	// рядом со словом «родился» иначе стало бы годом рождения.
	dateAnchorsBirthYear = []string{
		"г.р.", "г. р.", "г/р", "года рождения", "год рождения",
		"году рождения", "рождения", "род.", "родил", "рожден", "рождён",
	}
	dateAnchorsIssue = []string{"выдан", "выдач", "выдал", "issued"}
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

// numericDates разбирает числовые кандидаты документа. Отдельного прохода по
// тексту нет: числовые последовательности уже посчитаны один раз для всех
// детекторов.
func (dd dateDetector) numericDates(d *Doc) []Span {
	var out []Span
	for _, run := range d.NumRuns() {
		parts, ok := dateParts(run)
		if !ok || !datePlausibleParts(parts) {
			continue
		}
		start, end := NormalizeSpan(d.Text, run.Start, run.End)
		if s, ok := dd.classify(d, start, end, "numeric"); ok {
			out = append(out, s)
		}
	}
	return out
}

// dateParts проверяет форму числового кандидата и возвращает три группы цифр.
// Двух групп недостаточно: запись вида 12/27 и 12/2027 это срок действия
// карты, а не дата.
func dateParts(run NumRun) ([]string, bool) {
	if run.HasPlus || len(run.Groups) != 3 {
		return nil, false
	}
	if !dateSeps(run.Seps) || !dateGroupLengths(run.Groups) {
		return nil, false
	}
	parts := make([]string, 0, 3)
	off := 0
	for _, g := range run.Groups {
		parts = append(parts, run.Digits[off:off+g])
		off += g
	}
	return parts, true
}

// dateSeps требует, чтобы группы разделяла точка, дробь или дефис. Запись
// через один пробел датой не считается: так пишут серию и номер документа.
func dateSeps(seps string) bool {
	hasReal := false
	for _, r := range seps {
		switch {
		case strings.ContainsRune(dateSeparators, r):
			hasReal = true
		case r == ' ' || r == '\t' || r == '\u00a0':
		default:
			return false
		}
	}
	return hasReal
}

// dateGroupLengths допускает четыре цифры года в начале или в конце, две
// цифры года в конце и одну или две цифры для дня и месяца.
func dateGroupLengths(g []int) bool {
	shortPair := g[0] >= 1 && g[0] <= 2 && g[1] >= 1 && g[1] <= 2
	switch {
	case g[0] == 4:
		return g[1] >= 1 && g[1] <= 2 && g[2] >= 1 && g[2] <= 2
	case g[2] == 4 || g[2] == 2:
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

// dateYearValue разбирает год из двух или четырёх цифр и проверяет его
// правдоподобие. Двузначный год относим к прошлому веку, если в текущем веке
// он ещё не наступил.
func dateYearValue(s string) (int, bool) {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	now := time.Now().Year()
	if len(s) == 2 {
		if v <= now%100 {
			v += 2000
		} else {
			v += 1900
		}
	}
	if v < dateMinYear || v > now {
		return 0, false
	}
	return v, true
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

// wordDates ищет даты, записанные словами. Опорой служит слово-месяц: оно
// однозначно, а числа вокруг него проверяются на правдоподобие.
func (dd dateDetector) wordDates(d *Doc) []Span {
	var out []Span
	for i, tok := range d.Tokens {
		if tok.Kind != KindCyr && tok.Kind != KindLat {
			continue
		}
		month, ok := dateMonthWords[d.Lower[tok.Start:tok.End]]
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

// dayFirstDate разбирает запись «12 марта 1985» и «5-го марта 1990».
func (dd dateDetector) dayFirstDate(d *Doc, monthTok, month int) (Span, bool) {
	day, ok := dateDayTokenBefore(d, monthTok)
	if !ok {
		return Span{}, false
	}
	year, ok := dateYearTokenAfter(d, monthTok)
	if !ok {
		return Span{}, false
	}
	return dd.wordDateSpan(d, d.Tokens[day].Start, d.Tokens[year].End, day, month, year)
}

// monthFirstDate разбирает английскую запись «March 12, 1985».
func (dd dateDetector) monthFirstDate(d *Doc, monthTok, month int) (Span, bool) {
	day, ok := dateDayTokenAfter(d, monthTok)
	if !ok {
		return Span{}, false
	}
	year, ok := dateYearTokenAfter(d, day)
	if !ok {
		return Span{}, false
	}
	return dd.wordDateSpan(d, d.Tokens[monthTok].Start, d.Tokens[year].End, day, month, year)
}

// wordDateSpan проверяет правдоподобие даты словами и определяет её тип.
func (dd dateDetector) wordDateSpan(d *Doc, start, end, dayTok, month, yearTok int) (Span, bool) {
	year, ok := dateYearValue(dateTokenText(d, yearTok))
	if !ok {
		return Span{}, false
	}
	if !dateValidDayMonth(dateTokenText(d, dayTok), strconv.Itoa(month), year) {
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

// dateByAnchor разбирает дату по явному якорю. Из двух якорей побеждает тот,
// что стоит ближе: в анкете «паспорт выдан ..., 05.03.1990 г.р.» оба якоря
// попадают в одно окно, и решает расстояние.
func dateByAnchor(d *Doc, start, end int) (Type, float64, string, bool) {
	birth, hasBirth := dateAnchorDistance(d, start, end, dateAnchorsBirth, dateAnchorWindow, dateSuffixWindow)
	issue, hasIssue := dateAnchorDistance(d, start, end, dateAnchorsIssue, dateAnchorWindow, dateSuffixWindow)
	switch {
	case hasBirth && (!hasIssue || birth <= issue):
		return TypeDOB, ConfHigh, "birth_anchor", true
	case hasIssue:
		return TypeIssueDate, ConfHigh, "issue_anchor", true
	}
	// Дата вскоре после упоминания документа или органа выдачи: между словом
	// «паспорт» и датой помещаются серия, номер и название органа.
	if _, ok := ContainsAnyLower(d.LowerWindow(start, start, dateDocWindow, 0), dateAnchorsDoc); ok {
		return TypeIssueDate, ConfAnchored, "document_context", true
	}
	return "", 0, "", false
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
// данные: банковский якорь, отчество или длинный номер документа.
func dateHasPIIContext(d *Doc, start, end int) bool {
	lo, hi := d.WindowRunes(start, end, datePIIWindow, datePIIWindow)
	window := d.Lower[lo:hi]
	if _, ok := ContainsAnyLower(window, dateAnchorsPII); ok {
		return true
	}
	if _, ok := ContainsAnyLower(window, datePatronymics); ok {
		return true
	}
	return dateHasLongNumber(d, start, end, lo, hi)
}

// dateHasLongNumber ищет в окне длинный номер: паспорт, карту, телефон, ИНН.
// Сама дата в расчёт не берётся.
func dateHasLongNumber(d *Doc, start, end, lo, hi int) bool {
	for _, run := range d.NumRuns() {
		if run.End <= lo || run.Start >= hi {
			continue
		}
		if run.Start >= start && run.End <= end {
			continue
		}
		if len(run.Digits) >= 10 {
			return true
		}
	}
	return false
}

// dateWithinLine требует, чтобы фрагмент не пересекал перевод строки.
func dateWithinLine(d *Doc, start, end int) bool {
	lo, hi := d.LineBounds(start)
	return start >= lo && end <= hi
}
