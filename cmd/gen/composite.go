package main

import (
	"strings"

	"pii-guard/internal/pii"
)

// simpleGenerators перечисляет шаблоны с одним типом данных. Составные
// категории собираются из них, поэтому список возвращается функцией, а не
// живёт в изменяемой переменной пакета.
func simpleGenerators() []func(*Generator) []frag {
	return []func(*Generator) []frag{
		genFIO, genDOB, genBirthPlace, genPassport, genCitizenship, genIssuer,
		genDeptCode, genIssueDate, genDriverLicense, genAddress, genPostcode,
		genEmail, genPhone, genINN, genCard, genCVV, genPIN, genCardHolder,
		genSNILS, genForeignPassport, genResidencePermit, genBirthCert,
		genMilitaryID,
	}
}

// anySimple порождает предложение случайного однотипного шаблона.
func (g *Generator) anySimple() []frag {
	list := simpleGenerators()
	return list[g.r.IntN(len(list))](g)
}

// attribute — признак человека с якорем перед значением.
type attribute func(g *Generator, p person) []frag

// personAttributes перечисляет признаки, которые встречаются в анкете рядом с
// именем. Из них собираются сложные предложения.
func personAttributes() []attribute {
	return []attribute{
		func(g *Generator, p person) []frag {
			return concat(frags(lit("паспорт ")), g.passportFrags())
		},
		func(g *Generator, p person) []frag {
			return frags(lit("тел. "), val(pii.TypePhone, g.phone()))
		},
		func(g *Generator, p person) []frag {
			return frags(lit("ИНН "), val(pii.TypeINN, g.inn(true, true)))
		},
		func(g *Generator, p person) []frag {
			return frags(lit("почта "), val(pii.TypeEmail, g.email(p)))
		},
		func(g *Generator, p person) []frag {
			return concat(frags(lit("адрес ")), g.addressFrags())
		},
		func(g *Generator, p person) []frag {
			return concat(frags(lit("дата рождения ")), g.dateFrags(pii.TypeDOB, g.randomDate(1950, 2005)))
		},
		func(g *Generator, p person) []frag {
			return frags(lit("СНИЛС "), val(pii.TypeSNILS, g.snils(true)))
		},
		func(g *Generator, p person) []frag {
			_, number := g.cardNumber(true)
			return frags(lit("карта "), val(pii.TypeCard, number))
		},
		func(g *Generator, p person) []frag {
			return frags(lit("место рождения "), val(pii.TypeBirthPlace, g.birthPlace()))
		},
		func(g *Generator, p person) []frag {
			return frags(lit("водительское удостоверение "), val(pii.TypeDriverLicense, g.driverLicense()))
		},
	}
}

// attributesFor выбирает несколько разных признаков одного человека и
// соединяет их запятыми.
func (g *Generator) attributesFor(p person, count int) []frag {
	list := personAttributes()
	order := g.r.Perm(len(list))
	if count > len(order) {
		count = len(order)
	}
	out := make([]frag, 0, count*3)
	for i := 0; i < count; i++ {
		if i > 0 {
			out = append(out, lit(", "))
		}
		out = append(out, list[order[i]](g, p)...)
	}
	return out
}

// genMixed порождает сложное предложение, где встречаются несколько типов
// данных и два разных человека. Это главный случай, на котором ломается
// наивное разрешение пересечений.
func genMixed(g *Generator) []frag {
	first, second := g.person(), g.person()
	openings := []string{"Клиент ", "Заявитель ", "Оформляем перевод: ", "В анкете указан ", "Обратился "}
	links := []string{
		". Доверенное лицо: ", ". Созаёмщик ", ". Второй участник сделки — ",
		". Получатель перевода ", ". Контактное лицо: ",
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
	nl := frags(lit("\n"))
	return concat(
		mapFrags(base, strings.ToLower), nl,
		mapFrags(base, strings.ToUpper), nl,
		mapFrags(base, g.mixCase),
	)
}

// genLatin порождает текст латиницей и транслитерацией: анкеты для переводов
// за рубеж и данные, скопированные из международных форм.
func genLatin(g *Generator) []frag {
	p := g.person()
	name := titleLatin(p.name) + " " + titleLatin(p.surname)
	_, card := g.cardNumber(true)
	switch g.r.IntN(4) {
	case 0:
		return frags(
			lit("Client "), val(pii.TypeFIO, name), lit(", passport "),
			val(pii.TypePassport, g.digitsNonZero(4)+" "+g.digits(6)),
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

// genInvalidChecksum порождает значения с якорем, но с заведомо неверной
// контрольной суммой. Такие фрагменты обязаны маскироваться: жюри печатает
// выдуманные номера, и отказ по контрольной сумме означал бы пропуск.
func genInvalidChecksum(g *Generator) []frag {
	_, card := g.cardNumber(false)
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
	sep := g.pick([]string{", ", " ", ",  ", " | "})
	return concat(
		frags(val(pii.TypeFIO, g.fioVariant(p, caseNom)), lit(sep)),
		frags(val(pii.TypeDOB, date.format(g.r.IntN(formatCount))), lit(sep)),
		g.passportFrags(),
		frags(lit(g.pick([]string{"", ".", ";"}))),
	)
}

// tableRows перечисляет строки анкеты в виде «Поле: значение».
func tableRows() []func(g *Generator, p person) []frag {
	return []func(g *Generator, p person) []frag{
		func(g *Generator, p person) []frag {
			return fieldRow("ФИО", frags(val(pii.TypeFIO, p.full(caseNom))))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Дата рождения", g.dateFrags(pii.TypeDOB, g.randomDate(1950, 2005)))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Место рождения", frags(val(pii.TypeBirthPlace, g.birthPlace())))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Паспорт", g.passportFrags())
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Кем выдан", frags(val(pii.TypeIssuer, g.issuer())))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Код подразделения", frags(val(pii.TypeDeptCode, g.deptCode())))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Дата выдачи", g.dateFrags(pii.TypeIssueDate, g.randomDate(2002, 2024)))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Адрес регистрации", g.addressFrags())
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Телефон", frags(val(pii.TypePhone, g.phone())))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("E-mail", frags(val(pii.TypeEmail, g.email(p))))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("ИНН", frags(val(pii.TypeINN, g.inn(true, true))))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("СНИЛС", frags(val(pii.TypeSNILS, g.snils(true))))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Гражданство", frags(val(pii.TypeCitizenship, g.citizenship())))
		},
		func(g *Generator, p person) []frag {
			_, card := g.cardNumber(true)
			return fieldRow("Номер карты", frags(val(pii.TypeCard, card)))
		},
		func(g *Generator, p person) []frag {
			return fieldRow("Водительское удостоверение", frags(val(pii.TypeDriverLicense, g.driverLicense())))
		},
	}
}

// genTable порождает анкету строками «Поле: значение». Значение не должно
// пересекать перевод строки, поэтому каждая строка закрывается переводом.
func genTable(g *Generator) []frag {
	rows := tableRows()
	order := g.r.Perm(len(rows))
	count := 4 + g.r.IntN(5)
	p := g.person()
	out := frags(lit("Анкета клиента\n"))
	for i := 0; i < count; i++ {
		out = append(out, rows[order[i]](g, p)...)
	}
	return out
}

// typoVariants перечисляет искажения слова: перестановка соседних букв,
// удвоение, пропуск, замена буквы «ё» и лишний пробел.
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
		return string(r[:i]) + " " + string(r[i:])
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
		sep := lit(" ")
		if g.chance(30) {
			sep = lit("\n")
		}
		out = append(out, sep)
		size++
	}
	return out
}
