package pii

import (
	"strings"
	"unicode/utf8"
)

// Здесь лежит определение типа даты по её окружению: якорные слова рождения,
// выдачи и персонального контекста, окна поиска вокруг даты и правила, которые
// по ним решают, дата это рождения, дата выдачи или вовсе не персональные
// данные. Часть вынесена из detect_date.go отдельно, потому что от формы
// записи она не зависит: числовая дата, дата словами и одиночный год приходят
// сюда одинаково.

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
	// Основа «оформлен» покрывает «дату оформления» и «оформлен», «начала
	// действия» — канцелярскую замену даты выдачи. Отрицательные якоря
	// вроде «срок действия» проверяются отдельно и остаются сильнее.
	dateAnchorsIssue = []string{
		"выда", "issued", "оформлен", "оформления", "начала действия",
	}
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
	// dateNeighbourDigitsMin — сколько цифр рядом с датой означают документ
	// человека. Десять цифр это серия с номером паспорта, карта, телефон
	// или ИНН, то есть анкета, а не деловая запись.
	dateNeighbourDigitsMin = 10
)

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
	lo, _ := d.WindowRunes(start, start, before, 0)
	left := FoldHomoglyphs(d.Lower[lo:start])
	_, hi := d.WindowRunes(end, end, 0, after)
	right := FoldHomoglyphs(d.Lower[end:hi])
	best := dateAnchorDistBefore(left, anchors)
	if dist := dateAnchorDistAfter(right, anchors); dist >= 0 && (best < 0 || dist < best) {
		best = dist
	}
	if best < 0 && dateAnchorInCompact(left, anchors) {
		return 0, true
	}
	return best, best >= 0
}

// dateAnchorDistBefore возвращает расстояние от конца ближайшего якоря слева
// до даты или -1, если якоря в окне нет. Слева берётся последнее вхождение:
// именно оно ближе всего к дате.
func dateAnchorDistBefore(left string, anchors []string) int {
	best := -1
	for _, a := range anchors {
		na := FoldHomoglyphs(a)
		pos := strings.LastIndex(left, na)
		if pos < 0 {
			continue
		}
		if dist := len(left) - (pos + len(na)); best < 0 || dist < best {
			best = dist
		}
	}
	return best
}

// dateAnchorDistAfter возвращает расстояние от даты до ближайшего якоря
// справа или -1, если якоря в окне нет. Справа ближайшее вхождение — первое.
func dateAnchorDistAfter(right string, anchors []string) int {
	best := -1
	for _, a := range anchors {
		pos := strings.Index(right, FoldHomoglyphs(a))
		if pos < 0 {
			continue
		}
		if best < 0 || pos < best {
			best = pos
		}
	}
	return best
}

// dateAnchorInCompact ищет якорь в окне слева, из которого убраны пробелы.
// Пробел, вставленный внутрь якоря, разрывает слово: «ро жд» вместо «рожд».
// Короткие якоря вроде «др» не берём: они совпадают внутри чужих слов
// («адреса» содержит «др»).
func dateAnchorInCompact(left string, anchors []string) bool {
	compact := dateDropSpaces(left)
	for _, a := range anchors {
		if utf8.RuneCountInString(a) < 4 {
			continue
		}
		if strings.Contains(compact, dateDropSpaces(FoldHomoglyphs(a))) {
			return true
		}
	}
	return false
}

// dateDropSpaces убирает из строки пробелы. Сравнение без пробелов — способ
// узнать якорь, разорванный лишним пробелом, не заводя словаря опечаток.
func dateDropSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\u00a0' {
			return -1
		}
		return r
	}, s)
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
