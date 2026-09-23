package pii

import (
	"sort"
	"strings"
	"unicode"

	"pii-guard/internal/pii/dict"
)

// addrPartAnchors — якоря отдельных частей адреса. Общий список addrAnchors
// лежит в address_words.go и отвечает за адрес целиком; по этим словам
// маскируется одна часть записи: «Город проживания: Казань», «страна:
// Россия», «населённый пункт: Тула», «Клиент из города Казань», «на улице
// Ленина».
var addrPartAnchors = []string{
	// Формы слова «проживание»: «город проживания», «страна проживания».
	"проживания", "проживание", "проживанию", "проживании", "проживанием",
	// Якоря отдельных частей адреса: страна, населённый пункт, город.
	"страна", "страны", "стране", "страну", "страной",
	"населённый пункт", "населенный пункт",
	"из города",
	"на улице", "на улицу",
}

// addrAllAnchors — полный набор якорей адреса: общие слова из address_words.go
// вместе с якорями отдельных частей. Списки склеиваются один раз при старте, а
// не на каждом документе.
var addrAllAnchors = addrAnchorList()

// addrAnchorList склеивает общий список якорей адреса с якорями отдельных
// частей.
func addrAnchorList() []string {
	all := make([]string, 0, len(addrAnchors)+len(addrPartAnchors))
	all = append(all, addrAnchors...)
	all = append(all, addrPartAnchors...)
	return all
}

// addrAnchorWindow — окно поиска якоря слева от фрагмента, в рунах.
const addrAnchorWindow = 48

// addrMaxRunes — наибольшая длина адресного фрагмента в рунах. Компоненты
// дальше этого расстояния относятся уже к другому адресу.
const addrMaxRunes = 120

// addrNbsp — неразрывный пробел (U+00A0). Вынесен в константу, потому что
// в разборе адреса он стоит наравне с обычным пробелом сразу в трёх
// местах: в выгрузках его ставят между частями записи, а на вид он от
// обычного неотличим, и каждый лишний литерал — шанс при правке
// незаметно подменить его на обычный.
const addrNbsp = "\u00a0"

// Виды адресных компонентов, попадающие в поле Reason. Вынесены в константы,
// чтобы не повторять строковые литералы в разборе. Словари типов лежат в
// address_words.go и держат те же самые значения.
const (
	addrKindCity     = "city"
	addrKindRegion   = "region"
	addrKindStreet   = "street"
	addrKindHouse    = "house"
	addrKindCorp     = "corp"
	addrKindBuilding = "building"
	addrKindFlat     = "flat"
	addrKindOffice   = "office"
	addrKindPremise  = "premise"
)

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
// Признак solo означает, что компонента хватает на адрес в одиночку.
type addrComp struct {
	start int
	end   int
	kinds []string
	solo  bool
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
	comps = addrWithLatinCity(d, ws, comps)
	comps = addrWithPostcodes(d, comps)
	return addrSpans(d, comps)
}

// addrWithLatinCity расширяет уличный компонент влево на название населённого
// пункта, записанного латиницей: «Kemerovo, Druzhby str., 50». Латинских
// названий в словаре нет, поэтому пункт опознаётся по положению: отдельное
// слово с заглавной буквы, отделённое от улицы запятой.
func addrWithLatinCity(d *Doc, ws []addrWord, comps []addrComp) []addrComp {
	for i := range comps {
		p, ok := addrLatinCityBefore(d, ws, comps[i])
		if !ok || addrOverlaps(comps, ws[p].start, ws[p].end) {
			continue
		}
		comps[i].start = ws[p].start
		comps[i].kinds = append([]string{addrKindCity}, comps[i].kinds...)
	}
	return comps
}

// addrLatinCityBefore ищет слово-название пункта перед уличным компонентом.
// Слово должно стоять само по себе: в записи «John Smith, Druzhby str.»
// фамилия отделена от имени пробелом, и населённым пунктом не считается.
func addrLatinCityBefore(d *Doc, ws []addrWord, c addrComp) (int, bool) {
	if !addrHasKind(c, addrKindStreet) || addrHasKind(c, addrKindCity) {
		return 0, false
	}
	p := -1
	for i := range ws {
		if ws[i].end <= c.start {
			p = i
		}
	}
	if p < 0 || ws[p].kind != KindLat {
		return 0, false
	}
	// Название может быть дефисным: «Komsomolsk-na-amure» разбито токенизатором
	// на части. Идём влево по дефисам до первого заглавного слова.
	start := p
	for start > 0 && addrGap(d, ws, start-1) == "-" && ws[start-1].kind == KindLat {
		start--
	}
	if !ws[start].title {
		return 0, false
	}
	if len([]rune(ws[start].lower)) < 3 || addrIsTypeWord(ws[start].lower) {
		return 0, false
	}
	if d.Text[ws[p].end:c.start] != ", " {
		return 0, false
	}
	if start > 0 && addrGap(d, ws, start-1) != "" && strings.TrimSpace(addrGap(d, ws, start-1)) == "" {
		return 0, false
	}
	return start, true
}

// addrWithPostcodes добавляет к компонентам почтовый индекс, стоящий сразу за
// якорем адреса: запись «Адрес: 117312» состоит из одного индекса, и без этого
// компонента фрагмент вообще не находится.
func addrWithPostcodes(d *Doc, comps []addrComp) []addrComp {
	for _, run := range d.NumRuns() {
		if run.GroupsPattern() != "6" || addrForeignNumber(d, &run) {
			continue
		}
		if _, ok := d.AnchorBefore(run.Start, addrAllAnchors, addrAnchorWindow); !ok {
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
// Неразрывный пробел приходит из выгрузок и на вид неотличим от обычного.
func addrShortGap(gap string) bool {
	if len([]rune(gap)) > 3 {
		return false
	}
	for _, r := range gap {
		if !strings.ContainsRune(" .№#-\u00a0", r) {
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
		c, next = addrRegionTail(d, ws, c, next)
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
	if !addrHasKind(c, addrKindStreet) || addrHasKind(c, addrKindHouse) {
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
	c.kinds = append(c.kinds, addrKindHouse)
	return c, last + 1
}

// addrRegionTail расширяет компонент населённого пункта на скобочное
// сокращение региона: «ст. Ревда (Сверд.)», «клх Кропоткин (Краснод.)».
// Чужая разметка считает такое уточнение частью адреса. Внутри скобок
// допускается ровно одно сокращённое слово с заглавной буквы: развёрнутый
// комментарий вроде «(заявка принята)» к адресу не относится.
func addrRegionTail(d *Doc, ws []addrWord, c addrComp, next int) (addrComp, int) {
	if !addrHasKind(c, addrKindCity) || next >= len(ws) {
		return c, next
	}
	if strings.TrimLeft(d.Text[c.end:ws[next].start], " ") != "(" {
		return c, next
	}
	w := ws[next]
	if w.kind != KindCyr || !w.title || len([]rune(w.lower)) < 2 {
		return c, next
	}
	if !strings.HasPrefix(d.Text[w.end:], ".)") {
		return c, next
	}
	c.end = w.end + len(".)")
	return c, next + 1
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
	// Односимвольный тип требует точки, иначе предлог «с» из «с 5 утра»
	// превращается в название села. Исключение — следующее слово с заглавной
	// буквы: «г Пенза» и «с Ивановка» без точки это адрес, а не предлог.
	// Аббревиатура вроде «IMEI» названием не считается: «с IMEI» это предлог
	// с обозначением устройства, а не село.
	if len([]rune(ws[i].lower)) == 1 && !strings.HasPrefix(addrGap(d, ws, i), ".") {
		if i+1 >= len(ws) || !ws[i+1].title || addrAllCaps(d.Text[ws[i+1].start:ws[i+1].end]) {
			return "", 0, false
		}
	}
	return kind, i + 1, true
}

// addrAllCaps сообщает, что слово состоит только из заглавных букв: это
// аббревиатура вроде «IMEI», а не название населённого пункта.
func addrAllCaps(s string) bool {
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
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
	if addrIsTypeWord(w.lower) && (!single || !w.title) {
		return false
	}
	return !single || w.title
}

// addrNameHead проверяет слово в роли начала названия рядом с типом. В
// отличие от addrNameWord здесь допускается слово, совпадающее с типом
// другого компонента: в записях «бульвар Набережная» и «Naberezhnaya str.»
// слово «Набережная» это имя улицы, а не её тип. Различает их заглавная
// буква, поэтому одиночные буквы и слова со строчной сюда не проходят.
func addrNameHead(d *Doc, ws []addrWord, i int) bool {
	if i < 0 || i >= len(ws) {
		return false
	}
	return addrNameWord(ws, i) || addrNameTypeWord(d, ws, i)
}

// addrNameTypeWord допускает в роли названия слово, которое само служит типом.
// Заглавная буква снимает сомнения сразу. В тексте одним регистром признаком
// служит то, что собственного названия у слова нет: дальше стоит запятая или
// номер, как в записи «переулок набережная, д. 15». Типы адресных номеров
// сюда не пускаются: за ними всегда идёт число, а не название.
func addrNameTypeWord(d *Doc, ws []addrWord, i int) bool {
	w := ws[i]
	if w.kind != KindCyr && w.kind != KindLat {
		return false
	}
	if addrStopNames[w.lower] || addrGlueWords[w.lower] {
		return false
	}
	if len([]rune(w.lower)) < 2 || !addrIsTypeWord(w.lower) {
		return false
	}
	if w.title {
		return true
	}
	if _, house := addrHouseTypes[w.lower]; house {
		return false
	}
	gap := addrGap(d, ws, i)
	// Точка сразу за словом означает сокращение, а сокращение это всегда тип,
	// а не название: в записи «ул. пер. Тенистый» слово «пер.» относится к
	// переулку. Без этой проверки первый тип забирал второй себе в название,
	// настоящее название оставалось снаружи, и адрес распадался.
	if strings.HasPrefix(gap, ".") {
		return false
	}
	return gap != " "
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
	if !addrNameHead(d, ws, i) {
		return 0, 0, false
	}
	end, last := addrNameTail(d, ws, i)
	return end, last + 1, true
}

// addrNameTail продолжает название словами справа: «Малые Горки»,
// «40 лет Октября». Дальше трёх слов название не растягивается.
func addrNameTail(d *Doc, ws []addrWord, i int) (int, int) {
	end, last := ws[i].end, i
	for j := i + 1; j < len(ws) && j-i < 3; j++ {
		if !addrNameContinues(d, ws, j) {
			break
		}
		end, last = ws[j].end, j
	}
	return end, last
}

// addrNumericName разбирает название, начинающееся с числа: «ул. 8 Марта».
// Число без следующего слова названием не считается, иначе «с. 5 по 10»
// превращается в населённый пункт.
func addrNumericName(d *Doc, ws []addrWord, i int) (int, int, bool) {
	if i+1 >= len(ws) || !addrSpace(addrGap(d, ws, i)) {
		return 0, 0, false
	}
	if !addrNameWord(ws, i+1) && !addrNameConnectors[ws[i+1].lower] {
		return 0, 0, false
	}
	end, last := addrNameTail(d, ws, i+1)
	return end, last + 1, true
}

// addrNameContinues решает, входит ли слово j в составное название вроде
// «Малые Горки». Слово не берётся, если оно вплотную предшествует другому
// типу: в «Ивановское Московской обл.» слово «Московской» относится к области.
func addrNameContinues(d *Doc, ws []addrWord, j int) bool {
	gap := d.Text[ws[j-1].end:ws[j].start]
	// Инициал с точкой не заканчивает название, а начинает его: «алл.
	// М.Горького», «ш. К.Маркса». Пробела после точки там нет.
	if gap == "." && addrInitial(ws[j-1]) && ws[j].title {
		return true
	}
	if gap != " " && gap != addrNbsp && gap != "-" {
		return false
	}
	if gap == "-" {
		return addrHyphenPart(ws, j)
	}
	if !addrNameWord(ws, j) && !addrNameConnectors[ws[j].lower] {
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

// addrInitial сообщает, что слово это инициал: одна заглавная буква.
func addrInitial(w addrWord) bool {
	if w.kind != KindCyr && w.kind != KindLat {
		return false
	}
	return w.title && len([]rune(w.lower)) == 1
}

// addrHyphenPart сообщает, что слово продолжает составное название через
// дефис: «Ростов-на-Дону», «Комсомольск-на-Амуре». Через дефис допустимы и
// служебные слова, поэтому проверяется только то, что это буквенное слово и не
// якорь другого типа. Без этого название обрывалось на первой части, а адрес
// распадался на две группы и переставал опознаваться как один.
func addrHyphenPart(ws []addrWord, j int) bool {
	w := ws[j]
	if w.kind != KindCyr && w.kind != KindLat {
		return false
	}
	return !addrStopNames[w.lower]
}

// addrMatchTyped опознаёт компонент вида «тип плюс название» в любом порядке:
// и «ул. Ленина», и «Ленинская ул.», и «Baker Street».
func addrMatchTyped(d *Doc, ws []addrWord, i int, types map[string]string) (addrComp, int, bool) {
	if i < 0 || i >= len(ws) {
		return addrComp{}, 0, false
	}
	if kind, next, ok := addrLookupType(d, ws, i, types); ok {
		// Название после типа отсутствует — слово само может оказаться
		// названием при обратном порядке: в записи «Naberezhnaya str.» это
		// имя улицы, хотя оно же служит типом в записи «наб. Мира».
		if end, after, named := addrName(d, ws, next); named {
			c := addrComp{start: ws[i].start, end: end, kinds: []string{kind}}
			c.solo = addrSoloComp(d, ws, i, next, kind)
			return c, after, true
		}
	}
	return addrMatchNameFirst(d, ws, i, types)
}

// addrSpace сообщает, что промежуток между словами это пробел: обычный или
// неразрывный. Неразрывный пробел приходит из выгрузок и на вид неотличим от
// обычного, поэтому там, где ожидается пробел, он тоже допустим.
func addrSpace(gap string) bool { return gap == " " || gap == addrNbsp }

// addrMatchNameFirst разбирает обратный порядок «название плюс тип». Название
// из словаря сюда не пускается: в записи «Москва ул. Ленина» город не является
// названием улицы.
func addrMatchNameFirst(d *Doc, ws []addrWord, i int, types map[string]string) (addrComp, int, bool) {
	if !addrNameHead(d, ws, i) || !addrSpace(addrGap(d, ws, i)) {
		return addrComp{}, 0, false
	}
	kind, next, ok := addrLookupType(d, ws, i+1, types)
	// Тип может стоять через слово «автономный»: «Ямало-Ненецкий автономный
	// округ» — тип «округ» на третьем слове. Без этого составное название
	// региона распадалось и «автономный округ» оставалось без маски.
	if !ok && i+2 < len(ws) && ws[i+1].lower == "автономный" {
		kind, next, ok = addrLookupType(d, ws, i+2, types)
	}
	if !ok {
		return addrComp{}, 0, false
	}
	// Название из словаря не может быть именем улицы или пункта: в записи
	// «Москва ул. Ленина» город относится к себе. Для региона оговорка не
	// действует, иначе опечатка «Свердловска область» теряла бы слово
	// «область» и разрывала адрес.
	if _, known := dict.Place(ws[i].lower); known && kind != addrKindRegion {
		return addrComp{}, 0, false
	}
	// За типом стоит собственное название — значит тип относится к нему, а не
	// к предыдущему слову. Без этой проверки запись «адрес ул. Центральная»
	// разбиралась как улица «адрес», а настоящее название выпадало из
	// фрагмента вместе с номером дома.
	if _, _, named := addrName(d, ws, next); named {
		return addrComp{}, 0, false
	}
	return addrComp{start: ws[i].start, end: ws[next-1].end, kinds: []string{kind}}, next, true
}

// addrSoloComp решает, достаточно ли одного компонента для адреса. Нужны три
// условия: тип записан сокращением, тип относится к пункту или улице, а сразу
// за ним стоит имя собственное. Число после сокращения имя не образует, этим
// «д. 5» отличается от «д. Касимов».
func addrSoloComp(d *Doc, ws []addrWord, i, name int, kind string) bool {
	if kind != addrKindCity && kind != addrKindStreet {
		return false
	}
	if !addrSoloTypes[d.Lower[ws[i].start:ws[name-1].end]] {
		return false
	}
	w := ws[name]
	if w.kind != KindCyr && w.kind != KindLat || !w.title {
		return false
	}
	return len([]rune(w.lower)) > 1 && !addrIsTypeWord(w.lower)
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
	// Номер дома, записанный полным словом, уже адрес сам по себе: «дом 191»
	// без улицы и без якоря чужая разметка засчитывает. Сокращение «д.» так
	// не засчитывается, оно слишком многозначно, а корпус, квартира и офис в
	// одиночку значат номер помещения, а не место жительства.
	c := addrComp{start: ws[i].start, end: end, kinds: []string{kind}}
	c.solo = kind == addrKindHouse && addrHouseWords[ws[i].lower]
	// Квартира полным словом отдельно это адрес, если после номера нет
	// продолжения: «квартира 12» маскируется, а «квартира 12 в доме напротив»
	// это разговорное описание, а не адресная запись.
	if kind == addrKindFlat && ws[i].lower == "квартира" && addrFlatSolo(ws, last) {
		c.solo = true
	}
	return c, last + 1, true
}

// addrFlatSolo сообщает, что после номера квартиры нет продолжения адреса
// словом: «квартира 12» в конце строки это адрес, а «квартира 12 в доме
// напротив» — описание местоположения.
func addrFlatSolo(ws []addrWord, last int) bool {
	if last+1 >= len(ws) {
		return true
	}
	if ws[last+1].kind == KindCyr || ws[last+1].kind == KindLat {
		return false
	}
	return true
}

// addrNumberEnd расширяет номер дома на приклеенную литеру, на приклеенный
// номер корпуса и на дробную часть: «5а», «40к4», «12/1», «12-45».
func addrNumberEnd(d *Doc, ws []addrWord, i int) (int, int) {
	end, last := ws[i].end, i
	for j := last + 1; j < len(ws); j++ {
		if !addrNumberTail(d.Text[ws[j-1].end:ws[j].start], ws[j]) {
			break
		}
		end, last = ws[j].end, j
	}
	return end, last
}

// addrNumberTail сообщает, что слово продолжает номер дома. Приклеенная цифра
// после литеры это номер корпуса: запись «д. 40к4» одно число, и без неё
// хвостовая цифра оставалась снаружи фрагмента и рвала адрес надвое.
func addrNumberTail(gap string, w addrWord) bool {
	glued := gap == ""
	single := len([]rune(w.lower)) == 1
	letter := glued && w.kind != KindDigit && single
	corp := glued && w.kind == KindDigit
	// Дробная часть номера дома это одна-три цифры: «12/3», «40-2». Шесть
	// цифр после дефиса домом уже не бывают — так номер студенческого билета
	// «Студенческий 3827-178120» принимался за улицу с домом.
	fraction := (gap == "/" || gap == "-") && w.kind == KindDigit && len([]rune(w.lower)) <= 3
	return letter || corp || fraction
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
	// Номер дома в слитной записи это одна-три цифры: домов с номером 3827
	// не бывает, а «Студенческий 3827-178120» это номер студенческого билета.
	if len([]rune(d.Text[ws[j].start:ws[j].end])) > 3 {
		return addrComp{}, 0, false
	}
	if !addrShortGap(d.Text[w.end:ws[j].start]) {
		return addrComp{}, 0, false
	}
	end, last := addrNumberEnd(d, ws, j)
	return addrComp{start: w.start, end: end, kinds: []string{addrKindStreet, addrKindHouse}}, last + 1, true
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
	if !addrSpace(addrGap(d, ws, last)) {
		return addrComp{}, 0, false
	}
	c, next, ok := addrMatchTyped(d, ws, last+1, addrStreetTypes)
	if !ok {
		return addrComp{}, 0, false
	}
	c.start = ws[i].start
	c.kinds = append([]string{addrKindHouse}, c.kinds...)
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
		if gap := addrGap(d, ws, k); gap != " " && gap != addrNbsp && gap != "-" {
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
	return addrMergeOverlaps(out)
}

// addrMergeOverlaps склеивает соседние фрагменты, которые задевают друг друга.
// Пересечение возникает, когда почтовый индекс притянут сразу двумя группами:
// в записи «д. 3/7, 301204 ст. Бологое» индекс относится и к дому, и к
// станции. Движок оставил бы только первый фрагмент, и вторая половина адреса
// осталась бы без маски.
func addrMergeOverlaps(spans []Span) []Span {
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		n := len(out)
		if n == 0 || s.Start > out[n-1].End {
			out = append(out, s)
			continue
		}
		if s.End > out[n-1].End {
			out[n-1].End = s.End
		}
		if s.Conf > out[n-1].Conf {
			out[n-1].Conf = s.Conf
		}
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
	// Между компонентами одного адреса встречается лишнее слово-тип: в записи
	// «клх Темрюк, ул. ул. Шмидта» тип «ул.» продублирован. Такое слово адрес
	// не разрывает, иначе фрагмент распадался надвое и середина оставалась
	// открытой.
	for _, w := range strings.FieldsFunc(gap, addrNotLetter) {
		if !addrGlueWords[w] && !addrIsTypeWord(w) {
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
		if !addrSoloGroup(group) && !addrAnchored(d, start) {
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
	raw := end
	start, end = NormalizeSpan(d.Text, start, end)
	end = addrKeepRegionTail(d.Text, end, raw)
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

// addrSoloGroup сообщает, что среди компонентов есть самодостаточный.
func addrSoloGroup(group []addrComp) bool {
	for _, c := range group {
		if c.solo {
			return true
		}
	}
	return false
}

// addrAnchored сообщает, что слева от фрагмента стоит якорное слово адреса.
// Окно захватывает и несколько рун после начала фрагмента: якорь «из города»
// заканчивается на типе «города», который сам входит во фрагмент, и без этого
// запаса «Клиент из города Казань» не находилось бы.
func addrAnchored(d *Doc, start int) bool {
	_, ok := d.FindAnchor(start, start, addrAllAnchors, addrAnchorWindow, 12)
	return ok
}

// addrKeepRegionTail возвращает правой границе точку с закрывающей скобкой.
// NormalizeSpan срезает их как знаки препинания, но в уточнении региона
// «ст. Ревда (Сверд.)» они часть адреса, и без них фрагмент короче эталона.
func addrKeepRegionTail(text string, end, raw int) int {
	if raw > end && raw <= len(text) && text[end:raw] == ".)" {
		return raw
	}
	return end
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
		if run.GroupsPattern() != "6" || addrForeignNumber(d, &run) {
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
// номеру паспорта, заказа или договора. Последовательность передаётся
// указателем: проверка идёт в цикле по всем числам документа, а сама
// структура тяжёлая и копировать её на каждом шаге незачем.
func addrForeignNumber(d *Doc, run *NumRun) bool {
	if _, ok := d.AnchorBefore(run.Start, anchorsPassport, nearAnchorWindow); ok {
		return true
	}
	_, ok := d.AnchorBefore(run.Start, negAnchorsOrder, nearAnchorWindow)
	return ok
}

// addrPunctGap проверяет, что между индексом и адресом стоят только пробелы и
// запятые. Неразрывный пробел приходит из выгрузок и на вид неотличим от
// обычного.
func addrPunctGap(gap string) bool {
	if len([]rune(gap)) > 4 {
		return false
	}
	for _, r := range gap {
		if !strings.ContainsRune(" ,.\t\u00a0", r) {
			return false
		}
	}
	return true
}
