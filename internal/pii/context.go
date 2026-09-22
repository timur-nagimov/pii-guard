package pii

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"pii-guard/internal/pii/dict"
)

// Причины снятия фрагмента. Записываются в Reason отклонённого фрагмента,
// чтобы по журналу можно было понять, какое правило сработало.
const (
	reasonPublicFigure = "public_figure"
	reasonOrgAddress   = "org_address"
	reasonAllowList    = "allow_list"
	reasonStreetName   = "street_name"
)

// Окна поиска признаков. Все размеры заданы в рунах: байтовое окно для
// кириллицы вдвое короче задуманного и до признака не достаёт.
const (
	ctxFigureWindow = 80
	ctxOrgWindow    = 80
	ctxStreetWindow = 15
	// ctxMaxTail — сколько букв допускается после основы признака. Три буквы
	// покрывают падежные окончания и не пускают внутрь другое слово:
	// «автор» совпадает с «авторов», но не с «авторизацией».
	ctxMaxTail = 3
	// ctxMinStem — минимальная длина основы в рунах. Более короткая основа
	// склеивает разные фамилии, поэтому окончание не отрезаем.
	ctxMinStem = 3
	// ctxLeftPenalty и ctxSentencePenalty — поправки к расстоянию в рунах при
	// выборе адреса, к которому относится признак организации.
	ctxLeftPenalty     = 8
	ctxSentencePenalty = 16
)

// ContextOptions — настройки контекстных правил. Нулевое значение означает,
// что работают только правило про названия улиц и списки разрешённых значений.
type ContextOptions struct {
	// PublicFigures включает снятие имён известных людей.
	PublicFigures bool
	// OrgAddresses включает снятие адресов отделений и организаций.
	OrgAddresses bool
	// AllowPersons, AllowAddresses и AllowValues — значения, которые команда
	// вывела из-под маскирования: собственные телефоны банка, адрес офиса,
	// имена в шаблонах писем.
	AllowPersons   []string
	AllowAddresses []string
	AllowValues    []string
}

// ContextFilter снимает фрагменты, которые формально похожи на персональные
// данные, но ими не являются: имена исторических лиц, адреса отделений банка,
// улицы, названные в честь людей, и значения из списков разрешённых.
//
// Фильтр собирается один раз при старте и дальше только читается, поэтому
// безопасен для параллельных запросов.
type ContextFilter struct {
	publicFigures bool
	orgAddresses  bool
	allow         map[string]bool
	figureFull    map[string]bool
	figureSurname map[string]bool
}

// NewContextFilter собирает фильтр: раскладывает словарь известных людей по
// основам слов и нормализует списки разрешённых значений.
func NewContextFilter(opts ContextOptions) *ContextFilter {
	f := &ContextFilter{
		publicFigures: opts.PublicFigures,
		orgAddresses:  opts.OrgAddresses,
		allow:         make(map[string]bool),
		figureFull:    make(map[string]bool),
		figureSurname: make(map[string]bool),
	}
	for _, list := range [][]string{opts.AllowValues, opts.AllowPersons, opts.AllowAddresses} {
		for _, v := range list {
			if key := ctxNormalizeValue(v); key != "" {
				f.allow[key] = true
			}
		}
	}
	f.loadFigures(dict.Figures())
	return f
}

// loadFigures раскладывает записи вида «фамилия имя» на основы: по основе
// сравнение переживает падежные формы вроде «Пушкина» и «Пушкину».
func (f *ContextFilter) loadFigures(entries []string) {
	for _, e := range entries {
		words := ctxWords(ctxNormalizeValue(e))
		if len(words) == 0 {
			continue
		}
		surname := ctxStem(words[0])
		f.figureSurname[surname] = true
		for _, w := range words[1:] {
			f.figureFull[surname+" "+ctxStem(w)] = true
		}
	}
}

// Apply разбирает найденные фрагменты и возвращает оставленные и снятые.
// Исходный срез не изменяется: причина записывается в копию фрагмента.
func (f *ContextFilter) Apply(d *Doc, spans []Span) (kept []Span, dropped []Span) {
	if d == nil || len(spans) == 0 {
		return spans, nil
	}
	sc := &ctxScan{
		f: f,
		d: d,
		// Приведение «ё» к «е» не меняет длину строки в байтах, поэтому
		// смещения фрагментов остаются общими с исходным текстом.
		low:     ctxFoldYo(d.Lower),
		spans:   spans,
		reasons: make([]string, len(spans)),
	}
	// Списки разрешённых значений разбираем первым проходом: снятый по списку
	// фрагмент не должен считаться банковским контекстом для соседей.
	for i := range spans {
		if f.isAllowed(sc.spanText(i)) {
			sc.reasons[i] = reasonAllowList
		}
	}
	for i := range spans {
		if sc.reasons[i] == "" {
			sc.reasons[i] = sc.typeRules(i)
		}
	}
	if f.orgAddresses {
		sc.markOrgAddresses()
	}
	return sc.split()
}

// isAllowed сообщает, что значение выведено из-под маскирования настройкой.
func (f *ContextFilter) isAllowed(value string) bool {
	if value == "" {
		return false
	}
	return f.allow[ctxNormalizeValue(value)]
}

// ctxScan — состояние одного вызова Apply. Всё изменяемое живёт здесь, а не в
// фильтре, поэтому один фильтр обслуживает запросы параллельно.
type ctxScan struct {
	f       *ContextFilter
	d       *Doc
	low     string
	spans   []Span
	reasons []string
}

// spanText возвращает исходный текст фрагмента либо пустую строку, если
// границы фрагмента испорчены.
func (s *ctxScan) spanText(i int) string {
	sp := s.spans[i]
	if sp.Start < 0 || sp.End > len(s.d.Text) || sp.End <= sp.Start {
		return ""
	}
	return s.d.Text[sp.Start:sp.End]
}

// typeRules применяет правила, зависящие от типа фрагмента.
func (s *ctxScan) typeRules(i int) string {
	if s.spans[i].Type != TypeFIO {
		return ""
	}
	if s.isStreetName(i) {
		return reasonStreetName
	}
	if s.f.publicFigures && s.isPublicFigure(i) {
		return reasonPublicFigure
	}
	return ""
}

// split раскладывает фрагменты на оставленные и снятые.
func (s *ctxScan) split() (kept []Span, dropped []Span) {
	kept = make([]Span, 0, len(s.spans))
	for i, sp := range s.spans {
		if s.reasons[i] == "" {
			kept = append(kept, sp)
			continue
		}
		sp.Reason = appendReason(sp.Reason, s.reasons[i])
		dropped = append(dropped, sp)
	}
	return kept, dropped
}

// ctxStreetMarkers — адресные слова. Если такое слово стоит непосредственно
// слева, дальше идёт название улицы, а не человек: «улица Пушкина».
var ctxStreetMarkers = []string{
	"ул.", "улиц", "пр-т", "пр-кт", "просп", "проспект", "пер.", "переул",
	"пл.", "площад", "наб.", "набережн", "ш.", "шоссе", "б-р", "бульвар",
	"проезд", "туп.", "тупик", "мкр.", "микрорайон", "станци", "метро",
	"парк", "сквер", "имени", "им.",
}

// isStreetName проверяет адресное слово слева от фрагмента.
func (s *ctxScan) isStreetName(i int) bool {
	sp := s.spans[i]
	lo, _ := s.d.WindowRunes(sp.Start, sp.Start, ctxStreetWindow, 0)
	if lo >= sp.Start {
		return false
	}
	return ctxHasMarker(s.low[lo:sp.Start], ctxStreetMarkers)
}

// ctxRoleMarkers — признаки того, что рядом описан публичный человек, а не
// клиент банка. Хранятся основами: окончание снимает сравнение с хвостом.
var ctxRoleMarkers = []string{
	"поэт", "писател", "композитор", "художник", "актер", "актрис",
	"режиссер", "учен", "академик", "президент", "министр", "цар",
	"император", "полковод", "космонавт", "певец", "певц", "музыкант",
	"чемпион", "лауреат", "классик", "автор", "роман", "стихотворен",
	"поэм", "симфони", "памятник", "музе", "имени", "в честь",
	"по мотивам", "драматург", "скульптор", "философ",
}

// ctxBankAnchors — слова банковского контекста. Рядом с ними даже известная
// фамилия принадлежит клиенту-тёзке, поэтому фрагмент не снимается.
var ctxBankAnchors = []string{
	"клиент", "заявител", "заемщик", "плательщик", "держател", "паспорт",
	"счет", "карт", "договор", "заявк", "платеж", "анкет", "обращени",
}

// ctxStopWords — слова, которые совпали бы с основой признака по ошибке.
// «Поэтому» не делает человека поэтом, «картина» не делает текст банковским.
var ctxStopWords = map[string]bool{
	"поэтому": true, "картина": true, "картины": true, "картине": true,
	"картину": true, "картиной": true, "счетчик": true, "счетчика": true,
	"романов": true, "романова": true, "романовы": true, "романовых": true,
}

// isPublicFigure решает, снимать ли имя как упоминание известного человека.
//
// Условий три: (а) имя есть в словаре, (б) рядом есть ролевой признак,
// (в) в абзаце нет банковского контекста. Нужны два условия, причём (в) само
// по себе не срабатывает: без него имя вообще не снимается, иначе тёзка
// клиента перестанет маскироваться.
func (s *ctxScan) isPublicFigure(i int) bool {
	if !s.cleanParagraph(i) {
		return false
	}
	role := s.hasRole(i)
	return role || s.f.knownFigure(s.spanText(i), role)
}

// hasRole ищет ролевой признак или годы жизни в скобках рядом с именем.
func (s *ctxScan) hasRole(i int) bool {
	sp := s.spans[i]
	lo, hi := s.d.WindowRunes(sp.Start, sp.End, ctxFigureWindow, ctxFigureWindow)
	window := s.low[lo:hi]
	return ctxHasMarker(window, ctxRoleMarkers) || ctxHasLifeYears(window)
}

// cleanParagraph сообщает, что в абзаце нет банковского контекста: ни якорей,
// ни других персональных фрагментов.
func (s *ctxScan) cleanParagraph(i int) bool {
	lo, hi := ctxParagraphBounds(s.d.Text, s.spans[i].Start)
	if ctxHasMarker(s.low[lo:hi], ctxBankAnchors) {
		return false
	}
	for j, other := range s.spans {
		if j == i || s.reasons[j] != "" || !ctxBankingType(other.Type) {
			continue
		}
		if other.Start >= lo && other.Start < hi {
			return false
		}
	}
	return true
}

// ctxBankingType сообщает, что тип фрагмента говорит о работе с документами
// клиента. Имя, место рождения и гражданство сюда не входят: они сами по себе
// встречаются и в рассказе об известном человеке.
func ctxBankingType(t Type) bool {
	switch t {
	case TypeFIO, TypeBirthPlace, TypeCitizenship:
		return false
	default:
		return true
	}
}

// knownFigure ищет имя в словаре известных людей. Полное совпадение фамилии и
// имени принимается всегда, совпадение по одной фамилии — только вместе с
// ролевым признаком: одна фамилия слишком часто встречается у обычных людей.
func (f *ContextFilter) knownFigure(value string, role bool) bool {
	words := ctxWords(ctxNormalizeValue(value))
	stems := make([]string, 0, len(words))
	for _, w := range words {
		stems = append(stems, ctxStem(w))
	}
	for i, a := range stems {
		for j, b := range stems {
			if i != j && f.figureFull[a+" "+b] {
				return true
			}
		}
	}
	if !role {
		return false
	}
	for _, st := range stems {
		if f.figureSurname[st] {
			return true
		}
	}
	return false
}

// ctxOrgMarkers — признаки того, что адрес принадлежит организации, а не
// человеку. Формы записи перечислены явно, потому что признак из нескольких
// слов ищется подстрокой.
var ctxOrgMarkers = []string{
	"отделени", "доп. офис", "доп.офис", "доп офис", "допофис",
	"дополнительный офис", "дополнительного офиса", "дополнительном офисе",
	"офис банка", "офисе банка", "офиса банка",
	"головной офис", "головном офисе", "головного офиса",
	"филиал", "банкомат",
	"юридический адрес", "юридического адреса", "юридическому адресу",
	"юр. адрес", "юр.адрес",
	"адрес банка", "адреса банка", "адрес организации", "адреса организации",
	"адрес компании", "адреса компании", "адрес магазина", "адреса магазина",
	"пункт выдачи", "пункта выдачи", "пункте выдачи", "пвз",
	"альфа-банк", "альфабанк", "сбербанк", "втб", "тинькофф",
}

// ctxOrgForms — организационные формы. Ищутся как целое слово: «ао» внутри
// другого слова признаком не является.
var ctxOrgForms = []string{"ао", "оао", "пао", "ооо", "зао", "нао"}

// markOrgAddresses снимает адреса организаций. Каждый признак организации
// помечает ровно один адрес: тот, к которому он относится. Поэтому если в
// предложении есть и адрес клиента, и адрес отделения, снимается только
// ближайший к признаку, а второй остаётся под маскированием.
func (s *ctxScan) markOrgAddresses() {
	hits := ctxFindMarkers(s.low, ctxOrgMarkers, ctxMaxTail)
	hits = append(hits, ctxFindMarkers(s.low, ctxOrgForms, 0)...)
	if len(hits) == 0 {
		return
	}
	var addrs []int
	for i, sp := range s.spans {
		if sp.Type == TypeAddress && s.reasons[i] == "" {
			addrs = append(addrs, i)
		}
	}
	for _, h := range hits {
		if i, ok := s.ownerAddress(h, addrs); ok {
			s.reasons[i] = reasonOrgAddress
		}
	}
}

// ownerAddress выбирает адрес, к которому относится признак организации.
// Сравниваются расстояния в рунах с двумя поправками: метка обычно стоит перед
// значением, поэтому адрес слева от неё менее вероятен, а адрес из соседнего
// предложения менее вероятен, чем адрес из того же предложения.
func (s *ctxScan) ownerAddress(h ctxHit, addrs []int) (int, bool) {
	lo, hi := ctxSentenceBounds(s.d.Text, h.Start)
	best, bestScore := -1, 0
	for _, i := range addrs {
		sp := s.spans[i]
		dist := ctxRuneDistance(s.d, sp.Start, sp.End, h.Start, h.End)
		if dist > ctxOrgWindow {
			continue
		}
		score := dist
		if sp.End <= h.Start {
			score += ctxLeftPenalty
		}
		if sp.Start < lo || sp.Start >= hi {
			score += ctxSentencePenalty
		}
		if best < 0 || score < bestScore {
			best, bestScore = i, score
		}
	}
	return best, best >= 0
}

// ctxHit — найденное вхождение признака вместе с падежным окончанием.
type ctxHit struct {
	Start int
	End   int
}

// ctxFindMarkers собирает все вхождения признаков в строке нижнего регистра.
func ctxFindMarkers(text string, markers []string, maxTail int) []ctxHit {
	var hits []ctxHit
	for _, m := range markers {
		from := 0
		for {
			h, ok := ctxScanMarker(text, m, maxTail, from)
			if !ok {
				break
			}
			hits = append(hits, h)
			from = h.End
		}
	}
	return hits
}

// ctxHasMarker сообщает, что в строке есть хотя бы один признак.
func ctxHasMarker(text string, markers []string) bool {
	for _, m := range markers {
		if _, ok := ctxScanMarker(text, m, ctxMaxTail, 0); ok {
			return true
		}
	}
	return false
}

// ctxScanMarker ищет очередное вхождение признака начиная с позиции from.
// Слева от признака обязана быть граница слова, справа допускается только
// короткое падежное окончание: иначе «инн» находится внутри «терминала».
func ctxScanMarker(text, marker string, maxTail, from int) (ctxHit, bool) {
	if marker == "" || from < 0 || from > len(text) {
		return ctxHit{}, false
	}
	for pos := from; pos <= len(text); {
		idx := strings.Index(text[pos:], marker)
		if idx < 0 {
			return ctxHit{}, false
		}
		start := pos + idx
		end := start + len(marker)
		if tail, ok := ctxMarkerTail(text, marker, end, maxTail); ok && ctxLeftBoundary(text, start) {
			if !ctxStopWords[text[start:end+tail]] {
				return ctxHit{Start: start, End: end + tail}, true
			}
		}
		pos = start + len(marker)
	}
	return ctxHit{}, false
}

// ctxMarkerTail измеряет падежное окончание после признака.
func ctxMarkerTail(text, marker string, end, maxTail int) (int, bool) {
	if !ctxEndsWithLetter(marker) {
		// Признак вида «ул.» заканчивается точкой, окончания у него нет.
		return 0, true
	}
	tail, count := 0, 0
	for _, r := range text[end:] {
		if !unicode.IsLetter(r) {
			break
		}
		count++
		if count > maxTail {
			return 0, false
		}
		tail += utf8.RuneLen(r)
	}
	return tail, true
}

// ctxEndsWithLetter сообщает, что признак заканчивается буквой.
func ctxEndsWithLetter(marker string) bool {
	r, _ := utf8.DecodeLastRuneInString(marker)
	return unicode.IsLetter(r)
}

// ctxLeftBoundary сообщает, что слева от позиции кончается слово.
func ctxLeftBoundary(text string, pos int) bool {
	if pos == 0 {
		return true
	}
	r, _ := decodeLastRuneBefore(text, pos)
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// ctxHasLifeYears ищет годы жизни в скобках: «(1799-1837)». Это такой же
// признак публичного лица, как слово «поэт».
func ctxHasLifeYears(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] == '(' && ctxLifeYearsAt(text[i+1:]) {
			return true
		}
	}
	return false
}

// ctxLifeYearsAt проверяет, что сразу за скобкой идут два года через тире.
func ctxLifeYearsAt(text string) bool {
	rest, ok := ctxTakeYear(text)
	if !ok {
		return false
	}
	sep := 0
	for sep < len(rest) && !ctxIsDigitByte(rest[sep]) {
		sep++
	}
	if sep == 0 || sep > 8 || sep >= len(rest) {
		return false
	}
	_, ok = ctxTakeYear(rest[sep:])
	return ok
}

// ctxTakeYear снимает с начала строки четырёхзначный год.
func ctxTakeYear(text string) (string, bool) {
	i := 0
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if len(text)-i < 4 || (text[i] != '1' && text[i] != '2') {
		return "", false
	}
	for k := i; k < i+4; k++ {
		if !ctxIsDigitByte(text[k]) {
			return "", false
		}
	}
	if len(text) > i+4 && ctxIsDigitByte(text[i+4]) {
		return "", false
	}
	return text[i+4:], true
}

func ctxIsDigitByte(b byte) bool { return b >= '0' && b <= '9' }

// ctxRuneDistance возвращает расстояние между двумя диапазонами в рунах.
// Именно в рунах: для кириллицы байтовое расстояние вдвое больше настоящего.
func ctxRuneDistance(d *Doc, aStart, aEnd, bStart, bEnd int) int {
	if aStart < bEnd && bStart < aEnd {
		return 0
	}
	if aEnd <= bStart {
		return d.runeIndexAt(bStart) - d.runeIndexAt(aEnd)
	}
	return d.runeIndexAt(aStart) - d.runeIndexAt(bEnd)
}

// ctxParagraphBounds возвращает границы абзаца: банковский контекст ищется
// в пределах одного абзаца, соседние абзацы говорят о другом.
func ctxParagraphBounds(text string, off int) (int, int) {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	lo := 0
	if idx := strings.LastIndex(text[:off], "\n\n"); idx >= 0 {
		lo = idx + 2
	}
	hi := len(text)
	if idx := strings.Index(text[off:], "\n\n"); idx >= 0 {
		hi = off + idx
	}
	return lo, hi
}

// ctxAbbreviations — сокращения, после которых точка не заканчивает
// предложение. Без этого списка «ул. Тверская» разрывается пополам.
var ctxAbbreviations = map[string]bool{
	"ул": true, "д": true, "к": true, "корп": true, "стр": true, "кв": true,
	"оф": true, "г": true, "пос": true, "обл": true, "р": true, "им": true,
	"наб": true, "пер": true, "пл": true, "ш": true, "б": true, "мкр": true,
	"лит": true, "эт": true, "пр": true, "просп": true, "тел": true,
	"гр": true, "руб": true, "коп": true, "доп": true, "юр": true, "т": true,
}

// ctxSentenceBounds возвращает границы предложения вокруг смещения.
func ctxSentenceBounds(text string, off int) (int, int) {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	lo := 0
	for i := 0; i < off; i++ {
		if end, ok := ctxSentenceBreak(text, i); ok && end <= off {
			lo = end
		}
	}
	hi := len(text)
	for i := off; i < len(text); i++ {
		if end, ok := ctxSentenceBreak(text, i); ok && end > off {
			hi = end
			break
		}
	}
	return lo, hi
}

// ctxSentenceBreak проверяет, что в позиции i кончается предложение, и
// возвращает начало следующего.
func ctxSentenceBreak(text string, i int) (int, bool) {
	switch text[i] {
	case '\n':
		return i + 1, true
	case '.', '!', '?', ';':
	default:
		return 0, false
	}
	if text[i] == '.' && ctxAbbreviationBefore(text, i) {
		return 0, false
	}
	j := i + 1
	for j < len(text) && (text[j] == ' ' || text[j] == '\t' || text[j] == '\r') {
		j++
	}
	if j == i+1 && j < len(text) {
		// Точка без пробела после неё — часть числа или сокращения.
		return 0, false
	}
	if j < len(text) && ctxStartsLowerOrDigit(text[j:]) {
		return 0, false
	}
	return j, true
}

// ctxStartsLowerOrDigit сообщает, что строка начинается со строчной буквы или
// цифры: новое предложение так не начинается.
func ctxStartsLowerOrDigit(text string) bool {
	r, _ := utf8.DecodeRuneInString(text)
	return unicode.IsDigit(r) || unicode.IsLower(r)
}

// ctxAbbreviationBefore сообщает, что перед точкой стоит сокращение.
func ctxAbbreviationBefore(text string, dot int) bool {
	start := dot
	for start > 0 {
		r, size := decodeLastRuneBefore(text, start)
		if !unicode.IsLetter(r) || dot-start >= 12 {
			break
		}
		start -= size
	}
	if start == dot {
		return false
	}
	return ctxAbbreviations[ctxFoldYo(strings.ToLower(text[start:dot]))]
}

// ctxNormalizeValue приводит значение к виду, пригодному для сравнения:
// нижний регистр, «ё» как «е», одиночные пробелы.
func ctxNormalizeValue(value string) string {
	return strings.Join(strings.Fields(ctxFoldYo(strings.ToLower(value))), " ")
}

// ctxFoldYo заменяет «ё» на «е». Замена не меняет длину строки в байтах,
// поэтому смещения фрагментов после неё остаются верными.
func ctxFoldYo(s string) string {
	if !strings.ContainsRune(s, 'ё') && !strings.ContainsRune(s, 'Ё') {
		return s
	}
	r := strings.NewReplacer("ё", "е", "Ё", "Е")
	return r.Replace(s)
}

// ctxWords разбивает строку на слова из букв.
func ctxWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
}

// ctxCaseEndings — окончания, которые снимаются при сравнении по основе.
// Порядок важен: сначала длинные, иначе «толстого» превратится в «толстог».
var ctxCaseEndings = []string{
	"ого", "его", "ому", "ему", "ыми", "ими",
	"ая", "яя", "ое", "ее", "ые", "ие", "ых", "их",
	"ой", "ый", "ий", "ом", "ем", "ым", "им", "ую", "юю",
	"а", "я", "у", "ю", "ы", "и", "е", "о", "ь", "й",
}

// ctxStem отрезает у слова падежное окончание. Основа нужна, чтобы «Пушкин»,
// «Пушкина» и «Пушкину» сравнивались как одно слово.
func ctxStem(word string) string {
	for _, e := range ctxCaseEndings {
		if !strings.HasSuffix(word, e) || len(word) <= len(e) {
			continue
		}
		stem := word[:len(word)-len(e)]
		if utf8.RuneCountInString(stem) >= ctxMinStem {
			return stem
		}
	}
	return word
}
