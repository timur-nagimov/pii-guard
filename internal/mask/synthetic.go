// Правдоподобная подстановка для вида маскирования synthetic. Каждый тип
// заменяется значением той же формы: имя — другим именем того же пола, телефон —
// номером того же формата, карта — числом, проходящим проверку Луна, и так
// далее. Подстановка детерминирована по сиду от значения, поэтому одно и то же
// значение всегда получает одну и ту же подстановку, а значение по подстановке
// не восстанавливается.

package mask

import (
	"strings"
	"unicode"

	"pii-guard/internal/pii"
	"pii-guard/internal/pii/dict"
)

// hashValue считает детерминированный сид от значения. Сид не раскрывает
// значение: по нему нельзя восстановить исходную строку.
func hashValue(value string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(value); i++ {
		h ^= uint64(value[i])
		h *= 1099511628211
	}
	return h
}

// nextRand продвигает детерминированный генератор и возвращает следующее число.
func nextRand(seed *uint64) uint64 {
	*seed = *seed*6364136223846793005 + 1442695040888963407
	return *seed
}

// randomDigits возвращает строку из n случайных цифр по сиду.
func randomDigits(n int, seed uint64) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('0' + seed%10)
		seed = nextRand(&seed)
	}
	return string(b)
}

// randomLetter возвращает случайную строчную букву по сиду.
func randomLetter(seed uint64) byte {
	return byte('a' + seed%26)
}

// syntheticValue подставляет правдоподобное значение того же типа.
func syntheticValue(value string, t pii.Type, seed uint64) string {
	switch t {
	case pii.TypeFIO, pii.TypeCardHolder:
		return syntheticFIO(value, seed)
	case pii.TypePhone:
		return syntheticPhone(value, seed)
	case pii.TypeEmail:
		return syntheticEmail(value, seed)
	case pii.TypeCard:
		return syntheticCard(value, seed)
	case pii.TypeINN:
		return syntheticINN(value, seed)
	case pii.TypeSNILS:
		return syntheticSNILS(value, seed)
	case pii.TypeDOB, pii.TypeIssueDate:
		return syntheticDate(value, seed)
	case pii.TypeAddress, pii.TypeBirthPlace:
		return syntheticPlace(value, seed)
	case pii.TypePostcode:
		return randomDigits(len(digitsOnly(value)), seed)
	case pii.TypeCitizenship:
		return dict.RandomCountry(seed)
	default:
		return syntheticGeneric(value, seed)
	}
}

// digitsOnly возвращает только цифры из строки.
func digitsOnly(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// syntheticGeneric заменяет буквы случайными буквами, а цифры — случайными
// цифрами, сохраняя все остальные знаки. Подходит для типов, у которых форма
// важнее смысла: код подразделения, водительское удостоверение, пин-код, код
// безопасности и прочие номера.
func syntheticGeneric(value string, seed uint64) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			b.WriteByte(byte('0' + seed%10))
			seed = nextRand(&seed)
		case unicode.IsLetter(r):
			lower := unicode.ToLower(r)
			rep := rune(randomLetter(seed))
			seed = nextRand(&seed)
			if unicode.IsUpper(r) {
				rep = unicode.ToUpper(rep)
			}
			// Кириллица заменяется кириллицей, латиница — латиницей.
			if lower >= 'а' && lower <= 'я' {
				rep = rune('а' + seed%32)
				seed = nextRand(&seed)
				if unicode.IsUpper(r) {
					rep = unicode.ToUpper(rep)
				}
			}
			b.WriteRune(rep)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// splitNameWords разбивает имя на слова: последовательности букв и дефисов.
// Инициалы («И.», «И.И.») остаются отдельными словами.
func splitNameWords(value string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range value {
		if unicode.IsLetter(r) || r == '-' {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return words
}

// isLatin сообщает, что все буквы значения — латинские.
func isLatin(value string) bool {
	hasLetter := false
	for _, r := range value {
		if !unicode.IsLetter(r) {
			continue
		}
		hasLetter = true
		if r > unicode.MaxASCII {
			return false
		}
	}
	return hasLetter
}

// isInitial сообщает, что слово — инициал: одна буква с точкой или без.
func isInitial(w string) bool {
	letters := 0
	for _, r := range w {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters == 1
}

// fioGender определяет пол по отчеству, имени или окончанию фамилии.
func fioGender(words []string) dict.Gender {
	for _, w := range words {
		low := strings.ToLower(w)
		if strings.HasSuffix(low, "ич") {
			return dict.GenderMale
		}
		if strings.HasSuffix(low, "вна") {
			return dict.GenderFemale
		}
	}
	for _, w := range words {
		if g, ok := dict.LookupName(w); ok {
			return g
		}
	}
	for _, w := range words {
		low := strings.ToLower(w)
		switch {
		case strings.HasSuffix(low, "ова"), strings.HasSuffix(low, "ева"), strings.HasSuffix(low, "ина"), strings.HasSuffix(low, "ская"):
			return dict.GenderFemale
		case strings.HasSuffix(low, "ов"), strings.HasSuffix(low, "ев"), strings.HasSuffix(low, "ин"), strings.HasSuffix(low, "ский"):
			return dict.GenderMale
		}
	}
	return dict.GenderUnknown
}

// malePatronymics и femalePatronymics — распространённые отчества для
// подстановки. Список короткий, но достаточный, чтобы подстановка выглядела
// правдоподобно.
var (
	malePatronymics = []string{
		"Иванович", "Петрович", "Сергеевич", "Андреевич", "Алексеевич",
		"Николаевич", "Владимирович", "Дмитриевич", "Александрович", "Михайлович",
	}
	femalePatronymics = []string{
		"Ивановна", "Петровна", "Сергеевна", "Андреевна", "Алексеевна",
		"Николаевна", "Владимировна", "Дмитриевна", "Александровна", "Михайловна",
	}
)

// pickPatronymic выбирает отчество по полу и сиду.
func pickPatronymic(g dict.Gender, seed uint64) string {
	if g == dict.GenderFemale {
		return femalePatronymics[seed%uint64(len(femalePatronymics))]
	}
	return malePatronymics[seed%uint64(len(malePatronymics))]
}

// pickGivenName выбирает имя по полу и сиду.
func pickGivenName(g dict.Gender, seed uint64) string {
	if g == dict.GenderFemale {
		return dict.RandomFemaleName(seed)
	}
	return dict.RandomMaleName(seed)
}

// syntheticFIO подставляет другое имя того же пола и той же формы: три слова —
// фамилия имя отчество, инициалы — инициалы, латиница — латинское имя.
func syntheticFIO(value string, seed uint64) string {
	words := splitNameWords(value)
	if len(words) == 0 {
		return maskRunes(value, false)
	}
	if isLatin(value) {
		return syntheticLatinFIO(words, seed)
	}
	g := fioGender(words)
	if g == dict.GenderUnknown {
		g = dict.GenderMale
	}

	// Инициалы: фамилия + инициалы.
	if len(words) >= 2 && isInitial(words[len(words)-1]) {
		surname := dict.RandomSurname(seed)
		initials := make([]string, 0, len(words)-1)
		for i := 1; i < len(words); i++ {
			initials = append(initials, initialsOf(pickGivenName(g, seed+uint64(i))))
		}
		return strings.Join(append([]string{surname}, initials...), " ")
	}

	switch len(words) {
	case 1:
		return surnameFor(g, seed)
	case 2:
		return surnameFor(g, seed) + " " + pickGivenName(g, seed+1)
	default:
		return surnameFor(g, seed) + " " + pickGivenName(g, seed+1) + " " + pickPatronymic(g, seed+2)
	}
}

// surnameFor возвращает фамилию, согласованную с полом: для женщины — в
// женской форме.
func surnameFor(g dict.Gender, seed uint64) string {
	s := dict.RandomSurname(seed)
	if g == dict.GenderFemale {
		return feminineSurname(s)
	}
	return s
}

// feminineSurname приводит фамилию к женской форме: «Борисов» → «Борисова»,
// «Борисовский» → «Борисовская».
func feminineSurname(s string) string {
	low := strings.ToLower(s)
	switch {
	case strings.HasSuffix(low, "ский"):
		return s[:len(s)-2] + "ая"
	case strings.HasSuffix(low, "ов"), strings.HasSuffix(low, "ев"), strings.HasSuffix(low, "ин"):
		return s + "а"
	default:
		return s
	}
}

// initialsOf возвращает инициал имени: первая буква с точкой.
func initialsOf(name string) string {
	for _, r := range name {
		return string(unicode.ToUpper(r)) + "."
	}
	return ""
}

// syntheticLatinFIO подставляет латинское имя той же формы.
func syntheticLatinFIO(words []string, seed uint64) string {
	if len(words) == 1 {
		return dict.RandomLatinSurname(seed)
	}
	out := make([]string, 0, len(words))
	for i, w := range words {
		if isInitial(w) {
			out = append(out, initialsOf(dict.RandomLatinName(seed+uint64(i))))
			continue
		}
		if i == 0 {
			out = append(out, dict.RandomLatinSurname(seed))
			continue
		}
		out = append(out, dict.RandomLatinName(seed+uint64(i)))
	}
	return strings.Join(out, " ")
}

// digitRun — непрерывная последовательность цифр в строке.
type digitRun struct {
	start, end int
	digits     string
}

// splitDigitRuns разбивает строку на последовательности цифр.
func splitDigitRuns(value string) []digitRun {
	var runs []digitRun
	i := 0
	for i < len(value) {
		if value[i] >= '0' && value[i] <= '9' {
			j := i
			for j < len(value) && value[j] >= '0' && value[j] <= '9' {
				j++
			}
			runs = append(runs, digitRun{start: i, end: j, digits: value[i:j]})
			i = j
		} else {
			i++
		}
	}
	return runs
}

// rebuildWithRuns собирает строку, заменяя цифровые последовательности на
// значения из replacements.
func rebuildWithRuns(value string, runs []digitRun, replacements []string) string {
	var b strings.Builder
	b.Grow(len(value))
	prev := 0
	for i, r := range runs {
		b.WriteString(value[prev:r.start])
		b.WriteString(replacements[i])
		prev = r.end
	}
	b.WriteString(value[prev:])
	return b.String()
}

// syntheticPhone подставляет номер того же формата: сохраняет разделители и код
// страны, заменяет цифры абонента, используя резервный код оператора 900.
func syntheticPhone(value string, seed uint64) string {
	runs := splitDigitRuns(value)
	if len(runs) == 0 {
		return maskRunes(value, false)
	}
	allDigits := digitsOnly(value)
	// Длина кода страны: первая последовательность после знака «+», либо
	// ведущая восьмёрка без знака «+».
	countryLen := 0
	switch {
	case strings.HasPrefix(value, "+"):
		if len(runs) == 1 {
			countryLen = 1
		} else {
			countryLen = len(runs[0].digits)
		}
	case len(allDigits) >= 11 && allDigits[0] == '8':
		countryLen = 1
	}
	// Новые цифры: код страны сохраняется, абонент заменяется резервным кодом
	// оператора 900 и случайными цифрами.
	newDigits := make([]byte, len(allDigits))
	copy(newDigits, allDigits[:countryLen])
	subLen := len(allDigits) - countryLen
	if subLen >= 3 {
		newDigits[countryLen], newDigits[countryLen+1], newDigits[countryLen+2] = '9', '0', '0'
		for i := countryLen + 3; i < len(allDigits); i++ {
			newDigits[i] = byte('0' + seed%10)
			seed = nextRand(&seed)
		}
	} else {
		for i := countryLen; i < len(allDigits); i++ {
			newDigits[i] = byte('0' + seed%10)
			seed = nextRand(&seed)
		}
	}
	return rebuildDigits(value, newDigits)
}

// syntheticEmail подставляет другой адрес на домене example.com, сохраняя
// форму локальной части: буквы и цифры заменяются, разделители остаются.
func syntheticEmail(value string, seed uint64) string {
	at := strings.Index(value, "@")
	if at < 0 {
		return maskRunes(value, false)
	}
	local := value[:at]
	var b strings.Builder
	b.Grow(len(local))
	for _, r := range local {
		switch {
		case r >= '0' && r <= '9':
			b.WriteByte(byte('0' + seed%10))
			seed = nextRand(&seed)
		case unicode.IsLetter(r):
			b.WriteByte(randomLetter(seed))
			seed = nextRand(&seed)
		default:
			b.WriteRune(r)
		}
	}
	return b.String() + "@example.com"
}

// luhnCheckDigit считает контрольную цифру Луна для заданных цифр.
func luhnCheckDigit(digits []byte) byte {
	sum := 0
	double := true
	for i := len(digits) - 1; i >= 0; i-- {
		v := int(digits[i] - '0')
		if double {
			v *= 2
			if v > 9 {
				v -= 9
			}
		}
		sum += v
		double = !double
	}
	return byte('0' + (10-sum%10)%10)
}

// syntheticCard подставляет число, проходящее проверку Луна, с тестовым
// префиксом 4111 или 5555, той же длины и с теми же разделителями.
func syntheticCard(value string, seed uint64) string {
	runs := splitDigitRuns(value)
	total := 0
	for _, r := range runs {
		total += len(r.digits)
	}
	if total < 12 {
		return syntheticGeneric(value, seed)
	}
	prefix := "4111"
	if seed%2 == 1 {
		prefix = "5555"
	}
	digits := make([]byte, total)
	copy(digits, prefix)
	for i := len(prefix); i < total; i++ {
		digits[i] = byte('0' + seed%10)
		seed = nextRand(&seed)
	}
	digits[total-1] = luhnCheckDigit(digits[:total-1])
	replacements := make([]string, len(runs))
	pos := 0
	for i, r := range runs {
		replacements[i] = string(digits[pos : pos+len(r.digits)])
		pos += len(r.digits)
	}
	return rebuildWithRuns(value, runs, replacements)
}

// syntheticINN подставляет число с правильными контрольными суммами ИНН.
func syntheticINN(value string, seed uint64) string {
	n := len(digitsOnly(value))
	if n != 10 && n != 12 {
		return syntheticGeneric(value, seed)
	}
	digits := make([]byte, n)
	for i := range digits {
		digits[i] = byte('0' + seed%10)
		seed = nextRand(&seed)
	}
	if n == 10 {
		digits[9] = innCheckDigit(digits[:9], innWeights10)
	} else {
		digits[10] = innCheckDigit(digits[:10], innWeights11)
		digits[11] = innCheckDigit(digits[:11], innWeights12)
	}
	return rebuildDigits(value, digits)
}

// innWeights10, innWeights11 и innWeights12 — веса контрольных сумм ИНН.
var (
	innWeights10 = []int{2, 4, 10, 3, 5, 9, 4, 6, 8}
	innWeights11 = []int{7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
	innWeights12 = []int{3, 7, 2, 4, 10, 3, 5, 9, 4, 6, 8}
)

// innCheckDigit считает контрольную цифру ИНН по весам.
func innCheckDigit(digits []byte, weights []int) byte {
	sum := 0
	for i, w := range weights {
		sum += int(digits[i]-'0') * w
	}
	return byte('0' + sum%11%10)
}

// syntheticSNILS подставляет число с правильной контрольной суммой СНИЛС.
func syntheticSNILS(value string, seed uint64) string {
	if len(digitsOnly(value)) != 11 {
		return syntheticGeneric(value, seed)
	}
	digits := make([]byte, 11)
	for i := 0; i < 9; i++ {
		digits[i] = byte('0' + seed%10)
		seed = nextRand(&seed)
	}
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(digits[i]-'0') * (9 - i)
	}
	ctrl := sum % 101
	if ctrl == 100 {
		ctrl = 0
	}
	digits[9] = byte('0' + ctrl/10)
	digits[10] = byte('0' + ctrl%10)
	return rebuildDigits(value, digits)
}

// rebuildDigits собирает строку, заменяя цифровые последовательности на
// значения из digits по порядку.
func rebuildDigits(value string, digits []byte) string {
	runs := splitDigitRuns(value)
	replacements := make([]string, len(runs))
	pos := 0
	for i, r := range runs {
		replacements[i] = string(digits[pos : pos+len(r.digits)])
		pos += len(r.digits)
	}
	return rebuildWithRuns(value, runs, replacements)
}

// syntheticDate подставляет другую дату в том же формате записи и в
// правдоподобном диапазоне.
func syntheticDate(value string, seed uint64) string {
	runs := splitDigitRuns(value)
	if len(runs) == 0 {
		return maskRunes(value, false)
	}
	// Определяем формат по числу групп цифр: 3 группы — день.месяц.год.
	var day, month, year int
	if len(runs) >= 3 {
		day = int(seed%28) + 1
		month = int(seed/28%12) + 1
		year = 1950 + int(seed/336%56)
	} else if len(runs) == 2 {
		month = int(seed%12) + 1
		year = 1950 + int(seed/12%56)
	} else {
		year = 1950 + int(seed%56)
	}
	replacements := make([]string, len(runs))
	for i := range runs {
		switch {
		case len(runs) >= 3 && i == 0:
			replacements[i] = twoDigits(day)
		case len(runs) >= 3 && i == 1:
			replacements[i] = twoDigits(month)
		case len(runs) >= 3 && i == 2:
			replacements[i] = itoa(year)
		case len(runs) == 2 && i == 0:
			replacements[i] = twoDigits(month)
		case len(runs) == 2 && i == 1:
			replacements[i] = itoa(year)
		default:
			replacements[i] = itoa(year)
		}
	}
	return rebuildWithRuns(value, runs, replacements)
}

// twoDigits возвращает число двумя цифрами.
func twoDigits(v int) string {
	if v < 10 {
		return "0" + itoa(v)
	}
	return itoa(v)
}

// DeclineVariants возвращает склонённые формы имени для восстановления. Если
// модель просклоняла подстановку в ответе, по склонённой форме находится
// исходное значение. Возвращает родительный и дательный падежи трёхсловного
// имени; для остальных форм возвращает пустой список.
func DeclineVariants(name string) []string {
	words := strings.Fields(name)
	if len(words) != 3 {
		return nil
	}
	gen := declineWord(words[0], "gen") + " " + declineWord(words[1], "gen") + " " + declineWord(words[2], "gen")
	dat := declineWord(words[0], "dat") + " " + declineWord(words[1], "dat") + " " + declineWord(words[2], "dat")
	return []string{gen, dat}
}

// declineWord склоняет одно слово имени в родительный или дательный падеж.
// Правила покрывают распространённые окончания русских фамилий, имён и
// отчеств; редкие окончания остаются без изменений.
func declineWord(w, caseName string) string {
	low := strings.ToLower(w)
	var gen, dat func(string) string
	switch {
	case strings.HasSuffix(low, "ский"):
		gen = func(s string) string { return s[:len(s)-2] + "ого" }
		dat = func(s string) string { return s[:len(s)-2] + "ому" }
	case strings.HasSuffix(low, "ская"):
		gen = func(s string) string { return s[:len(s)-2] + "ой" }
		dat = func(s string) string { return s[:len(s)-2] + "ой" }
	case strings.HasSuffix(low, "ова"), strings.HasSuffix(low, "ева"), strings.HasSuffix(low, "ина"):
		gen = func(s string) string { return s[:len(s)-1] + "ой" }
		dat = func(s string) string { return s[:len(s)-1] + "ой" }
	// Отчество на «вна» — частный случай слова на «а» и склоняется так же,
	// поэтому правило записано одной веткой.
	case strings.HasSuffix(low, "вна"), strings.HasSuffix(low, "а"):
		gen = func(s string) string { return s[:len(s)-1] + "ы" }
		dat = func(s string) string { return s[:len(s)-1] + "е" }
	case strings.HasSuffix(low, "я"):
		gen = func(s string) string { return s[:len(s)-1] + "и" }
		dat = func(s string) string { return s[:len(s)-1] + "е" }
	// Слова на согласный склоняются дописыванием окончания. Сюда же попадают
	// фамилии на «ов», «ев», «ин» и отчества на «ич»: своего правила у них нет,
	// а на «а» или «я» такое слово закончиться не может.
	default:
		gen = func(s string) string { return s + "а" }
		dat = func(s string) string { return s + "у" }
	}
	if caseName == "gen" {
		return gen(w)
	}
	return dat(w)
}

// syntheticPlace подставляет другой город в адресе или месте рождения.
func syntheticPlace(value string, seed uint64) string {
	// Заменяем город из словаря, если он есть в значении. Поиск идёт без учёта
	// регистра, а подстановка сохраняет заглавную букву.
	low := strings.ToLower(value)
	for _, city := range cityCandidates {
		idx := strings.Index(low, city)
		if idx < 0 {
			continue
		}
		repl := dict.RandomCity(seed)
		return value[:idx] + repl + value[idx+len(city):]
	}
	// Города нет — заменяем цифры и буквы, сохраняя форму.
	return syntheticGeneric(value, seed)
}

// cityCandidates возвращает список городов для поиска в значении. Список
// строится один раз.
var cityCandidates = func() []string {
	return []string{
		"москва", "санкт-петербург", "новосибирск", "екатеринбург", "казань",
		"нижний новгород", "челябинск", "самара", "омск", "ростов-на-дону",
		"уфа", "красноярск", "воронеж", "пермь", "волгоград", "краснодар",
		"саратов", "тюмень", "тольятти", "ижевск", "барнаул", "ульяновск",
		"иркутск", "хабаровск", "ярославль", "владивосток", "махачкала", "томск",
		"оренбург", "кемерово", "новокузнецк", "рязань", "астрахань",
		"набережные челны", "пенза", "липецк", "киров", "чебоксары", "тула",
		"калининград", "балашиха", "курск", "севастополь", "сочи", "ставрополь",
		"улан-удэ", "тверь", "магнитогорск", "иваново", "брянск", "белгород",
		"сургут", "владимир", "нижний тагил", "архангельск", "чита", "симферополь",
		"калуга", "смоленск", "волжский", "курган", "череповец", "орел", "вологда",
		"саранск", "якутск", "владикавказ", "мурманск", "тамбов", "стерлитамак",
		"грозный", "кострома", "петрозаводск", "нижневартовск", "новороссийск",
		"йошкар-ола", "таганрог", "комсомольск-на-амуре", "сыктывкар", "нальчик",
		"шахты", "дзержинск", "орск", "братск", "ангарск", "благовещенск", "псков",
		"бийск", "прокопьевск", "южно-сахалинск", "армавир", "рыбинск",
		"северодвинск", "абакан", "петропавловск-камчатский", "норильск",
		"сызрань", "волгодонск", "новочеркасск", "златоуст", "керчь", "элиста",
		"майкоп", "магадан", "салехард", "ханты-мансийск", "анапа",
		"геленджик", "новый уренгой", "нефтеюганск", "ноябрьск", "обнинск",
		"великий новгород", "старый оскол", "нижнекамск", "альметьевск",
		"химки", "подольск", "мытищи", "королев", "люберцы", "красногорск",
		"одинцово", "домодедово", "серпухов", "сергиев посад", "щелково",
		"раменское", "жуковский", "пушкино", "реутов", "ногинск", "зеленоград",
		"долгопрудный", "лобня", "видное", "дмитров", "клин", "наро-фоминск",
		"ступино", "коломна", "электросталь", "орехово-зуево", "балашов",
		"ленинград", "свердловск", "горький", "куйбышев", "сталинград",
		"минск", "киев", "алматы", "астана", "ташкент", "баку", "ереван",
		"тбилиси", "бишкек", "душанбе", "кишинев", "рига", "вильнюс", "таллин",
		"лондон", "париж", "берлин", "нью-йорк", "прага", "варшава", "стамбул",
	}
}()
