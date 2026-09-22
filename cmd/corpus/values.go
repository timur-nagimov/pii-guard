package main

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// maker порождает синтетические значения персональных данных для вставки в
// настоящие тексты. Источник случайности один и задаётся зерном, поэтому два
// запуска с одним зерном дают побайтово равный результат.
type maker struct{ r *rand.Rand }

// newMaker создаёт порождатель значений с явным источником случайности.
// Глобальный источник math/rand разделяется всей программой и
// воспроизводимости не даёт.
func newMaker(seed uint64) *maker {
	//nolint:gosec // здесь нужна воспроизводимость, а не криптостойкость
	return &maker{r: rand.New(rand.NewPCG(seed, seed^0x2545f4914f6cdd1d))}
}

// roll возвращает случайное число от нуля до n без единицы.
func (m *maker) roll(n int) int { return m.r.IntN(n) }

// pick выбирает случайную строку из списка.
func (m *maker) pick(list []string) string { return list[m.r.IntN(len(list))] }

// digitSet — цифры для сборки номеров. Выборка по индексу вместо счёта в
// байтах: так нет приведения типа и нечему переполниться.
const digitSet = "0123456789"

// num собирает строку из n случайных цифр.
func (m *maker) num(n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = digitSet[m.r.IntN(10)]
	}
	return string(buf)
}

// numNZ собирает строку из n цифр, начинающуюся не с нуля.
func (m *maker) numNZ(n int) string {
	if n < 1 {
		return ""
	}
	i := 1 + m.r.IntN(9)
	return digitSet[i:i+1] + m.num(n-1)
}

// human — набор именных частей одного вымышленного человека.
type human struct{ surname, name, patronymic string }

// human выбирает человека: мужского или женского рода согласованно.
func (m *maker) human() human {
	if m.roll(100) < 45 {
		i := m.roll(len(femalePatronymics))
		return human{
			surname:    m.pick(femaleSurnames),
			name:       m.pick(femaleNames),
			patronymic: femalePatronymics[i],
		}
	}
	i := m.roll(len(malePatronymics))
	return human{
		surname:    m.pick(maleSurnames),
		name:       m.pick(maleNames),
		patronymic: malePatronymics[i],
	}
}

// initial возвращает первую букву слова с точкой.
func initial(w string) string {
	for _, r := range w {
		return string(r) + "."
	}
	return ""
}

// fio записывает имя человека в одной из принятых форм.
func (m *maker) fio() string {
	h := m.human()
	forms := []string{
		h.surname + " " + h.name + " " + h.patronymic,
		h.name + " " + h.patronymic + " " + h.surname,
		h.surname + " " + initial(h.name) + initial(h.patronymic),
		initial(h.name) + initial(h.patronymic) + " " + h.surname,
		h.name + " " + h.surname,
		h.surname + " " + h.name,
	}
	return forms[m.roll(len(forms))]
}

// phone записывает мобильный номер в одном из шести начертаний.
func (m *maker) phone() string {
	code, a, b, c := m.pick(phoneCodes), m.num(3), m.num(2), m.num(2)
	layouts := []string{
		"+7 (%s) %s-%s-%s", "8%s%s%s%s", "+7%s%s%s%s",
		"8 (%s) %s %s %s", "8-%s-%s-%s-%s", "7 %s %s-%s-%s",
	}
	return fmt.Sprintf(layouts[m.roll(len(layouts))], code, a, b, c)
}

// email собирает адрес почты из латинского написания имени.
func (m *maker) email() string {
	login := strings.ToLower(m.pick(latinSurnames))
	switch m.roll(4) {
	case 0:
		login += "." + strings.ToLower(m.pick(latinNames))
	case 1:
		login += m.num(2)
	case 2:
		login = strings.ToLower(m.pick(latinNames)) + "_" + login
	}
	return login + "@" + m.pick(mailHosts)
}

// cardholder записывает имя держателя карты латиницей заглавными буквами.
func (m *maker) cardholder() string {
	return strings.ToUpper(m.pick(latinNames) + " " + m.pick(latinSurnames))
}

// address собирает почтовый адрес из региона, города, улицы и квартиры.
// Часть частей пропускается: в живых текстах адрес редко бывает полным.
func (m *maker) address() string {
	parts := make([]string, 0, 4)
	if m.roll(100) < 30 {
		parts = append(parts, m.pick(regionNames))
	}
	parts = append(parts, m.pick(settlementKinds)+" "+m.pick(cityNames))
	parts = append(parts, fmt.Sprintf("%s %s, д. %d", m.pick(streetKinds), m.pick(streetNames), 1+m.roll(140)))
	if m.roll(100) < 55 {
		parts = append(parts, fmt.Sprintf("%s %d", m.pick(flatKinds), 1+m.roll(320)))
	}
	return strings.Join(parts, ", ")
}

// birthPlace записывает место рождения.
func (m *maker) birthPlace() string {
	if m.roll(100) < 35 {
		return m.pick(settlementKinds) + " " + m.pick(cityNames) + ", " + m.pick(regionNames)
	}
	return m.pick(settlementKinds) + " " + m.pick(cityNames)
}

// issuer записывает орган, выдавший документ.
func (m *maker) issuer() string {
	out := fmt.Sprintf(m.pick(issuerForms), strings.ToUpper(m.pick(cityNames)))
	if m.roll(100) < 45 {
		return strings.ToUpper(out)
	}
	return out
}

// date записывает дату в одном из шести начертаний.
func (m *maker) date(minYear, maxYear int) string {
	d, mo, y := 1+m.roll(28), 1+m.roll(12), minYear+m.roll(maxYear-minYear+1)
	switch m.roll(6) {
	case 0:
		return fmt.Sprintf("%02d.%02d.%04d", d, mo, y)
	case 1:
		return fmt.Sprintf("%02d/%02d/%04d", d, mo, y)
	case 2:
		return fmt.Sprintf("%04d-%02d-%02d", y, mo, d)
	case 3:
		return fmt.Sprintf("%d %s %d года", d, monthNames[mo-1], y)
	case 4:
		return fmt.Sprintf("%02d-%02d-%04d", d, mo, y)
	default:
		return fmt.Sprintf("%02d %s %d", d, monthNames[mo-1], y)
	}
}

// group расставляет разделитель после каждых size цифр.
func group(digits, sep string, size int) string {
	var b strings.Builder
	for i := 0; i < len(digits); i += size {
		if i > 0 {
			b.WriteString(sep)
		}
		end := min(i+size, len(digits))
		b.WriteString(digits[i:end])
	}
	return b.String()
}

// weighted считает контрольный разряд по весам разрядов и модулю.
func weighted(digits string, weights []int, mod int) int {
	sum := 0
	for i, w := range weights {
		sum += int(digits[i]-'0') * w
	}
	return sum % mod
}

// Веса контрольных разрядов ИНН из приказа налоговой службы.
var (
	innW11 = []int{7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
	innW12 = []int{3, 7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
	innW10 = []int{2, 4, 10, 3, 5, 9, 4, 6, 8}
)

// innDigit возвращает контрольный разряд ИНН.
func innDigit(digits string, weights []int) string {
	return fmt.Sprintf("%d", weighted(digits, weights, 11)%10)
}

// inn порождает ИНН человека из двенадцати цифр или организации из десяти.
func (m *maker) inn() string {
	if m.roll(100) < 70 {
		body := m.numNZ(10)
		body += innDigit(body, innW11)
		return body + innDigit(body, innW12)
	}
	body := m.numNZ(9)
	return body + innDigit(body, innW10)
}

// snils порождает страховой номер с верной контрольной суммой.
func (m *maker) snils() string {
	body := m.num(9)
	weights := []int{9, 8, 7, 6, 5, 4, 3, 2, 1}
	ctrl := weighted(body, weights, 101)
	if ctrl >= 100 {
		ctrl = 0
	}
	full := body + fmt.Sprintf("%02d", ctrl)
	switch m.roll(3) {
	case 0:
		return full
	case 1:
		return group(full[:9], " ", 3) + " " + full[9:]
	default:
		return group(full[:9], "-", 3) + " " + full[9:]
	}
}

// luhn возвращает контрольную цифру номера карты по алгоритму Луна.
func luhn(body string) string {
	sum := 0
	for i := 0; i < len(body); i++ {
		v := int(body[i] - '0')
		if (len(body)-i)%2 == 1 {
			if v *= 2; v > 9 {
				v -= 9
			}
		}
		sum += v
	}
	return fmt.Sprintf("%d", (10-sum%10)%10)
}

// card порождает номер платёжной карты с верной контрольной цифрой.
func (m *maker) card() string {
	brand := cardPrefixes[m.roll(len(cardPrefixes))]
	body := brand.prefix + m.num(brand.length-len(brand.prefix)-1)
	number := body + luhn(body)
	switch m.roll(3) {
	case 0:
		return number
	case 1:
		return group(number, " ", 4)
	default:
		return group(number, "-", 4)
	}
}

// passport записывает серию и номер паспорта.
func (m *maker) passport() string {
	series, number := m.numNZ(4), m.num(6)
	switch m.roll(4) {
	case 0:
		return series + " " + number
	case 1:
		return series + number
	case 2:
		return series[:2] + " " + series[2:] + " " + number
	default:
		return series + " № " + number
	}
}

// driverLicense записывает водительское удостоверение старого или нового
// образца.
func (m *maker) driverLicense() string {
	region := fmt.Sprintf("%02d", 1+m.roll(89))
	if m.roll(100) < 40 {
		return region + " " + m.pick(docLetters) + " " + m.num(6)
	}
	return region + " " + m.num(2) + " " + m.num(6)
}
