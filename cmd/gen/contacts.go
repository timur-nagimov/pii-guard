package main

import (
	"fmt"
	"strings"

	"pii-guard/internal/pii"
)

// translitTable — правила перевода кириллицы в латиницу. Нужны для почтовых
// адресов, имени держателя карты и категории с латиницей: в реальных анкетах
// одно и то же имя встречается в обеих графиках.
var translitTable = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// translit переводит строку в латиницу, сохраняя пробелы и знаки.
func translit(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if rep, ok := translitTable[r]; ok {
			b.WriteString(rep)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// titleLatin переводит строку в латиницу и делает первую букву каждого слова
// заглавной: так записывают имя в анкетах и в загранпаспорте.
func titleLatin(s string) string {
	words := strings.Fields(translit(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// email возвращает адрес почты: латиницей, в кириллической зоне или с плюсом
// в локальной части.
func (g *Generator) email(p person) string {
	switch g.r.IntN(6) {
	case 0:
		return translit(p.name) + "." + translit(p.surname) + "@" + g.pick(mailDomains)
	case 1:
		return translit(p.surname) + fmt.Sprintf("%d", 1960+g.r.IntN(50)) + "@" + g.pick(mailDomains)
	case 2:
		return translit(p.name)[:1] + "_" + translit(p.surname) + "@" + g.pick(mailDomains)
	case 3:
		return translit(p.name) + "." + translit(p.surname) + "+" +
			g.pick([]string{"bank", "alfa", "spam", "shop"}) + "@" + g.pick(mailDomains)
	case 4:
		return strings.ToLower(p.name) + "." + strings.ToLower(p.surname) + "@" + g.pick(mailDomainsCyr)
	default:
		return translit(p.surname) + "-" + translit(p.name)[:1] + "@" + g.pick(mailDomains)
	}
}

// phone возвращает номер телефона в одном из восьми форматов записи.
func (g *Generator) phone() string {
	code := g.pick(phoneCodes)
	a, b, c := g.digits(3), g.digits(2), g.digits(2)
	switch g.r.IntN(8) {
	case 0:
		return "+7 (" + code + ") " + a + "-" + b + "-" + c
	case 1:
		return "8 " + code + " " + a + " " + b + " " + c
	case 2:
		return "+7" + code + a + b + c
	case 3:
		return "8" + code + a + b + c
	case 4:
		return "8-" + code + "-" + a + "-" + b + "-" + c
	case 5:
		return "+7-" + code + "-" + a + b + c
	case 6:
		return "7 " + code + " " + a + "-" + b + "-" + c
	default:
		return "8 (" + code + ") " + a + b + c
	}
}

// postcode возвращает почтовый индекс из шести цифр.
func (g *Generator) postcode() string { return g.digitsNonZero(6) }

// streetPart возвращает улицу с номером дома и при необходимости с корпусом.
func (g *Generator) streetPart() string {
	street := g.pick(streetTypes) + " " + g.pick(streetNames)
	house := fmt.Sprintf("д. %d", 1+g.r.IntN(120))
	switch g.r.IntN(4) {
	case 0:
		house = fmt.Sprintf("дом %d", 1+g.r.IntN(120))
	case 1:
		house = fmt.Sprintf("д. %dк%d", 1+g.r.IntN(60), 1+g.r.IntN(5))
	case 2:
		house = fmt.Sprintf("д. %d, стр. %d", 1+g.r.IntN(60), 1+g.r.IntN(4))
	}
	return street + ", " + house
}

// flatPart возвращает запись квартиры.
func (g *Generator) flatPart() string {
	n := 1 + g.r.IntN(300)
	switch g.r.IntN(4) {
	case 0:
		return fmt.Sprintf("кв. %d", n)
	case 1:
		return fmt.Sprintf("кв.%d", n)
	case 2:
		return fmt.Sprintf("квартира %d", n)
	default:
		return fmt.Sprintf("к. %d", n)
	}
}

// cityPart возвращает населённый пункт с типом.
func (g *Generator) cityPart() string {
	return g.pick(settlementTypes) + " " + g.pick(cities)
}

// addressBody собирает адрес без индекса по компонентной грамматике: регион,
// населённый пункт, улица с домом, квартира. Часть компонентов пропускается,
// потому что в анкетах адрес редко бывает полным.
func (g *Generator) addressBody() string {
	parts := make([]string, 0, 4)
	if g.chance(35) {
		parts = append(parts, g.pick(regions))
	}
	if g.chance(85) {
		parts = append(parts, g.cityPart())
	}
	parts = append(parts, g.streetPart())
	if g.chance(60) {
		parts = append(parts, g.flatPart())
	}
	return strings.Join(parts, ", ")
}

// addressLatin собирает адрес латиницей: такие записи встречаются в анкетах
// для международных переводов.
func (g *Generator) addressLatin() string {
	city := titleLatin(g.pick(cities))
	street := titleLatin(g.pick(streetNames))
	out := fmt.Sprintf("%s, %s str., %d", city, street, 1+g.r.IntN(80))
	if g.chance(60) {
		out += fmt.Sprintf(", apt. %d", 1+g.r.IntN(200))
	}
	return out
}

// addressFrags возвращает адрес вместе с индексом. Индекс размечается своим
// типом и не входит в фрагмент адреса: в техническом задании это разные типы.
func (g *Generator) addressFrags() []frag {
	body := g.addressBody()
	switch g.r.IntN(5) {
	case 0:
		return frags(val(pii.TypePostcode, g.postcode()), lit(", "), val(pii.TypeAddress, body))
	case 1:
		return frags(val(pii.TypeAddress, body), lit(", "), val(pii.TypePostcode, g.postcode()))
	case 2:
		return frags(lit("индекс "), val(pii.TypePostcode, g.postcode()), lit(", "), val(pii.TypeAddress, body))
	case 3:
		return frags(val(pii.TypeAddress, g.streetPart()))
	default:
		return frags(val(pii.TypeAddress, body))
	}
}
