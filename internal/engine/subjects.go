package engine

import (
	"sort"
	"strings"

	"pii-guard/internal/pii"
)

// Basis — основание считать два фрагмента относящимися к одному человеку.
// Каждое основание даёт свою степень уверенности в связи.
type Basis string

// Основания связи фрагментов одного субъекта.
const (
	// BasisSentence — соседство в одном предложении. Надёжность средняя:
	// «Иванов И.И., тел +7...» почти наверняка один человек, но предложение
	// может перечислять и разных людей.
	BasisSentence Basis = "sentence"
	// BasisAnchor — общий якорь анкеты: «ФИО:», «Телефон:» в одной анкете.
	// Надёжность высокая: анкета описывает одного человека.
	BasisAnchor Basis = "anchor"
	// BasisRepeat — повтор значения: одно и то же ФИО дважды в тексте.
	// Надёжность высокая: одинаковое значение почти всегда один человек.
	BasisRepeat Basis = "repeat"
)

// Уверенность связи по основанию. Согласована с уровнями pii.Conf*.
const (
	confSentence = 0.6
	confAnchor   = 0.9
	confRepeat   = 0.9
)

// Edge — основание считать два фрагмента (по индексам в исходном срезе)
// относящимися к одному человеку.
type Edge struct {
	// A и B — индексы фрагментов в срезе, из которого строились связи.
	A, B int
	// Basis — основание связи.
	Basis Basis
	// Conf — уверенность связи от нуля до единицы.
	Conf float64
}

// Subject — один человек: набор фрагментов, связанных основаниями.
// Фрагменты перечислены индексами в исходном срезе.
type Subject struct {
	// Fragments — индексы фрагментов, отнесённых к этому субъекту.
	Fragments []int
	// Types — типы персональных данных, найденные у субъекта.
	Types []pii.Type
}

// HasType сообщает, есть ли у субъекта фрагмент указанного типа.
func (s Subject) HasType(t pii.Type) bool {
	for _, tp := range s.Types {
		if tp == t {
			return true
		}
	}
	return false
}

// linkResult — результат построения связей: рёбра и разбиение на субъектов.
type linkResult struct {
	Edges    []Edge
	Subjects []Subject
}

// linkSubjects строит связи между фрагментами и разбивает их на субъектов.
// Детекторы не меняются: связи строятся поверх уже найденных фрагментов.
//
// Оснований три: соседство в предложении, общий якорь анкеты и повтор
// значения. Каждое даёт свою степень уверенности. Рёбра — это основание для
// правила сочетаний, а не само правило: сами по себе они ничего не маскируют.
func linkSubjects(doc *pii.Doc, spans []pii.Span) linkResult {
	if len(spans) < 2 {
		return linkResult{}
	}

	// Связи по соседству в предложении и по общему якорю анкеты.
	sentenceEdges := linkBySentence(doc, spans)
	anchorEdges := linkByAnchor(doc, spans)

	// Связи по повтору значения.
	repeatEdges := linkByRepeat(doc.Text, spans)

	edges := make([]Edge, 0, len(sentenceEdges)+len(anchorEdges)+len(repeatEdges))
	edges = append(edges, sentenceEdges...)
	edges = append(edges, anchorEdges...)
	edges = append(edges, repeatEdges...)

	return linkResult{
		Edges:    edges,
		Subjects: components(len(spans), spans, edges),
	}
}

// linkBySentence связывает фрагменты, лежащие в одном предложении.
func linkBySentence(doc *pii.Doc, spans []pii.Span) []Edge {
	bounds := sentenceBounds(doc.Text)
	// Группируем фрагменты по предложению, чтобы не перебирать все пары.
	bySentence := make(map[int][]int)
	for i, s := range spans {
		if si := sentenceIndex(s.Start, bounds); si >= 0 {
			bySentence[si] = append(bySentence[si], i)
		}
	}
	return edgesWithinGroups(bySentence, BasisSentence, confSentence)
}

// linkByAnchor связывает фрагменты, лежащие в одной анкете: блоке строк,
// где хотя бы одна строка несёт якорь вида «ФИО:», «Телефон:».
func linkByAnchor(doc *pii.Doc, spans []pii.Span) []Edge {
	forms := formBounds(doc)
	byForm := make(map[int][]int)
	for i, s := range spans {
		if fi := formIndex(s.Start, forms); fi >= 0 {
			byForm[fi] = append(byForm[fi], i)
		}
	}
	return edgesWithinGroups(byForm, BasisAnchor, confAnchor)
}

// edgesWithinGroups строит рёбра между всеми парами фрагментов внутри каждой
// группы. Группы малы (предложение или анкета), поэтому квадратичный перебор
// внутри группы дёшев.
func edgesWithinGroups(groups map[int][]int, basis Basis, conf float64) []Edge {
	var edges []Edge
	for _, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		for a := 0; a < len(idx); a++ {
			for b := a + 1; b < len(idx); b++ {
				edges = append(edges, Edge{A: idx[a], B: idx[b], Basis: basis, Conf: conf})
			}
		}
	}
	return edges
}

// linkByRepeat связывает фрагменты с одинаковым значением. Повтор значения —
// сильное основание: одно и то же ФИО дважды в тексте почти всегда один человек.
func linkByRepeat(text string, spans []pii.Span) []Edge {
	// Группируем фрагменты по нормализованному значению. Нормализация
	// снимает регистр и схлопывает пробелы, чтобы «Иванов Иван» и «иванов
	// иван» считались одним значением.
	byValue := make(map[string][]int)
	for i, s := range spans {
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			continue
		}
		v := normalizeValue(text[s.Start:s.End])
		if v == "" {
			continue
		}
		byValue[v] = append(byValue[v], i)
	}
	var edges []Edge
	for _, idx := range byValue {
		if len(idx) < 2 {
			continue
		}
		for a := 0; a < len(idx); a++ {
			for b := a + 1; b < len(idx); b++ {
				edges = append(edges, Edge{A: idx[a], B: idx[b], Basis: BasisRepeat, Conf: confRepeat})
			}
		}
	}
	return edges
}

// components разбивает фрагменты на связные компоненты по рёбрам. Фрагмент без
// рёбер образует собственный субъект из одного элемента.
func components(n int, spans []pii.Span, edges []Edge) []Subject {
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	for _, e := range edges {
		union(e.A, e.B)
	}

	groups := make(map[int][]int)
	for i := 0; i < n; i++ {
		r := find(i)
		groups[r] = append(groups[r], i)
	}
	subjects := make([]Subject, 0, len(groups))
	for _, frags := range groups {
		sort.Ints(frags)
		sub := Subject{Fragments: frags}
		seen := make(map[pii.Type]bool)
		for _, fi := range frags {
			if !seen[spans[fi].Type] {
				seen[spans[fi].Type] = true
				sub.Types = append(sub.Types, spans[fi].Type)
			}
		}
		subjects = append(subjects, sub)
	}
	sort.Slice(subjects, func(i, j int) bool {
		return subjects[i].Fragments[0] < subjects[j].Fragments[0]
	})
	return subjects
}

// sentenceBounds возвращает байтовые границы предложений. Предложение
// заканчивается переводом строки или знаком конца предложения, за которым
// следует пробел или конец текста. Точки в инициалах «И.И.» границей не
// считаются: за ними нет пробела.
func sentenceBounds(text string) [][2]int {
	var bounds [][2]int
	start := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == '\n' {
			bounds = append(bounds, [2]int{start, i})
			start = i + 1
			continue
		}
		if sentenceStop(c) && stopEndsSentence(text, i) {
			bounds = append(bounds, [2]int{start, i + 1})
			start = i + 1
		}
	}
	if start < len(text) {
		bounds = append(bounds, [2]int{start, len(text)})
	}
	return bounds
}

// sentenceStop сообщает, что знак способен закончить предложение.
func sentenceStop(b byte) bool {
	switch b {
	case '.', '!', '?', ';':
		return true
	default:
		return false
	}
}

// stopEndsSentence сообщает, что знак в позиции i действительно закрыл
// предложение: за ним идёт пробел, перевод строки или конец текста. Проверка
// длины оставлена здесь же, рядом с обращением по i+1: порознь они дали бы
// выход за границу строки на последнем байте текста.
func stopEndsSentence(text string, i int) bool {
	return i+1 >= len(text) || text[i+1] == ' ' || text[i+1] == '\n'
}

// sentenceIndex возвращает индекс предложения, в которое попадает смещение.
func sentenceIndex(off int, bounds [][2]int) int {
	for i, b := range bounds {
		if off >= b[0] && off < b[1] {
			return i
		}
	}
	return -1
}

// formAnchors — якоря, по которым строка опознаётся как строка анкеты.
// Слова в нижнем регистре; совпадение ищется по началу строки до двоеточия.
var formAnchors = []string{
	"фио", "фамилия", "имя", "отчество",
	"телефон", "тел", "мобильный", "контактный",
	"паспорт", "серия", "номер паспорта", "кем выдан", "дата выдачи",
	"инн", "снилс", "адрес", "индекс", "место рождения", "гражданство",
	"карта", "номер карты", "пин", "пин-код", "cvv",
	"почта", "email", "e-mail", "водительское",
}

// formBounds возвращает байтовые границы анкет: блоков строк, где хотя бы одна
// строка несёт якорь вида «ФИО:». Строки анкеты идут подряд, поэтому блок
// заканчивается на пустой строке или на строке без якоря.
func formBounds(doc *pii.Doc) [][2]int {
	var bounds [][2]int
	text := doc.Text
	lines := lineBounds(text)
	start := -1
	for _, ln := range lines {
		line := strings.ToLower(text[ln[0]:ln[1]])
		anchored := hasFormAnchor(line)
		if anchored && start < 0 {
			start = ln[0]
		}
		if !anchored && start >= 0 {
			bounds = append(bounds, [2]int{start, ln[0]})
			start = -1
		}
	}
	if start >= 0 {
		bounds = append(bounds, [2]int{start, len(text)})
	}
	return bounds
}

// hasFormAnchor сообщает, начинается ли строка с якоря анкеты, за которым
// следует двоеточие: «ФИО:», «Телефон:». Двоеточие обязательно, иначе строка
// «Пин 1234. Карта 4111...» была бы принята за анкету целиком и связала бы
// фрагменты, которые к одному человеку не относятся.
func hasFormAnchor(line string) bool {
	for _, a := range formAnchors {
		if strings.HasPrefix(line, a) {
			rest := line[len(a):]
			if strings.HasPrefix(rest, ":") {
				return true
			}
		}
	}
	return false
}

// formIndex возвращает индекс анкеты, в которую попадает смещение.
func formIndex(off int, forms [][2]int) int {
	for i, f := range forms {
		if off >= f[0] && off < f[1] {
			return i
		}
	}
	return -1
}

// lineBounds возвращает байтовые границы строк текста.
func lineBounds(text string) [][2]int {
	var bounds [][2]int
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			bounds = append(bounds, [2]int{start, i})
			start = i + 1
		}
	}
	if start < len(text) {
		bounds = append(bounds, [2]int{start, len(text)})
	}
	return bounds
}

// normalizeValue приводит значение фрагмента к каноническому виду для
// сравнения повторов: нижний регистр и схлопнутые пробелы.
func normalizeValue(v string) string {
	return strings.Join(strings.Fields(strings.ToLower(v)), " ")
}
