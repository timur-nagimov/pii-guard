package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"
)

// typeCVV — имя типа персональных данных для кода проверки карты. Повторяется
// в разметке, в списке ярлыков и в перечнях типов, поэтому вынесено в константу.
const typeCVV = "CVV"

// kind описывает один вид вставляемого значения: тип разметки, вес в выборке,
// естественные ярлыки перед значением и способ порождения самого значения.
type kind struct {
	typ    string
	weight int
	labels []string
	build  func(*maker) string
}

// injectKinds перечисляет виды значений для вставки. Вес задаёт, насколько
// часто вид встречается: телефон, почта и имя попадаются в живых текстах чаще
// военного билета, и набор должен повторять этот перекос.
var injectKinds = []kind{
	{"FIO", 10, []string{"клиент", "заявитель", "получатель", "ФИО:"}, (*maker).fio},
	{"PHONE", 9, []string{"тел.", "телефон", "моб.", "звоните по номеру"}, (*maker).phone},
	{"EMAIL", 8, []string{"почта", "e-mail:", "пишите на"}, (*maker).email},
	{"ADDRESS", 7, []string{"адрес", "адрес:", "проживает по адресу"}, (*maker).address},
	{"CARD", 6, []string{"карта", "номер карты", "карта №"}, (*maker).card},
	{"PASSPORT", 6, []string{"паспорт", "паспорт РФ", "серия и номер"}, (*maker).passport},
	{"INN", 5, []string{"ИНН", "ИНН:"}, (*maker).inn},
	{"SNILS", 4, []string{"СНИЛС", "СНИЛС №"}, (*maker).snils},
	{"DOB", 4, []string{"дата рождения", "д.р.", "род."}, func(m *maker) string { return m.date(1945, 2006) }},
	{"BIRTH_PLACE", 3, []string{"место рождения", "уроженец"}, (*maker).birthPlace},
	{"DRIVER_LICENSE", 3, []string{"в/у", "водительское удостоверение", "права"}, (*maker).driverLicense},
	{"POSTCODE", 3, []string{"индекс", "почтовый индекс"}, func(m *maker) string { return m.numNZ(6) }},
	{"CARDHOLDER", 3, []string{"держатель", "на карте"}, (*maker).cardholder},
	{"ISSUER", 3, []string{"выдан", "кем выдан"}, (*maker).issuer},
	{"ISSUE_DATE", 3, []string{"дата выдачи", "выдан"}, func(m *maker) string { return m.date(2005, 2024) }},
	{"DEPT_CODE", 2, []string{"код подразделения"}, func(m *maker) string { return m.numNZ(3) + "-" + m.num(3) }},
	{typeCVV, 2, []string{typeCVV, "CVC", "код с оборота"}, func(m *maker) string { return m.num(3) }},
	{"PIN", 2, []string{"пин-код", "ПИН"}, func(m *maker) string { return m.num(4) }},
	{"CITIZENSHIP", 2, []string{"гражданство"}, func(m *maker) string { return m.pick(citizenships) }},
	{"FOREIGN_PASSPORT", 2, []string{"загранпаспорт", "заграничный паспорт"}, func(m *maker) string {
		return m.numNZ(2) + " " + m.num(7)
	}},
	{"RESIDENCE_PERMIT", 1, []string{"вид на жительство", "ВНЖ"}, func(m *maker) string {
		return m.numNZ(2) + " " + m.num(7)
	}},
	{"BIRTH_CERT", 1, []string{"свидетельство о рождении"}, func(m *maker) string {
		return m.pick(romanSeries) + "-" + m.pick(docLetters) + " № " + m.num(6)
	}},
	{"MILITARY_ID", 1, []string{"военный билет"}, func(m *maker) string {
		return m.pick(docLetters) + " № " + m.num(7)
	}},
}

// citizenships перечисляет записи гражданства.
var citizenships = []string{
	"Российская Федерация", "РФ", "гражданин России", "Россия",
	"Республика Беларусь", "Республика Казахстан", "Азербайджан", "Таджикистан",
}

// romanSeries перечисляет римские серии свидетельства о рождении.
var romanSeries = []string{"I", "II", "III", "IV", "V", "VII", "IX"}

// totalWeight — сумма весов видов значений, считается один раз при запуске.
var totalWeight = func() int {
	sum := 0
	for _, k := range injectKinds {
		sum += k.weight
	}
	return sum
}()

// pickKind выбирает вид значения с учётом веса.
func pickKind(m *maker) kind {
	point := m.roll(totalWeight)
	for _, k := range injectKinds {
		if point < k.weight {
			return k
		}
		point -= k.weight
	}
	return injectKinds[0]
}

// anchorOnlyTypes — типы, которые без ярлыка рядом не являются персональными
// данными и маскироваться не должны. Четырёхзначное число посреди отзыва это
// не пин-код, а название страны в перечислении это не гражданство.
//
// Вставлять такие значения голыми означает требовать от сервиса того, чего
// делать нельзя: маскировать любое короткое число и любую страну. Поэтому для
// них ярлык обязателен.
var anchorOnlyTypes = map[string]bool{
	"PIN":              true,
	"DOB":              true,
	typeCVV:            true,
	"CITIZENSHIP":      true,
	"DEPT_CODE":        true,
	"FOREIGN_PASSPORT": true,
	"RESIDENCE_PERMIT": true,
	"BIRTH_CERT":       true,
	"MILITARY_ID":      true,
	"SNILS":            true,
	"POSTCODE":         true,
	"BIRTH_PLACE":      true,
	"ISSUER":           true,
	"ISSUE_DATE":       true,
}

// pickLabel возвращает ярлык перед значением или пустую строку. Голые
// значения без ярлыка нужны не реже: детектор обязан узнавать их без подсказки.
// Исключение составляют типы из anchorOnlyTypes, для них ярлык обязателен.
func pickLabel(m *maker, k kind) string {
	if len(k.labels) > 0 && anchorOnlyTypes[k.typ] {
		return m.pick(k.labels) + " "
	}
	if len(k.labels) == 0 || m.roll(100) < 45 {
		return ""
	}
	return m.pick(k.labels) + " "
}

// insertion запоминает точку исходного текста и длину вставки в ней.
type insertion struct{ at, size int }

// woven — результат вставки значений в один носитель.
type woven struct {
	text    string
	spans   []Span
	values  []string
	inserts []insertion
}

// isSpace сообщает, что байт разделяет слова. Все разделители однобайтовые,
// поэтому проверка по байту безопасна для многобайтовых букв.
func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// spotSet — точки вставки: все годные и отдельно предпочтительные.
// Предпочтительные стоят после знака препинания, и вставка там читается
// естественнее, чем в разрыве придаточного предложения.
type spotSet struct {
	any    []int
	strong []int
}

// stopByte сообщает, что знак завершает предложение или его часть.
func stopByte(b byte) bool {
	return b == '.' || b == '!' || b == '?' || b == ';' || b == ':' || b == ','
}

// lastNonSpace возвращает смещение ближайшего слева непробельного байта.
// Возвращает минус единицу, если слева одни пробелы.
func lastNonSpace(text string, from int) int {
	for i := from; i >= 0; i-- {
		if !isSpace(text[i]) {
			return i
		}
	}
	return -1
}

// boundaries перечисляет байтовые смещения, куда можно вставить значение:
// начало слова после пробела и конец текста. Внутрь слова и внутрь уже
// записанного числа вставка по построению не попадает.
func boundaries(text string, taken []Span) spotSet {
	var set spotSet
	for i := 1; i < len(text); i++ {
		if !isSpace(text[i-1]) || isSpace(text[i]) || covered(taken, i) {
			continue
		}
		set.any = append(set.any, i)
		if j := lastNonSpace(text, i-1); j >= 0 && stopByte(text[j]) {
			set.strong = append(set.strong, i)
		}
	}
	if len(text) > 0 {
		set.any = append(set.any, len(text))
		set.strong = append(set.strong, len(text))
	}
	return set
}

// covered сообщает, что смещение попадает внутрь уже размеченного фрагмента.
// Вставлять туда нельзя: разметка носителя порвалась бы пополам.
func covered(spans []Span, at int) bool {
	for _, s := range spans {
		if at > s.Start && at < s.End {
			return true
		}
	}
	return false
}

// choose отбирает различные точки вставки и возвращает их по возрастанию.
// Предпочтительные точки берутся чаще, но не всегда: значение посреди фразы
// тоже встречается в живых текстах, и детектор обязан его находить.
func (m *maker) choose(spots spotSet, count int) []int {
	picked := make(map[int]bool, count)
	for tries := 0; len(picked) < count && tries < count*8; tries++ {
		pool := spots.any
		if len(spots.strong) > 0 && m.roll(100) < 70 {
			pool = spots.strong
		}
		picked[pool[m.roll(len(pool))]] = true
	}
	out := make([]int, 0, len(picked))
	for at := range picked {
		out = append(out, at)
	}
	sort.Ints(out)
	return out
}

// lead возвращает пробел, если перед точкой вставки его ещё нет.
func lead(text string, at int) string {
	if at == 0 || isSpace(text[at-1]) {
		return ""
	}
	return " "
}

// tail возвращает то, чем вставка отделяется от продолжения текста. Если
// значение само кончается точкой, точка не удваивается.
func (m *maker) tail(value string, atEnd bool) string {
	dotted := strings.HasSuffix(value, ".")
	if atEnd {
		if dotted {
			return ""
		}
		return "."
	}
	seps := []string{", ", ". ", " ", "; "}
	if dotted {
		seps = []string{" ", ", "}
	}
	return seps[m.roll(len(seps))]
}

// weave собирает новый текст, вставляя значение в каждую выбранную точку.
// Точки идут по возрастанию, поэтому границы уже записанных фрагментов не
// съезжают: всё новое дописывается правее них.
func weave(m *maker, text string, spots []int) woven {
	var b strings.Builder
	w := woven{
		spans:   make([]Span, 0, len(spots)),
		values:  make([]string, 0, len(spots)),
		inserts: make([]insertion, 0, len(spots)),
	}
	prev := 0
	for _, at := range spots {
		b.WriteString(text[prev:at])
		before := b.Len()
		k := pickKind(m)
		value := k.build(m)
		b.WriteString(lead(text, at))
		b.WriteString(pickLabel(m, k))
		start := b.Len()
		b.WriteString(value)
		w.spans = append(w.spans, Span{Start: start, End: b.Len(), Type: k.typ})
		w.values = append(w.values, value)
		b.WriteString(m.tail(value, at == len(text)))
		w.inserts = append(w.inserts, insertion{at: at, size: b.Len() - before})
		prev = at
	}
	b.WriteString(text[prev:])
	w.text = b.String()
	return w
}

// shiftSpans сдвигает разметку носителя на длину вставок левее фрагмента.
func shiftSpans(spans []Span, inserts []insertion) []Span {
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		delta := 0
		for _, ins := range inserts {
			if ins.at <= s.Start {
				delta += ins.size
			}
		}
		out = append(out, Span{Start: s.Start + delta, End: s.End + delta, Type: s.Type})
	}
	return out
}

// verifySpans проверяет, что каждый фрагмент вырезается из текста и совпадает
// со вставленным значением. Без этой проверки ошибка в смещениях дожила бы до
// отчёта и тихо испортила бы весь замер.
func verifySpans(text string, spans []Span, values []string) error {
	for i, s := range spans {
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			return fmt.Errorf("границы [%d,%d) не лежат в тексте длиной %d", s.Start, s.End, len(text))
		}
		if got := text[s.Start:s.End]; got != values[i] {
			return fmt.Errorf("срез %q не совпал со значением %q", got, values[i])
		}
	}
	return nil
}

// injectInto вставляет в носитель значения и возвращает готовый элемент.
// Признак частичной разметки сохраняется: в исходном тексте могли остаться
// свои неразмеченные имена, и считать их ошибкой нельзя.
func injectInto(m *maker, carrier Record, category string, maxValues int) (Record, error) {
	spots := boundaries(carrier.Text, carrier.Spans)
	if len(spots.any) == 0 {
		return Record{}, fmt.Errorf("в тексте %q нет точки вставки", carrier.ID)
	}
	count := min(1+m.roll(maxValues), len(spots.any))
	w := weave(m, carrier.Text, m.choose(spots, count))
	if err := verifySpans(w.text, w.spans, w.values); err != nil {
		return Record{}, fmt.Errorf("носитель %s: %w", carrier.ID, err)
	}
	spans := append(shiftSpans(carrier.Spans, w.inserts), w.spans...)
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	return Record{
		ID:            carrier.ID + "-inj",
		Category:      category,
		Source:        carrier.Source,
		Text:          w.text,
		Spans:         spans,
		PartialLabels: true,
	}, nil
}

// injectCategory выбирает категорию по виду носителя.
func injectCategory(source, override string) string {
	if override != "" {
		return override
	}
	switch source {
	case sourceWikipedia:
		return "wiki_injected"
	case sourceReviews:
		return "informal_injected"
	case "":
		return "injected"
	default:
		return source + "_injected"
	}
}

// runInject разбирает флаги подкоманды inject и вставляет значения в носители.
func runInject(args []string) error {
	fs := flag.NewFlagSet("inject", flag.ExitOnError)
	in := fs.String("in", "", "файл носителей в формате JSON Lines")
	out := fs.String("out", "", "куда записать результат")
	seed := fs.Uint64("seed", 7, "зерно случайности, один seed даёт один и тот же результат")
	maxValues := fs.Int("max-values", 3, "сколько значений вставлять в один носитель максимум")
	category := fs.String("category", "", "категория результата, пусто означает выбор по источнику")
	limit := fs.Int("limit", 0, "обработать не больше стольких носителей, ноль снимает предел")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return fmt.Errorf("нужны оба флага: -in и -out")
	}
	if *maxValues < 1 {
		return fmt.Errorf("значение -max-values должно быть не меньше единицы")
	}

	carriers, broken, err := readRecords(*in)
	if err != nil {
		return err
	}
	if *limit > 0 && len(carriers) > *limit {
		carriers = carriers[:*limit]
	}

	m := newMaker(*seed)
	result := make([]Record, 0, len(carriers))
	byType := map[string]int{}
	skipped := 0
	for _, c := range carriers {
		rec, err := injectInto(m, c, injectCategory(c.Source, *category), *maxValues)
		if err != nil {
			skipped++
			continue
		}
		for _, s := range rec.Spans {
			byType[s.Type]++
		}
		result = append(result, rec)
	}
	if err := writeRecords(*out, result); err != nil {
		return err
	}
	fmt.Printf("inject: носителей %d, готово %d, пропущено %d, битых строк %d\n",
		len(carriers), len(result), skipped, broken)
	printCounts("Фрагменты по типам", byType)
	return nil
}
