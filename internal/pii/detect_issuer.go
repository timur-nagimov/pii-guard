package pii

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Здесь лежит детектор органа, выдавшего документ: «паспорт выдан ГУ МВД
// России по г. Москве». Разбор держится отдельно от соседних словесных
// детекторов (detect_docwords.go), потому что своих перечней и правил
// окончания значения он ни с кем не делит, а общее берёт только через
// вспомогательные функции с приставкой dw.

// issuerAnchors — слова, после которых в документе стоит название органа.
// Сам якорь в маскируемый фрагмент не входит.
var issuerAnchors = []string{
	"кем выдан", "орган выдачи", "выдавший орган", "выдан", "выдано", "выдана",
	"выданный", "выданная", "выданного", "выданным", "выдавший", "выдавшим",
	"выдавшего", "issued by",
	// Косвенные падежи и канцелярские обороты: в анкетах пишут
	// «наименование органа выдачи», «подразделение выдачи», и без этих
	// форм якорь не находился вовсе.
	"наименование органа выдачи", "органа выдачи", "подразделение выдачи",
	"подразделением выдачи", "орган, выдавший", "кем выдано", "кем выдана",
}

// Якорь готовится один раз при запуске: подбор идёт на каждом слове каждого
// текста, и повторная подготовка там недопустима.
var issuerAnchorSet = newAnchorSet(issuerAnchors)

// issuerAgencyWords — ведомственные сокращения. Без такого признака внутри
// фрагмента «выдан кредит» и «выдана справка» органом выдачи не считаются.
var issuerAgencyWords = []string{
	"мвд", "гумвд", "умвд", "омвд", "оумвд", "оуфмс", "уфмс", "фмс", "овд",
	"ровд", "оувд", "увд", "гувд", "овм", "оувм", "тп", "мрэо", "мро",
}

// issuerAgencyPhrases — развёрнутые названия органов. Хранятся основами,
// чтобы одинаково ловить «отдел внутренних дел» и «отделом внутренних дел».
var issuerAgencyPhrases = []string{
	"внутренних дел", "вопросам миграции", "паспортный стол", "паспортным столом",
	"паспортного стола", "отделение полиции", "отделением полиции",
	"отдел полиции", "отделом полиции", "миграционной службы", "миграционная служба",
	"территориальный пункт", "территориальным пунктом", "миграционным пунктом",
}

// issuerPrefixWords — служебные слова перед сокращением, которые относятся к
// названию органа: «ГУ МВД», «Отделом УФМС».
var issuerPrefixWords = []string{
	"гу", "ту", "мо", "мп", "главное", "главным", "отдел", "отделом", "отделение",
	"отделением", "отд", "управление", "управлением", "территориальный",
	"территориальным", "пункт", "пунктом",
}

// issuerStopWords — слова, на которых сведения об органе заканчиваются.
var issuerStopWords = []string{
	"код подразделения", "код подр", "к/п", "дата выдачи", "зарегистрирован",
	"проживающ", "проживает", "место рождения", "место жительства", "адрес",
	"снилс", "телефон", "номер паспорта", "серия",
}

// issuerAbbrev — сокращения длиной от четырёх букв, после которых точка не
// заканчивает предложение: «по Респ. Татарстан».
var issuerAbbrev = []string{"респ", "терр", "адм", "упр", "авт"}

// issuerGeoWords — слова, с которых начинается уточнение места внутри
// названия органа. После них запятая не заканчивает название: запись
// «по г. Тюмень, район Заводской» — один орган, а не два сведения.
var issuerGeoWords = []string{
	"район", "района", "районе", "районам", "г", "гор", "город", "города",
	"обл", "область", "области", "край", "крае", "края", "респ", "республика",
	"республике", "округ", "округе",
}

// issuerMaxRunes ограничивает длину названия органа. Без ограничения ошибка
// поиска стоп-признака утащила бы в маску весь абзац.
const issuerMaxRunes = 120

// issuerDetector находит орган, выдавший документ. Ищет двумя путями: после
// слова «выдан» и по самому ведомственному сокращению в паспортном контексте.
type issuerDetector struct{}

// NewIssuerDetector создаёт детектор органа выдачи.
func NewIssuerDetector() Detector { return issuerDetector{} }

// Types перечисляет типы, которые находит детектор.
func (issuerDetector) Types() []Type { return []Type{TypeIssuer} }

// Detect ищет название органа выдачи.
func (issuerDetector) Detect(d *Doc) []Span {
	out := issuerByAnchor(d)
	return append(out, issuerByAgency(d, out)...)
}

// issuerByAnchor собирает названия органа, стоящие после слова «выдан».
func issuerByAnchor(d *Doc) []Span {
	var out []Span
	for _, tok := range d.Tokens {
		if tok.Kind != KindCyr && tok.Kind != KindLat {
			continue
		}
		if dwIsWordRune(dwRuneBefore(d.Lower, tok.Start)) {
			continue
		}
		anchorLen, ok := dwMatchLongestAnchor(d.Lower, tok.Start, issuerAnchorSet)
		if !ok {
			continue
		}
		start, end, ok := issuerValueAfter(d, tok.Start+anchorLen)
		if !ok || dwOverlaps(out, start, end) {
			continue
		}
		out = append(out, Span{Start: start, End: end, Type: TypeIssuer, Conf: ConfHigh, Reason: "issuer:anchor"})
	}
	return out
}

// issuerValueAfter вычисляет границы названия органа, начинающегося сразу за
// якорем, и проверяет, что внутри есть ведомственный признак.
func issuerValueAfter(d *Doc, from int) (int, int, bool) {
	start, ok := dwSkipSeparators(d.Lower, from)
	if !ok {
		return 0, 0, false
	}
	_, lineEnd := d.LineBounds(start)
	start, end := NormalizeSpan(d.Text, start, issuerValueEnd(d, start, lineEnd))
	if start >= end || !issuerHasAgency(d.Lower[start:end]) {
		return 0, 0, false
	}
	return start, end, true
}

// issuerByAgency находит орган, названный без слова «выдан»: в паспортном
// контексте строка «ОУФМС России по г. Москве» сама по себе является органом.
func issuerByAgency(d *Doc, found []Span) []Span {
	var out []Span
	for _, tok := range d.Tokens {
		if tok.Kind != KindCyr || !dwInList(d.Lower[tok.Start:tok.End], issuerAgencyWords) {
			continue
		}
		start := issuerExtendLeft(d, tok.Start)
		_, lineEnd := d.LineBounds(start)
		start, end := NormalizeSpan(d.Text, start, issuerValueEnd(d, start, lineEnd))
		if start >= end || dwOverlaps(found, start, end) || dwOverlaps(out, start, end) {
			continue
		}
		if _, ok := d.FindAnchor(start, end, anchorsPassport, anchorWindow, anchorWindow/2); !ok {
			continue
		}
		out = append(out, Span{Start: start, End: end, Type: TypeIssuer, Conf: ConfAnchored, Reason: "issuer:agency_context"})
	}
	return out
}

// issuerExtendLeft добавляет к фрагменту служебные слова слева: в записи
// «ГУ МВД России» сокращение начинается не с найденного слова.
func issuerExtendLeft(d *Doc, start int) int {
	lineLo, _ := d.LineBounds(start)
	pos := start
	for n := 0; n < 2; n++ {
		ws, we, ok := dwPrevWord(d.Lower, lineLo, pos)
		if !ok || !dwInList(d.Lower[ws:we], issuerPrefixWords) {
			break
		}
		pos = ws
	}
	return pos
}

// issuerValueEnd ищет конец названия органа: стоп-слово, дату, запятую,
// точку с запятой или конец предложения. Дальше границы строки не уходит.
func issuerValueEnd(d *Doc, start, lineEnd int) int {
	if _, hi := d.WindowRunes(start, start, 0, issuerMaxRunes); hi < lineEnd {
		lineEnd = hi
	}
	for i := start; i < lineEnd; {
		if issuerStopsAt(d.Lower, i, lineEnd) {
			return i
		}
		_, size := firstRune(d.Lower[i:])
		if size == 0 {
			break
		}
		i += size
	}
	return lineEnd
}

// issuerStopsAt сообщает, что в данном смещении начинается стоп-признак:
// знак препинания или слово, после которого идут уже другие сведения.
func issuerStopsAt(low string, i, lineEnd int) bool {
	if stop, decided := issuerPunctStops(low, i, lineEnd); decided {
		return stop
	}
	if dwIsWordRune(dwRuneBefore(low, i)) {
		return false
	}
	for _, w := range issuerStopWords {
		if strings.HasPrefix(low[i:], w) {
			return true
		}
	}
	return false
}

// issuerPunctStops разбирает знак в текущем смещении. Второе значение
// сообщает, что знак разобран и проверять стоп-слова уже не нужно.
//
// Скобка и тире с пробелом слева заканчивают название: за ними идёт
// пояснение вида «(заявка принята)» или продолжение предложения.
func issuerPunctStops(low string, i, lineEnd int) (bool, bool) {
	r, _ := firstRune(low[i:])
	switch r {
	case ';', '(', ')':
		return true, true
	case ',':
		return !issuerGeoContinues(low, i, lineEnd), true
	case '.':
		return issuerSentenceEnd(low, i), true
	case '-', '–', '—':
		return unicode.IsSpace(dwRuneBefore(low, i)), true
	}
	if r >= '0' && r <= '9' {
		return issuerDigitStops(low, i, lineEnd), true
	}
	return false, false
}

// issuerGeoContinues сообщает, что после запятой идёт уточнение места, а не
// новые сведения. Без этого название обрывалось на запятой и район города в
// маску не попадал.
//
// Проверяются два слова: уточнение бывает как с признаком впереди («район
// Заводской»), так и с признаком позади («Московская область»).
func issuerGeoContinues(low string, comma, lineEnd int) bool {
	pos := comma + 1
	for n := 0; n < 2; n++ {
		ws, we, ok := dwNextWord(low, pos, lineEnd)
		if !ok {
			return false
		}
		if issuerIsGeoWord(low, ws, we) {
			return true
		}
		pos = we
	}
	return false
}

// issuerIsGeoWord сообщает, что слово обозначает единицу административного
// деления.
func issuerIsGeoWord(low string, ws, we int) bool {
	word := low[ws:we]
	// Сокращение «р-н» распадается при разборе на слово из одной буквы,
	// поэтому хвост проверяется отдельно.
	if word == "р" {
		return strings.HasPrefix(low[we:], "-н")
	}
	return dwInList(word, issuerGeoWords)
}

// issuerDigitStops сообщает, что число похоже на дату, год или код, а не на
// номер отделения. Номер вида «ОП № 3» остаётся частью названия.
func issuerDigitStops(low string, i, lineEnd int) bool {
	j := i
	for j < lineEnd && low[j] >= '0' && low[j] <= '9' {
		j++
	}
	if j-i >= 4 {
		return true
	}
	return j+1 < lineEnd && strings.ContainsRune("./-", rune(low[j])) &&
		low[j+1] >= '0' && low[j+1] <= '9'
}

// issuerSentenceEnd отличает конец предложения от точки в сокращении: после
// «г.» и «Респ.» название продолжается.
func issuerSentenceEnd(low string, i int) bool {
	if next := dwRuneAt(low, i+1); next != 0 && !unicode.IsSpace(next) {
		return false
	}
	word := dwWordBefore(low, i)
	if utf8.RuneCountInString(word) < 4 {
		return false
	}
	return !dwInList(word, issuerAbbrev)
}

// issuerHasAgency проверяет ведомственный признак внутри фрагмента. Вариант
// без точек нужен для записей вида «О.У.Ф.М.С.».
func issuerHasAgency(frag string) bool {
	// Ведомственный признак ищется по свёрнутому тексту: в распознанных
	// сканах пишут «внутpенних дел» с латинской «p», и признак не находился,
	// а без него весь фрагмент отбрасывался. Смещения здесь не нужны — ответ
	// булев, поэтому обычное сворачивание годится.
	if hasASCIIHomoglyph(frag) {
		frag = FoldHomoglyphs(frag)
	}
	for _, w := range issuerAgencyWords {
		if dwContainsWord(frag, w) {
			return true
		}
	}
	compact := strings.ReplaceAll(frag, ".", "")
	for _, w := range issuerAgencyWords {
		if dwContainsWord(compact, w) {
			return true
		}
	}
	_, ok := ContainsAnyLower(frag, issuerAgencyPhrases)
	return ok
}
