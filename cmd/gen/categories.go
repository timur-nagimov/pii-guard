package main

import "fmt"

// categorySpec — категория набора: имя, относительный вес в выпуске и
// порождающая функция.
type categorySpec struct {
	name     string
	weight   int
	negative bool
	gen      func(*Generator) []frag
}

// simpleCategories возвращает категории с одним типом данных в тексте: по
// одной на каждый тип из технического задания. Список берётся из
// спецификаций типов, поэтому новый тип попадает в выпуск сам собой.
func simpleCategories() []categorySpec {
	specs := valueSpecs()
	out := make([]categorySpec, 0, len(specs))
	for _, s := range specs {
		out = append(out, categorySpec{name: s.name, weight: 4, gen: simpleCategoryGen(s)})
	}
	return out
}

// compositeCategories возвращает категории со сложными текстами: несколько
// типов в одном тексте, вариации регистра, латиница, таблицы и опечатки.
func compositeCategories() []categorySpec {
	return []categorySpec{
		{name: "mixed", weight: 10, gen: genMixed},
		{name: "case_variants", weight: 4, gen: genCaseVariants},
		{name: "latin", weight: 4, gen: genLatin},
		{name: "latin_names", weight: 4, gen: genLatinNames},
		{name: "intl_phone", weight: 4, gen: genIntlPhone},
		{name: "english_anchor", weight: 4, gen: genEnglishAnchor},
		{name: "mixed_anchor", weight: 4, gen: genMixedAnchor},
		{name: "anchored_invalid_checksum", weight: 5, gen: genInvalidChecksum},
		{name: "date_no_anchor", weight: 4, gen: genDateNoAnchor},
		{name: "table", weight: 6, gen: genTable},
		{name: "typos", weight: 4, gen: genTypos},
	}
}

// textKindCategories возвращает категории с разными видами текста: разговор,
// заявление, выгрузка, таблица, письмо, заметка и тексты с несколькими
// упоминаниями людей. Анкета — не единственная форма, в которой приходят
// персональные данные, и окружение значения в каждой форме своё.
func textKindCategories() []categorySpec {
	return []categorySpec{
		{name: "dialog", weight: 6, gen: genDialog},
		{name: "statement", weight: 5, gen: genStatement},
		{name: "export_row", weight: 6, gen: genExportRow},
		{name: "table_five", weight: 5, gen: genTableFive},
		{name: "email_letter", weight: 5, gen: genEmailLetter},
		{name: "free_note", weight: 5, gen: genFreeNote},
		{name: "two_people", weight: 5, gen: genTwoPeople},
		{name: "three_mentions", weight: 5, gen: genThreeMentions},
		{name: "bilingual", weight: 5, gen: genBilingual},
	}
}

// negativeCategories возвращает категории с пустой разметкой. По ним
// измеряются ложные срабатывания.
func negativeCategories() []categorySpec {
	return []categorySpec{
		{name: "neg_famous_person", gen: negFamous, negative: true},
		{name: "neg_memorial_street", gen: negMemorialStreet, negative: true},
		{name: "neg_bank_office", gen: negBankOffice, negative: true},
		{name: "neg_org_requisites", gen: negOrgRequisites, negative: true},
		{name: "neg_money", gen: negMoney, negative: true},
		{name: "neg_order_number", gen: negOrder, negative: true},
		{name: "neg_payment_date", gen: negPaymentDate, negative: true},
		{name: "neg_card_expiry", gen: negCardExpiry, negative: true},
		{name: "neg_sms_code", gen: negSMSCode, negative: true},
		{name: "neg_network_address", gen: negNetwork, negative: true},
		{name: "neg_version", gen: negVersion, negative: true},
		{name: "neg_law_reference", gen: negLaw, negative: true},
		{name: "neg_pin_word", gen: negPINWord, negative: true},
		{name: "neg_three_digits", gen: negThreeDigits, negative: true},
		{name: "neg_support_phone", gen: negSupportPhone, negative: true},
		{name: "neg_bank_requisites", gen: negBankRequisites, negative: true},
		{name: "neg_branch_address_tail", gen: negBranchAddressTail, negative: true},
		{name: "neg_rate_date", gen: negRateDate, negative: true},
		{name: "neg_branch_number", gen: negBranchNumber, negative: true},
		{name: "neg_account_number", gen: negAccountNumber, negative: true},
		{name: "neg_company_like_surname", gen: negCompanyLikeSurname, negative: true},
		{name: "neg_named_position", gen: negNamedPosition, negative: true},
		{name: "neg_law_quote", gen: negLawQuote, negative: true},
		{name: "neg_article_code", gen: negArticleCode, negative: true},
		{name: "neg_confirm_code", gen: negConfirmCode, negative: true},
		{name: "neg_flight_number", gen: negFlightNumber, negative: true},
		{name: "neg_track_number", gen: negTrackNumber, negative: true},
	}
}

// longCategory — категория с очень длинным текстом. Выделена отдельно, потому
// что её объём задаётся не весом, а штучным количеством.
func longCategory() categorySpec {
	return categorySpec{name: "long", weight: 0, gen: genLong}
}

// allCategories собирает полный список категорий в устойчивом порядке.
// Порядок фиксирован, потому что от него зависит воспроизводимость выпуска.
func allCategories() []categorySpec {
	out := simpleCategories()
	out = append(out, compositeCategories()...)
	out = append(out, textKindCategories()...)
	out = append(out, longCategory())
	return append(out, negativeCategories()...)
}

// isNegativeCategory сообщает, что у категории не должно быть разметки.
func isNegativeCategory(name string) bool {
	for _, c := range negativeCategories() {
		if c.name == name {
			return true
		}
	}
	return false
}

// Доли выпуска. Отрицательных строк должно хватать на все отрицательные
// категории с запасом, иначе ложные срабатывания не на чем мерить: при
// двадцати семи категориях нижняя граница даёт два десятка строк на каждую.
const (
	negativeSharePercent = 14
	minNegativeRecords   = 540
)

// negativeTotal возвращает число отрицательных строк для набора размера n.
func negativeTotal(n int) int {
	c := n * negativeSharePercent / 100
	if n >= 2000 && c < minNegativeRecords {
		c = minNegativeRecords
	}
	if c > n/2 {
		c = n / 2
	}
	return c
}

// longTotal ограничивает число длинных текстов: каждый занимает четыреста
// килобайт, и большое их число раздувает файл без пользы для измерения.
func longTotal(n int) int {
	switch {
	case n >= 8000:
		return 4
	case n >= 1000:
		return 2
	case n >= 400:
		return 1
	default:
		return 0
	}
}

// planCounts распределяет n строк по категориям: отрицательные получают свою
// долю поровну, длинные — штучное число, остальные — пропорционально весу.
func planCounts(n int) []int {
	cats := allCategories()
	counts := make([]int, len(cats))
	if n <= 0 {
		return counts
	}
	left := n
	left -= assignLong(cats, counts, longTotal(n), left)
	left -= assignNegative(cats, counts, negativeTotal(n), left)
	assignByWeight(cats, counts, left)
	return counts
}

// assignLong выделяет строки категории с длинным текстом.
func assignLong(cats []categorySpec, counts []int, want, left int) int {
	if want > left {
		want = left
	}
	for i, c := range cats {
		if c.name == "long" {
			counts[i] = want
			return want
		}
	}
	return 0
}

// assignNegative делит отрицательные строки поровну между отрицательными
// категориями, остаток отдаёт первым по порядку.
func assignNegative(cats []categorySpec, counts []int, want, left int) int {
	if want > left {
		want = left
	}
	idx := indicesOf(cats, true)
	if len(idx) == 0 || want <= 0 {
		return 0
	}
	base, rest := want/len(idx), want%len(idx)
	for k, i := range idx {
		counts[i] = base
		if k < rest {
			counts[i]++
		}
	}
	return want
}

// assignByWeight делит оставшиеся строки между положительными категориями
// пропорционально весу.
func assignByWeight(cats []categorySpec, counts []int, left int) {
	idx := indicesOf(cats, false)
	total := 0
	for _, i := range idx {
		total += cats[i].weight
	}
	if total == 0 || left <= 0 {
		return
	}
	used := 0
	for _, i := range idx {
		if cats[i].weight == 0 {
			continue
		}
		counts[i] += left * cats[i].weight / total
		used += left * cats[i].weight / total
	}
	for k := 0; used < left; k++ {
		i := idx[k%len(idx)]
		if cats[i].weight == 0 {
			continue
		}
		counts[i]++
		used++
	}
}

// indicesOf возвращает позиции категорий нужного знака.
func indicesOf(cats []categorySpec, negative bool) []int {
	out := make([]int, 0, len(cats))
	for i, c := range cats {
		if c.negative == negative {
			out = append(out, i)
		}
	}
	return out
}

// order строит перемешанную последовательность категорий выпуска. Перемешивание
// идёт из того же источника случайности, поэтому набор воспроизводим.
func (g *Generator) order(n int) []int {
	counts := planCounts(n)
	seq := make([]int, 0, n)
	for i, c := range counts {
		for k := 0; k < c; k++ {
			seq = append(seq, i)
		}
	}
	g.r.Shuffle(len(seq), func(i, j int) { seq[i], seq[j] = seq[j], seq[i] })
	return seq
}

// Each порождает n записей и передаёт их обработчику по одной. Потоковый
// обход нужен из-за длинных текстов: держать весь набор в памяти незачем.
func (g *Generator) Each(n int, fn func(Record) error) error {
	cats := allCategories()
	for i, ci := range g.order(n) {
		c := cats[ci]
		// Префикс источника в идентификаторе: набор склеивается из нескольких
		// источников, и одинаковых номеров между ними быть не должно.
		id := fmt.Sprintf("%s-%s-%06d", sourceSynthetic, c.name, i)
		if err := fn(buildRecord(id, c.name, c.gen(g))); err != nil {
			return err
		}
	}
	return nil
}

// Generate порождает n записей в память. Используется тестами и мелкими
// выборками, для больших наборов применяется Each.
func (g *Generator) Generate(n int) []Record {
	out := make([]Record, 0, n)
	_ = g.Each(n, func(r Record) error {
		out = append(out, r)
		return nil
	})
	return out
}
