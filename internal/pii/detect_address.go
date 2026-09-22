package pii

import (
	"sort"
	"strings"
	"unicode"

	"pii-guard/internal/pii/dict"
)

// addrAnchors — якорные слова адреса. Одиночный компонент считается адресом
// только рядом с одним из них: название города само по себе персональными
// данными не является, фраза «поехал в Казань» маскироваться не должна.
var addrAnchors = []string{
	"адрес", "проживает", "проживающ", "прописан", "прописка",
	"зарегистрирован", "регистрац", "доставк", "место жительства",
	"жительств", "живет", "живёт", "пребывани",
}

// addrAnchorWindow — окно поиска якоря слева от фрагмента, в рунах.
const addrAnchorWindow = 48

// addrMaxRunes — наибольшая длина адресного фрагмента в рунах. Компоненты
// дальше этого расстояния относятся уже к другому адресу.
const addrMaxRunes = 120

// addrSettlementTypes — типы населённых пунктов. Ключ задаётся в нижнем
// регистре без точки, значение попадает в поле Reason.
var addrSettlementTypes = map[string]string{
	"г": "city", "гор": "city", "город": "city", "города": "city",
	"городе": "city", "г-д": "city", "гп": "city",
	"с": "city", "село": "city", "села": "city", "селе": "city",
	"п": "city", "пос": "city", "поселок": "city", "посёлок": "city",
	"поселке": "city", "посёлке": "city", "пгт": "city", "рп": "city",
	"д": "city", "дер": "city", "деревня": "city", "деревне": "city",
	"х": "city", "хутор": "city", "ст-ца": "city", "станица": "city",
	"станице": "city", "снт": "city", "днп": "city", "аул": "city",
	"улус": "city", "г-к": "city",
	"gorod": "city", "g": "city", "pos": "city", "derevnya": "city",
}

// addrRegionTypes — типы регионов и районов.
var addrRegionTypes = map[string]string{
	"обл": "region", "область": "region", "области": "region",
	"край": "region", "края": "region", "крае": "region",
	"респ": "region", "республика": "region", "республики": "region",
	"республике": "region", "р-н": "region", "район": "region",
	"района": "region", "районе": "region", "округ": "region",
	"округа": "region", "ао": "region", "губерния": "region",
}

// addrStreetTypes — типы улиц, включая английские сокращения.
var addrStreetTypes = map[string]string{
	"ул": "street", "улица": "street", "улице": "street", "улицы": "street",
	"пр-т": "street", "пр-кт": "street", "просп": "street",
	"проспект": "street", "проспекте": "street", "пр": "street",
	"пер": "street", "переулок": "street", "переулке": "street",
	"б-р": "street", "бул": "street", "бульвар": "street",
	"ш": "street", "шоссе": "street",
	"наб": "street", "набережная": "street", "набережной": "street",
	"проезд": "street", "проезде": "street", "туп": "street",
	"тупик": "street", "мкр": "street", "микрорайон": "street",
	"кв-л": "street", "квартал": "street", "пл": "street",
	"площадь": "street", "аллея": "street", "линия": "street",
	"тракт": "street", "street": "street", "st": "street", "ave": "street",
	"avenue": "street", "rd": "street", "road": "street", "ln": "street",
	"lane": "street", "blvd": "street", "boulevard": "street",
	"dr": "street", "drive": "street", "sq": "street", "square": "street",
	"hwy": "street", "highway": "street",
	"ul": "street", "ulitsa": "street", "prospekt": "street",
	"pereulok": "street", "naberezhnaya": "street", "shosse": "street",
	"proezd": "street", "bulvar": "street",
}

// addrHouseTypes — типы адресных номеров: дом, корпус, строение, квартира,
// офис и помещение.
var addrHouseTypes = map[string]string{
	"д": "house", "дом": "house", "дома": "house", "доме": "house",
	"вл": "house", "влад": "house", "владение": "house",
	"к": "corp", "кор": "corp", "корп": "corp", "корпус": "corp",
	"корпусе": "corp", "стр": "building", "строение": "building",
	"строении": "building", "кв": "flat", "кварт": "flat",
	"квартира": "flat", "квартире": "flat", "квартиры": "flat",
	"оф": "office", "офис": "office", "офисе": "office",
	"пом": "premise", "помещение": "premise", "помещении": "premise",
	"лит": "premise", "литера": "premise", "эт": "premise", "этаж": "premise",
	"apt": "flat", "apartment": "flat", "suite": "flat", "ste": "flat",
	"unit": "flat", "room": "flat", "bld": "building", "bldg": "building",
	"office": "office", "floor": "premise",
	"dom": "house", "korp": "corp", "kv": "flat", "kvartira": "flat",
	"str": "building",
}

// addrStopNames — слова, которые не могут быть названием улицы или пункта.
// Без них якорное слово из соседнего предложения попадает во фрагмент.
var addrStopNames = map[string]bool{
	"и": true, "или": true, "а": true, "но": true, "не": true, "что": true,
	"это": true, "же": true, "тел": true, "телефон": true, "паспорт": true,
	"инн": true, "снилс": true, "email": true, "почта": true, "дата": true,
	"серия": true, "номер": true, "выдан": true, "код": true, "сумма": true,
	"заказ": true, "договор": true, "банк": true, "отделение": true,
	"филиал": true, "рождения": true, "рожд": true, "год": true,
	"года": true, "году": true, "лет": true, "руб": true, "рублей": true,
}

// addrNameConnectors — служебные слова внутри названия улицы: «наб. реки
// Фонтанки», «ул. имени Гагарина».
var addrNameConnectors = map[string]bool{
	"реки": true, "имени": true, "им": true, "маршала": true,
	"генерала": true, "академика": true, "братьев": true, "the": true,
}

// addrGlueWords — слова, допустимые между компонентами одного адреса:
// «проживает в г. Москва на ул. Ленина».
var addrGlueWords = map[string]bool{
	"в": true, "во": true, "на": true, "по": true, "у": true, "из": true,
}

// addrStreetSuffixes — окончания названий улиц, записанных без типа:
// «Тверская 12-45».
var addrStreetSuffixes = []string{"ская", "ский", "ское", "ской", "ском", "ские"}

// addrWord — значимое слово документа: буквенный или цифровой токен.
// Знаки препинания и пробелы в список не попадают, они разбираются как
// промежутки между словами.
type addrWord struct {
	start int
	end   int
	lower string
	kind  Kind
	title bool
}

// addrComp — найденный компонент адреса вместе с его признаками. Один
// компонент может нести два признака сразу: «Тверская 12» это улица и дом.
type addrComp struct {
	start int
	end   int
	kinds []string
}

// addressDetector находит почтовые адреса по набору опознанных компонентов.
// Детектор не хранит состояния и безопасен для параллельного вызова.
type addressDetector struct{}

// NewAddressDetector создаёт детектор адресов.
func NewAddressDetector() Detector { return addressDetector{} }

// Types перечисляет типы, которые находит детектор.
func (addressDetector) Types() []Type { return []Type{TypeAddress} }

// Detect собирает компоненты адреса и объединяет соседние в один фрагмент.
func (addressDetector) Detect(d *Doc) []Span {
	ws := addrWords(d)
	comps := addrScan(d, ws)
	comps = addrWithPostcodes(d, comps)
	return addrSpans(d, comps)
}

// addrWithPostcodes добавляет к компонентам почтовый индекс, стоящий сразу за
// якорем адреса: запись «Адрес: 117312» состоит из одного индекса, и без этого
// компонента фрагмент вообще не находится.
func addrWithPostcodes(d *Doc, comps []addrComp) []addrComp {
	for _, run := range d.NumRuns() {
		if run.GroupsPattern() != "6" || addrForeignNumber(d, run) {
			continue
		}
		if _, ok := d.AnchorBefore(run.Start, addrAnchors, addrAnchorWindow); !ok {
			continue
		}
		if addrOverlaps(comps, run.Start, run.End) {
			continue
		}
		comps = append(comps, addrComp{start: run.Start, end: run.End, kinds: []string{"postcode"}})
	}
	sort.SliceStable(comps, func(i, j int) bool { return comps[i].start < comps[j].start })
	return comps
}

// addrOverlaps сообщает, что диапазон уже занят найденным компонентом.
func addrOverlaps(comps []addrComp, start, end int) bool {
	for _, c := range comps {
		if c.start < end && start < c.end {
			return true
		}
	}
	return false
}

// addrWords выбирает из документа значимые слова с их регистром.
func addrWords(d *Doc) []addrWord {
	ws := make([]addrWord, 0, len(d.Tokens)/2+1)
	for _, t := range d.Tokens {
		if t.Kind != KindCyr && t.Kind != KindLat && t.Kind != KindDigit {
			continue
		}
		ws = append(ws, addrWord{
			start: t.Start,
			end:   t.End,
			lower: d.Lower[t.Start:t.End],
			kind:  t.Kind,
			title: addrIsTitle(d.Text[t.Start:t.End]),
		})
	}
	return ws
}

// addrIsTitle сообщает, что слово начинается с заглавной буквы. Регистр не
// является условием обнаружения, он только уточняет границы названия.
func addrIsTitle(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

// addrGap возвращает текст между словом i и следующим словом.
func addrGap(d *Doc, ws []addrWord, i int) string {
	if i+1 >= len(ws) {
		return ""
	}
	return d.Text[ws[i].end:ws[i+1].start]
}

// addrShortGap проверяет, что промежуток внутри компонента короткий и состоит
// только из пробелов и служебных знаков. Перевод строки компонент разрывает.
func addrShortGap(gap string) bool {
	if len([]rune(gap)) > 3 {
		return false
	}
	for _, r := range gap {
		if !strings.ContainsRune(" .№#-", r) {
			return false
		}
	}
	return true
}

// addrScan проходит по словам слева направо и собирает компоненты адреса.
func addrScan(d *Doc, ws []addrWord) []addrComp {
	var comps []addrComp
	for i := 0; i < len(ws); {
		c, next, ok := addrMatchComponent(d, ws, i)
		if !ok || next <= i {
			i++
			continue
		}
		c, next = addrAttachHouseNumber(d, ws, c, next)
		comps = append(comps, c)
		i = next
	}
	return comps
}

// addrAttachHouseNumber присоединяет к улице номер дома, записанный без типа:
// «пр-т Мира, 15». Длинные числа не берутся: шесть цифр это индекс, а не дом.
func addrAttachHouseNumber(d *Doc, ws []addrWord, c addrComp, next int) (addrComp, int) {
	if next >= len(ws) || ws[next].kind != KindDigit || len(ws[next].lower) > 4 {
		return c, next
	}
	if !addrHasKind(c, "street") || addrHasKind(c, "house") {
		return c, next
	}
	gap := d.Text[c.end:ws[next].start]
	if len([]rune(gap)) > 3 {
		return c, next
	}
	for _, r := range gap {
		if !strings.ContainsRune(" ,.№#", r) {
			return c, next
		}
	}
	end, last := addrNumberEnd(d, ws, next)
	c.end = end
	c.kinds = append(c.kinds, "house")
	return c, last + 1
}

// addrHasKind сообщает, что у компонента есть указанный признак.
func addrHasKind(c addrComp, kind string) bool {
	for _, k := range c.kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// addrMatchComponent пробует опознать компонент, начинающийся со слова i.
// Порядок проверок важен: «д. 5» это дом, а «д. Малые Горки» деревня, их
// различает только то, что стоит за типом.
func addrMatchComponent(d *Doc, ws []addrWord, i int) (addrComp, int, bool) {
	if ws[i].kind == KindDigit {
		return addrMatchNumberStreet(d, ws, i)
	}
	if c, n, ok := addrMatchHouse(d, ws, i); ok {
		return c, n, true
	}
	if c, n, ok := addrMatchTyped(d, ws, i, addrSettlementTypes); ok {
		return c, n, true
	}
	if c, n, ok := addrMatchTyped(d, ws, i, addrRegionTypes); ok {
		return c, n, true
	}
	if c, n, ok := addrMatchTyped(d, ws, i, addrStreetTypes); ok {
		return c, n, true
	}
	if c, n, ok := addrMatchBareStreet(d, ws, i); ok {
		return c, n, true
	}
	return addrMatchGeo(d, ws, i)
}

// addrLookupType распознаёт слово как тип компонента. Составные сокращения
// вида «пр-т» и «ст-ца» разбиты токенизатором на части, поэтому сначала
// проверяется склейка через дефис.
//
// Односимвольный тип требует точки: без неё предлог «с» из «с 5 утра»
// превращается в название села.
func addrLookupType(d *Doc, ws []addrWord, i int, types map[string]string) (string, int, bool) {
	if ws[i].kind == KindDigit {
		return "", 0, false
	}
	if i+1 < len(ws) && addrGap(d, ws, i) == "-" {
		if kind, ok := types[ws[i].lower+"-"+ws[i+1].lower]; ok {
			return kind, i + 2, true
		}
	}
	kind, ok := types[ws[i].lower]
	if !ok {
		return "", 0, false
	}
	if len([]rune(ws[i].lower)) == 1 && !strings.HasPrefix(addrGap(d, ws, i), ".") {
		return "", 0, false
	}
	return kind, i + 1, true
}

// addrIsTypeWord сообщает, что слово является каким-либо адресным типом.
func addrIsTypeWord(w string) bool {
	if _, ok := addrStreetTypes[w]; ok {
		return true
	}
	if _, ok := addrHouseTypes[w]; ok {
		return true
	}
	if _, ok := addrRegionTypes[w]; ok {
		return true
	}
	_, ok := addrSettlementTypes[w]
	return ok
}

// addrNameWord сообщает, что слово годится на роль названия улицы или пункта.
func addrNameWord(ws []addrWord, i int) bool {
	if i < 0 || i >= len(ws) {
		return false
	}
	w := ws[i]
	if w.kind != KindCyr && w.kind != KindLat {
		return false
	}
	if addrStopNames[w.lower] || addrGlueWords[w.lower] {
		return false
	}
	single := len([]rune(w.lower)) == 1
	if addrIsTypeWord(w.lower) && !(single && w.title) {
		return false
	}
	return !single || w.title
}

// addrName собирает название после типа и возвращает его конец и индекс
// следующего необработанного слова.
func addrName(d *Doc, ws []addrWord, i int) (int, int, bool) {
	if i <= 0 || i >= len(ws) {
		return 0, 0, false
	}
	if !addrShortGap(d.Text[ws[i-1].end:ws[i].start]) {
		return 0, 0, false
	}
	if ws[i].kind == KindDigit {
		return addrNumericName(d, ws, i)
	}
	if !addrNameWord(ws, i) {
		return 0, 0, false
	}
	end, last := ws[i].end, i
	for j := i + 1; j < len(ws) && j-i < 3; j++ {
		if !addrNameContinues(d, ws, j) {
			break
		}
		end, last = ws[j].end, j
	}
	return end, last + 1, true
}

// addrNumericName разбирает название, начинающееся с числа: «ул. 8 Марта».
// Число без следующего слова названием не считается, иначе «с. 5 по 10»
// превращается в населённый пункт.
func addrNumericName(d *Doc, ws []addrWord, i int) (int, int, bool) {
	if i+1 >= len(ws) || addrGap(d, ws, i) != " " || !addrNameWord(ws, i+1) {
		return 0, 0, false
	}
	return ws[i+1].end, i + 2, true
}

// addrNameContinues решает, входит ли слово j в составное название вроде
// «Малые Горки». Слово не берётся, если оно вплотную предшествует другому
// типу: в «Ивановское Московской обл.» слово «Московской» относится к области.
func addrNameContinues(d *Doc, ws []addrWord, j int) bool {
	gap := d.Text[ws[j-1].end:ws[j].start]
	if gap != " " && gap != "-" {
		return false
	}
	if !addrNameWord(ws, j) {
		return false
	}
	if !ws[j].title && !addrNameConnectors[ws[j].lower] {
		return false
	}
	if j+1 < len(ws) && addrGap(d, ws, j) == " " && addrIsTypeWord(ws[j+1].lower) {
		return false
	}
	return true
}

// addrMatchTyped опознаёт компонент вида «тип плюс название» в любом порядке:
// и «ул. Ленина», и «Ленинская ул.», и «Baker Street».
func addrMatchTyped(d *Doc, ws []addrWord, i int, types map[string]string) (addrComp, int, bool) {
	if i < 0 || i >= len(ws) {
		return addrComp{}, 0, false
	}
	if kind, next, ok := addrLookupType(d, ws, i, types); ok {
		end, after, named := addrName(d, ws, next)
		if !named {
			return addrComp{}, 0, false
		}
		return addrComp{start: ws[i].start, end: end, kinds: []string{kind}}, after, true
	}
	return addrMatchNameFirst(d, ws, i, types)
}

// addrMatchNameFirst разбирает обратный порядок «название плюс тип». Название
// из словаря сюда не пускается: в записи «Москва ул. Ленина» город не является
// названием улицы.
func addrMatchNameFirst(d *Doc, ws []addrWord, i int, types map[string]string) (addrComp, int, bool) {
	if !addrNameWord(ws, i) || addrGap(d, ws, i) != " " {
		return addrComp{}, 0, false
	}
	if _, known := dict.Place(ws[i].lower); known {
		return addrComp{}, 0, false
	}
	kind, next, ok := addrLookupType(d, ws, i+1, types)
	if !ok {
		return addrComp{}, 0, false
	}
	return addrComp{start: ws[i].start, end: ws[next-1].end, kinds: []string{kind}}, next, true
}

// addrMatchHouse опознаёт номер дома, корпуса, строения, квартиры или офиса.
func addrMatchHouse(d *Doc, ws []addrWord, i int) (addrComp, int, bool) {
	kind, next, ok := addrLookupType(d, ws, i, addrHouseTypes)
	if !ok || next >= len(ws) || ws[next].kind != KindDigit {
		return addrComp{}, 0, false
	}
	if !addrShortGap(d.Text[ws[next-1].end:ws[next].start]) {
		return addrComp{}, 0, false
	}
	end, last := addrNumberEnd(d, ws, next)
	return addrComp{start: ws[i].start, end: end, kinds: []string{kind}}, last + 1, true
}

// addrNumberEnd расширяет номер дома на приклеенную литеру и на дробную часть:
// «5а», «12/1», «12-45».
func addrNumberEnd(d *Doc, ws []addrWord, i int) (int, int) {
	end, last := ws[i].end, i
	for j := last + 1; j < len(ws); j++ {
		gap := d.Text[ws[j-1].end:ws[j].start]
		letter := gap == "" && ws[j].kind != KindDigit && len([]rune(ws[j].lower)) == 1
		fraction := (gap == "/" || gap == "-") && ws[j].kind == KindDigit
		if !letter && !fraction {
			break
		}
		end, last = ws[j].end, j
	}
	return end, last
}

// addrMatchBareStreet опознаёт слитную запись «Тверская 12-45»: название с
// уличным окончанием и сразу за ним номер дома.
func addrMatchBareStreet(d *Doc, ws []addrWord, i int) (addrComp, int, bool) {
	w := ws[i]
	if w.kind != KindCyr || !w.title || !addrHasStreetSuffix(w.lower) {
		return addrComp{}, 0, false
	}
	j := i + 1
	if j >= len(ws) || ws[j].kind != KindDigit {
		return addrComp{}, 0, false
	}
	if !addrShortGap(d.Text[w.end:ws[j].start]) {
		return addrComp{}, 0, false
	}
	end, last := addrNumberEnd(d, ws, j)
	return addrComp{start: w.start, end: end, kinds: []string{"street", "house"}}, last + 1, true
}

// addrHasStreetSuffix проверяет улично-прилагательное окончание названия.
func addrHasStreetSuffix(w string) bool {
	for _, s := range addrStreetSuffixes {
		if strings.HasSuffix(w, s) {
			return true
		}
	}
	return false
}

// addrMatchNumberStreet разбирает английский порядок «12 Baker Street», где
// номер дома стоит перед названием улицы.
func addrMatchNumberStreet(d *Doc, ws []addrWord, i int) (addrComp, int, bool) {
	if len(ws[i].lower) > 4 {
		return addrComp{}, 0, false
	}
	_, last := addrNumberEnd(d, ws, i)
	if addrGap(d, ws, last) != " " {
		return addrComp{}, 0, false
	}
	c, next, ok := addrMatchTyped(d, ws, last+1, addrStreetTypes)
	if !ok {
		return addrComp{}, 0, false
	}
	c.start = ws[i].start
	c.kinds = append([]string{"house"}, c.kinds...)
	return c, next, true
}

// addrMatchGeo опознаёт название из словаря городов и стран. Пробуются сначала
// длинные склейки, чтобы «Нижний Новгород» не распался на два слова.
func addrMatchGeo(d *Doc, ws []addrWord, i int) (addrComp, int, bool) {
	if ws[i].kind == KindDigit {
		return addrComp{}, 0, false
	}
	for n := dict.MaxPlaceWords; n >= 1; n-- {
		j := i + n - 1
		if j >= len(ws) || !addrWordsJoinable(d, ws, i, j) {
			continue
		}
		kind, ok := dict.Place(d.Lower[ws[i].start:ws[j].end])
		if !ok {
			continue
		}
		return addrComp{start: ws[i].start, end: ws[j].end, kinds: []string{kind}}, j + 1, true
	}
	return addrComp{}, 0, false
}

// addrWordsJoinable проверяет, что слова с i по j соединены только пробелом
// или дефисом и могут образовать одно название.
func addrWordsJoinable(d *Doc, ws []addrWord, i, j int) bool {
	for k := i; k < j; k++ {
		if gap := addrGap(d, ws, k); gap != " " && gap != "-" {
			return false
		}
	}
	return true
}

// addrSpans объединяет соседние компоненты в группы и превращает группы во
// фрагменты.
func addrSpans(d *Doc, comps []addrComp) []Span {
	var out []Span
	for i := 0; i < len(comps); {
		j := addrGroupEnd(d, comps, i)
		if s, ok := addrSpanOf(d, comps[i:j]); ok {
			out = append(out, s)
		}
		i = j
	}
	return out
}

// addrGroupEnd возвращает индекс за последним компонентом группы.
func addrGroupEnd(d *Doc, comps []addrComp, i int) int {
	j := i + 1
	for j < len(comps) {
		if !addrJoinGap(d.Lower[comps[j-1].end:comps[j].start]) {
			break
		}
		if d.runeIndexAt(comps[j].end)-d.runeIndexAt(comps[i].start) > addrMaxRunes {
			break
		}
		j++
	}
	return j
}

// addrJoinGap проверяет, что между компонентами стоят только знаки препинания,
// пробелы и связки вроде «в» и «на». Посторонние слова и числа означают, что
// компоненты относятся к разным местам текста.
func addrJoinGap(gap string) bool {
	if strings.ContainsAny(gap, "\n\r;:") {
		return false
	}
	if len([]rune(gap)) > 24 {
		return false
	}
	for _, r := range gap {
		if r >= '0' && r <= '9' {
			return false
		}
	}
	for _, w := range strings.FieldsFunc(gap, addrNotLetter) {
		if !addrGlueWords[w] {
			return false
		}
	}
	return true
}

func addrNotLetter(r rune) bool { return !unicode.IsLetter(r) }

// addrSpanOf строит фрагмент по группе компонентов. Два и более компонента
// дают высокую уверенность, одиночный компонент требует якоря.
func addrSpanOf(d *Doc, group []addrComp) (Span, bool) {
	start, end := group[0].start, group[len(group)-1].end
	kinds := addrKinds(group)
	count := addrCount(group)
	start, end, withIndex := addrAttachPostcode(d, start, end)
	if withIndex {
		kinds = append([]string{"postcode"}, kinds...)
		count++
	}
	conf := ConfHigh
	if count < 2 {
		if _, ok := d.HasAnchorBefore(start, addrAnchors, addrAnchorWindow); !ok {
			return Span{}, false
		}
		conf = ConfAnchored
	}
	lo, hi := d.LineBounds(start)
	if start < lo {
		start = lo
	}
	if end > hi {
		end = hi
	}
	start, end = NormalizeSpan(d.Text, start, end)
	if start >= end {
		return Span{}, false
	}
	return Span{
		Start:  start,
		End:    end,
		Type:   TypeAddress,
		Conf:   conf,
		Reason: "address:" + strings.Join(kinds, "+"),
	}, true
}

// addrKinds перечисляет признаки группы без повторов, сохраняя порядок.
func addrKinds(group []addrComp) []string {
	seen := make(map[string]bool, 4)
	out := make([]string, 0, 4)
	for _, c := range group {
		for _, k := range c.kinds {
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// addrCount считает признаки группы вместе с повторами: два дома в одной
// строке это тоже два компонента.
func addrCount(group []addrComp) int {
	n := 0
	for _, c := range group {
		n += len(c.kinds)
	}
	return n
}

// addrAttachPostcode присоединяет к фрагменту шестизначный индекс, стоящий
// вплотную перед адресом или сразу после него. Индекс рядом с паспортными или
// заказными якорями не берётся: там шесть цифр значат другое.
func addrAttachPostcode(d *Doc, start, end int) (int, int, bool) {
	for _, run := range d.NumRuns() {
		if run.GroupsPattern() != "6" || addrForeignNumber(d, run) {
			continue
		}
		if run.End <= start && addrPunctGap(d.Text[run.End:start]) {
			return run.Start, end, true
		}
		if run.Start >= end && addrPunctGap(d.Text[end:run.Start]) {
			return start, run.End, true
		}
	}
	return start, end, false
}

// addrForeignNumber сообщает, что шесть цифр принадлежат другой сущности:
// номеру паспорта, заказа или договора.
func addrForeignNumber(d *Doc, run NumRun) bool {
	if _, ok := d.AnchorBefore(run.Start, anchorsPassport, nearAnchorWindow); ok {
		return true
	}
	_, ok := d.AnchorBefore(run.Start, negAnchorsOrder, nearAnchorWindow)
	return ok
}

// addrPunctGap проверяет, что между индексом и адресом стоят только пробелы и
// запятые.
func addrPunctGap(gap string) bool {
	if len([]rune(gap)) > 4 {
		return false
	}
	for _, r := range gap {
		if !strings.ContainsRune(" ,.\t", r) {
			return false
		}
	}
	return true
}

// birthAnchors — якоря места рождения. Место рождения ищется только по якорю:
// само по себе название города персональными данными не является.
var birthAnchors = []string{
	"место рождения", "место рожд", "места рождения", "мест. рожд",
	"м.р.", "м. р.", "родился в", "родилась в", "родился", "родилась",
	"уроженец", "уроженка", "уроженцем", "уроженки", "родом из",
}

// birthMaxRunes — наибольшая длина места рождения в рунах.
const birthMaxRunes = 60

// birthPrepositions — предлоги между якорем и значением, они во фрагмент не
// входят: фрагмент начинается с первого значимого слова.
var birthPrepositions = map[string]bool{
	"в": true, "во": true, "на": true, "из": true, "с": true, "the": true,
}

// birthStopWords — слова, на которых место рождения заканчивается: якоря
// других типов и глаголы следующего утверждения.
var birthStopWords = map[string]bool{
	"паспорт": true, "паспорта": true, "гражданство": true,
	"гражданства": true, "гражданин": true, "гражданка": true, "дата": true,
	"серия": true, "серии": true, "номер": true, "выдан": true,
	"выдано": true, "выдана": true, "адрес": true, "адресу": true,
	"прописан": true, "прописка": true, "зарегистрирован": true,
	"проживает": true, "проживающий": true, "живет": true, "живёт": true,
	"тел": true, "телефон": true, "инн": true, "снилс": true, "email": true,
	"почта": true, "пол": true, "образование": true, "работает": true,
	"учился": true, "переехал": true, "затем": true, "потом": true,
	"где": true, "и": true, "а": true, "но": true, "место": true,
	"рождения": true, "года": true, "год": true, "семье": true,
	"семьи": true, "семью": true, "браке": true, "не": true,
	"неизвестно": true, "указано": true, "отсутствует": true, "нет": true,
}

// birthAbbrev — сокращения, после которых точка не заканчивает предложение.
var birthAbbrev = map[string]bool{
	"г": true, "гор": true, "с": true, "п": true, "пос": true, "пгт": true,
	"д": true, "дер": true, "обл": true, "респ": true, "кр": true,
	"ст": true, "х": true, "им": true, "р": true, "н": true,
}

// birthPlaceDetector находит место рождения по якорным словам. Детектор не
// хранит состояния и безопасен для параллельного вызова.
type birthPlaceDetector struct{}

// NewBirthPlaceDetector создаёт детектор места рождения.
func NewBirthPlaceDetector() Detector { return birthPlaceDetector{} }

// Types перечисляет типы, которые находит детектор.
func (birthPlaceDetector) Types() []Type { return []Type{TypeBirthPlace} }

// Detect обходит все вхождения якорей и забирает значение после каждого.
func (birthPlaceDetector) Detect(d *Doc) []Span {
	ws := addrWords(d)
	var out []Span
	for _, a := range birthAnchors {
		out = append(out, birthByAnchor(d, ws, a)...)
	}
	return birthDedup(out)
}

// birthByAnchor находит все вхождения одного якоря и разбирает значение.
func birthByAnchor(d *Doc, ws []addrWord, anchor string) []Span {
	var out []Span
	for pos := 0; pos+len(anchor) <= len(d.Lower); {
		idx := strings.Index(d.Lower[pos:], anchor)
		if idx < 0 {
			break
		}
		at := pos + idx
		pos = at + len(anchor)
		if !birthBoundary(d.Lower, at, pos) {
			continue
		}
		if s, ok := birthValue(d, ws, pos); ok {
			out = append(out, s)
		}
	}
	return out
}

// birthBoundary требует, чтобы якорь был отдельным словом: иначе «родился»
// находился бы внутри другого слова.
func birthBoundary(s string, start, end int) bool {
	if start > 0 {
		r, _ := decodeLastRuneBefore(s, start)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	if end < len(s) {
		r, _ := firstRune(s[end:])
		if unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// birthValue собирает значение места рождения, начиная с позиции за якорем.
func birthValue(d *Doc, ws []addrWord, from int) (Span, bool) {
	_, lineHi := d.LineBounds(from)
	first := birthFirstWord(ws, from)
	if first < 0 || ws[first].start >= lineHi {
		return Span{}, false
	}
	if !birthLead(d.Text[from:ws[first].start]) {
		return Span{}, false
	}
	i := birthSkipPrepositions(d, ws, first)
	if i >= len(ws) || ws[i].start >= lineHi || !birthValueWord(ws[i]) {
		return Span{}, false
	}
	start, end := ws[i].start, ws[i].end
	for j := i + 1; j < len(ws); j++ {
		if !birthContinues(d, ws, j, start) {
			break
		}
		end = ws[j].end
	}
	start, end = NormalizeSpan(d.Text, start, end)
	if start >= end {
		return Span{}, false
	}
	return Span{
		Start:  start,
		End:    end,
		Type:   TypeBirthPlace,
		Conf:   ConfHigh,
		Reason: "birth_place:anchor",
	}, true
}

// birthFirstWord возвращает индекс первого значимого слова за позицией.
func birthFirstWord(ws []addrWord, from int) int {
	for i := range ws {
		if ws[i].start >= from {
			return i
		}
	}
	return -1
}

// birthLead проверяет, что между якорем и значением стоят только разделители.
func birthLead(gap string) bool {
	if len([]rune(gap)) > 6 {
		return false
	}
	for _, r := range gap {
		if !strings.ContainsRune(" \t:,.-–—«\"'()", r) {
			return false
		}
	}
	return true
}

// birthSkipPrepositions пропускает предлоги вроде «в» и «из»: фрагмент
// начинается с первого значимого слова.
func birthSkipPrepositions(d *Doc, ws []addrWord, i int) int {
	for k := 0; k < 2 && i+1 < len(ws); k++ {
		if !birthPrepositions[ws[i].lower] {
			break
		}
		gap := d.Text[ws[i].end:ws[i+1].start]
		if gap == "" || strings.TrimSpace(gap) != "" {
			break
		}
		i++
	}
	return i
}

// birthValueWord проверяет, что значение начинается со слова, а не с числа:
// «родился в 1990 году» местом рождения не является.
func birthValueWord(w addrWord) bool {
	if w.kind != KindCyr && w.kind != KindLat {
		return false
	}
	return !birthStopWords[w.lower]
}

// birthContinues решает, входит ли слово во фрагмент. Признаки остановки:
// перевод строки, точка с запятой, конец предложения, число, якорь другого
// типа. Запятая продолжает фрагмент только перед названием или типом, поэтому
// «в Москве, работает в банке» обрывается на глаголе.
func birthContinues(d *Doc, ws []addrWord, j, start int) bool {
	if d.runeIndexAt(ws[j].end)-d.runeIndexAt(start) > birthMaxRunes {
		return false
	}
	gap := d.Text[ws[j-1].end:ws[j].start]
	if strings.ContainsAny(gap, "\n\r;:") || len([]rune(gap)) > 3 {
		return false
	}
	if strings.Contains(gap, ".") && !birthAbbrev[ws[j-1].lower] {
		return false
	}
	if ws[j].kind == KindDigit || birthStopWords[ws[j].lower] {
		return false
	}
	if strings.Contains(gap, ",") && !ws[j].title && !addrIsTypeWord(ws[j].lower) {
		return false
	}
	return true
}

// birthDedup убирает пересечения, возникшие из-за нескольких подходящих
// якорей, оставляя более длинный фрагмент.
func birthDedup(spans []Span) []Span {
	if len(spans) < 2 {
		return spans
	}
	SortSpans(spans)
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		overlap := false
		for _, kept := range out {
			if kept.Overlaps(s) {
				overlap = true
				break
			}
		}
		if !overlap {
			out = append(out, s)
		}
	}
	return out
}
