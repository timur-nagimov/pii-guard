package pii

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Здесь собран детектор места рождения. От разбора адреса он отделён потому,
// что устроен иначе: адрес складывается из опознанных компонентов и типов
// улиц, а место рождения читается строго от якорного слова до стоп-слова, и
// словари у них почти не пересекаются. Общее у детекторов только разбиение
// текста на слова — addrWords из detect_address.go.
//
// Здесь же лежит поиск якоря с учётом омоглифов: он написан для места
// рождения, где подмена кириллицы похожей латиницей встречается чаще всего.
// Проверку hasASCIIHomoglyph переиспользует и detect_docwords.go.

// birthAnchors — якоря места рождения. Место рождения ищется только по якорю:
// само по себе название города персональными данными не является.
var birthAnchors = []string{
	"место рождения", "место рожд", "места рождения", "мест. рожд",
	"м.р.", "м. р.", "мр", "родился в", "родилась в", "родился", "родилась",
	"уроженец", "уроженка", "уроженцем", "уроженки", "родом из",
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

// birthBreakRunes — знаки, которые обрывают место рождения: за ними идёт
// служебный хвост записи, а не продолжение названия.
const birthBreakRunes = "—–()[]{}«»\"|/"

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
