package main

import (
	"fmt"
	"strings"

	"pii-guard/internal/pii"
)

// gcase — падеж, в котором записывается имя человека.
type gcase int

// Падежи, которые нужны шаблонам: именительный для перечислений, родительный
// для «паспорт гражданина», дательный для «перевод получателю».
const (
	caseNom gcase = iota
	caseGen
	caseDat
)

// person — человек, от которого порождаются все формы записи имени.
// Фамилия хранится в мужском именительном падеже, остальные формы выводятся
// правилами: так словарь остаётся коротким, а вариативность высокой.
type person struct {
	surname    string
	name       string
	patronymic string
	female     bool
}

// person порождает случайного человека.
func (g *Generator) person() person {
	female := g.chance(45)
	p := person{surname: g.fromPool(g.surnames), female: female}
	pair := patronymicPairs[g.r.IntN(len(patronymicPairs))]
	if female {
		p.name = g.fromPool(g.femNames)
		p.patronymic = pair.female
		return p
	}
	p.name = g.fromPool(g.maleNames)
	p.patronymic = pair.male
	return p
}

// hasAnySuffix сообщает, оканчивается ли слово на одно из окончаний.
func hasAnySuffix(w string, suffixes ...string) bool {
	for _, s := range suffixes {
		if strings.HasSuffix(w, s) {
			return true
		}
	}
	return false
}

// cutSuffix отрезает окончание заданной длины в рунах.
func cutSuffix(w string, runes int) string {
	r := []rune(w)
	if len(r) <= runes {
		return w
	}
	return string(r[:len(r)-runes])
}

// hissing сообщает, что перед окончанием стоит шипящая или заднеязычная:
// после них в родительном падеже пишется «и», а не «ы».
func hissing(stem string) bool {
	return hasAnySuffix(stem, "к", "г", "х", "ж", "ч", "ш", "щ")
}

// surnameForm возвращает фамилию в нужном роде и падеже.
func surnameForm(base string, female bool, c gcase) string {
	if hasAnySuffix(base, "ко", "ых", "их") {
		return base
	}
	if hasAnySuffix(base, "ский", "цкий") {
		return adjSurname(cutSuffix(base, 2), female, c)
	}
	return possessiveMale(base, female, c)
}

// adjSurname склоняет фамилию прилагательного типа: Вишневский, Троицкая.
func adjSurname(stem string, female bool, c gcase) string {
	if female {
		if c == caseNom {
			return stem + "ая"
		}
		return stem + "ой"
	}
	switch c {
	case caseGen:
		return stem + "ого"
	case caseDat:
		return stem + "ому"
	default:
		return stem + "ий"
	}
}

// possessiveMale склоняет фамилию притяжательного типа: Иванов, Иванова.
// Фамилии на согласную без суффикса (Кравчук) у женщин не склоняются.
func possessiveMale(base string, female bool, c gcase) string {
	declinable := hasAnySuffix(base, "ов", "ев", "ёв", "ин", "ын")
	if female {
		if !declinable {
			return base
		}
		if c == caseNom {
			return base + "а"
		}
		return base + "ой"
	}
	switch c {
	case caseGen:
		return base + "а"
	case caseDat:
		return base + "у"
	default:
		return base
	}
}

// maleNameStems — основы имён с беглой гласной. Общее правило даёт для них
// неверную форму: «Павела» вместо «Павла».
var maleNameStems = map[string]string{"Пётр": "Петр", "Павел": "Павл", "Лев": "Льв"}

// maleNounForm склоняет мужское имя или отчество.
func maleNounForm(w string, c gcase) string {
	if c == caseNom {
		return w
	}
	if stem, ok := maleNameStems[w]; ok {
		if c == caseGen {
			return stem + "а"
		}
		return stem + "у"
	}
	switch {
	case hasAnySuffix(w, "й", "ь"):
		return cutSuffix(w, 1) + softEnding(c)
	case hasAnySuffix(w, "а"):
		return femaleNounForm(w, c)
	case hasAnySuffix(w, "я"):
		return femaleNounForm(w, c)
	case c == caseGen:
		return w + "а"
	default:
		return w + "у"
	}
}

// softEnding возвращает окончание мягкого склонения.
func softEnding(c gcase) string {
	if c == caseGen {
		return "я"
	}
	return "ю"
}

// femaleNounForm склоняет женское имя, отчество и мужские имена на «а» и «я».
func femaleNounForm(w string, c gcase) string {
	if c == caseNom {
		return w
	}
	stem := cutSuffix(w, 1)
	switch {
	case strings.HasSuffix(w, "ия"):
		return stem + "и"
	case strings.HasSuffix(w, "я"):
		if c == caseGen {
			return stem + "и"
		}
		return stem + "е"
	case strings.HasSuffix(w, "а"):
		if c == caseGen {
			if hissing(stem) {
				return stem + "и"
			}
			return stem + "ы"
		}
		return stem + "е"
	default:
		return w
	}
}

// nameForm возвращает имя в нужном падеже.
func (p person) nameForm(c gcase) string {
	if p.female {
		return femaleNounForm(p.name, c)
	}
	return maleNounForm(p.name, c)
}

// patronymicForm возвращает отчество в нужном падеже.
func (p person) patronymicForm(c gcase) string {
	if p.female {
		return femaleNounForm(p.patronymic, c)
	}
	return maleNounForm(p.patronymic, c)
}

// surnameForm возвращает фамилию человека в нужном падеже.
func (p person) surnameForm(c gcase) string {
	return surnameForm(p.surname, p.female, c)
}

// full возвращает запись «Фамилия Имя Отчество».
func (p person) full(c gcase) string {
	return p.surnameForm(c) + " " + p.nameForm(c) + " " + p.patronymicForm(c)
}

// nameFirst возвращает запись «Имя Отчество Фамилия».
func (p person) nameFirst(c gcase) string {
	return p.nameForm(c) + " " + p.patronymicForm(c) + " " + p.surnameForm(c)
}

// firstRune возвращает первую руну слова: нужна для инициалов.
func firstRune(w string) string {
	for _, r := range w {
		return string(r)
	}
	return ""
}

// fioVariantCount — число разных записей имени.
const fioVariantCount = 14

// fioVariant возвращает одну из записей имени. Вариативность записи важнее
// числа разных людей: детектор обязан узнавать имя в любой форме.
func (g *Generator) fioVariant(p person, c gcase) string {
	return p.fioForm(g.r.IntN(fioVariantCount), c)
}

// fioForm возвращает запись имени с указанным номером. Номер, а не выбор
// внутри функции, потому что тест обязан перебрать все записи.
func (p person) fioForm(i int, c gcase) string {
	ni, pi := firstRune(p.name), firstRune(p.patronymic)
	switch i % fioVariantCount {
	case 0:
		return p.full(c)
	case 1:
		return p.nameFirst(c)
	case 2:
		return p.surnameForm(c) + " " + ni + "." + pi + "."
	case 3:
		return ni + "." + pi + ". " + p.surnameForm(c)
	case 4:
		return p.surnameForm(c) + " " + p.nameForm(c)
	case 5:
		return p.nameForm(c) + " " + p.surnameForm(c)
	case 6:
		return p.surnameForm(c) + " " + ni + ". " + pi + "."
	case 7:
		return strings.ToUpper(p.surnameForm(c)) + " " + p.nameForm(c) + " " + p.patronymicForm(c)
	case 8:
		return p.surnameForm(c) + " " + ni + pi
	case 9:
		return p.nameForm(c) + " " + p.patronymicForm(c)
	case 10:
		return p.surnameForm(c) + ", " + p.nameForm(c) + " " + p.patronymicForm(c)
	case 11:
		return ni + ". " + p.surnameForm(c)
	case 12:
		return p.surnameForm(c) + " " + p.nameForm(c) + " " + p.patronymicForm(c)
	default:
		return strings.ToUpper(p.full(c))
	}
}

// dateVal — дата, которую можно записать в разных форматах.
type dateVal struct {
	day   int
	month int
	year  int
}

// randomDate порождает дату в заданном диапазоне лет. День ограничен
// двадцать восьмым числом, чтобы не порождать несуществующие даты.
func (g *Generator) randomDate(minYear, maxYear int) dateVal {
	return dateVal{
		day:   1 + g.r.IntN(28),
		month: 1 + g.r.IntN(12),
		year:  minYear + g.r.IntN(maxYear-minYear+1),
	}
}

// formatCount — число поддерживаемых записей даты.
const formatCount = 12

// format записывает дату в одном из восьми форматов.
func (d dateVal) format(i int) string {
	switch i % formatCount {
	case 0:
		return fmt.Sprintf("%02d.%02d.%04d", d.day, d.month, d.year)
	case 1:
		return fmt.Sprintf("%02d/%02d/%04d", d.day, d.month, d.year)
	case 2:
		return fmt.Sprintf("%02d-%02d-%04d", d.day, d.month, d.year)
	case 3:
		return fmt.Sprintf("%04d-%02d-%02d", d.year, d.month, d.day)
	case 4:
		return fmt.Sprintf("%d %s %d", d.day, monthsGenitive[d.month-1], d.year)
	case 5:
		return fmt.Sprintf("%02d %s %d", d.day, monthsGenitive[d.month-1], d.year)
	case 6:
		return fmt.Sprintf("%02d.%02d.%02d", d.day, d.month, d.year%100)
	case 7:
		return fmt.Sprintf("%02d %02d %04d", d.day, d.month, d.year)
	case 8:
		return fmt.Sprintf("%d.%d.%04d", d.day, d.month, d.year)
	case 9:
		return fmt.Sprintf("%02d/%02d/%02d", d.day, d.month, d.year%100)
	case 10:
		return fmt.Sprintf("%04d.%02d.%02d", d.year, d.month, d.day)
	default:
		return fmt.Sprintf("«%02d» %s %04d", d.day, monthsGenitive[d.month-1], d.year)
	}
}

// dateFrags возвращает дату как размечаемое значение. Сокращение «г.» после
// года выносится в обрамляющий текст: в эталоне оно не часть даты.
func (g *Generator) dateFrags(t pii.Type, d dateVal) []frag {
	text := d.format(g.r.IntN(formatCount))
	switch {
	case g.chance(18):
		return frags(val(t, text), lit(" г."))
	case g.chance(10):
		return frags(val(t, text), lit(" года"))
	case g.chance(8):
		return frags(val(t, text), lit(" г.р."))
	default:
		return frags(val(t, text))
	}
}

// passportFrags возвращает серию и номер паспорта в одной из шести записей.
// Часть записей разрывает значение служебными словами, поэтому возвращается
// последовательность фрагментов, а не строка.
func (g *Generator) passportFrags() []frag {
	series, number := g.digitsNonZero(4), g.digits(6)
	t := pii.TypePassport
	switch g.r.IntN(10) {
	case 0:
		return frags(val(t, series+" "+number))
	case 1:
		return frags(val(t, series[:2]+" "+series[2:]+" "+number))
	case 2:
		return frags(val(t, series+number))
	case 3:
		return frags(val(t, series+"-"+number))
	case 4:
		return frags(val(t, series), lit(" № "), val(t, number))
	case 5:
		return frags(lit("серия "), val(t, series), lit(" номер "), val(t, number))
	case 6:
		return frags(lit("с. "), val(t, series), lit(" н. "), val(t, number))
	case 7:
		return frags(val(t, series[:2]+"-"+series[2:]+" "+number))
	case 8:
		return frags(val(t, series+" "+number[:3]+" "+number[3:]))
	default:
		return frags(lit("серия/номер "), val(t, series+"/"+number))
	}
}

// deptCode возвращает код подразделения в одной из трёх записей.
func (g *Generator) deptCode() string {
	a, b := g.digitsNonZero(3), g.digits(3)
	switch g.r.IntN(5) {
	case 0:
		return a + "-" + b
	case 1:
		return a + " " + b
	case 2:
		return a + "/" + b
	case 3:
		return a + "." + b
	default:
		return a + b
	}
}

// issuer возвращает орган выдачи паспорта по шаблону.
func (g *Generator) issuer() string {
	tmpl := g.pick(issuerTemplates)
	var where string
	switch g.r.IntN(3) {
	case 0:
		where = "Г. " + strings.ToUpper(g.pick(cities))
	case 1:
		where = g.pick(regions)
	default:
		where = "Г. " + strings.ToUpper(g.pick(cities)) + ", РАЙОН " + g.pick(cityDistricts)
	}
	out := fmt.Sprintf(tmpl, where)
	if g.chance(40) {
		return strings.ToUpper(out)
	}
	return out
}

// driverLicense возвращает водительское удостоверение старого образца с
// буквами серии или нового образца из одних цифр.
func (g *Generator) driverLicense() string {
	region := fmt.Sprintf("%02d", 1+g.r.IntN(89))
	if g.chance(45) {
		letters := []string{"АА", "АВ", "ВС", "ЕК", "МН", "ОР", "ТУ", "ХА", "РТ", "СН"}
		switch g.r.IntN(3) {
		case 0:
			return region + " " + g.pick(letters) + " " + g.digits(6)
		case 1:
			return region + g.pick(letters) + g.digits(6)
		default:
			return region + " " + g.pick(letters) + "-" + g.digits(6)
		}
	}
	switch g.r.IntN(5) {
	case 0:
		return region + " " + g.digits(2) + " " + g.digits(6)
	case 1:
		return region + g.digits(2) + " " + g.digits(6)
	case 2:
		return region + " " + g.digits(2) + "-" + g.digits(6)
	case 3:
		return region + "-" + g.digits(2) + "-" + g.digits(6)
	default:
		return region + g.digits(2) + g.digits(6)
	}
}

// Веса контрольных разрядов ИНН. Значения заданы приказом ФНС и повторяют
// проверку движка: генератору нужен обратный расчёт, а не проверка.
var (
	innWeights10 = []int{2, 4, 10, 3, 5, 9, 4, 6, 8}
	innWeights11 = []int{7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
	innWeights12 = []int{3, 7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
)

// innControl считает контрольный разряд по весам.
func innControl(digits string, weights []int) byte {
	sum := 0
	for i, w := range weights {
		sum += int(digits[i]-'0') * w
	}
	return byte('0' + sum%11%10)
}

// inn возвращает ИНН из десяти или двенадцати цифр. Неверная контрольная
// сумма нужна отдельной категорией: жюри печатает выдуманные номера, и
// детектор обязан находить их по якорю, а не по сумме.
func (g *Generator) inn(long, valid bool) string {
	if long {
		body := g.digitsNonZero(10)
		full := body + string(innControl(body, innWeights11))
		full += string(innControl(full, innWeights12))
		return breakLast(full, valid)
	}
	body := g.digitsNonZero(9)
	return breakLast(body+string(innControl(body, innWeights10)), valid)
}

// breakLast портит последнюю цифру, если значение должно быть неверным.
func breakLast(digits string, valid bool) string {
	if valid || digits == "" {
		return digits
	}
	last := digits[len(digits)-1]
	return digits[:len(digits)-1] + string(byte('0'+(int(last-'0')+1)%10))
}

// luhnControl считает контрольную цифру по алгоритму Луна для тела номера.
func luhnControl(body string) byte {
	sum, double := 0, true
	for i := len(body) - 1; i >= 0; i-- {
		v := int(body[i] - '0')
		if double {
			if v *= 2; v > 9 {
				v -= 9
			}
		}
		sum += v
		double = !double
	}
	return byte('0' + (10-sum%10)%10)
}

// cardNumber возвращает номер карты выбранной платёжной системы в одной из
// записей: слитно, по четыре через пробел или через дефис.
func (g *Generator) cardNumber(valid bool) string {
	brand := cardBrands[g.r.IntN(len(cardBrands))]
	body := brand.prefix + g.digits(brand.length-len(brand.prefix)-1)
	number := breakLast(body+string(luhnControl(body)), valid)
	return groupDigits(number, g.r.IntN(3))
}

// groupDigits расставляет разделители внутри номера карты.
func groupDigits(number string, style int) string {
	if style == 0 {
		return number
	}
	sep := " "
	if style == 2 {
		sep = "-"
	}
	var b strings.Builder
	for i := 0; i < len(number); i += 4 {
		if i > 0 {
			b.WriteString(sep)
		}
		end := i + 4
		if end > len(number) {
			end = len(number)
		}
		b.WriteString(number[i:end])
	}
	return b.String()
}

// snils возвращает страховой номер в одной из записей.
func (g *Generator) snils(valid bool) string {
	body := g.digits(9)
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(body[i]-'0') * (9 - i)
	}
	ctrl := sum % 101
	if ctrl >= 100 {
		ctrl = 0
	}
	full := body + fmt.Sprintf("%02d", ctrl)
	full = breakLast(full, valid)
	switch g.r.IntN(5) {
	case 0:
		return full[:3] + "-" + full[3:6] + "-" + full[6:9] + " " + full[9:]
	case 1:
		return full
	case 2:
		return full[:3] + "-" + full[3:6] + "-" + full[6:9] + "-" + full[9:]
	case 3:
		return full[:3] + " " + full[3:6] + " " + full[6:9] + "-" + full[9:]
	default:
		return full[:3] + " " + full[3:6] + " " + full[6:9] + " " + full[9:]
	}
}

// foreignPassport возвращает номер заграничного паспорта: две цифры серии и
// семь цифр номера.
func (g *Generator) foreignPassport() string {
	series, number := g.digitsNonZero(2), g.digits(7)
	switch g.r.IntN(5) {
	case 0:
		return series + " " + number
	case 1:
		return series + number
	case 2:
		return series + "-" + number
	case 3:
		return series + " № " + number
	default:
		return series + "№" + number
	}
}

// residencePermit возвращает номер вида на жительство.
func (g *Generator) residencePermit() string {
	switch g.r.IntN(4) {
	case 0:
		return g.digitsNonZero(2) + " " + g.digits(7)
	case 1:
		return g.digitsNonZero(4) + " " + g.digits(6)
	case 2:
		return g.digitsNonZero(2) + "№" + g.digits(7)
	default:
		return g.digitsNonZero(4) + "-" + g.digits(6)
	}
}

// birthCert возвращает реквизиты свидетельства о рождении: римская серия,
// буквенный код и номер.
func (g *Generator) birthCert() string {
	roman := []string{"I", "II", "III", "IV", "V", "VI", "VII", "VIII", "IX", "X"}
	codes := []string{"МЮ", "АГ", "ТО", "БК", "НР", "СВ", "ИК", "ПН", "ЖТ", "ЕА"}
	head := g.pick(roman) + "-" + g.pick(codes)
	switch g.r.IntN(4) {
	case 0:
		return head + " № " + g.digits(6)
	case 1:
		return head + " " + g.digits(6)
	case 2:
		return head + "№" + g.digits(6)
	default:
		return head + " No " + g.digits(6)
	}
}

// militaryID возвращает номер военного билета.
func (g *Generator) militaryID() string {
	codes := []string{"АС", "АН", "НА", "ТА", "ЕС", "МТ", "АЕ", "НП", "ТВ", "СО"}
	head := g.pick(codes)
	switch g.r.IntN(4) {
	case 0:
		return head + " № " + g.digits(7)
	case 1:
		return head + " " + g.digits(7)
	case 2:
		return head + "-" + g.digits(7)
	default:
		return head + g.digits(7)
	}
}

// citizenship возвращает запись гражданства.
func (g *Generator) citizenship() string {
	return g.pick([]string{
		"Российская Федерация", "РФ", "гражданин России", "Россия",
		"Республика Беларусь", "Республика Казахстан", "Узбекистан",
		"Киргизская Республика", "Армения", "гражданка России",
		"Республика Таджикистан", "Азербайджанская Республика", "Молдова",
		"Украина", "Грузия", "Туркменистан", "Russian Federation", "RU",
		"Россия и Израиль", "Республика Армения", "Азербайджан",
	})
}

// birthPlace возвращает место рождения: город, село с районом или запись с
// областью.
func (g *Generator) birthPlace() string {
	city := g.pick(cities)
	switch g.r.IntN(8) {
	case 0:
		return "г. " + city
	case 1:
		return "город " + city
	case 2:
		return "с. " + g.pick(villages) + ", " + g.pick(regions)
	case 3:
		return "г. " + city + ", " + g.pick(regions)
	case 4:
		return "д. " + g.pick(villages) + ", " + g.pick(regions) + ", Россия"
	case 5:
		return "пос. " + g.pick(villages) + " " + g.pick(regions)
	case 6:
		return city
	default:
		return "гор. " + city + " " + g.pick(regions)
	}
}

// accountNumber возвращает номер банковского счёта: двадцать цифр, начинающихся
// на балансовый счёт физического лица. Записывается слитно либо группами.
func (g *Generator) accountNumber() string {
	prefix := g.pick([]string{"40817", "40820", "40802", "42301", "42601"})
	number := prefix + g.digits(15)
	switch g.r.IntN(4) {
	case 0:
		return number
	case 1:
		return number[:5] + " " + number[5:8] + " " + number[8:9] + " " + number[9:13] + " " + number[13:]
	case 2:
		return number[:5] + " " + number[5:]
	default:
		return number[:8] + " " + number[8:]
	}
}

// omsNumber возвращает номер полиса ОМС: шестнадцать цифр, слитно либо по
// четыре.
func (g *Generator) omsNumber() string {
	number := g.digitsNonZero(16)
	switch g.r.IntN(3) {
	case 0:
		return number
	case 1:
		return number[:4] + " " + number[4:8] + " " + number[8:12] + " " + number[12:]
	default:
		return number[:4] + "-" + number[4:8] + "-" + number[8:12] + "-" + number[12:]
	}
}

// plateLetters — буквы госномера, имеющие латинские двойники по начертанию.
var plateLetters = []string{"А", "В", "Е", "К", "М", "Н", "О", "Р", "С", "Т", "У", "Х"}

// plateNumber возвращает госномер автомобиля: буква, три цифры, две буквы, две
// или три цифры региона. Иногда буквы набирают латиницей.
func (g *Generator) plateNumber() string {
	plate := g.pick(plateLetters) + g.digits(3) + g.pick(plateLetters) + g.pick(plateLetters)
	region := g.digits(2)
	if g.chance(40) {
		region = g.digits(3)
	}
	plate += region
	runes := []rune(plate)
	switch g.r.IntN(4) {
	case 0:
		return plate
	case 1:
		return string(runes[0]) + " " + string(runes[1:4]) + " " + string(runes[4:6]) + " " + string(runes[6:])
	case 2:
		return g.replaceHomoglyphs(plate)
	default:
		return string(runes[0]) + string(runes[1:4]) + " " + string(runes[4:6]) + " " + string(runes[6:])
	}
}

// vinChars — допустимые знаки VIN: латиница и цифры без букв I, O и Q.
var vinChars = []rune{
	'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'J', 'K', 'L', 'M', 'N',
	'P', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z',
	'0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
}

// vinNumber возвращает идентификационный номер транспортного средства:
// семнадцать знаков латиницы и цифр без букв I, O и Q.
func (g *Generator) vinNumber() string {
	var b strings.Builder
	for i := 0; i < 17; i++ {
		b.WriteRune(vinChars[g.r.IntN(len(vinChars))])
	}
	return b.String()
}

// ipAddress возвращает адрес в сети: IPv4 из четырёх чисел от нуля до 255.
// Служебные адреса не порождаются: они на человека не указывают.
func (g *Generator) ipAddress() string {
	return fmt.Sprintf("%d.%d.%d.%d",
		1+g.r.IntN(254), g.r.IntN(256), g.r.IntN(256), 1+g.r.IntN(254))
}
