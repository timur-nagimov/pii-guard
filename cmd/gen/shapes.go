package main

import (
	"strings"
	"unicode"
)

// Формы записи значения. Одно и то же значение в настоящих документах
// записывают по-разному: строкой таблицы, в кавычках, в скобках, через
// неразрывный пробел, с переносом на следующую строку, с латинскими буквами
// вместо похожих кириллических. Детектор обязан находить значение в любой из
// этих форм, поэтому каждая форма отдельно попадает в набор.

// Служебные знаки, которыми различаются формы записи.
const (
	// nbsp — неразрывный пробел: его вставляют текстовые редакторы.
	nbsp = " "
	// hyphenLong — длинное тире, hyphenNoBreak — неразрывный дефис.
	hyphenLong    = "—"
	hyphenNoBreak = "‑"
	colonSpace    = ": "
	nl            = "\n"
	space         = " "
	dashAround    = " — "
)

// hyphenKinds — виды дефиса: обычный, длинный и неразрывный. Все три
// встречаются в выгрузках из разных систем и выглядят почти одинаково.
var hyphenKinds = []string{"-", hyphenLong, hyphenNoBreak}

// homoglyphs — латинские буквы, неотличимые на вид от кириллических. Их
// подставляют и редакторы, и люди, копирующие текст из разных источников;
// значение при этом остаётся персональными данными.
var homoglyphs = map[rune]rune{
	'а': 'a', 'е': 'e', 'о': 'o', 'р': 'p', 'с': 'c', 'у': 'y', 'х': 'x',
	'А': 'A', 'Е': 'E', 'О': 'O', 'Р': 'P', 'С': 'C', 'У': 'Y', 'Х': 'X',
}

// leadIns — вступления для формы, где значение стоит в конце предложения.
var leadIns = []string{
	"В анкете указано", "По документам проходит", "Со слов клиента",
	"В заявке значится", "В карточке клиента записано", "Оператор внёс",
	"Из скана документа распознано", "В обращении приведено",
}

// lowerFirst переводит первую букву в нижний регистр: якорь из начала строки
// переносится в середину предложения.
func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// upperFirst переводит первую букву в верхний регистр: фраза из словаря
// становится началом предложения.
func upperFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// replaceHomoglyphs заменяет часть кириллических букв похожими латинскими.
// Заменяется не всё подряд: смешанная запись труднее для детектора, чем
// сплошная латиница.
func (g *Generator) replaceHomoglyphs(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if lat, ok := homoglyphs[r]; ok && g.chance(55) {
			b.WriteRune(lat)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// replaceHyphens меняет вид дефиса во всей записи разом: в одном документе
// дефис обычно один и тот же.
func replaceHyphens(s, kind string) string {
	return strings.ReplaceAll(s, "-", kind)
}

// withNBSP меняет пробелы на неразрывные.
func withNBSP(s string) string { return strings.ReplaceAll(s, space, nbsp) }

// withDoubleSpaces удваивает пробелы: так выглядит текст после неаккуратного
// выравнивания или после склейки колонок.
func withDoubleSpaces(s string) string { return strings.ReplaceAll(s, space, "  ") }

// splitText делит текст на две непустые части: по пробелу ближе к середине, а
// если пробела нет — по границе рун. Части не должны начинаться или
// заканчиваться пробелом, иначе разметка захватит пробел.
func splitText(s string) (string, string, bool) {
	r := []rune(s)
	if len(r) < 4 {
		return "", "", false
	}
	mid := len(r) / 2
	if i := nearestSpace(r, mid); i > 0 {
		if a, b, ok := trimmedPair(string(r[:i]), string(r[i+1:])); ok {
			return a, b, true
		}
	}
	return trimmedPair(string(r[:mid]), string(r[mid:]))
}

// nearestSpace ищет пробел, ближайший к середине слова.
func nearestSpace(r []rune, mid int) int {
	for d := 0; d < len(r); d++ {
		if i := mid - d; i > 0 && r[i] == ' ' {
			return i
		}
		if i := mid + d; i < len(r)-1 && r[i] == ' ' {
			return i
		}
	}
	return -1
}

// trimmedPair принимает пару частей, только если ни одна не обрамлена
// пробелами и ни одна не пуста.
func trimmedPair(a, b string) (string, string, bool) {
	if a == "" || b == "" || strings.TrimSpace(a) != a || strings.TrimSpace(b) != b {
		return "", "", false
	}
	return a, b, true
}

// splitLastValue разрывает последнее размечаемое значение переводом строки.
// Обе половины остаются размеченными своим типом: значение одно, просто
// напечатано в две строки, как в анкете с узкой колонкой.
func splitLastValue(in []frag) []frag {
	idx := -1
	for i, fr := range in {
		if fr.typ != "" {
			idx = i
		}
	}
	if idx < 0 {
		return in
	}
	left, right, ok := splitText(in[idx].text)
	if !ok {
		return in
	}
	out := make([]frag, 0, len(in)+2)
	out = append(out, in[:idx]...)
	out = append(out, frag{text: left, typ: in[idx].typ}, lit(nl), frag{text: right, typ: in[idx].typ})
	return append(out, in[idx+1:]...)
}

// writeShape — форма записи значения в тексте.
type writeShape struct {
	name   string
	render func(g *Generator, s valueSpec, p person) []frag
}

// wrap собирает «якорь, разделитель, значение, окончание».
func wrap(anchor, sep string, value []frag, tail frag) []frag {
	return concat(frags(lit(anchor+sep)), value, frags(tail))
}

// writeShapes перечисляет формы записи. Порядок фиксирован: от него зависит
// воспроизводимость выпуска по seed.
func writeShapes() []writeShape {
	out := []writeShape{
		{name: "phrase", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap(g.anchor(s), space, s.value(g, p), g.tail())
		}},
		{name: "colon", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap(g.anchor(s), colonSpace, s.value(g, p), g.tail())
		}},
		{name: "table_row", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap(s.label, colonSpace, s.value(g, p), lit(nl))
		}},
		{name: "quotes_ru", render: func(g *Generator, s valueSpec, p person) []frag {
			return concat(frags(lit(g.anchor(s)+": «")), s.value(g, p), frags(lit("»"), g.tail()))
		}},
		{name: "quotes_lat", render: func(g *Generator, s valueSpec, p person) []frag {
			return concat(frags(lit(g.anchor(s)+": \"")), s.value(g, p), frags(lit("\""), g.tail()))
		}},
		{name: "parens", render: func(g *Generator, s valueSpec, p person) []frag {
			return concat(frags(lit(g.anchor(s)+" (")), s.value(g, p), frags(lit(")"), g.tail()))
		}},
		{name: "brackets", render: func(g *Generator, s valueSpec, p person) []frag {
			return concat(frags(lit(g.anchor(s)+" [")), s.value(g, p), frags(lit("]"), g.tail()))
		}},
		{name: "sentence_end", render: func(g *Generator, s valueSpec, p person) []frag {
			head := g.pick(leadIns) + space + lowerFirst(g.anchor(s)) + dashAround
			return concat(frags(lit(head)), s.value(g, p), frags(lit(".")))
		}},
		{name: "semicolons", render: func(g *Generator, s valueSpec, p person) []frag {
			a := g.anchor(s)
			return concat(
				frags(lit(a+colonSpace)), s.value(g, p), frags(lit("; "+a+colonSpace)),
				s.value(g, p), frags(lit(";")),
			)
		}},
		{name: "nbsp", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap(g.anchor(s)+":", nbsp, mapFrags(s.value(g, p), withNBSP), g.tail())
		}},
		{name: "double_space", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap(g.anchor(s)+":", "  ", mapFrags(s.value(g, p), withDoubleSpaces), g.tail())
		}},
		{name: "line_break", render: func(g *Generator, s valueSpec, p person) []frag {
			return concat(frags(lit(g.anchor(s)+":"+nl)), splitLastValue(s.value(g, p)), frags(lit(nl)))
		}},
		{name: "homoglyph", render: func(g *Generator, s valueSpec, p person) []frag {
			body := wrap(g.anchor(s), colonSpace, s.value(g, p), g.tail())
			return mapFrags(body, g.replaceHomoglyphs)
		}},
		{name: "hyphen", render: func(g *Generator, s valueSpec, p person) []frag {
			kind := g.pick(hyphenKinds)
			body := wrap(g.anchor(s), space+kind+space, s.value(g, p), g.tail())
			return mapFrags(body, func(x string) string { return replaceHyphens(x, kind) })
		}},
		{name: "upper_anchor", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap(strings.ToUpper(g.anchor(s)), colonSpace, s.value(g, p), g.tail())
		}},
		{name: "bullet", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap(hyphenLong+space+s.label, colonSpace, s.value(g, p), lit(nl))
		}},
		{name: "equals", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap(s.label, "=", s.value(g, p), lit(";"))
		}},
		{name: "pipe", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap("| "+s.label+" | ", "", s.value(g, p), lit(" |"+nl))
		}},
		{name: "reversed", render: func(g *Generator, s valueSpec, p person) []frag {
			return concat(s.value(g, p), frags(lit(dashAround+lowerFirst(g.anchor(s))), g.tail()))
		}},
		{name: "label_parens", render: func(g *Generator, s valueSpec, p person) []frag {
			return wrap("("+s.label+")", space, s.value(g, p), g.tail())
		}},
	}
	return out
}

// shapeCount — число форм записи. Вынесено, чтобы не пересобирать список ради
// одного числа.
func shapeCount() int { return len(writeShapes()) }

// render записывает значение в форме с указанным номером.
func (g *Generator) render(s valueSpec, p person, shape int) []frag {
	shapes := writeShapes()
	return shapes[shape%len(shapes)].render(g, s, p)
}

// renderAny записывает значение в случайной форме.
func (g *Generator) renderAny(s valueSpec, p person) []frag {
	return g.render(s, p, g.r.IntN(shapeCount()))
}
