package pii

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Здесь точка входа детектора дат и разбор записей, опорой которых служит
// число: числовая дата, дата одной группой цифр и одиночный год рождения.
// Разбор дат словами лежит в detect_date_words.go, определение типа даты по
// окружению — в detect_date_context.go.

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

// Границы правдоподобной даты: какие год и день возможны и какой длины бывает
// запись без разделителей.
const (
	// dateMinYear — нижняя граница правдоподобного года.
	dateMinYear = 1900
	// dateMaxDay — наибольший возможный день месяца.
	dateMaxDay = 31
	// dateCompactLen — длина даты, записанной одной группой цифр: «12031985».
	dateCompactLen = 8
)

// dateSeparators — знаки, которыми разделяют части числовой даты.
const dateSeparators = "./-–—"

// dateDetector находит даты рождения и даты выдачи документов. Один тип
// фрагмента здесь разделяется на два по контексту, поэтому оба типа
// обслуживает один детектор: он читает окружение даты ровно один раз.
type dateDetector struct {
	// mode — режим обработки дат без явного якоря. Значение детектора
	// неизменяемо и свободно копируется: разбор числовых дат, дат словами и
	// определение типа по контексту лежат в трёх файлах и работают с копией.
	// Смена режима на лету публикует новое значение целиком, см.
	// dateModeSwitch.
	mode string
}

// DateDetector — детектор дат, чей режим можно менять на лету. Нужен сервису,
// чтобы режим по умолчанию следовал за файлом настроек без перезапуска.
type DateDetector interface {
	Detector
	// SetMode меняет режим обработки дат без явного якоря.
	SetMode(mode string)
}

// dateModeSwitch — детектор дат с режимом, сменяемым на лету. Режим не
// правится на месте: новое значение детектора публикуется целиком и читается
// атомарно. Так применение настроек не ждёт перезапуска и не создаёт гонки с
// запросами, которые уже разбирают текст со старым режимом.
type dateModeSwitch struct {
	det atomic.Pointer[dateDetector]
}

// NewDateDetector создаёт детектор дат в заданном режиме. Неизвестное
// значение режима приводится к режиму по умолчанию: ошибка в конфигурации не
// должна выключать целый тип персональных данных.
func NewDateDetector(mode string) DateDetector {
	s := &dateModeSwitch{}
	s.SetMode(mode)
	return s
}

// SetMode меняет режим обработки дат без явного якоря. Вызывается при
// применении новых настроек: режим по умолчанию обязан следовать за файлом,
// иначе ослабление режима не действовало бы до перезапуска.
func (s *dateModeSwitch) SetMode(mode string) {
	s.det.Store(&dateDetector{mode: dateKnownMode(mode)})
}

// dateKnownMode приводит неизвестное значение режима к режиму по умолчанию.
func dateKnownMode(mode string) string {
	switch mode {
	case DateModeAnchorOnly, DateModeAny:
		return mode
	default:
		return DateModePIIContext
	}
}

// current возвращает действующий снимок детектора. Нулевой указатель здесь
// невозможен при обычном создании, но режим по умолчанию безопаснее пустого:
// пустой режим выключил бы разбор дат без якоря целиком.
func (s *dateModeSwitch) current() dateDetector {
	if det := s.det.Load(); det != nil {
		return *det
	}
	return dateDetector{mode: DateModePIIContext}
}

// Types перечисляет типы, которые находит детектор.
func (s *dateModeSwitch) Types() []Type { return s.current().Types() }

// Detect разбирает документ детектором в действующем режиме.
func (s *dateModeSwitch) Detect(d *Doc) []Span { return s.current().Detect(d) }

// Types перечисляет типы, которые находит детектор. Список от режима не
// зависит: режим решает только судьбу даты без якоря.
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
		// Через пробел дата встречается только в двух порядках целиком:
		// «день месяц год» и «год месяц день». Смешанных длин вроде «2 2 2»
		// среди них нет, поэтому каждый порядок назван отдельно.
		dayFirst := a == 2 && b == 2 && c == 4
		yearFirst := a == 4 && b == 2 && c == 2
		return dayFirst || yearFirst
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

// dateWithinLine требует, чтобы фрагмент не пересекал перевод строки.
func dateWithinLine(d *Doc, start, end int) bool {
	lo, hi := d.LineBounds(start)
	return start >= lo && end <= hi
}
