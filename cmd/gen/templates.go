package main

// Обрамляющий текст для шаблонов. В литералах не должно быть персональных
// данных: всё, что похоже на них, обязано идти через размечаемое значение,
// иначе набор будет наказывать детектор за верное срабатывание.
var neutralTails = []string{
	".", ", проверьте данные.", " — данные взяты из анкеты.", ";",
	". Обращение принято в работу.", " (заявка принята).",
	". Ответ направим в течение трёх рабочих дней.",
	". Сведения внесены в карточку клиента.", "!", " —",
	". Уточните, если что-то указано неверно.", ".",
}

// tail возвращает нейтральное окончание предложения.
func (g *Generator) tail() frag { return lit(g.pick(neutralTails)) }

// simpleCategoryGen возвращает порождающую функцию простой категории: одно
// значение одного типа, записанное в случайной форме.
func simpleCategoryGen(s valueSpec) func(*Generator) []frag {
	return func(g *Generator) []frag { return g.renderAny(s, g.person()) }
}

// fieldRow возвращает строку таблицы вида «Поле: значение».
func fieldRow(label string, value []frag) []frag {
	return concat(frags(lit(label+colonSpace)), value, frags(lit(nl)))
}
