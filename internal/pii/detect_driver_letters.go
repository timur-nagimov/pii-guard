package pii

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Здесь лежит детектор серии водительского удостоверения старого образца:
// «77 АВ 123456». От соседних словесных детекторов (detect_docwords.go) он
// отличается тем, что значением служит не слово, а форма записи из трёх
// частей, поэтому разбор идёт по токенам подряд, а не по границам слов.

// driverLettersAnchors — якоря водительского удостоверения. Короткие «в/у» и
// «ву» ищутся словом целиком, длинные — как начало слова.
var driverLettersAnchors = []string{
	"в/у", "ву", "водительск", "удостоверение водителя", "удостоверения водителя",
	"права", "правами", "driver", "driving", "license", "licence",
}

// driverSepRunes — знаки, допустимые между группами номера удостоверения.
const driverSepRunes = " \t-–—.№/"

// driverStandardLetters — буквы, которые применяются в сериях российских
// документов. Совпадение с этим набором позволяет маскировать номер даже без
// якоря рядом.
const driverStandardLetters = "авекмнорстух"

// driverLettersDetector находит водительское удостоверение старого образца
// вида «77 АА 123456». Форма из десяти цифр разбирается детектором форматных
// типов, здесь она намеренно не повторяется.
type driverLettersDetector struct{}

// NewDriverLicenseLettersDetector создаёт детектор удостоверения с буквенной
// серией.
func NewDriverLicenseLettersDetector() Detector { return driverLettersDetector{} }

// Types перечисляет типы, которые находит детектор.
func (driverLettersDetector) Types() []Type { return []Type{TypeDriverLicense} }

// Detect ищет номера вида «код региона, серия из двух букв, шесть цифр».
func (driverLettersDetector) Detect(d *Doc) []Span {
	var out []Span
	for i := range d.Tokens {
		tok := d.Tokens[i]
		if tok.Kind != KindDigit || tok.End-tok.Start != 2 {
			continue
		}
		s, ok := driverLettersSpan(d, i)
		if !ok || dwOverlaps(out, s.Start, s.End) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// driverLettersSpan проверяет форму номера и назначает уверенность: с якорем
// рядом — высокую, без якоря — только для стандартных букв серии.
func driverLettersSpan(d *Doc, i int) (Span, bool) {
	last, series, ok := driverLettersParts(d, i)
	if !ok {
		return Span{}, false
	}
	start, end := NormalizeSpan(d.Text, d.Tokens[i].Start, d.Tokens[last].End)
	if start >= end {
		return Span{}, false
	}
	window := d.LowerWindow(start, end, anchorWindow, anchorWindow/2)
	if _, found := dwAnyAnchor(window, driverLettersAnchors); found {
		return Span{Start: start, End: end, Type: TypeDriverLicense, Conf: ConfHigh, Reason: "driver:letters_anchor"}, true
	}
	if !driverStandardSeries(series) {
		return Span{}, false
	}
	return Span{Start: start, End: end, Type: TypeDriverLicense, Conf: ConfAnchored, Reason: "driver:letters_shape"}, true
}

// driverLettersParts разбирает три части номера и возвращает индекс токена с
// номером и серию, приведённую к кириллице.
func driverLettersParts(d *Doc, i int) (int, string, bool) {
	toks := d.Tokens
	li, ok := driverNextPart(d, i)
	if !ok {
		return 0, "", false
	}
	last, series, ok := driverSeriesPart(d, li)
	if !ok {
		return 0, "", false
	}
	ni, ok := driverNextPart(d, last)
	if !ok || toks[ni].Kind != KindDigit || toks[ni].End-toks[ni].Start != 6 {
		return 0, "", false
	}
	// Буквы, приклеенные к номеру справа, означают, что это часть другого
	// обозначения, а не номер удостоверения.
	if ni+1 < len(toks) && toks[ni+1].Start == toks[ni].End && driverIsLetterToken(toks[ni+1]) {
		return 0, "", false
	}
	return ni, series, true
}

// driverSeriesPart собирает серию из букв. Серия может распасться на два
// токена, если её набрали вперемешку кириллицей и латиницей: «Aа».
func driverSeriesPart(d *Doc, li int) (int, string, bool) {
	toks := d.Tokens
	if !driverIsLetterToken(toks[li]) {
		return 0, "", false
	}
	last := li
	raw := d.Lower[toks[li].Start:toks[li].End]
	if utf8.RuneCountInString(raw) == 1 && li+1 < len(toks) &&
		toks[li+1].Start == toks[li].End && driverIsLetterToken(toks[li+1]) {
		last = li + 1
		raw += d.Lower[toks[last].Start:toks[last].End]
	}
	series, ok := driverSeries(raw)
	if !ok {
		return 0, "", false
	}
	return last, series, true
}

// driverIsLetterToken сообщает, что токен состоит из букв.
func driverIsLetterToken(t Token) bool {
	return t.Kind == KindCyr || t.Kind == KindLat
}

// driverNextPart находит следующую значащую часть номера, проверяя, что
// разделитель короткий, допустимый и не содержит перевода строки.
func driverNextPart(d *Doc, i int) (int, bool) {
	toks := d.Tokens
	j := i + 1
	for j < len(toks) && (toks[j].Kind == KindSpace || toks[j].Kind == KindPunct) {
		j++
	}
	if j >= len(toks) {
		return 0, false
	}
	sep := d.Text[toks[i].End:toks[j].Start]
	if utf8.RuneCountInString(sep) > 3 {
		return 0, false
	}
	for _, r := range sep {
		if !strings.ContainsRune(driverSepRunes, r) {
			return 0, false
		}
	}
	return j, true
}

// driverSeries приводит две буквы серии к кириллице, учитывая латинские
// двойники, и отвергает всё, что не является парой кириллических букв.
func driverSeries(raw string) (string, bool) {
	var b strings.Builder
	count := 0
	for _, r := range raw {
		f := dwFoldHomoglyph(r)
		if !unicode.Is(unicode.Cyrillic, f) {
			return "", false
		}
		b.WriteRune(f)
		count++
	}
	if count != 2 {
		return "", false
	}
	return b.String(), true
}

// driverStandardSeries сообщает, что обе буквы серии взяты из набора,
// применяемого в российских документах.
func driverStandardSeries(series string) bool {
	for _, r := range series {
		if !strings.ContainsRune(driverStandardLetters, r) {
			return false
		}
	}
	return true
}
