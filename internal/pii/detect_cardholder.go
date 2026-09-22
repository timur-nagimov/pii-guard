package pii

import (
	"strings"
	"unicode/utf8"
)

// cardHolderWindow — окно поиска карточного контекста вокруг латинского имени,
// в рунах. На слипе и в выписке имя держателя стоит рядом с номером карты, но
// между ними помещаются срок действия и код безопасности, поэтому окно широкое.
const cardHolderWindow = 120

// cardHolderMaxWords — сколько слов после якоря просматривается. Ограничение не
// даёт якорю из одного предложения дотянуться до имени из следующего.
const cardHolderMaxWords = 6

// cardHolderMaxNameWords — имя держателя на карте состоит не более чем из трёх
// слов: имя, фамилия и иногда отчество или вторая фамилия.
const cardHolderMaxNameWords = 3

// cardHolderAnchors — явные указания на имя держателя карты. Все якоря заданы в
// нижнем регистре, потому что поиск идёт по Doc.Lower. Русские якоря даны
// основой слова: «держатель», «держателя», «держателем» это одно и то же
// указание, а хвост слова проверяет cardHolderAnchorEnd.
var cardHolderAnchors = []string{
	"держател", "владелец карты", "владельца карты",
	"имя на карте", "на карте указано", "на карте написано",
	"cardholder", "card holder", "holder name",
}

// cardHolderNearAnchors — признаки карточного контекста. Рядом с ними имя
// латиницей маскируется и без явного якоря держателя: на карточном реквизите
// подпись держателя не подписывают словом «держатель».
var cardHolderNearAnchors = []string{
	"номер карты", "карт", "card", "pan", "visa", "mastercard", "maestro",
	"valid", "thru", "expires", "срок действия", "действительна до",
	"действует до", "cvv", "cvc", "код безопасности",
}

// cardHolderStopWords — слова, которые не могут быть именем держателя. Список
// из технического задания дополнен кириллическими написаниями тех же брендов:
// жюри печатает реквизиты и по-русски, а проверка идёт по нижнему регистру.
// Карта только читается, поэтому детектор остаётся безопасным для параллельных
// вызовов.
var cardHolderStopWords = map[string]bool{
	"visa": true, "mastercard": true, "maestro": true, "mir": true,
	"world": true, "gold": true, "platinum": true, "classic": true,
	"standard": true, "premium": true, "business": true, "credit": true,
	"debit": true, "bank": true, "alfa": true, "alfabank": true,
	"sber": true, "tinkoff": true, "valid": true, "thru": true,
	"expires": true, "secure": true, "cvv": true, "cvc": true, "pan": true,

	"мир": true, "виза": true, "альфа": true, "альфабанк": true,
	"сбер": true, "сбербанк": true, "тинькофф": true, "банк": true,
	"голд": true, "платинум": true, "классик": true, "премиум": true,
}

// cardHolderServiceWords — служебные слова оформления. Перед именем они
// пропускаются, а внутри имени разрывают последовательность, поэтому строка
// «CARDHOLDER NAME IVAN PETROV» даёт имя из двух слов, а не из четырёх.
var cardHolderServiceWords = map[string]bool{
	"карты": true, "карта": true, "карте": true, "картой": true,
	"указано": true, "указан": true, "указана": true, "имя": true,
	"фио": true, "держатель": true, "держателя": true, "владелец": true,
	"владельца": true, "номер": true, "на": true, "по": true, "для": true,
	"card": true, "cardholder": true, "holder": true, "name": true,
	"number": true, "the": true, "and": true, "for": true, "your": true,
	"our": true, "this": true, "that": true, "with": true, "from": true,
	"please": true, "account": true, "payment": true, "transaction": true,
	"amount": true, "total": true, "limit": true, "balance": true,
	"expiry": true, "expiration": true, "date": true, "code": true,
	"security": true, "issued": true, "sberbank": true, "raiffeisen": true,
	"vtb": true, "pao": true, "ooo": true, "jsc": true, "ltd": true,
	"inc": true, "llc": true, "dear": true, "customer": true,
	"client": true, "hello": true, "regards": true, "thanks": true,
	"thank": true, "will": true, "shall": true, "send": true, "sent": true,
	"receive": true, "new": true, "soon": true, "use": true, "used": true,
	"check": true,
}

// cardHolderGapRunes — знаки, допустимые между якорем и именем и между словами
// имени. Всё остальное считается концом фрагмента.
const cardHolderGapRunes = " \t\r\n :-–—«»\"'.,/()№#"

// cardHolderWord — слово-кандидат в состав имени держателя.
type cardHolderWord struct {
	start    int
	end      int
	lower    string
	kind     Kind
	lineWrap bool
}

// cardHolderDetector находит имя держателя карты. Работает двумя путями: по
// явному якорю и по соседству латинского имени с карточными реквизитами.
type cardHolderDetector struct{}

// NewCardHolderDetector создаёт детектор имени держателя карты.
func NewCardHolderDetector() Detector { return cardHolderDetector{} }

// Types перечисляет типы, которые находит детектор.
func (cardHolderDetector) Types() []Type { return []Type{TypeCardHolder} }

// Detect ищет имя держателя. Сначала берутся фрагменты по явному якорю: они
// точнее, поэтому соседние кандидаты без якоря к ним не добавляются.
func (c cardHolderDetector) Detect(d *Doc) []Span {
	out := c.detectByAnchor(d)
	for _, s := range c.detectNearCard(d) {
		if !cardHolderOverlaps(out, s) {
			out = append(out, s)
		}
	}
	SortSpans(out)
	return out
}

// detectByAnchor находит имя, стоящее сразу после явного указания на держателя.
func (c cardHolderDetector) detectByAnchor(d *Doc) []Span {
	var out []Span
	for _, anchor := range cardHolderAnchors {
		for _, at := range cardHolderAnchorHits(d, anchor) {
			s, ok := cardHolderNameAfter(d, at)
			if !ok || cardHolderOverlaps(out, s) {
				continue
			}
			out = append(out, s)
		}
	}
	return out
}

// cardHolderAnchorHits возвращает смещения концов якоря. Якорь учитывается,
// только если начинается с границы слова: иначе «держатель» находится внутри
// любого слова с таким куском.
func cardHolderAnchorHits(d *Doc, anchor string) []int {
	var out []int
	for pos := 0; pos+len(anchor) <= len(d.Lower); {
		i := strings.Index(d.Lower[pos:], anchor)
		if i < 0 {
			break
		}
		at := pos + i
		pos = at + len(anchor)
		if d.IsTokenBoundary(at, at) {
			out = append(out, pos)
		}
	}
	return out
}

// cardHolderNameAfter собирает имя, идущее за якорем.
func cardHolderNameAfter(d *Doc, anchorEnd int) (Span, bool) {
	from, ok := cardHolderAnchorEnd(d, anchorEnd)
	if !ok {
		return Span{}, false
	}
	name := cardHolderPickName(cardHolderWordsAfter(d, from), 1)
	if len(name) == 0 || !cardHolderLooksLikeValue(d, from, name[0]) {
		return Span{}, false
	}
	start, end := NormalizeSpan(d.Text, name[0].start, name[len(name)-1].end)
	if start >= end {
		return Span{}, false
	}
	return Span{Start: start, End: end, Type: TypeCardHolder, Conf: ConfHigh, Reason: "cardholder:anchor"}, true
}

// cardHolderValueMarks — знаки, которыми оформляют значение поля. После них
// идёт именно значение, а не продолжение фразы.
const cardHolderValueMarks = ":=—–-\"«»\n"

// cardHolderLooksLikeValue отсеивает продолжение обычной фразы. В тексте
// «держатели карт получают бонусы» за якорем идут такие же обычные слова, и
// без этой проверки они попали бы в имя. Признаком значения считается либо
// оформление через двоеточие или тире, либо заглавная буква в первом слове.
func cardHolderLooksLikeValue(d *Doc, from int, first cardHolderWord) bool {
	if from <= first.start && strings.ContainsAny(d.Text[from:first.start], cardHolderValueMarks) {
		return true
	}
	return cardHolderCapitalized(d, first)
}

// cardHolderCapitalized сообщает, что слово начинается с заглавной буквы.
// Имя и на карте, и в тексте пишут с заглавной или капсом, а обычное слово
// внутри фразы — со строчной.
func cardHolderCapitalized(d *Doc, w cardHolderWord) bool {
	upper, _ := firstRune(d.Text[w.start:w.end])
	lower, _ := firstRune(d.Lower[w.start:w.end])
	return upper != lower
}

// cardHolderAnchorEnd доводит конец якоря до конца слова. Якорь «держатель»
// внутри «держателя» это то же самое указание, а внутри «держательница» уже
// другое слово, поэтому длинный хвост считается несовпадением.
func cardHolderAnchorEnd(d *Doc, p int) (int, bool) {
	i := d.TokenIndexAt(p)
	if i < 0 {
		return p, true
	}
	t := d.Tokens[i]
	if (t.Kind != KindLat && t.Kind != KindCyr) || t.Start >= p {
		return p, true
	}
	if utf8.RuneCountInString(d.Text[p:t.End]) > 3 {
		return 0, false
	}
	return t.End, true
}

// cardHolderWordsAfter собирает слова, идущие за смещением, пока между ними
// стоят только допустимые разделители.
func cardHolderWordsAfter(d *Doc, from int) []cardHolderWord {
	i := d.TokenIndexAt(from)
	if i < 0 {
		return nil
	}
	var out []cardHolderWord
	wrap := false
	for ; i < len(d.Tokens) && len(out) < cardHolderMaxWords; i++ {
		t := d.Tokens[i]
		if t.Kind == KindLat || t.Kind == KindCyr {
			out = append(out, cardHolderWord{
				start: t.Start, end: t.End,
				lower: d.Lower[t.Start:t.End], kind: t.Kind, lineWrap: wrap,
			})
			wrap = false
			continue
		}
		if t.Kind != KindSpace && t.Kind != KindPunct {
			break
		}
		gap := d.Text[t.Start:t.End]
		if !cardHolderGapOK(gap) {
			break
		}
		if strings.ContainsAny(gap, "\n\r") {
			wrap = true
		}
	}
	return out
}

// cardHolderGapOK разрешает между словами только пробелы и знаки оформления.
// Один перевод строки допустим: на макете карты подпись держателя часто стоит
// строкой ниже подписи поля. Сам фрагмент имени перевод строки не пересекает,
// это обеспечивает cardHolderPickName.
func cardHolderGapOK(gap string) bool {
	if strings.Count(gap, "\n") > 1 || utf8.RuneCountInString(gap) > 6 {
		return false
	}
	for _, r := range gap {
		if !strings.ContainsRune(cardHolderGapRunes, r) {
			return false
		}
	}
	return true
}

// cardHolderPickName выбирает из слов те, что образуют имя: служебные слова до
// имени пропускаются, внутри имени обрывают его, письменность всех слов имени
// одна и та же.
func cardHolderPickName(words []cardHolderWord, minWords int) []cardHolderWord {
	var out []cardHolderWord
	for _, w := range words {
		if len(out) > 0 && (w.lineWrap || w.kind != out[0].kind) {
			break
		}
		if cardHolderSkipWord(w.lower) {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = append(out, w)
		if len(out) == cardHolderMaxNameWords {
			break
		}
	}
	if len(out) < minWords {
		return nil
	}
	return out
}

// cardHolderSkipWord сообщает, что слово не может быть частью имени: оно из
// стоп-списка, служебное или слишком короткое для имени.
func cardHolderSkipWord(lower string) bool {
	if cardHolderStopWords[lower] || cardHolderServiceWords[lower] {
		return true
	}
	n := utf8.RuneCountInString(lower)
	return n < 2 || n > 24
}

// detectNearCard находит имя латиницей рядом с карточными реквизитами. Якоря
// держателя тут нет, поэтому уверенность ниже, чем у явного указания, а первое
// слово обязано начинаться с заглавной буквы: иначе рядом со словом «card»
// маскируется любая пара английских слов.
func (cardHolderDetector) detectNearCard(d *Doc) []Span {
	var out []Span
	for _, seg := range cardHolderLatinSegments(d) {
		if len(seg) < 2 || len(seg) > cardHolderMaxNameWords {
			continue
		}
		if !cardHolderCapitalized(d, seg[0]) {
			continue
		}
		start, end := NormalizeSpan(d.Text, seg[0].start, seg[len(seg)-1].end)
		if start >= end || !cardHolderCardNear(d, start, end) {
			continue
		}
		out = append(out, Span{
			Start: start, End: end, Type: TypeCardHolder,
			Conf: ConfAnchored, Reason: "cardholder:near_card",
		})
	}
	return out
}

// cardHolderLatinSegments режет текст на последовательности латинских слов.
// Слова из стоп-списка и служебные слова работают разделителями, поэтому из
// строки «IVAN IVANOV VALID THRU» остаётся только имя.
func cardHolderLatinSegments(d *Doc) [][]cardHolderWord {
	var segs [][]cardHolderWord
	var cur []cardHolderWord
	flush := func() {
		if len(cur) > 0 {
			segs = append(segs, cur)
			cur = nil
		}
	}
	for _, t := range d.Tokens {
		text := d.Text[t.Start:t.End]
		switch {
		case t.Kind == KindLat && !cardHolderSkipWord(d.Lower[t.Start:t.End]):
			cur = append(cur, cardHolderWord{start: t.Start, end: t.End, lower: d.Lower[t.Start:t.End], kind: t.Kind})
		case t.Kind == KindSpace && !strings.ContainsAny(text, "\n\r") && utf8.RuneCountInString(text) <= 3:
			// Пробел внутри имени последовательность не разрывает.
		default:
			flush()
		}
	}
	flush()
	return segs
}

// cardHolderCardNear сообщает, что рядом с фрагментом есть карточные реквизиты:
// номер карты из тринадцати-девятнадцати цифр, срок действия или карточный
// якорь.
func cardHolderCardNear(d *Doc, start, end int) bool {
	lo, hi := d.WindowRunes(start, end, cardHolderWindow, cardHolderWindow)
	for _, run := range d.NumRuns() {
		if run.End <= lo || run.Start >= hi {
			continue
		}
		if n := len(run.Digits); n >= 13 && n <= 19 {
			return true
		}
		if cardHolderExpiry(run) {
			return true
		}
	}
	_, ok := d.FindAnchor(start, end, cardHolderNearAnchors, cardHolderWindow, cardHolderWindow)
	return ok
}

// cardHolderExpiry сообщает, что кандидат похож на срок действия карты вида
// 12/25: две цифры месяца, косая черта, две цифры года.
func cardHolderExpiry(run NumRun) bool {
	if run.GroupsPattern() != "2-2" || !strings.Contains(run.Seps, "/") {
		return false
	}
	month := run.Digits[:2]
	return month >= "01" && month <= "12"
}

// cardHolderOverlaps сообщает, что фрагмент пересекается с уже найденным.
func cardHolderOverlaps(spans []Span, s Span) bool {
	for _, e := range spans {
		if e.Overlaps(s) {
			return true
		}
	}
	return false
}

// Ниже — документы, удостоверяющие личность, помимо паспорта России. У них нет
// контрольных сумм, поэтому решение принимается по якорю и по форме записи.

// extraDocAnchorsSNILS — якоря страхового номера. Числовой детектор уже разбирает
// привычные формы, здесь добираются остальные.
var extraDocAnchorsSNILS = []string{
	"снилс", "страховой номер", "страховое свидетельство", "пенсионное страхование",
}

// extraDocAnchorsForeign — якоря заграничного паспорта.
var extraDocAnchorsForeign = []string{
	"загранпаспорт", "заграничный паспорт", "заграничного паспорта",
	"загран. паспорт", "загран паспорт", "passport no", "travel document",
}

// extraDocAnchorsPermit — якоря вида на жительство.
var extraDocAnchorsPermit = []string{
	"вид на жительство", "вида на жительство", "виде на жительство",
	"внж", "residence permit",
}

// extraDocAnchorsBirthCert — якоря свидетельства о рождении.
var extraDocAnchorsBirthCert = []string{
	"свидетельство о рождении", "свидетельства о рождении",
	"свид. о рождении", "свид-во о рождении", "о рождении",
}

// extraDocAnchorsMilitary — якоря военного билета.
var extraDocAnchorsMilitary = []string{
	"военный билет", "военного билета", "военник", "воинский учет",
	"воинский учёт", "military id",
}

// extraDocGapRunes — знаки, допустимые между серией документа и его номером.
// Перевода строки здесь нет: фрагмент не должен пересекать строки.
const extraDocGapRunes = " \t №#:-–—."

// extraDocRomanRunes — знаки римских цифр серии свидетельства о рождении.
// Кириллические х, с, м и і добавлены потому, что серию часто набирают
// русской раскладкой, а символы выглядят одинаково.
const extraDocRomanRunes = "ivxlcdmхсмі"

// extraDocDetector находит документы, удостоверяющие личность, кроме паспорта
// России: страховой номер в неразобранной числовым детектором форме,
// заграничный паспорт, вид на жительство, свидетельство о рождении и военный
// билет. Детектор без состояния и безопасен для параллельного вызова.
type extraDocDetector struct{}

// NewExtraDocumentsDetector создаёт детектор дополнительных документов.
func NewExtraDocumentsDetector() Detector { return extraDocDetector{} }

// Types перечисляет типы, которые находит детектор.
func (extraDocDetector) Types() []Type {
	return []Type{
		TypeSNILS, TypeForeignPassport, TypeResidencePermit,
		TypeBirthCert, TypeMilitaryID,
	}
}

// Detect разбирает числовых кандидатов документа. Один кандидат даёт не более
// одного фрагмента: типы документов различаются якорями и не пересекаются.
func (e extraDocDetector) Detect(d *Doc) []Span {
	runs := d.NumRuns()
	var out []Span
	for i := range runs {
		if s, ok := e.classify(d, runs, i); ok {
			out = append(out, s)
		}
	}
	return out
}

// classify относит числового кандидата к одному из дополнительных документов.
// Порядок проверок идёт от более строгой формы к более свободной.
func (extraDocDetector) classify(d *Doc, runs []NumRun, i int) (Span, bool) {
	if s, ok := extraDocSNILS(d, runs[i]); ok {
		return s, true
	}
	if s, ok := extraDocForeign(d, runs, i); ok {
		return s, true
	}
	if s, ok := extraDocBirthCert(d, runs[i]); ok {
		return s, true
	}
	if s, ok := extraDocMilitary(d, runs[i]); ok {
		return s, true
	}
	return extraDocPermit(d, runs[i])
}

// extraDocSNILS находит страховой номер там, где числовой детектор его не
// берёт. Самый частый случай — номер, начинающийся с семёрки или восьмёрки:
// числовой детектор считает такие одиннадцать цифр телефоном.
func extraDocSNILS(d *Doc, run NumRun) (Span, bool) {
	if len(run.Digits) != 11 {
		return Span{}, false
	}
	if _, ok := d.FindAnchor(run.Start, run.End, extraDocAnchorsSNILS, anchorWindow, nearAnchorWindow); !ok {
		return Span{}, false
	}
	if extraDocSNILSByNumeric(d, run) {
		return Span{}, false
	}
	return extraDocSpan(d, run.Start, run.End, TypeSNILS, ConfHigh, "snils:anchor_extra")
}

// extraDocSNILSByNumeric повторяет условия числового детектора для страхового
// номера. Если фрагмент с теми же границами он уже отдаёт, второй раз его
// возвращать не нужно.
func extraDocSNILSByNumeric(d *Doc, run NumRun) bool {
	if isPhoneShape(run) {
		return false
	}
	pattern := run.GroupsPattern()
	if pattern != "3-3-3-2" && pattern != "11" {
		return false
	}
	if _, ok := d.FindAnchor(run.Start, run.End, anchorsSNILS, anchorWindow, anchorWindow/2); ok {
		return true
	}
	return SNILSValid(run.Digits) && pattern == "3-3-3-2"
}

// extraDocForeign находит заграничный паспорт: две цифры серии и семь цифр
// номера при явном якоре.
func extraDocForeign(d *Doc, runs []NumRun, i int) (Span, bool) {
	end, ok := extraDocForeignShape(d, runs, i)
	if !ok {
		return Span{}, false
	}
	start := runs[i].Start
	// Якорь ищем только слева и без чисел между ним и значением: иначе в
	// перечислении документов якорь заграничного паспорта дотягивается до
	// номера следующего документа.
	if _, ok := d.AnchorBefore(start, extraDocAnchorsForeign, anchorWindow); !ok {
		return Span{}, false
	}
	return extraDocSpan(d, start, end, TypeForeignPassport, ConfHigh, "foreign_passport:anchor_shape")
}

// extraDocForeignShape проверяет форму заграничного паспорта и возвращает конец
// фрагмента. Серия и номер могут быть разделены словом «номер», тогда это два
// соседних числовых кандидата.
func extraDocForeignShape(d *Doc, runs []NumRun, i int) (int, bool) {
	run := runs[i]
	if pattern := run.GroupsPattern(); pattern == "2-7" || pattern == "9" {
		return run.End, true
	}
	if len(run.Digits) != 2 || i+1 >= len(runs) {
		return 0, false
	}
	next := runs[i+1]
	if len(next.Digits) != 7 {
		return 0, false
	}
	gap := d.Lower[run.End:next.Start]
	if strings.ContainsAny(gap, "\n\r") || utf8.RuneCountInString(gap) > 16 || !onlyConnectors(gap) {
		return 0, false
	}
	return next.End, true
}

// extraDocBirthCert находит свидетельство о рождении. Форма серии — римские
// цифры, дефис и две буквы; если серии нет, хватает якоря прямо перед номером.
func extraDocBirthCert(d *Doc, run NumRun) (Span, bool) {
	if len(run.Groups) != 1 || len(run.Digits) < 5 || len(run.Digits) > 7 {
		return Span{}, false
	}
	if start, ok := extraDocCertSeries(d, run.Start); ok {
		if _, found := d.FindAnchor(start, run.End, extraDocAnchorsBirthCert, anchorWindow, nearAnchorWindow); found {
			return extraDocSpan(d, start, run.End, TypeBirthCert, ConfHigh, "birth_cert:series_number")
		}
		return Span{}, false
	}
	if _, found := d.AnchorBefore(run.Start, extraDocAnchorsBirthCert, anchorWindow); !found {
		return Span{}, false
	}
	return extraDocSpan(d, run.Start, run.End, TypeBirthCert, ConfAnchored, "birth_cert:anchor")
}

// extraDocMilitary находит военный билет: две буквы серии и семь цифр номера
// при якоре. Без серии номер берётся, только если якорь стоит прямо перед ним.
func extraDocMilitary(d *Doc, run NumRun) (Span, bool) {
	if len(run.Groups) != 1 || len(run.Digits) != 7 {
		return Span{}, false
	}
	if start, ok := extraDocSeries(d, run.Start); ok {
		if _, found := d.FindAnchor(start, run.End, extraDocAnchorsMilitary, anchorWindow, nearAnchorWindow); found {
			return extraDocSpan(d, start, run.End, TypeMilitaryID, ConfHigh, "military_id:series_number")
		}
		return Span{}, false
	}
	if _, found := d.AnchorBefore(run.Start, extraDocAnchorsMilitary, anchorWindow); !found {
		return Span{}, false
	}
	return extraDocSpan(d, run.Start, run.End, TypeMilitaryID, ConfAnchored, "military_id:anchor")
}

// extraDocPermit находит номер вида на жительство. Единой формы у него нет,
// поэтому требуется якорь слева без других чисел между якорем и номером.
func extraDocPermit(d *Doc, run NumRun) (Span, bool) {
	if n := len(run.Digits); n < 6 || n > 12 {
		return Span{}, false
	}
	if _, ok := d.AnchorBefore(run.Start, extraDocAnchorsPermit, anchorWindow); !ok {
		return Span{}, false
	}
	return extraDocSpan(d, run.Start, run.End, TypeResidencePermit, ConfAnchored, "residence_permit:anchor")
}

// extraDocSpan собирает фрагмент с обрезанной по краям пунктуацией.
func extraDocSpan(d *Doc, start, end int, t Type, conf float64, reason string) (Span, bool) {
	s, e := NormalizeSpan(d.Text, start, end)
	if s >= e {
		return Span{}, false
	}
	return Span{Start: s, End: e, Type: t, Conf: conf, Reason: reason}, true
}

// extraDocSeries возвращает начало двухбуквенной серии, стоящей прямо перед
// номером документа.
func extraDocSeries(d *Doc, at int) (int, bool) {
	i := d.TokenIndexAt(at)
	if i < 0 {
		return 0, false
	}
	j, ok := extraDocPrevWord(d, i-1)
	if !ok || !extraDocIsLetters(d, j, 2) {
		return 0, false
	}
	return d.Tokens[j].Start, true
}

// extraDocCertSeries возвращает начало серии свидетельства о рождении вида
// «II-МЮ», стоящей прямо перед номером.
func extraDocCertSeries(d *Doc, at int) (int, bool) {
	i := d.TokenIndexAt(at)
	if i < 0 {
		return 0, false
	}
	j, ok := extraDocPrevWord(d, i-1)
	if !ok || !extraDocIsLetters(d, j, 2) {
		return 0, false
	}
	k, ok := extraDocPrevSpace(d, j-1)
	if !ok || !extraDocIsDash(d, k) {
		return 0, false
	}
	m, ok := extraDocPrevSpace(d, k-1)
	if !ok {
		return 0, false
	}
	return extraDocRomanStart(d, m)
}

// extraDocPrevWord возвращает индекс ближайшего слова слева, пропуская
// допустимые разделители.
func extraDocPrevWord(d *Doc, i int) (int, bool) {
	for j := i; j >= 0; j-- {
		t := d.Tokens[j]
		if t.Kind != KindSpace && t.Kind != KindPunct {
			return j, true
		}
		if !extraDocGapOK(d.Text[t.Start:t.End]) {
			return 0, false
		}
	}
	return 0, false
}

// extraDocPrevSpace возвращает индекс ближайшего непробельного токена слева.
func extraDocPrevSpace(d *Doc, i int) (int, bool) {
	for j := i; j >= 0; j-- {
		t := d.Tokens[j]
		if t.Kind != KindSpace {
			return j, true
		}
		if !extraDocGapOK(d.Text[t.Start:t.End]) {
			return 0, false
		}
	}
	return 0, false
}

// extraDocGapOK разрешает между частями документа только короткие разделители
// без перевода строки.
func extraDocGapOK(s string) bool {
	if utf8.RuneCountInString(s) > 4 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(extraDocGapRunes, r) {
			return false
		}
	}
	return true
}

// extraDocIsLetters сообщает, что токен состоит ровно из n букв.
func extraDocIsLetters(d *Doc, i, n int) bool {
	t := d.Tokens[i]
	if t.Kind != KindCyr && t.Kind != KindLat {
		return false
	}
	return utf8.RuneCountInString(d.Text[t.Start:t.End]) == n
}

// extraDocIsDash сообщает, что токен состоит только из дефисов или тире.
func extraDocIsDash(d *Doc, i int) bool {
	t := d.Tokens[i]
	return t.Kind == KindPunct && strings.Trim(d.Text[t.Start:t.End], "-–—") == ""
}

// extraDocIsRoman сообщает, что токен похож на римскую цифру серии.
func extraDocIsRoman(d *Doc, i int) bool {
	t := d.Tokens[i]
	if t.Kind != KindCyr && t.Kind != KindLat {
		return false
	}
	body := d.Lower[t.Start:t.End]
	n := utf8.RuneCountInString(body)
	if n < 1 || n > 4 {
		return false
	}
	for _, r := range body {
		if !strings.ContainsRune(extraDocRomanRunes, r) {
			return false
		}
	}
	return true
}

// extraDocRomanStart возвращает начало римской цифры. Серию набирают и
// смешанной раскладкой, тогда токенизатор делит её на соседние куски разной
// письменности, и их нужно собрать обратно.
func extraDocRomanStart(d *Doc, i int) (int, bool) {
	if !extraDocIsRoman(d, i) {
		return 0, false
	}
	start := d.Tokens[i].Start
	for j := i - 1; j >= 0; j-- {
		if d.Tokens[j].End != d.Tokens[j+1].Start || !extraDocIsRoman(d, j) {
			break
		}
		start = d.Tokens[j].Start
	}
	return start, true
}
