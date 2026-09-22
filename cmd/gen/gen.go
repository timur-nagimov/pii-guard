package main

import (
	"math/rand/v2"
	"strings"

	"pii-guard/internal/pii"
)

// Span — размеченный фрагмент в координатах байтов текста записи.
// Смещения байтовые, как во всём проекте: движок работает с байтами, и набор
// данных должен сравниваться с его выводом без пересчёта координат.
type Span struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Type  string `json:"type"`
}

// Record — одна строка набора данных в формате JSON Lines.
type Record struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Text     string   `json:"text"`
	Spans    []Span   `json:"spans"`
	Values   []string `json:"-"`
}

// frag — кусок будущего текста. Пустой тип означает обрамляющий текст, который
// не размечается; непустой — подставленное значение, попадающее в разметку.
//
// Текст собирается из фрагментов, поэтому границы известны по построению и
// размечать готовую строку не требуется.
type frag struct {
	text string
	typ  pii.Type
}

// lit создаёт неразмечаемый кусок текста.
func lit(text string) frag { return frag{text: text} }

// val создаёт размечаемое значение указанного типа.
func val(t pii.Type, text string) frag { return frag{text: text, typ: t} }

// frags собирает фрагменты в последовательность.
func frags(items ...frag) []frag { return items }

// concat склеивает несколько последовательностей фрагментов.
func concat(parts ...[]frag) []frag {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]frag, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// mapFrags применяет преобразование к тексту каждого фрагмента. Нужен для
// категорий с изменением регистра и с опечатками: разметка пересчитывается
// заново по преобразованным кускам, а не переносится со старыми смещениями.
func mapFrags(in []frag, f func(string) string) []frag {
	out := make([]frag, len(in))
	for i, fr := range in {
		out[i] = frag{text: f(fr.text), typ: fr.typ}
	}
	return out
}

// builder накапливает текст записи и разметку одновременно, поэтому смещения
// всегда согласованы с текстом.
type builder struct {
	sb     strings.Builder
	spans  []Span
	values []string
}

// add дописывает фрагмент и, если он размечаемый, запоминает его границы.
func (b *builder) add(fr frag) {
	if fr.text == "" {
		return
	}
	start := b.sb.Len()
	b.sb.WriteString(fr.text)
	if fr.typ == "" {
		return
	}
	b.spans = append(b.spans, Span{Start: start, End: b.sb.Len(), Type: string(fr.typ)})
	b.values = append(b.values, fr.text)
}

// write дописывает последовательность фрагментов.
func (b *builder) write(in []frag) {
	for _, fr := range in {
		b.add(fr)
	}
}

// record собирает готовую запись набора.
func (b *builder) record(id, category string) Record {
	spans := b.spans
	if spans == nil {
		spans = []Span{}
	}
	return Record{
		ID:       id,
		Category: category,
		Text:     b.sb.String(),
		Spans:    spans,
		Values:   b.values,
	}
}

// buildRecord собирает запись из фрагментов.
func buildRecord(id, category string, in []frag) Record {
	var b builder
	b.write(in)
	return b.record(id, category)
}

// pool — словарь, разделённый на основную и отложенную части. Отложенные
// значения не попадают в основной набор: только на них можно честно измерить
// обобщение детекторов, а не запоминание словаря.
type pool struct {
	main []string
	held []string
}

// Отложенная выборка — три значения из каждых двадцати, то есть пятнадцать
// процентов словаря.
const (
	holdoutBucket = 20
	holdoutTake   = 3
)

// isHeldOut решает по позиции в словаре, уходит ли значение в отложенную
// выборку. Правило не зависит от seed, поэтому отложенная часть одна и та же
// при любом запуске и результаты разных прогонов сравнимы между собой.
func isHeldOut(i int) bool { return (i*7+3)%holdoutBucket < holdoutTake }

// splitPool делит словарь на основную и отложенную части.
func splitPool(all []string) pool {
	var p pool
	for i, v := range all {
		if isHeldOut(i) {
			p.held = append(p.held, v)
		} else {
			p.main = append(p.main, v)
		}
	}
	return p
}

// Generator порождает записи набора. Вся случайность идёт из одного источника,
// заданного seed, поэтому два запуска с одним seed дают побайтово равные файлы.
type Generator struct {
	r         *rand.Rand
	holdout   bool
	surnames  pool
	maleNames pool
	femNames  pool
}

// NewGenerator создаёт генератор с источником случайности от seed.
// Применяется math/rand/v2 с явным источником: глобальный источник math/rand
// разделяется всей программой и воспроизводимости не даёт.
func NewGenerator(seed uint64, holdout bool) *Generator {
	src := rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)
	return &Generator{
		r:         rand.New(src),
		holdout:   holdout,
		surnames:  splitPool(surnamesAll),
		maleNames: splitPool(maleNamesAll),
		femNames:  splitPool(femaleNamesAll),
	}
}

// pick выбирает случайный элемент словаря.
func (g *Generator) pick(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[g.r.IntN(len(list))]
}

// fromPool выбирает значение из основной части словаря, а в режиме отложенной
// выборки — только из отложенной.
func (g *Generator) fromPool(p pool) string {
	if g.holdout && len(p.held) > 0 {
		return g.pick(p.held)
	}
	return g.pick(p.main)
}

// chance сообщает, наступило ли событие с вероятностью percent процентов.
func (g *Generator) chance(percent int) bool { return g.r.IntN(100) < percent }

// digits порождает строку из n случайных цифр.
func (g *Generator) digits(n int) string {
	var b strings.Builder
	b.Grow(n)
	for i := 0; i < n; i++ {
		b.WriteByte(byte('0' + g.r.IntN(10)))
	}
	return b.String()
}

// digitsNonZero порождает строку из n цифр, у которой первая цифра не ноль.
func (g *Generator) digitsNonZero(n int) string {
	if n <= 0 {
		return ""
	}
	return string(byte('1'+g.r.IntN(9))) + g.digits(n-1)
}
