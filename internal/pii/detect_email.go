package pii

import (
	"strings"
	"unicode"
)

// emailDetector находит адреса электронной почты. Адрес ищется от знака
// собаки в обе стороны, поэтому поддерживаются и латинские, и кириллические
// домены, и знак плюс в левой части.
type emailDetector struct{}

// NewEmailDetector создаёт детектор адресов электронной почты.
func NewEmailDetector() Detector { return emailDetector{} }

// Types перечисляет типы, которые находит детектор.
func (emailDetector) Types() []Type { return []Type{TypeEmail} }

// localChars — знаки, допустимые в левой части адреса помимо букв и цифр.
const localChars = ".!#$%&'*+/=?^_`{|}~-"

// Detect ищет адреса электронной почты.
func (emailDetector) Detect(d *Doc) []Span {
	var out []Span
	text := d.Text
	for i := 0; i < len(text); i++ {
		if text[i] != '@' {
			continue
		}
		start := scanLocalPart(text, i)
		end, ok := scanDomain(text, i+1)
		if !ok || start == i {
			continue
		}
		start, end = NormalizeSpan(text, start, end)
		if start >= end {
			continue
		}
		out = append(out, Span{Start: start, End: end, Type: TypeEmail, Conf: ConfHigh, Reason: "email:shape"})
		i = end
	}
	return out
}

// scanLocalPart идёт влево от знака собаки по допустимым знакам левой части.
func scanLocalPart(text string, at int) int {
	start := at
	for start > 0 {
		r, size := decodeLastRuneBefore(text, start)
		if size == 0 {
			break
		}
		if !isLocalRune(r) {
			break
		}
		start -= size
	}
	// Точка и дефис на краю к адресу не относятся.
	for start < at && strings.ContainsRune(".-_", rune(text[start])) {
		start++
	}
	return start
}

func isLocalRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	return strings.ContainsRune(localChars, r)
}

// scanDomain идёт вправо от знака собаки и требует хотя бы одну точку и
// доменную зону из букв.
func scanDomain(text string, from int) (int, bool) {
	end, dots := domainEnd(text, from)
	if dots == 0 {
		return 0, false
	}
	// Хвостовая точка в конец адреса не входит: в предложении «почта a@b.ru.»
	// последняя точка принадлежит предложению, а не адресу.
	for end > from && text[end-1] == '.' {
		end--
	}
	// После обрезки последняя точка домена могла оказаться за границей, тогда
	// её нужно найти заново внутри укороченного диапазона.
	lastDot := strings.LastIndexByte(text[from:end], '.')
	if lastDot < 0 {
		return 0, false
	}
	if !isDomainZone(text[from+lastDot+1 : end]) {
		return 0, false
	}
	return end, true
}

// domainEnd отмечает конец доменной части: буквы, цифры, дефисы и точки.
// Точки считаются отдельно, потому что без точки домена не бывает: запись
// вида «user@localhost» адресом не считается.
func domainEnd(text string, from int) (end, dots int) {
	for end = from; end < len(text); {
		r, size := firstRune(text[end:])
		if size == 0 {
			break
		}
		if r == '.' {
			dots++
		} else if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' {
			break
		}
		end += size
	}
	return end, dots
}

// isDomainZone проверяет хвост домена после последней точки. Зоны короче двух
// букв не бывает, и цифр в зоне не бывает тоже: без этой проверки адресом
// считался бы любой набор чисел через точку вроде «user@1.23».
func isDomainZone(zone string) bool {
	if len([]rune(zone)) < 2 {
		return false
	}
	for _, r := range zone {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func firstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}
