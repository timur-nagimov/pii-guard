package main

import (
	"strings"

	"pii-guard/internal/pii"
)

// anySimple порождает предложение случайного однотипного шаблона.
func (g *Generator) anySimple() []frag {
	specs := valueSpecs()
	return g.renderAny(specs[g.r.IntN(len(specs))], g.person())
}

// attributeFor записывает признак человека так, как его пишут в середине
// предложения: якорь строчными буквами, затем значение.
func (g *Generator) attributeFor(s valueSpec, p person) []frag {
	sep := space
	if g.chance(40) {
		sep = colonSpace
	}
	return concat(frags(lit(lowerFirst(g.anchor(s))+sep)), s.value(g, p))
}

// attributesFor выбирает несколько разных признаков одного человека и
// соединяет их запятыми.
func (g *Generator) attributesFor(p person, count int) []frag {
	specs := valueSpecs()
	order := g.r.Perm(len(specs))
	if count > len(order) {
		count = len(order)
	}
	out := make([]frag, 0, count*4)
	for i := 0; i < count; i++ {
		if i > 0 {
			out = append(out, lit(", "))
		}
		out = append(out, g.attributeFor(specs[order[i]], p)...)
	}
	return out
}

// genMixed порождает сложное предложение, где встречаются несколько типов
// данных и два разных человека. Это главный случай, на котором ломается
// наивное разрешение пересечений.
func genMixed(g *Generator) []frag {
	first, second := g.person(), g.person()
	openings := []string{
		"Клиент ", "Заявитель ", "Оформляем перевод: ", "В анкете указан ",
		"Обратился ", "Из карточки клиента: ", "Данные по заявке: ",
	}
	links := []string{
		". Доверенное лицо: ", ". Созаёмщик ", ". Второй участник сделки — ",
		". Получатель перевода ", ". Контактное лицо: ", ". Поручитель ",
		". Законный представитель: ",
	}
	return concat(
		frags(lit(g.pick(openings))),
		frags(val(pii.TypeFIO, g.fioVariant(first, caseNom)), lit(", ")),
		g.attributesFor(first, 2+g.r.IntN(3)),
		frags(lit(g.pick(links))),
		frags(val(pii.TypeFIO, g.fioVariant(second, caseNom)), lit(", ")),
		g.attributesFor(second, 2+g.r.IntN(2)),
		frags(lit(".")),
	)
}

// mixCase случайно меняет регистр каждой буквы: так пишут в мессенджерах, и
// детектор обязан работать по тексту в нижнем регистре, а не по исходному.
func (g *Generator) mixCase(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if g.chance(50) {
			b.WriteString(strings.ToUpper(string(r)))
			continue
		}
		b.WriteString(strings.ToLower(string(r)))
	}
	return b.String()
}

// genCaseVariants повторяет один и тот же текст в нижнем, верхнем и смешанном
// регистре. Разметка пересчитывается для каждой копии отдельно, потому что
// смена регистра может изменить длину строки в байтах.
func genCaseVariants(g *Generator) []frag {
	base := g.anySimple()
	sep := frags(lit(nl))
	return concat(
		mapFrags(base, strings.ToLower), sep,
		mapFrags(base, strings.ToUpper), sep,
		mapFrags(base, g.mixCase),
	)
}

// genLatin порождает текст латиницей и транслитерацией: анкеты для переводов
// за рубеж и данные, скопированные из международных форм.
func genLatin(g *Generator) []frag {
	p := g.person()
	name := titleLatin(p.name) + space + titleLatin(p.surname)
	card := g.cardNumber(true)
	switch g.r.IntN(4) {
	case 0:
		return frags(
			lit("Client "), val(pii.TypeFIO, name), lit(", passport "),
			val(pii.TypePassport, g.digitsNonZero(4)+space+g.digits(6)),
			lit(", email "), val(pii.TypeEmail, g.email(p)), lit("."),
		)
	case 1:
		return frags(
			lit("Cardholder "), val(pii.TypeCardHolder, strings.ToUpper(name)),
			lit(", card number "), val(pii.TypeCard, card),
			lit(", phone "), val(pii.TypePhone, g.phone()), lit("."),
		)
	case 2:
		return frags(
			lit("Address: "), val(pii.TypeAddress, g.addressLatin()),
			lit(", zip "), val(pii.TypePostcode, g.postcode()),
			lit(", e-mail "), val(pii.TypeEmail, g.email(p)), lit("."),
		)
	default:
		return frags(
			lit("FIO: "), val(pii.TypeFIO, strings.ToUpper(name)),
			lit(", INN "), val(pii.TypeINN, g.inn(true, true)),
			lit(", tel "), val(pii.TypePhone, g.phone()), lit("."),
		)
	}
}

// genLatinMixed порождает смешанные тексты, где русский якорь стоит рядом с
// латинским значением и наоборот, а телефон записан в международной форме.
// Такие записи встречаются в анкетах для переводов и в переписке с
// зарубежными отделениями.
func genLatinMixed(g *Generator) []frag {
	p := g.person()
	latin := titleLatin(p.name) + space + titleLatin(p.surname)
	switch g.r.IntN(5) {
	case 0:
		// Русский якорь, латинское значение.
		return concat(
			frags(lit("ФИО ")), frags(val(pii.TypeFIO, latin)),
			frags(lit(", телефон ")), frags(val(pii.TypePhone, g.phoneIntl())),
			frags(lit(", паспорт ")), g.passportFrags(), frags(lit(".")),
		)
	case 1:
		// Английский якорь, русское значение.
		return concat(
			frags(lit("Name: ")), frags(val(pii.TypeFIO, p.full(caseNom))),
			frags(lit(", phone ")), frags(val(pii.TypePhone, g.phoneIntl())),
			frags(lit(", email ")), frags(val(pii.TypeEmail, g.email(p))), frags(lit(".")),
		)
	case 2:
		// Имя латиницей, отчество кириллицей.
		return concat(
			frags(lit("Клиент ")), frags(val(pii.TypeFIO, latin)),
			frags(lit(" ")), frags(val(pii.TypeFIO, p.patronymicForm(caseNom))),
			frags(lit(", ИНН ")), frags(val(pii.TypeINN, g.inn(true, true))), frags(lit(".")),
		)
	case 3:
		// Международный телефон рядом с русским якорем.
		return concat(
			frags(lit("Контактный телефон ")), frags(val(pii.TypePhone, g.phoneIntl())),
			frags(lit(", e-mail ")), frags(val(pii.TypeEmail, g.email(p))), frags(lit(".")),
		)
	default:
		// Полностью латинская анкета с международным телефоном.
		return concat(
			frags(lit("Full name: ")), frags(val(pii.TypeFIO, latin)),
			frags(lit(", date of birth ")), g.dateFrags(pii.TypeDOB, g.randomDate(1950, 2004)),
			frags(lit(", phone ")), frags(val(pii.TypePhone, g.phoneIntl())), frags(lit(".")),
		)
	}
}

// phoneIntl возвращает телефон в международной записи: с кодом страны и без
// скобок, как его пишут в международных формах.
func (g *Generator) phoneIntl() string {
	code := g.pick(phoneCodes)
	a, b, c := g.digits(3), g.digits(2), g.digits(2)
	switch g.r.IntN(4) {
	case 0:
		return "+7" + code + a + b + c
	case 1:
		return "+7 " + code + " " + a + " " + b + " " + c
	case 2:
		return "+7-" + code + "-" + a + "-" + b + "-" + c
	default:
		return "+7 (" + code + ") " + a + "-" + b + "-" + c
	}
}

// genInvalidChecksum порождает значения с якорем, но с заведомо неверной
// контрольной суммой. Такие фрагменты обязаны маскироваться: жюри печатает
// выдуманные номера, и отказ по контрольной сумме означал бы пропуск.
func genInvalidChecksum(g *Generator) []frag {
	card := g.cardNumber(false)
	variants := [][]frag{
		{lit("ИНН "), val(pii.TypeINN, g.inn(true, false))},
		{lit("ИНН организации "), val(pii.TypeINN, g.inn(false, false))},
		{lit("номер карты "), val(pii.TypeCard, card)},
		{lit("СНИЛС "), val(pii.TypeSNILS, g.snils(false))},
	}
	order := g.r.Perm(len(variants))
	count := 1 + g.r.IntN(2)
	out := frags(lit(g.pick([]string{"Проверьте реквизиты: ", "В заявке указаны ", "Из анкеты: "})))
	for i := 0; i < count; i++ {
		if i > 0 {
			out = append(out, lit(", "))
		}
		out = append(out, variants[order[i]]...)
	}
	return append(out, g.tail())
}

// genDateNoAnchor ставит дату рядом с именем и паспортом без слов о рождении.
// Детектор обязан понять тип по соседству, а не по якорю.
func genDateNoAnchor(g *Generator) []frag {
	p := g.person()
	date := g.randomDate(1950, 2004)
	sep := g.pick([]string{", ", space, ",  ", " | ", "; ", " / "})
	return concat(
		frags(val(pii.TypeFIO, g.fioVariant(p, caseNom)), lit(sep)),
		frags(val(pii.TypeDOB, date.format(g.r.IntN(formatCount))), lit(sep)),
		g.passportFrags(),
		frags(lit(g.pick([]string{"", ".", ";"}))),
	)
}

// genTable порождает анкету строками «Поле: значение». Значение не должно
// пересекать перевод строки, поэтому каждая строка закрывается переводом.
func genTable(g *Generator) []frag {
	specs := valueSpecs()
	order := g.r.Perm(len(specs))
	count := 4 + g.r.IntN(7)
	p := g.person()
	out := frags(lit("Анкета клиента" + nl))
	for i := 0; i < count; i++ {
		s := specs[order[i]]
		out = append(out, fieldRow(s.label, s.value(g, p))...)
	}
	return out
}

// typo искажает слово: перестановка соседних букв, удвоение, пропуск, замена
// буквы «ё» и лишний пробел.
func (g *Generator) typo(s string) string {
	r := []rune(s)
	if len(r) < 4 {
		return s
	}
	i := 1 + g.r.IntN(len(r)-2)
	switch g.r.IntN(5) {
	case 0:
		r[i], r[i-1] = r[i-1], r[i]
	case 1:
		return string(r[:i]) + string(r[i]) + string(r[i:])
	case 2:
		return string(r[:i]) + string(r[i+1:])
	case 3:
		return strings.NewReplacer("ё", "е", "Ё", "Е", "й", "и").Replace(s)
	default:
		return string(r[:i]) + space + string(r[i:])
	}
	return string(r)
}

// genTypos порождает текст с опечатками. Опечатки вносятся и в обрамляющие
// слова, и в сами значения: клиент пишет с ошибками, а данные всё равно
// остаются персональными.
func genTypos(g *Generator) []frag {
	base := g.anySimple()
	return mapFrags(base, func(s string) string {
		if !g.chance(45) {
			return s
		}
		return g.typo(s)
	})
}

// noiseKinds — виды шума, которые вносятся в значение. Каждый вид отдельно
// проверяет устойчивость детектора: значение остаётся персональными данными,
// но записано с ошибкой, как в живом тексте.
func (g *Generator) noiseValue(s string) string {
	switch g.r.IntN(6) {
	case 0:
		// ё вместо е: «Сергей» → «Сергей» с ё на месте е.
		return strings.NewReplacer("е", "ё", "Е", "Ё").Replace(s)
	case 1:
		// ноль вместо буквы о: «Иванов» → «Иван0в».
		return strings.NewReplacer("о", "0", "О", "0").Replace(s)
	case 2:
		// лишний пробел внутри номера: «123456» → «123 456».
		return g.insertSpace(s)
	case 3:
		// латинские буквы того же начертания вместо русских.
		return g.replaceHomoglyphs(s)
	case 4:
		// перестановка соседних букв.
		return g.typo(s)
	default:
		// замена буквы на похожую по клавиатуре.
		return g.keyboardNeighbor(s)
	}
}

// insertSpace вставляет пробел в середину значения, если в нём есть цифры.
func (g *Generator) insertSpace(s string) string {
	idx := -1
	for i, r := range s {
		if r >= '0' && r <= '9' && i > 0 && i < len(s)-1 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return s
	}
	return s[:idx] + space + s[idx:]
}

// keyboardNeighbor заменяет одну букву на соседнюю по раскладке: так получаются
// опечатки «иваноа» вместо «иванов».
func (g *Generator) keyboardNeighbor(s string) string {
	r := []rune(s)
	if len(r) < 3 {
		return s
	}
	i := 1 + g.r.IntN(len(r)-2)
	neighbors := map[rune][]rune{
		'а': {'о', 'с'}, 'о': {'а', 'п'}, 'е': {'р', 'у'}, 'н': {'г', 'т'},
		'и': {'ш', 'м'}, 'в': {'с', 'а'}, 'р': {'е', 'к'}, 'т': {'н', 'ь'},
	}
	if n, ok := neighbors[r[i]]; ok {
		r[i] = n[g.r.IntN(len(n))]
	}
	return string(r)
}

// genNoise порождает текст, где значение записано с шумом: ё вместо е, ноль
// вместо о, лишние пробелы в номерах, латинские омоглифы. Обрамляющий текст
// остаётся чистым, шум вносится только в само значение.
func genNoise(g *Generator) []frag {
	spec := valueSpecs()[g.r.IntN(len(valueSpecs()))]
	p := g.person()
	base := g.renderAny(spec, p)
	return mapFrags(base, func(s string) string {
		if !g.chance(60) {
			return s
		}
		return g.noiseValue(s)
	})
}

// longTargetBytes — целевой размер длинного текста, четыреста килобайт.
// Такой объём проверяет, что движок не деградирует на больших документах.
const longTargetBytes = 400 * 1024

// genLong склеивает предложения, пока текст не достигнет целевого размера.
func genLong(g *Generator) []frag {
	out := make([]frag, 0, 4096)
	size := 0
	for size < longTargetBytes {
		part := g.anySimple()
		if g.chance(25) {
			part = genMixed(g)
		}
		for _, fr := range part {
			size += len(fr.text)
		}
		out = append(out, part...)
		sep := lit(space)
		if g.chance(30) {
			sep = lit(nl)
		}
		out = append(out, sep)
		size++
	}
	return out
}

// long64kTargetBytes — целевой размер текста, чуть больше порога разбиения
// движка в шестьдесят четыре килобайта. Именно на таких текстах живут ошибки
// со смещениями: фрагмент попадает на границу куска, и его надо найти целиком
// хотя бы в одном из них.
const long64kTargetBytes = 70 * 1024

// genLong64k склеивает предложения до размера чуть больше порога разбиения.
// В отличие от genLong, текст не раздувается до четырёхсот килобайт: здесь
// важна именно близость к границе куска, где перекрытие соседних кусков
// решает, найдётся ли фрагмент целиком.
func genLong64k(g *Generator) []frag {
	out := make([]frag, 0, 1024)
	size := 0
	for size < long64kTargetBytes {
		part := g.anySimple()
		if g.chance(25) {
			part = genMixed(g)
		}
		for _, fr := range part {
			size += len(fr.text)
		}
		out = append(out, part...)
		sep := lit(space)
		if g.chance(30) {
			sep = lit(nl)
		}
		out = append(out, sep)
		size++
	}
	return out
}
