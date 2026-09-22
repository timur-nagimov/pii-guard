package pii

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind — класс символа, к которому отнесён токен.
type Kind uint8

// Классы токенов, которые различает токенизатор.
const (
	KindSpace Kind = iota
	KindDigit
	KindCyr
	KindLat
	KindPunct
	KindOther
)

// Token — непрерывный участок текста из символов одного класса.
// Границы заданы байтовыми смещениями в Doc.Text.
type Token struct {
	Kind  Kind
	Start int
	End   int
}

// Doc — разобранный документ: исходный текст, его версия в нижнем регистре с
// теми же байтовыми смещениями, разбиение на токены и индекс начал рун.
//
// Doc создаётся один раз на запрос и дальше только читается, поэтому его можно
// без блокировок передавать всем детекторам.
type Doc struct {
	// Text — исходная строка. Никогда не изменяется.
	Text string
	// Lower — строка в нижнем регистре, побайтово той же длины, что и Text.
	// Руна приводится к нижнему регистру только если её нижний регистр
	// занимает столько же байт; для кириллицы, латиницы и цифр это всегда так.
	// Благодаря этому смещения в Lower и Text совпадают.
	Lower string
	// Tokens — разбиение текста на участки одного класса символов.
	Tokens []Token

	runeStarts []int32
	runs       []NumRun
}

// NewDoc разбирает текст: строит нижний регистр, токены и индекс рун.
func NewDoc(text string) *Doc {
	d := &Doc{Text: text}
	d.Lower = lowerSameWidth(text)
	d.tokenize()
	return d
}

// lowerSameWidth приводит строку к нижнему регистру, сохраняя байтовую длину.
// Руны, у которых нижний регистр занимает другое число байт, остаются как есть.
func lowerSameWidth(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		lr := unicode.ToLower(r)
		if lr != r && utf8.RuneLen(lr) != utf8.RuneLen(r) {
			lr = r
		}
		b.WriteRune(lr)
	}
	return b.String()
}

// classify относит руну к классу токена.
func classify(r rune) Kind {
	switch {
	case unicode.IsSpace(r):
		return KindSpace
	case r >= '0' && r <= '9':
		return KindDigit
	case unicode.Is(unicode.Cyrillic, r):
		return KindCyr
	case unicode.Is(unicode.Latin, r):
		return KindLat
	case unicode.IsPunct(r) || unicode.IsSymbol(r):
		return KindPunct
	default:
		return KindOther
	}
}

// tokenize проходит по тексту один раз и заполняет Tokens и runeStarts.
func (d *Doc) tokenize() {
	n := len(d.Text)
	d.Tokens = make([]Token, 0, n/4+1)
	d.runeStarts = make([]int32, 0, n/2+1)

	cur := Token{Start: 0, End: 0, Kind: KindOther}
	started := false
	for i, r := range d.Text {
		d.runeStarts = append(d.runeStarts, int32(i))
		k := classify(r)
		if !started {
			cur = Token{Kind: k, Start: i, End: i + utf8.RuneLen(r)}
			started = true
			continue
		}
		if k == cur.Kind {
			cur.End = i + utf8.RuneLen(r)
			continue
		}
		d.Tokens = append(d.Tokens, cur)
		cur = Token{Kind: k, Start: i, End: i + utf8.RuneLen(r)}
	}
	if started {
		d.Tokens = append(d.Tokens, cur)
	}
	d.runeStarts = append(d.runeStarts, int32(n))
}

// RuneLen возвращает число рун в документе.
func (d *Doc) RuneLen() int {
	if len(d.runeStarts) == 0 {
		return 0
	}
	return len(d.runeStarts) - 1
}

// runeIndexAt возвращает индекс руны, которой принадлежит байтовое смещение.
func (d *Doc) runeIndexAt(byteOff int) int {
	if byteOff <= 0 {
		return 0
	}
	if byteOff >= len(d.Text) {
		return d.RuneLen()
	}
	i := sort.Search(len(d.runeStarts), func(i int) bool {
		return int(d.runeStarts[i]) > byteOff
	})
	if i > 0 {
		i--
	}
	return i
}

// byteAtRune возвращает байтовое смещение начала руны с указанным индексом.
func (d *Doc) byteAtRune(runeIdx int) int {
	if runeIdx <= 0 {
		return 0
	}
	if runeIdx >= len(d.runeStarts) {
		return len(d.Text)
	}
	return int(d.runeStarts[runeIdx])
}

// WindowRunes расширяет диапазон [start, end) на before рун влево и after рун
// вправо и возвращает байтовые границы получившегося окна.
//
// Окна задаются в рунах, а не в байтах: для кириллицы байтовое окно вдвое
// короче задуманного, и якорь перестаёт доставать до значения.
func (d *Doc) WindowRunes(start, end, before, after int) (int, int) {
	if start < 0 {
		start = 0
	}
	if end > len(d.Text) {
		end = len(d.Text)
	}
	lo := d.byteAtRune(d.runeIndexAt(start) - before)
	hi := d.byteAtRune(d.runeIndexAt(end) + after)
	if hi > len(d.Text) {
		hi = len(d.Text)
	}
	if lo > hi {
		lo = hi
	}
	return lo, hi
}

// LowerWindow возвращает участок текста в нижнем регистре вокруг диапазона.
func (d *Doc) LowerWindow(start, end, before, after int) string {
	lo, hi := d.WindowRunes(start, end, before, after)
	return d.Lower[lo:hi]
}

// FindAnchor ищет любое из якорных слов в окне вокруг диапазона и возвращает
// найденное слово. Якоря задаются в нижнем регистре.
func (d *Doc) FindAnchor(start, end int, anchors []string, before, after int) (string, bool) {
	w := d.LowerWindow(start, end, before, after)
	best := ""
	for _, a := range anchors {
		if a == "" {
			continue
		}
		if strings.Contains(w, a) && len(a) > len(best) {
			best = a
		}
	}
	return best, best != ""
}

// HasAnchorBefore ищет якорь только слева от диапазона.
func (d *Doc) HasAnchorBefore(start int, anchors []string, before int) (string, bool) {
	return d.FindAnchor(start, start, anchors, before, 0)
}

// LineBounds возвращает байтовые границы строки, в которую попадает смещение.
// Фрагменты персональных данных не должны пересекать перевод строки.
func (d *Doc) LineBounds(off int) (int, int) {
	if off < 0 {
		off = 0
	}
	if off > len(d.Text) {
		off = len(d.Text)
	}
	lo := strings.LastIndexByte(d.Text[:off], '\n') + 1
	hi := strings.IndexByte(d.Text[off:], '\n')
	if hi < 0 {
		hi = len(d.Text)
	} else {
		hi += off
	}
	return lo, hi
}

// TokenIndexAt возвращает индекс токена, которому принадлежит байтовое
// смещение, либо -1, если смещение вне текста.
func (d *Doc) TokenIndexAt(byteOff int) int {
	i := sort.Search(len(d.Tokens), func(i int) bool {
		return d.Tokens[i].End > byteOff
	})
	if i < len(d.Tokens) && d.Tokens[i].Start <= byteOff {
		return i
	}
	return -1
}

// IsTokenBoundary сообщает, что диапазон начинается и заканчивается на границе
// токена. Нужно, чтобы якорь «инн» не срабатывал внутри слова «терминн».
func (d *Doc) IsTokenBoundary(start, end int) bool {
	if start < 0 || end > len(d.Text) || start > end {
		return false
	}
	startOK := start == 0
	if !startOK {
		if i := d.TokenIndexAt(start); i >= 0 {
			startOK = d.Tokens[i].Start == start
		}
	}
	endOK := end == len(d.Text)
	if !endOK {
		if i := d.TokenIndexAt(end); i >= 0 {
			endOK = d.Tokens[i].Start == end
		}
	}
	return startOK && endOK
}
