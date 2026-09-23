package main

import (
	"strings"
	"testing"

	"pii-guard/internal/pii"
)

// Проверка формы порождаемых значений: длины, разделители и контрольные суммы
// каждого значения по отдельности. Вынесено из gen_test.go отдельным файлом,
// потому что там проверяется набор целиком — свойства записей, разметка,
// покрытие категорий и форм записи, — а здесь только сами значения, и растёт
// эта часть с каждым новым типом.

// valueCase — один случай табличной проверки: имя подтеста и тело проверки.
type valueCase struct {
	name  string
	check func(t *testing.T)
}

// TestValueShapes — табличная проверка порождаемых значений: длины, наличие
// разделителей, контрольные суммы и верные, и заведомо неверные. Случаи
// разложены по группам значений, но генератор у них общий и порядок случаев
// задаёт последовательность случайных чисел, поэтому группы склеиваются в
// одном и том же порядке.
func TestValueShapes(t *testing.T) {
	g := NewGenerator(99, false)
	var cases []valueCase
	for _, group := range [][]valueCase{
		innValueCases(g),
		cardValueCases(g),
		numericValueCases(g),
		passportValueCases(g),
		otherDocValueCases(g),
		emailValueCases(g),
		dateValueCases(),
		translitValueCases(g),
		personValueCases(g),
		distortionValueCases(g),
		addressValueCases(g),
	} {
		cases = append(cases, group...)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.check(t) })
	}
}

// emailAppears ищет редкое написание адреса серией попыток: написание
// выбирается случайно, поэтому по одной попытке нельзя судить, порождается ли
// оно вообще.
func emailAppears(g *Generator, p person, match func(string) bool) bool {
	for i := 0; i < 200; i++ {
		if match(g.email(p)) {
			return true
		}
	}
	return false
}

// innValueCases — случаи ИНН: у него есть контрольная сумма, поэтому
// проверяются обе стороны — и верное значение, и намеренно испорченное.
func innValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"ИНН из десяти цифр проходит проверку", func(t *testing.T) {
			v := g.inn(false, true)
			if len(v) != 10 || !pii.INNValid(v) {
				t.Errorf("ИНН %q не проходит проверку", v)
			}
		}},
		{"ИНН из двенадцати цифр проходит проверку", func(t *testing.T) {
			v := g.inn(true, true)
			if len(v) != 12 || !pii.INNValid(v) {
				t.Errorf("ИНН %q не проходит проверку", v)
			}
		}},
		{"испорченный ИНН из десяти цифр не проходит проверку", func(t *testing.T) {
			if v := g.inn(false, false); pii.INNValid(v) {
				t.Errorf("ИНН %q неожиданно верен", v)
			}
		}},
		{"испорченный ИНН из двенадцати цифр не проходит проверку", func(t *testing.T) {
			if v := g.inn(true, false); pii.INNValid(v) {
				t.Errorf("ИНН %q неожиданно верен", v)
			}
		}},
	}
}

// cardValueCases — случаи номера карты: он обязан проходить алгоритм Луна,
// а испорченный — не проходить, иначе набор не проверяет распознавание.
func cardValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"номер карты проходит алгоритм Луна", func(t *testing.T) {
			number := g.cardNumber(true)
			if !pii.Luhn(pii.DigitsOnly(number)) {
				t.Errorf("номер %q не проходит алгоритм Луна", number)
			}
		}},
		{"испорченный номер карты не проходит алгоритм Луна", func(t *testing.T) {
			number := g.cardNumber(false)
			if pii.Luhn(pii.DigitsOnly(number)) {
				t.Errorf("номер %q неожиданно верен", number)
			}
		}},
		{"номер карты содержит шестнадцать цифр", func(t *testing.T) {
			number := g.cardNumber(true)
			if len(pii.DigitsOnly(number)) != 16 {
				t.Errorf("в номере %q не шестнадцать цифр", number)
			}
		}},
	}
}

// numericValueCases — значения из одних цифр: СНИЛС, телефон, почтовый
// индекс и код подразделения. У них проверяется длина и контрольная сумма
// там, где она есть.
func numericValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"СНИЛС проходит проверку", func(t *testing.T) {
			v := g.snils(true)
			if !pii.SNILSValid(pii.DigitsOnly(v)) {
				t.Errorf("СНИЛС %q не проходит проверку", v)
			}
		}},
		{"испорченный СНИЛС не проходит проверку", func(t *testing.T) {
			if v := g.snils(false); pii.SNILSValid(pii.DigitsOnly(v)) {
				t.Errorf("СНИЛС %q неожиданно верен", v)
			}
		}},
		{"телефон содержит одиннадцать цифр", func(t *testing.T) {
			v := g.phone()
			if n := len(pii.DigitsOnly(v)); n != 11 {
				t.Errorf("в телефоне %q %d цифр", v, n)
			}
		}},
		{"почтовый индекс из шести цифр", func(t *testing.T) {
			if v := g.postcode(); len(v) != 6 || v[0] == '0' {
				t.Errorf("индекс %q неверной формы", v)
			}
		}},
		{"код подразделения из шести цифр", func(t *testing.T) {
			if v := g.deptCode(); len(pii.DigitsOnly(v)) != 6 {
				t.Errorf("код подразделения %q неверной формы", v)
			}
		}},
	}
}

// passportValueCases — документы, удостоверяющие личность: паспорт,
// водительское удостоверение и заграничный паспорт.
func passportValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"паспорт содержит десять цифр", func(t *testing.T) {
			total := 0
			for _, fr := range g.passportFrags() {
				if fr.typ == pii.TypePassport {
					total += len(pii.DigitsOnly(fr.text))
				}
			}
			if total != 10 {
				t.Errorf("в паспорте %d цифр вместо десяти", total)
			}
		}},
		{"водительское удостоверение содержит десять знаков", func(t *testing.T) {
			v := g.driverLicense()
			if n := len(pii.DigitsOnly(v)); n != 8 && n != 10 {
				t.Errorf("в удостоверении %q %d цифр", v, n)
			}
		}},
		{"заграничный паспорт из девяти цифр", func(t *testing.T) {
			if v := g.foreignPassport(); len(pii.DigitsOnly(v)) != 9 {
				t.Errorf("загранпаспорт %q неверной формы", v)
			}
		}},
	}
}

// otherDocValueCases — остальные документы: вид на жительство,
// свидетельство о рождении и военный билет.
func otherDocValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"вид на жительство из девяти или десяти цифр", func(t *testing.T) {
			v := g.residencePermit()
			if n := len(pii.DigitsOnly(v)); n != 9 && n != 10 {
				t.Errorf("вид на жительство %q неверной формы", v)
			}
		}},
		{"свидетельство о рождении содержит римскую серию", func(t *testing.T) {
			v := g.birthCert()
			if !strings.Contains(v, "-") || len(pii.DigitsOnly(v)) != 6 {
				t.Errorf("свидетельство %q неверной формы", v)
			}
		}},
		{"военный билет содержит номер", func(t *testing.T) {
			if v := g.militaryID(); len(pii.DigitsOnly(v)) != 7 {
				t.Errorf("военный билет %q неверной формы", v)
			}
		}},
	}
}

// emailValueCases — случаи почтового адреса. Кроме общей формы проверяется
// то, что редкие написания вообще порождаются: без них набор не научит
// узнавать адрес в кириллической зоне и адрес с плюсом.
func emailValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"почта содержит собаку и точку", func(t *testing.T) {
			v := g.email(g.person())
			if !strings.Contains(v, "@") || !strings.Contains(v, ".") {
				t.Errorf("адрес %q неверной формы", v)
			}
		}},
		{"почта в зоне рф встречается", func(t *testing.T) {
			if !emailAppears(g, g.person(), func(v string) bool { return strings.HasSuffix(v, ".рф") }) {
				t.Error("адрес в кириллической зоне ни разу не встретился")
			}
		}},
		{"почта с плюсом встречается", func(t *testing.T) {
			if !emailAppears(g, g.person(), func(v string) bool { return strings.Contains(v, "+") }) {
				t.Error("адрес с плюсом ни разу не встретился")
			}
		}},
	}
}

// dateValueCases — случаи даты: форматов двенадцать, и они обязаны
// отличаться друг от друга, иначе набор проверяет один и тот же вид записи.
func dateValueCases() []valueCase {
	return []valueCase{
		{"двенадцать форматов даты различаются", func(t *testing.T) {
			d := dateVal{day: 5, month: 3, year: 1984}
			seen := make(map[string]bool)
			for i := 0; i < formatCount; i++ {
				seen[d.format(i)] = true
			}
			if len(seen) != formatCount {
				t.Errorf("различных форматов %d вместо %d", len(seen), formatCount)
			}
		}},
		{"дата с названием месяца", func(t *testing.T) {
			d := dateVal{day: 12, month: 5, year: 1984}
			if got := d.format(4); got != "12 мая 1984" {
				t.Errorf("получено %q", got)
			}
		}},
		{"дата в обратном порядке", func(t *testing.T) {
			d := dateVal{day: 12, month: 5, year: 1984}
			if got := d.format(3); got != "1984-05-12" {
				t.Errorf("получено %q", got)
			}
		}},
	}
}

// translitValueCases — перевод имени в латиницу: он участвует и в адресе
// почты, и в имени держателя карты.
func translitValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"транслитерация фамилии", func(t *testing.T) {
			if got := translit("Иванов"); got != "ivanov" {
				t.Errorf("получено %q", got)
			}
		}},
		{"транслитерация с шипящими", func(t *testing.T) {
			if got := translit("Щукина"); got != "shchukina" {
				t.Errorf("получено %q", got)
			}
		}},
		{"латиница с заглавных букв", func(t *testing.T) {
			if got := titleLatin("иван петров"); got != "Ivan Petrov" {
				t.Errorf("получено %q", got)
			}
		}},
		{"имя держателя карты заглавными", func(t *testing.T) {
			v := g.holderName(person{surname: "Иванов", name: "Иван"})
			if v != strings.ToUpper(v) || !strings.Contains(v, " ") {
				t.Errorf("имя держателя %q неверной формы", v)
			}
		}},
	}
}

// geoMarkers — пометы, по которым в названии органа выдачи узнаётся география:
// город, область, край, республика или округ.
var geoMarkers = []string{"г.", "обл", "край", "республик", "округ"}

// hasGeoMarker отвечает, названа ли в строке хотя бы одна географическая
// помета. Проверка вынесена из условия: пять «содержит» подряд — это одно
// свойство строки, а не пять разных, и читать его надо целиком.
func hasGeoMarker(v string) bool {
	for _, marker := range geoMarkers {
		if strings.Contains(v, marker) {
			return true
		}
	}
	return false
}

// personValueCases — сведения о человеке из документов: гражданство, место
// рождения и орган выдачи.
func personValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"гражданство непустое", func(t *testing.T) {
			if g.citizenship() == "" {
				t.Error("гражданство пустое")
			}
		}},
		{"место рождения непустое", func(t *testing.T) {
			if v := g.birthPlace(); v == "" || strings.ContainsAny(v, "\n") {
				t.Errorf("место рождения %q неверной формы", v)
			}
		}},
		{"орган выдачи содержит географию", func(t *testing.T) {
			v := strings.ToLower(g.issuer())
			if !hasGeoMarker(v) {
				t.Errorf("орган выдачи %q без географии", v)
			}
		}},
	}
}

// distortionValueCases — искажения написания: опечатка и смешанный регистр
// обязаны менять вид записи, но не сами буквы и не строку целиком.
func distortionValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"опечатка не ломает строку", func(t *testing.T) {
			v := g.typo("Иванов Иван Иванович")
			if v == "" || strings.ContainsAny(v, "\n\r") {
				t.Errorf("опечатка дала %q", v)
			}
		}},
		{"смешанный регистр сохраняет буквы", func(t *testing.T) {
			v := g.mixCase("Иванов")
			if strings.ToLower(v) != "иванов" {
				t.Errorf("смешанный регистр дал %q", v)
			}
		}},
	}
}

// addressValueCases — случаи адреса: и кириллицей, и латиницей.
func addressValueCases(g *Generator) []valueCase {
	return []valueCase{
		{"адрес содержит дом", func(t *testing.T) {
			if v := g.addressBody(); !strings.Contains(v, "д. ") && !strings.Contains(v, "дом ") {
				t.Errorf("адрес %q без номера дома", v)
			}
		}},
		{"адрес латиницей содержит улицу", func(t *testing.T) {
			if v := g.addressLatin(); !strings.Contains(v, "str.") {
				t.Errorf("адрес %q неверной формы", v)
			}
		}},
	}
}
