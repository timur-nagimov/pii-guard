package pii

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"pii-guard/internal/pii/dict"
)

// addrAnchors — якорные слова адреса. Одиночный компонент считается адресом
// только рядом с одним из них: название города само по себе персональными
// данными не является, фраза «поехал в Казань» маскироваться не должна.
var addrAnchors = []string{
	anchorWordAddress, anchorWordLives, "проживающ", "прописан", "прописка",
	anchorWordRegistered, "регистрац", "доставк", "место жительства",
	"жительств", "живет", "живёт", "пребывани",
	// Формы слова «проживание»: «город проживания», «страна проживания».
	"проживания", "проживание", "проживанию", "проживании", "проживанием",
	// Якоря отдельных частей адреса: страна, населённый пункт, город.
	"страна", "страны", "стране", "страну", "страной",
	"населённый пункт", "населенный пункт",
	"из города",
	"на улице", "на улицу",
}

// addrAnchorWindow — окно поиска якоря слева от фрагмента, в рунах.
const addrAnchorWindow = 48

// addrMaxRunes — наибольшая длина адресного фрагмента в рунах. Компоненты
// дальше этого расстояния относятся уже к другому адресу.
const addrMaxRunes = 120

// Виды адресных компонентов, попадающие в поле Reason. Вынесены в константы,
// чтобы не повторять строковые литералы в таблицах типов.
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

// addrSettlementTypes — типы населённых пунктов. Ключ задаётся в нижнем
// регистре без точки, значение попадает в поле Reason.
var addrSettlementTypes = map[string]string{
	"г": addrKindCity, anchorWordGor: addrKindCity, "город": addrKindCity, "города": addrKindCity,
	"городе": addrKindCity, "г-д": addrKindCity, "гп": addrKindCity,
	"с": addrKindCity, "село": addrKindCity, "села": addrKindCity, "селе": addrKindCity,
	"п": addrKindCity, anchorWordPos: addrKindCity, "поселок": addrKindCity, "посёлок": addrKindCity,
	"поселке": addrKindCity, "посёлке": addrKindCity, "пгт": addrKindCity, "рп": addrKindCity,
	"д": addrKindCity, "дер": addrKindCity, "деревня": addrKindCity, "деревне": addrKindCity,
	"х": addrKindCity, "хутор": addrKindCity, "ст-ца": addrKindCity, "станица": addrKindCity,
	"станице": addrKindCity, "снт": addrKindCity, "днп": addrKindCity, "аул": addrKindCity,
	"улус": addrKindCity, "г-к": addrKindCity,
	"ст": addrKindCity, "станция": addrKindCity, "станции": addrKindCity, "станцие": addrKindCity,
	"клх": addrKindCity, "колхоз": addrKindCity, "к": addrKindCity,
	"gorod": addrKindCity, "g": addrKindCity, "pos": addrKindCity, "derevnya": addrKindCity,
}

// addrRegionTypes — типы регионов и районов.
var addrRegionTypes = map[string]string{
	anchorWordRegion: addrKindRegion, "область": addrKindRegion, "области": addrKindRegion,
	"край": addrKindRegion, "края": addrKindRegion, "крае": addrKindRegion,
	anchorWordResp: addrKindRegion, "республика": addrKindRegion, "республики": addrKindRegion,
	"республике": addrKindRegion, "р-н": addrKindRegion, "район": addrKindRegion,
	"района": addrKindRegion, "районе": addrKindRegion, "округ": addrKindRegion,
	"округа": addrKindRegion, "ао": addrKindRegion, "губерния": addrKindRegion,
}

// addrStreetTypes — типы улиц, включая английские сокращения.
var addrStreetTypes = map[string]string{
	"ул": addrKindStreet, "улица": addrKindStreet, "улице": addrKindStreet, "улицы": addrKindStreet,
	"пр-т": addrKindStreet, "пр-кт": addrKindStreet, anchorWordProsp: addrKindStreet,
	"проспект": addrKindStreet, "проспекте": addrKindStreet, "пр": addrKindStreet,
	"пер": addrKindStreet, "переулок": addrKindStreet, "переулке": addrKindStreet,
	"б-р": addrKindStreet, "бул": addrKindStreet, "бульвар": addrKindStreet,
	"ш": addrKindStreet, "шоссе": addrKindStreet,
	"наб": addrKindStreet, "набережная": addrKindStreet, "набережной": addrKindStreet,
	"проезд": addrKindStreet, "проезде": addrKindStreet, "туп": addrKindStreet,
	"тупик": addrKindStreet, anchorWordMkr: addrKindStreet, "микрорайон": addrKindStreet,
	"кв-л": addrKindStreet, "квартал": addrKindStreet, "пл": addrKindStreet,
	"площадь": addrKindStreet, "аллея": addrKindStreet, "алл": addrKindStreet,
	"линия": addrKindStreet, "тракт": addrKindStreet, "просек": addrKindStreet,
	"просека": addrKindStreet, "кольцо": addrKindStreet, "съезд": addrKindStreet,
	"спуск": addrKindStreet, "взвоз": addrKindStreet, "заезд": addrKindStreet,
	"разъезд": addrKindStreet, addrKindStreet: addrKindStreet, "st": addrKindStreet, "ave": addrKindStreet,
	"avenue": addrKindStreet, "rd": addrKindStreet, "road": addrKindStreet, "ln": addrKindStreet,
	"lane": addrKindStreet, "blvd": addrKindStreet, "boulevard": addrKindStreet,
	"dr": addrKindStreet, "drive": addrKindStreet, "sq": addrKindStreet, "square": addrKindStreet,
	"hwy": addrKindStreet, "highway": addrKindStreet,
	"str": addrKindStreet, "strasse": addrKindStreet,
	"ul": addrKindStreet, "ulitsa": addrKindStreet, "prospekt": addrKindStreet,
	"pereulok": addrKindStreet, "naberezhnaya": addrKindStreet, "shosse": addrKindStreet,
	"proezd": addrKindStreet, "bulvar": addrKindStreet,
}

// addrHouseTypes — типы адресных номеров: дом, корпус, строение, квартира,
// офис и помещение.
var addrHouseTypes = map[string]string{
	"д": addrKindHouse, "дом": addrKindHouse, "дома": addrKindHouse, "доме": addrKindHouse,
	"вл": addrKindHouse, "влад": addrKindHouse, "владение": addrKindHouse,
	"к": addrKindCorp, "кор": addrKindCorp, "корп": addrKindCorp, "корпус": addrKindCorp,
	"корпусе": addrKindCorp, "стр": addrKindBuilding, "строение": addrKindBuilding,
	"строении": addrKindBuilding, "кв": addrKindFlat, "кварт": addrKindFlat,
	"квартира": addrKindFlat, "квартире": addrKindFlat, "квартиры": addrKindFlat,
	"оф": addrKindOffice, "офис": addrKindOffice, "офисе": addrKindOffice,
	"пом": addrKindPremise, "помещение": addrKindPremise, "помещении": addrKindPremise,
	"лит": addrKindPremise, "литера": addrKindPremise, "эт": addrKindPremise, "этаж": addrKindPremise,
	"apt": addrKindFlat, "apartment": addrKindFlat, "suite": addrKindFlat, "ste": addrKindFlat,
	"unit": addrKindFlat, "room": addrKindFlat, "bld": addrKindBuilding, "bldg": addrKindBuilding,
	addrKindOffice: addrKindOffice, "floor": addrKindPremise,
	"dom": addrKindHouse, "korp": addrKindCorp, "kv": addrKindFlat, "kvartira": addrKindFlat,
	"str": addrKindBuilding,
}

// addrHouseWords — полные слова для номера дома. Только с ними одиночный
// номер считается адресом: у сокращения «д.» слишком много значений.
var addrHouseWords = map[string]bool{
	"дом": true, "дома": true, "доме": true,
	"владение": true, "владения": true, "владении": true,
}

// addrStopNames — слова, которые не могут быть названием улицы или пункта.
// Без них якорное слово из соседнего предложения попадает во фрагмент.
var addrStopNames = map[string]bool{
	"и": true, "или": true, "а": true, "но": true, "не": true, "что": true,
	"это": true, "же": true, anchorWordTel: true, anchorWordPhone: true, anchorWordPassport: true,
	anchorWordInn: true, anchorWordSNILS: true, "email": true, "почта": true, "дата": true,
	anchorWordSeriesFull: true, "номер": true, anchorWordIssued: true, "код": true, "сумма": true,
	"заказ": true, anchorWordContract: true, "банк": true, "отделение": true,
	"филиал": true, "рождения": true, "рожд": true, "год": true,
	"года": true, "году": true, "лет": true, "руб": true, "рублей": true,
}

// addrNameConnectors — служебные слова внутри названия улицы: «наб. реки
// Фонтанки», «ул. имени Гагарина».
var addrNameConnectors = map[string]bool{
	"реки": true, "имени": true, "им": true, "маршала": true,
	"генерала": true, "академика": true, "братьев": true, "the": true,
	// «лет» держит числовые названия вроде «40 лет Октября»: без связки
	// название обрывалось на числе и улица не опознавалась.
	"лет": true,
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

// addrSoloTypes — сокращения типов населённого пункта и улицы. Сокращение с
// собственным названием рядом это уже адрес: «ст. Якша», «алл. Маркса»,
// «д. Касимов». Полных слов здесь нет намеренно: «улица», «площадь» и
// «город» встречаются в обычной речи о месте («ремонт дороги затронет улица
// Менделеева», «поехал в город Казань»), и одного такого слова для адреса
// мало, ему по-прежнему нужен второй компонент или якорь.
var addrSoloTypes = map[string]bool{
	"г": true, anchorWordGor: true, "г-д": true, "г-к": true, "гп": true,
	"с": true, "д": true, "дер": true, "п": true, anchorWordPos: true,
	"пгт": true, "рп": true, "ст": true, "ст-ца": true, "х": true,
	"клх": true, "к": true, "снт": true, "днп": true, anchorWordMkr: true,
	"кв-л": true,
	"ул":   true, "пер": true, "пр": true, "пр-т": true, "пр-кт": true,
	anchorWordProsp: true, "пл": true, "наб": true, "ш": true, "б-р": true,
	"бул": true, "алл": true, "туп": true,
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
	if gap != " " && gap != "\u00a0" && gap != "-" {
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
func addrSpace(gap string) bool { return gap == " " || gap == "\u00a0" }

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
	fraction := (gap == "/" || gap == "-") && w.kind == KindDigit
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
		if gap := addrGap(d, ws, k); gap != " " && gap != "\u00a0" && gap != "-" {
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
	_, ok := d.FindAnchor(start, start, addrAnchors, addrAnchorWindow, 12)
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

// birthAnchors — якоря места рождения. Место рождения ищется только по якорю:
// само по себе название города персональными данными не является.
var birthAnchors = []string{
	"место рождения", "место рожд", "места рождения", "мест. рожд",
	"м.р.", "м. р.", "мр", "родился в", "родилась в", "родился", "родилась",
	"уроженец", "уроженка", "уроженцем", "уроженки", "родом из",
	"родина", "родины",
	"страна и город рождения", "город рождения", "страна рождения",
	"населённый пункт рождения", "населенный пункт рождения",
	"нас. пункт рождения", "нп рождения",
	"place of birth", "birth place", "born in",
}

// birthQualifiers — уточнения источника сведений между якорем и значением:
// «место рождения по паспорту: г. Курск». Само уточнение во фрагмент не
// входит, иначе маска съедала бы слова «по паспорту».
var birthQualifiers = []string{
	"по паспорту", "по документу", "по документам", "по анкете",
	"по данным паспорта", "согласно паспорту", "в паспорте",
	// Уточнения, чьё это место: без них слово «заявителя» само
	// принималось за место рождения и маска накрывала его вместо города.
	"заявителя", "заявительницы", "клиента", "клиентки", "гражданина",
	"гражданки", "владельца", "держателя", "субъекта",
	"ребёнка", "ребенка", "сына", "дочери", "супруга", "супруги",
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
	anchorWordPassport: true, "паспорта": true, "гражданство": true,
	"гражданства": true, anchorWordCitizen: true, "гражданка": true, "дата": true,
	anchorWordSeriesFull: true, anchorWordSeries: true, "номер": true, anchorWordIssued: true,
	"выдано": true, "выдана": true, anchorWordAddress: true, "адресу": true,
	"прописан": true, "прописка": true, anchorWordRegistered: true,
	anchorWordLives: true, "проживающий": true, "живет": true, "живёт": true,
	anchorWordTel: true, anchorWordPhone: true, anchorWordInn: true, anchorWordSNILS: true, "email": true,
	"почта": true, "пол": true, "образование": true, "работает": true,
	"учился": true, "переехал": true, "затем": true, "потом": true,
	"где": true, "и": true, "а": true, "но": true, "место": true,
	"рождения": true, "года": true, "год": true, "семье": true,
	"семьи": true, "семью": true, "браке": true, "не": true,
	"неизвестно": true, "указано": true, "отсутствует": true, "нет": true,
}

// birthBreakRunes — знаки, которые обрывают место рождения: за ними идёт
// служебный хвост записи, а не продолжение названия.
const birthBreakRunes = "—–()[]{}«»\"|/"

// birthAbbrev — сокращения, после которых точка не заканчивает предложение.
var birthAbbrev = map[string]bool{
	"г": true, anchorWordGor: true, "с": true, "п": true, anchorWordPos: true, "пгт": true,
	"д": true, "дер": true, anchorWordRegion: true, anchorWordResp: true, "кр": true,
	"ст": true, "х": true, "им": true, "р": true, "н": true,
}

// birthCountryWords — названия стран, которые продолжают место рождения после
// запятой: «г. Калуга, Россия». В нижнем регистре слово не заглавное, и без
// этого списка запятая обрывала бы фрагмент на названии города.
var birthCountryWords = map[string]bool{
	"россия": true, "рф": true, "казахстан": true, "беларусь": true,
	"белоруссия": true, "украина": true, "армения": true, "азербайджан": true,
	"грузия": true, "молдова": true, "молдавия": true, "узбекистан": true,
	"таджикистан": true, "киргизия": true, "киргизстан": true,
	"туркменистан": true, "латвия": true, "литва": true, "эстония": true,
	"германия": true, "сша": true, "китай": true, "польша": true,
	"финляндия": true, "израиль": true, "турция": true, "болгария": true,
	"сербия": true, "чехия": true, "словакия": true, "венгрия": true,
	"румыния": true, "италия": true, "испания": true, "франция": true,
	"великобритания": true, "англия": true, "япония": true, "корея": true,
	"индия": true, "египет": true, "таиланд": true, "вьетнам": true,
	"монголия": true, "афганистан": true, "иран": true, "ирак": true,
	"сирия": true, "пакистан": true, "непал": true, "шри-ланка": true,
	"куба": true, "мексика": true, "бразилия": true, "аргентина": true,
	"канада": true, "австралия": true, "нидерланды": true, "бельгия": true,
	"швейцария": true, "австрия": true, "швеция": true, "норвегия": true,
	"дания": true, "греция": true, "португалия": true, "ирландия": true,
	"хорватия": true, "словения": true, "македония": true, "албания": true,
	"черногория": true, "босния": true, "люксембург": true, "исландия": true,
	"мальта": true, "кипр": true,
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
	// Сворачивание омоглифов стоит дорого: посимвольный обход вместо
	// strings.Index, и так на каждый якорь. Проверка наличия латинских
	// двойников делается один раз на весь текст, и чистый текст — а это
	// почти весь поток — идёт прежним быстрым путём. Без этого разделения
	// горячий путь замедлился на сорок процентов.
	folded := hasASCIIHomoglyph(d.Lower)
	out := make([]Span, 0, len(birthAnchors))
	for _, a := range birthAnchors {
		out = append(out, birthByAnchor(d, ws, a, folded)...)
	}
	return birthDedup(out)
}

// birthByAnchor находит все вхождения одного якоря и разбирает значение.
func birthByAnchor(d *Doc, ws []addrWord, anchor string, folded bool) []Span {
	var out []Span
	var runes []rune
	if folded {
		runes = []rune(anchor)
	}
	for pos := 0; pos < len(d.Lower); {
		var at, end int
		var ok bool
		if folded {
			at, end, ok = foldedIndex(d.Lower, pos, runes)
		} else {
			idx := strings.Index(d.Lower[pos:], anchor)
			if idx >= 0 {
				at, end, ok = pos+idx, pos+idx+len(anchor), true
			}
		}
		if !ok {
			break
		}
		pos = end
		if !birthBoundary(d.Lower, at, end) {
			continue
		}
		if s, ok := birthValue(d, ws, end); ok {
			out = append(out, s)
		}
	}
	return out
}

// hasASCIIHomoglyph сообщает, что в тексте есть латинские буквы, похожие на
// кириллические. Проверка идёт по байтам и обрывается на первой находке.
func hasASCIIHomoglyph(s string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 128 && homoglyphASCII[b] != 0 {
			return true
		}
	}
	return false
}

// foldedIndex ищет якорь, считая латинские буквы, похожие на кириллические,
// той же буквой: «Мeстo pождeния» с латинскими e, o и p — это тот же якорь.
// Смещения возвращаются в координатах исходной строки, поэтому сворачивание
// не сдвигает границы значения. Готовый FoldHomoglyphs здесь не годится: он
// заменяет однобайтовую латиницу двухбайтовой кириллицей и все смещения после
// первой же замены уезжают.
//
// Якорь передаётся рунами и сам сворачивания не требует: все якоря написаны
// кириллицей или латиницей целиком.
func foldedIndex(low string, from int, anchor []rune) (int, int, bool) {
	if len(anchor) == 0 {
		return 0, 0, false
	}
	for i := from; i < len(low); {
		r, size := utf8.DecodeRuneInString(low[i:])
		if foldRune(r) == anchor[0] {
			j, k := i, 0
			for k < len(anchor) && j < len(low) {
				rr, sz := utf8.DecodeRuneInString(low[j:])
				if foldRune(rr) != anchor[k] {
					break
				}
				j += sz
				k++
			}
			if k == len(anchor) {
				return i, j, true
			}
		}
		i += size
	}
	return 0, 0, false
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
	from = birthSkipQualifier(d.Lower, from)
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

// birthSkipQualifier сдвигает позицию за уточнение источника, если оно стоит
// сразу за якорем. Уточнение должно кончаться на границе слова, иначе «по
// анкете» совпало бы с началом другого слова.
func birthSkipQualifier(low string, from int) int {
	rest := low[from:]
	trimmed := strings.TrimLeft(rest, " \t:,-")
	off := from + len(rest) - len(trimmed)
	for _, q := range birthQualifiers {
		if !strings.HasPrefix(trimmed, q) {
			continue
		}
		end := off + len(q)
		if r, _ := firstRune(low[end:]); end < len(low) && unicode.IsLetter(r) {
			continue
		}
		return end
	}
	return from
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
		// Квадратная скобка и черта таблицы разделяют якорь и значение не
		// реже двоеточия: «МР [город Братск]», «| Место рождения | Казань |».
		// Неразрывный пробел приходит из выгрузок и на вид неотличим от
		// обычного.
		if !strings.ContainsRune(" \t\u00a0:,.-–—«\"'()[]|", r) {
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
	// Тире и скобка отделяют от места рождения служебный хвост записи:
	// «г. Пенза — данные взяты из анкеты», «г. Оренбург (заявка принята)».
	if strings.ContainsAny(gap, birthBreakRunes) || strings.Contains(gap, " - ") {
		return false
	}
	if strings.Contains(gap, ".") && !birthAbbrev[ws[j-1].lower] {
		return false
	}
	if ws[j].kind == KindDigit || birthStopWords[ws[j].lower] {
		return false
	}
	if strings.Contains(gap, ",") && !ws[j].title && !addrIsTypeWord(ws[j].lower) {
		// После запятой место рождения продолжается хвостом региона или
		// страны: «г. Калуга, ханты-мансийский автономный округ», «г. Калуга,
		// Россия», «д. Михайловка, липецкая область». В нижнем регистре слово
		// не заглавное, поэтому опорой служат словарь мест, тип населённого
		// пункта или региона впереди и список стран.
		if !birthPlaceContinuation(ws, j) && !birthRegionTail(ws, j) && !birthCountryWords[ws[j].lower] {
			return false
		}
	}
	return true
}

// birthPlaceContinuation сообщает, что слово после запятой продолжает место
// рождения, даже если написано со строчной буквы. Заглавная буква в нижнем
// регистре недоступна, поэтому опорой становятся словарь мест и тип региона
// или населённого пункта впереди: «липецкая область», «ханты-мансийский
// автономный округ».
func birthPlaceContinuation(ws []addrWord, j int) bool {
	norm := FoldHomoglyphs(ws[j].lower)
	if _, ok := dict.Place(norm); ok {
		return true
	}
	for k := j + 1; k < len(ws) && k <= j+3; k++ {
		if addrIsTypeWord(FoldHomoglyphs(ws[k].lower)) {
			return true
		}
	}
	return false
}

// birthRegionTail сообщает, что слово начинает хвост региона: прилагательное
// вроде «московская» или «ханты-мансийский», за которым в ближайших словах
// идёт тип региона («область», «край», «округ»). Дефисное название разбивается
// токенизатором на части, поэтому смотрим на несколько слов вперёд.
func birthRegionTail(ws []addrWord, j int) bool {
	for k := j + 1; k < len(ws) && k <= j+3; k++ {
		if _, ok := addrRegionTypes[ws[k].lower]; ok {
			return true
		}
	}
	return false
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
