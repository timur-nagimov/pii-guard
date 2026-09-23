package pii

import (
	"unicode"
	"unicode/utf8"
)

// Здесь лежит детектор гражданства: «гражданство — Российская Федерация».
// От соседних словесных детекторов (detect_docwords.go) он отличается тем,
// что якорь у него бывает и справа от значения, поэтому значение ищется в обе
// стороны; свои у него и перечни — уточняющие слова и основы названий стран.

// citizenshipAnchors — слова, после которых стоит гражданство. Без якоря
// название страны гражданством не считается: это может быть место поездки.
var citizenshipAnchors = []string{
	"гражданство", "гражданства", "гражданстве", "гражданин", "гражданина",
	"гражданину", "гражданином", "гражданка", "гражданки", "гражданке",
	"гражданкой", "подданство", "подданства", "подданный", "подданная",
	"гражданская принадлежность", "гражданской принадлежности",
	"гражданскую принадлежность", "гражданской принадлежностью",
	"citizenship", "nationality",
}

// Якорь готовится один раз при запуске: подбор идёт на каждом слове каждого
// текста, и повторная подготовка там недопустима.
var citizenshipAnchorSet = newAnchorSet(citizenshipAnchors)

// citizenshipQualifiers — слова, которые входят в значение гражданства, но
// сами гражданством не являются: «Республика», «Федерация», «Штаты». Сюда же
// отнесены «гражданин» и «подданный»: в записи «гражданство: гражданин
// России» эталонное значение начинается именно с этого слова.
var citizenshipQualifiers = []string{
	"республика", "республики", "республику", "республике", "респ",
	"федерация", "федерации", "федерацию", "народная", "народной",
	"демократическая", "исламская", "королевство", "королевства",
	"соединенные", "соединённые", "штаты", "южная", "северная", "новая",
	"гражданин", "гражданина", "гражданка", "гражданки", "подданный",
	"подданная", "подданного", "citizen", "national", "и",
	"republic", "federation", "united", "states",
}

// citizenshipFillerWords — служебные слова между якорем и значением:
// «гражданство по паспорту», «гражданство клиента». В маску они не попадают,
// но и значение за ними терять нельзя.
var citizenshipFillerWords = []string{
	"по", "паспорту", "паспорта", "документу", "документам", "клиента",
	"клиенту", "заявителя", "заявителю", "страна", "страны", "of",
	"владельца", "владельцу", "счёта", "счета", "счет",
}

// citizenshipMaxFillers — сколько служебных слов допускается между якорем и
// значением. Двух хватает на запись «гражданство по паспорту».
const citizenshipMaxFillers = 2

// citizenshipStems — основы названий стран и прилагательных-демонимов.
// Основа покрывает все падежные формы, а ограничение хвоста не даёт ей
// зацепить постороннее слово.
var citizenshipStems = []string{
	"росси", "российск", "россиян", "рф", "казахстан", "казахстанск", "казахск",
	"казах", "беларус", "белорус", "узбекистан", "узбекск", "узбек", "армени",
	"армянск", "армян", "киргиз", "кыргыз", "кыргызстан", "таджикистан",
	"таджикск", "таджик", "украин", "азербайджан", "грузи", "грузинск",
	"молдав", "молдов", "туркмен", "туркменистан", "латви", "латышск", "литв", "литовск",
	"эстон", "израил", "израильск", "герман", "немецк", "турци", "турецк",
	"кита", "китайск", "вьетнам", "сша", "америк", "американск", "инди",
	"индийск", "серб", "польш", "польск", "поляк", "финлянд", "финск", "итал",
	"франци", "французск", "испани", "испанск", "великобритани", "британск",
	"англи", "английск", "абхаз", "осети", "монгол", "афган", "сири", "иран",
	"ирак", "египет", "египетск", "канад", "бразил", "япон", "коре",
	"швейцар", "швеци", "шведск", "норвеги", "нидерланд", "бельги", "австри",
	"австрал", "чехи", "чешск", "словак", "словени", "болгари", "болгарск",
	"румыни", "венгри", "греци", "греческ", "португал", "иностранн", "двойн",
	"russia", "russian", "kazakh", "belarus", "ukrain", "uzbek", "armenia",
	"georgia", "germany", "turkey", "china", "india", "usa", "ru", "rf",
}

// citizenshipMaxWords — сколько слов может занимать значение гражданства:
// «гражданин Соединённых Штатов Америки» — самый длинный ожидаемый случай.
const citizenshipMaxWords = 4

// citizenshipMaxRunes ограничивает длину значения гражданства.
const citizenshipMaxRunes = 60

// citizenshipDetector находит гражданство, указанное после якорного слова.
type citizenshipDetector struct{}

// NewCitizenshipDetector создаёт детектор гражданства.
func NewCitizenshipDetector() Detector { return citizenshipDetector{} }

// Types перечисляет типы, которые находит детектор.
func (citizenshipDetector) Types() []Type { return []Type{TypeCitizenship} }

// Detect ищет значение гражданства после якоря. Маскируется только значение,
// само слово «гражданство» остаётся в тексте.
func (citizenshipDetector) Detect(d *Doc) []Span {
	var out []Span
	for _, tok := range d.Tokens {
		s, ok := citizenshipAtToken(d, tok)
		if !ok {
			continue
		}
		if dwOverlaps(out, s.Start, s.End) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// citizenshipAtToken разбирает один токен как якорное слово и возвращает
// значение гражданства, к которому этот якорь относится.
func citizenshipAtToken(d *Doc, tok Token) (Span, bool) {
	if tok.Kind != KindCyr && tok.Kind != KindLat {
		return Span{}, false
	}
	if dwIsWordRune(dwRuneBefore(d.Lower, tok.Start)) {
		return Span{}, false
	}
	anchorLen, ok := dwMatchLongestAnchor(d.Lower, tok.Start, citizenshipAnchorSet)
	if !ok {
		return Span{}, false
	}
	if start, end, ok := citizenshipValue(d, tok.Start+anchorLen); ok {
		return Span{Start: start, End: end, Type: TypeCitizenship, Conf: ConfHigh, Reason: "citizenship:anchor"}, true
	}
	// Якорь бывает и справа от значения: «Республика Армения —
	// гражданская принадлежность». Смотрим назад по той же строке.
	start, end, ok := citizenshipValueBefore(d, tok.Start)
	if !ok {
		return Span{}, false
	}
	return Span{Start: start, End: end, Type: TypeCitizenship, Conf: ConfHigh, Reason: "citizenship:anchor_after"}, true
}

// citizenshipValueBefore ищет название страны слева от якоря, в пределах той
// же строки. Между значением и якорем допускаются только знаки и пробелы:
// слово между ними означает, что якорь относится не к этому значению.
func citizenshipValueBefore(d *Doc, anchorStart int) (int, int, bool) {
	lineLo, _ := d.LineBounds(anchorStart)
	// Отступаем через разделители влево.
	pos := anchorStart
	for pos > lineLo {
		r, size := utf8.DecodeLastRuneInString(d.Lower[lineLo:pos])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			break
		}
		pos -= size
	}
	if pos == anchorStart || pos <= lineLo {
		return 0, 0, false
	}
	// Собираем до citizenshipMaxWords слов влево и берём самую длинную
	// запись, которую разбор признаёт страной: «Республика Армения» целиком,
	// а не одно слово «Армения».
	best := -1
	wordStart := pos
	for n := 0; n < citizenshipMaxWords; n++ {
		ws, ok := dwPrevWordStart(d.Lower, lineLo, wordStart)
		if !ok {
			break
		}
		if end, ok := citizenshipWordsEnd(d.Lower, ws, pos); ok && end == pos {
			best = ws
		}
		wordStart = ws
	}
	if best < 0 {
		return 0, 0, false
	}
	start, end := NormalizeSpan(d.Text, best, pos)
	if start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// dwPrevWordStart возвращает начало слова, стоящего непосредственно перед
// позицией, пропуская разделители.
func dwPrevWordStart(low string, lineLo, pos int) (int, bool) {
	ws, _, ok := dwPrevWord(low, lineLo, pos)
	if !ok {
		return 0, false
	}
	return ws, true
}

// citizenshipValue вычисляет границы названия страны или демонима после якоря.
func citizenshipValue(d *Doc, from int) (int, int, bool) {
	start, ok := dwSkipSeparators(d.Lower, from)
	if !ok {
		return 0, 0, false
	}
	_, lineEnd := d.LineBounds(start)
	start = citizenshipSkipFillers(d.Lower, start, lineEnd)
	if _, hi := d.WindowRunes(start, start, 0, citizenshipMaxRunes); hi < lineEnd {
		lineEnd = hi
	}
	end, ok := citizenshipWordsEnd(d.Lower, start, lineEnd)
	if !ok {
		return 0, 0, false
	}
	start, end = NormalizeSpan(d.Text, start, end)
	if start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// citizenshipSkipFillers пропускает служебные слова между якорем и значением.
// Без этого запись «гражданство по паспорту: Россия» терялась целиком: сразу
// за якорем стоит предлог, а не название страны.
func citizenshipSkipFillers(low string, start, lineEnd int) int {
	pos := start
	for n := 0; n < citizenshipMaxFillers; n++ {
		ws, we, ok := dwNextWord(low, pos, lineEnd)
		if !ok || ws != pos || !dwInList(low[ws:we], citizenshipFillerWords) {
			return pos
		}
		next, ok := dwSkipSeparators(low, we)
		if !ok {
			return pos
		}
		pos = next
	}
	return pos
}

// citizenshipWordsEnd набирает слова значения, пока они остаются частью
// названия страны. Хотя бы одно слово обязано быть названием: из одного
// уточняющего слова «Республика» гражданство не следует.
func citizenshipWordsEnd(low string, start, limit int) (int, bool) {
	pos, end, matched := start, 0, false
	for n := 0; n < citizenshipMaxWords; n++ {
		ws, we, ok := dwNextWord(low, pos, limit)
		if !ok {
			break
		}
		word := low[ws:we]
		isStem := dwHasStem(word, citizenshipStems)
		if !isStem && !dwInList(word, citizenshipQualifiers) {
			break
		}
		matched = matched || isStem
		end, pos = we, we
	}
	if !matched {
		return 0, false
	}
	return end, true
}
