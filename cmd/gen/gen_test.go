package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"pii-guard/internal/pii"
)

// samplePath — пробная выборка в репозитории. Тесты опираются на неё, чтобы
// не порождать набор заново на каждом прогоне.
const samplePath = "../../testdata/sample.jsonl"

// minShapesPerType — сколько разных форм записи обязано быть у каждого типа.
// Требование задания: не меньше пятнадцати.
const minShapesPerType = 15

// knownTypes собирает все типы, которые вправе встретиться в разметке.
func knownTypes() map[string]bool {
	out := make(map[string]bool)
	for _, t := range pii.AllTypes() {
		out[string(t)] = true
	}
	for _, t := range []pii.Type{
		pii.TypeSNILS, pii.TypeForeignPassport, pii.TypeResidencePermit,
		pii.TypeBirthCert, pii.TypeMilitaryID,
	} {
		out[string(t)] = true
	}
	return out
}

// TestGenerateDeterministic проверяет главное свойство набора: один seed даёт
// побайтово один и тот же результат. Без этого нельзя сравнивать замеры
// качества между запусками.
func TestGenerateDeterministic(t *testing.T) {
	first := NewGenerator(42, false).Generate(200)
	second := NewGenerator(42, false).Generate(200)
	if len(first) != len(second) {
		t.Fatalf("разное число записей: %d и %d", len(first), len(second))
	}
	for i := range first {
		a, err := json.Marshal(first[i])
		if err != nil {
			t.Fatalf("запись %d не сериализуется: %v", i, err)
		}
		b, err := json.Marshal(second[i])
		if err != nil {
			t.Fatalf("запись %d не сериализуется: %v", i, err)
		}
		if string(a) != string(b) {
			t.Fatalf("запись %d отличается между запусками:\n%s\n%s", i, a, b)
		}
	}
}

// TestGenerateSeedChangesCorpus проверяет, что другой seed даёт другой набор:
// иначе seed не влияет ни на что и выборки разных запусков совпадают.
func TestGenerateSeedChangesCorpus(t *testing.T) {
	a := NewGenerator(42, false).Generate(40)
	b := NewGenerator(43, false).Generate(40)
	same := 0
	for i := range a {
		if a[i].Text == b[i].Text {
			same++
		}
	}
	if same == len(a) {
		t.Fatal("наборы с разными seed совпали полностью")
	}
}

// checkRecord проверяет одну запись: границы байтовые и в пределах текста,
// срез текста по границам совпадает с подставленным значением, фрагменты не
// пересекаются, не захватывают пробелы и перевод строки.
func checkRecord(t *testing.T, r Record, types map[string]bool) {
	t.Helper()
	if !utf8.ValidString(r.Text) {
		t.Fatalf("%s: текст не является корректным UTF-8", r.ID)
	}
	if r.Source != sourceSynthetic {
		t.Fatalf("%s: источник %q вместо %q", r.ID, r.Source, sourceSynthetic)
	}
	if r.PartialLabels {
		t.Fatalf("%s: порождённая запись помечена как размеченная частично", r.ID)
	}
	if len(r.Spans) != len(r.Values) {
		t.Fatalf("%s: %d фрагментов при %d значениях", r.ID, len(r.Spans), len(r.Values))
	}
	prevEnd := 0
	for i, s := range r.Spans {
		switch {
		case s.Start < 0 || s.End > len(r.Text) || s.Start >= s.End:
			t.Fatalf("%s: фрагмент %d вне границ текста: %d..%d при длине %d", r.ID, i, s.Start, s.End, len(r.Text))
		case s.Start < prevEnd:
			t.Fatalf("%s: фрагмент %d пересекается с предыдущим", r.ID, i)
		case !types[s.Type]:
			t.Fatalf("%s: неизвестный тип %q", r.ID, s.Type)
		}
		prevEnd = s.End
		got := r.Text[s.Start:s.End]
		if !utf8.ValidString(got) {
			t.Fatalf("%s: фрагмент %d обрывает букву посреди кодовой последовательности: %q", r.ID, i, got)
		}
		if got != r.Values[i] {
			t.Fatalf("%s: фрагмент %d указывает на %q вместо подставленного %q", r.ID, i, got, r.Values[i])
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Fatalf("%s: фрагмент %d пересекает перевод строки: %q", r.ID, i, got)
		}
		if strings.TrimSpace(got) != got {
			t.Fatalf("%s: фрагмент %d захватил пробелы по краям: %q", r.ID, i, got)
		}
	}
}

// TestSpansPointToValues проверяет разметку всего набора: смещения байтовые и
// указывают ровно на подставленное значение.
func TestSpansPointToValues(t *testing.T) {
	types := knownTypes()
	for _, r := range NewGenerator(7, false).Generate(1500) {
		checkRecord(t, r, types)
	}
}

// TestSpansAreByteOffsets проверяет, что смещения именно байтовые, а не по
// символам. Кириллическая буква занимает два байта, поэтому у записи с
// кириллицей перед фрагментом число байтов до него больше числа символов;
// если бы смещения считались по символам, срез попал бы не туда.
func TestSpansAreByteOffsets(t *testing.T) {
	checked := 0
	for _, r := range NewGenerator(21, false).Generate(800) {
		for i, s := range r.Spans {
			runesBefore := utf8.RuneCountInString(r.Text[:s.Start])
			if runesBefore == s.Start {
				continue // до фрагмента только однобайтовые знаки, проверять нечего
			}
			checked++
			if r.Text[s.Start:s.End] != r.Values[i] {
				t.Fatalf("%s: байтовый срез не совпал со значением %q", r.ID, r.Values[i])
			}
			// Срез по тем же числам, но в рунах, обязан отличаться: это и
			// означает, что смещения байтовые, а не символьные.
			runes := []rune(r.Text)
			if s.End <= len(runes) && string(runes[s.Start:s.End]) == r.Values[i] {
				t.Fatalf("%s: срез по рунам совпал со значением, смещения похожи на символьные", r.ID)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("проверено всего %d фрагментов с кириллицей перед ними", checked)
	}
}

// TestJSONRoundTripKeepsOffsets проверяет, что после записи в JSON Lines и
// обратного разбора границы по-прежнему указывают на значение: набор
// передаётся именно в этом виде.
func TestJSONRoundTripKeepsOffsets(t *testing.T) {
	for _, r := range NewGenerator(13, false).Generate(300) {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("%s: не сериализуется: %v", r.ID, err)
		}
		var back Record
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s: не разбирается: %v", r.ID, err)
		}
		if back.Source != sourceSynthetic || back.PartialLabels {
			t.Fatalf("%s: поля источника потерялись после разбора", r.ID)
		}
		for i, s := range back.Spans {
			if back.Text[s.Start:s.End] != r.Values[i] {
				t.Fatalf("%s: после разбора фрагмент %d указывает не на значение", r.ID, i)
			}
		}
	}
}

// TestShapeCatalog проверяет сам перечень форм записи: их не меньше
// пятнадцати и имена не повторяются.
func TestShapeCatalog(t *testing.T) {
	shapes := writeShapes()
	if len(shapes) < minShapesPerType {
		t.Fatalf("форм записи %d, требуется не меньше %d", len(shapes), minShapesPerType)
	}
	seen := make(map[string]bool, len(shapes))
	for _, s := range shapes {
		if seen[s.name] {
			t.Errorf("форма записи %s объявлена дважды", s.name)
		}
		seen[s.name] = true
	}
	if shapeCount() != len(shapes) {
		t.Errorf("shapeCount вернул %d при %d формах", shapeCount(), len(shapes))
	}
}

// TestEveryShapeForEveryType прогоняет каждый тип через каждую форму записи:
// разметка обязана оставаться верной в любом сочетании.
func TestEveryShapeForEveryType(t *testing.T) {
	types := knownTypes()
	shapes := writeShapes()
	for _, spec := range valueSpecs() {
		for i, shape := range shapes {
			g := NewGenerator(uint64(1000+i), false)
			r := buildRecord("shape-test", spec.name, g.render(spec, g.person(), i))
			checkRecord(t, r, types)
			if !hasType(r, spec.typ) {
				t.Errorf("тип %s в форме %s не попал в разметку: %q", spec.typ, shape.name, r.Text)
			}
		}
	}
}

// hasType сообщает, встретился ли в разметке нужный тип.
func hasType(r Record, typ pii.Type) bool {
	for _, s := range r.Spans {
		if s.Type == string(typ) {
			return true
		}
	}
	return false
}

// skeleton заменяет значения на метку и оставляет только обрамляющий текст.
// Два разных скелета означают две разные формы записи.
func skeleton(r Record) string {
	var b strings.Builder
	prev := 0
	for _, s := range r.Spans {
		b.WriteString(r.Text[prev:s.Start])
		b.WriteString("<V>")
		prev = s.End
	}
	b.WriteString(r.Text[prev:])
	return b.String()
}

// TestFifteenWritingFormsPerType проверяет главное требование к разнообразию:
// у каждого типа не меньше пятнадцати различных форм записи. Форма считается
// по обрамляющему тексту, а не по самому значению.
func TestFifteenWritingFormsPerType(t *testing.T) {
	for _, spec := range valueSpecs() {
		g := NewGenerator(77, false)
		forms := make(map[string]bool)
		for i := 0; i < 600; i++ {
			r := buildRecord("form-test", spec.name, g.renderAny(spec, g.person()))
			forms[skeleton(r)] = true
		}
		if len(forms) < minShapesPerType {
			t.Errorf("тип %s: различных форм записи %d, требуется не меньше %d",
				spec.typ, len(forms), minShapesPerType)
		}
	}
}

// TestHardWritingFormsAppear проверяет, что трудные формы записи действительно
// попадают в текст: неразрывный пробел, двойные пробелы, перенос значения на
// другую строку, длинный или неразрывный дефис, латинские омоглифы.
func TestHardWritingFormsAppear(t *testing.T) {
	cases := []struct {
		shape string
		name  string
		found func(text string) bool
	}{
		{"nbsp", "неразрывный пробел", func(s string) bool { return strings.Contains(s, nbsp) }},
		{"double_space", "двойные пробелы", func(s string) bool { return strings.Contains(s, "  ") }},
		{"line_break", "перенос значения", func(s string) bool { return strings.Contains(s, nl) }},
		{"hyphen", "особый дефис", func(s string) bool {
			return strings.Contains(s, hyphenLong) || strings.Contains(s, hyphenNoBreak)
		}},
		{"homoglyph", "латинские омоглифы", hasLatinHomoglyph},
		{"quotes_ru", "кавычки", func(s string) bool { return strings.Contains(s, "«") }},
		{"parens", "скобки", func(s string) bool { return strings.Contains(s, "(") }},
		{"semicolons", "точка с запятой", func(s string) bool { return strings.Contains(s, ";") }},
	}
	spec, ok := specByName("phone")
	if !ok {
		t.Fatal("спецификация phone не найдена")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx := shapeIndex(t, c.shape)
			g := NewGenerator(5, false)
			found := false
			for i := 0; i < 60 && !found; i++ {
				r := buildRecord("hard-test", spec.name, g.render(spec, g.person(), idx))
				found = c.found(r.Text)
			}
			if !found {
				t.Errorf("форма %s ни разу не дала ожидаемого знака", c.shape)
			}
		})
	}
}

// shapeIndex ищет номер формы записи по имени.
func shapeIndex(t *testing.T, name string) int {
	t.Helper()
	for i, s := range writeShapes() {
		if s.name == name {
			return i
		}
	}
	t.Fatalf("форма записи %s не найдена", name)
	return 0
}

// hasLatinHomoglyph сообщает, встретилась ли в кириллическом тексте латинская
// буква из набора похожих.
func hasLatinHomoglyph(s string) bool {
	if !strings.ContainsAny(s, "абвгдежзийклмнопрстуфхцчшщъыьэюяАБВГДЕЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯ") {
		return false
	}
	return strings.ContainsAny(s, "aeopcyxAEOPCYX")
}

// TestNegativeCategoriesEmpty проверяет, что отрицательные категории не несут
// разметки: по ним измеряются ложные срабатывания.
func TestNegativeCategoriesEmpty(t *testing.T) {
	seen := make(map[string]int)
	for _, r := range NewGenerator(11, false).Generate(4000) {
		if !isNegativeCategory(r.Category) {
			continue
		}
		seen[r.Category]++
		if len(r.Spans) != 0 {
			t.Fatalf("%s: в отрицательной категории есть разметка: %v", r.ID, r.Spans)
		}
	}
	for _, c := range negativeCategories() {
		if seen[c.name] == 0 {
			t.Errorf("отрицательная категория %s не встретилась в выпуске", c.name)
		}
	}
}

// TestNewCategoriesPresent проверяет, что новые виды текста и новые трудные
// отрицательные примеры действительно попадают в выпуск.
func TestNewCategoriesPresent(t *testing.T) {
	want := []string{
		// виды текста
		"dialog", "statement", "export_row", "table_five", "email_letter",
		"free_note", "two_people", "three_mentions", "bilingual",
		// латиница и международные данные
		"latin_names", "intl_phone", "english_anchor", "mixed_anchor",
		// трудные отрицательные примеры
		"neg_support_phone", "neg_bank_requisites", "neg_branch_address_tail",
		"neg_rate_date", "neg_branch_number", "neg_account_number",
		"neg_company_like_surname", "neg_named_position", "neg_law_quote",
		"neg_article_code", "neg_confirm_code", "neg_flight_number",
		"neg_track_number",
	}
	seen := make(map[string]int)
	for _, r := range NewGenerator(17, false).Generate(4000) {
		seen[r.Category]++
	}
	for _, name := range want {
		if seen[name] == 0 {
			t.Errorf("категория %s не встретилась в выпуске", name)
		}
	}
}

// TestAllCategoriesAppearInOutput проверяет, что в выпуске встречается каждая
// объявленная категория, а не только та, что попала в план.
func TestAllCategoriesAppearInOutput(t *testing.T) {
	seen := make(map[string]int)
	for _, r := range NewGenerator(23, false).Generate(4000) {
		seen[r.Category]++
	}
	for _, c := range allCategories() {
		if seen[c.name] == 0 {
			t.Errorf("категория %s не встретилась в выпуске на четыре тысячи строк", c.name)
		}
	}
}

// TestPlanCoversAllCategories проверяет, что в выпуске на тридцать тысяч строк
// есть каждая категория и что сумма по категориям сходится.
func TestPlanCoversAllCategories(t *testing.T) {
	const want = 30000
	counts := planCounts(want)
	total := 0
	for i, c := range allCategories() {
		if counts[i] == 0 {
			t.Errorf("категория %s не попала в выпуск", c.name)
		}
		total += counts[i]
	}
	if total != want {
		t.Errorf("сумма по категориям %d вместо %d", total, want)
	}
	if got := negativeTotal(want); got < minNegativeRecords {
		t.Errorf("отрицательных строк %d, требуется не меньше %d", got, minNegativeRecords)
	}
}

// TestEveryTypeIsCovered проверяет, что в выпуске встречается каждый тип из
// технического задания: иначе по нему нечего измерять.
func TestEveryTypeIsCovered(t *testing.T) {
	seen := make(map[string]int)
	for _, r := range NewGenerator(31, false).Generate(2000) {
		for _, s := range r.Spans {
			seen[s.Type]++
		}
	}
	for typ := range knownTypes() {
		if seen[typ] == 0 {
			t.Errorf("тип %s не встретился в выпуске", typ)
		}
	}
}

// TestLongCategorySize проверяет, что длинный текст действительно достигает
// четырёхсот килобайт: на таком объёме проверяется скорость движка.
func TestLongCategorySize(t *testing.T) {
	g := NewGenerator(3, false)
	r := buildRecord("long-test", "long", genLong(g))
	if len(r.Text) < longTargetBytes {
		t.Fatalf("длинный текст занимает %d байт вместо %d", len(r.Text), longTargetBytes)
	}
	checkRecord(t, r, knownTypes())
}

// TestHoldoutPoolsDisjoint проверяет отложенную выборку: она не пересекается с
// основной и занимает около пятнадцати процентов словаря.
func TestHoldoutPoolsDisjoint(t *testing.T) {
	pools := map[string]pool{
		"фамилии":       splitPool(surnamesAll),
		"мужские имена": splitPool(maleNamesAll),
		"женские имена": splitPool(femaleNamesAll),
	}
	for name, p := range pools {
		main := make(map[string]bool, len(p.main))
		for _, v := range p.main {
			main[v] = true
		}
		for _, v := range p.held {
			if main[v] {
				t.Errorf("%s: значение %q есть и в основной, и в отложенной части", name, v)
			}
		}
		share := len(p.held) * 100 / (len(p.held) + len(p.main))
		if share < 10 || share > 20 {
			t.Errorf("%s: отложено %d%% вместо примерно пятнадцати", name, share)
		}
	}
}

// TestHoldoutNamesUnusedInMainCorpus проверяет, что основной набор берёт имена
// только из основной части словаря: иначе по отложенным именам нельзя честно
// измерить обобщение детектора.
func TestHoldoutNamesUnusedInMainCorpus(t *testing.T) {
	g := NewGenerator(5, false)
	held := make(map[string]bool)
	for _, p := range []pool{g.surnames, g.maleNames, g.femNames} {
		for _, v := range p.held {
			held[v] = true
		}
	}
	for i := 0; i < 500; i++ {
		p := g.person()
		if held[p.surname] || held[p.name] {
			t.Fatalf("в основной набор попало отложенное значение: %s %s", p.surname, p.name)
		}
	}
	h := NewGenerator(5, true)
	for i := 0; i < 100; i++ {
		p := h.person()
		if !held[p.surname] || !held[p.name] {
			t.Fatalf("отложенная выборка взяла значение из основной части: %s %s", p.surname, p.name)
		}
	}
}

// TestSampleFile проверяет пробную выборку в репозитории: она должна читаться
// и быть согласованной, чтобы тесты работали без порождения набора.
func TestSampleFile(t *testing.T) {
	f, err := os.Open(samplePath)
	if err != nil {
		t.Skipf("пробная выборка недоступна: %v", err)
	}
	defer func() { _ = f.Close() }()

	types := knownTypes()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	lines := 0
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("строка %d не разбирается: %v", lines+1, err)
		}
		lines++
		for i, s := range r.Spans {
			if s.Start < 0 || s.End > len(r.Text) || s.Start >= s.End {
				t.Fatalf("%s: фрагмент %d вне границ текста", r.ID, i)
			}
			if !types[s.Type] {
				t.Fatalf("%s: неизвестный тип %q", r.ID, s.Type)
			}
		}
		if isNegativeCategory(r.Category) && len(r.Spans) != 0 {
			t.Fatalf("%s: отрицательная категория с разметкой", r.ID)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("чтение выборки: %v", err)
	}
	if lines < 100 {
		t.Fatalf("в пробной выборке %d строк, ожидалось не меньше ста", lines)
	}
}

// TestFIOFormsDistinct проверяет, что записей имени действительно много и все
// они разные: имя — самый частый тип в наборе.
func TestFIOFormsDistinct(t *testing.T) {
	p := person{surname: "Иванов", name: "Иван", patronymic: "Иванович"}
	seen := make(map[string]bool)
	for i := 0; i < fioVariantCount; i++ {
		seen[p.fioForm(i, caseNom)] = true
	}
	if len(seen) < 10 {
		t.Errorf("различных записей имени %d, ожидалось не меньше десяти", len(seen))
	}
}

// TestDeclension — табличная проверка склонения имён и фамилий. Падежные
// формы важны: в текстах анкет имя редко стоит в именительном падеже.
func TestDeclension(t *testing.T) {
	cases := []struct {
		name   string
		word   string
		female bool
		c      gcase
		want   string
	}{
		{"фамилия на -ов, родительный", "Иванов", false, caseGen, "Иванова"},
		{"фамилия на -ов, дательный", "Иванов", false, caseDat, "Иванову"},
		{"фамилия на -ов, женская", "Иванов", true, caseNom, "Иванова"},
		{"фамилия на -ов, женская родительный", "Иванов", true, caseGen, "Ивановой"},
		{"фамилия на -ин, женская дательный", "Никитин", true, caseDat, "Никитиной"},
		{"фамилия на -ёв, родительный", "Королёв", false, caseGen, "Королёва"},
		{"фамилия на -ский, родительный", "Вишневский", false, caseGen, "Вишневского"},
		{"фамилия на -ский, дательный", "Вишневский", false, caseDat, "Вишневскому"},
		{"фамилия на -ский, женская", "Вишневский", true, caseNom, "Вишневская"},
		{"фамилия на -ский, женская дательный", "Вишневский", true, caseDat, "Вишневской"},
		{"фамилия на -цкий, родительный", "Троицкий", false, caseGen, "Троицкого"},
		{"несклоняемая на -ко", "Шевченко", true, caseGen, "Шевченко"},
		{"несклоняемая на -ых", "Черных", false, caseDat, "Черных"},
		{"фамилия на -ук, мужская", "Кравчук", false, caseGen, "Кравчука"},
		{"фамилия на -ук, женская", "Кравчук", true, caseGen, "Кравчук"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := surnameForm(c.word, c.female, c.c); got != c.want {
				t.Errorf("получено %q, ожидалось %q", got, c.want)
			}
		})
	}
}

// TestNameForms — табличная проверка склонения имён и отчеств.
func TestNameForms(t *testing.T) {
	cases := []struct {
		name   string
		word   string
		female bool
		c      gcase
		want   string
	}{
		{"имя на согласную", "Иван", false, caseGen, "Ивана"},
		{"имя на согласную, дательный", "Иван", false, caseDat, "Ивану"},
		{"имя с беглой гласной", "Пётр", false, caseGen, "Петра"},
		{"имя с беглой гласной, дательный", "Павел", false, caseDat, "Павлу"},
		{"имя на -й", "Сергей", false, caseGen, "Сергея"},
		{"имя на -ий", "Дмитрий", false, caseDat, "Дмитрию"},
		{"имя на -ь", "Игорь", false, caseGen, "Игоря"},
		{"имя на -а", "Никита", false, caseGen, "Никиты"},
		{"имя на -я", "Илья", false, caseDat, "Илье"},
		{"отчество мужское", "Иванович", false, caseGen, "Ивановича"},
		{"отчество мужское, дательный", "Ильич", false, caseDat, "Ильичу"},
		{"имя женское на -а", "Анна", true, caseGen, "Анны"},
		{"имя женское после заднеязычной", "Ольга", true, caseGen, "Ольги"},
		{"имя женское на -ия", "Мария", true, caseGen, "Марии"},
		{"имя женское на -ия, дательный", "Мария", true, caseDat, "Марии"},
		{"имя женское на -ья", "Дарья", true, caseDat, "Дарье"},
		{"отчество женское", "Ивановна", true, caseGen, "Ивановны"},
		{"отчество женское, дательный", "Ильинична", true, caseDat, "Ильиничне"},
		{"именительный не меняется", "Анна", true, caseNom, "Анна"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := maleNounForm(c.word, c.c)
			if c.female {
				got = femaleNounForm(c.word, c.c)
			}
			if got != c.want {
				t.Errorf("получено %q, ожидалось %q", got, c.want)
			}
		})
	}
}

// TestTextHelpers — проверка вспомогательных преобразований формы записи.
func TestTextHelpers(t *testing.T) {
	t.Run("разрыв значения по пробелу", func(t *testing.T) {
		in := frags(lit("Паспорт: "), val(pii.TypePassport, "1234 567890"))
		out := splitLastValue(in)
		r := buildRecord("split", "test", out)
		if !strings.Contains(r.Text, "1234\n567890") {
			t.Fatalf("значение не разорвано переводом строки: %q", r.Text)
		}
		if len(r.Spans) != 2 {
			t.Fatalf("получено %d фрагментов вместо двух", len(r.Spans))
		}
		checkRecord(t, r, knownTypes())
	})
	t.Run("короткое значение не разрывается", func(t *testing.T) {
		in := frags(val(pii.TypeCVV, "123"))
		if len(splitLastValue(in)) != 1 {
			t.Error("значение из трёх знаков разорвано")
		}
	})
	t.Run("неразрывный пробел", func(t *testing.T) {
		if got := withNBSP("а б"); got != "а"+nbsp+"б" {
			t.Errorf("получено %q", got)
		}
	})
	t.Run("двойные пробелы", func(t *testing.T) {
		if got := withDoubleSpaces("а б"); got != "а  б" {
			t.Errorf("получено %q", got)
		}
	})
	t.Run("длинный дефис", func(t *testing.T) {
		if got := replaceHyphens("8-916", hyphenLong); got != "8"+hyphenLong+"916" {
			t.Errorf("получено %q", got)
		}
	})
	t.Run("первая буква в нижний регистр", func(t *testing.T) {
		if got := lowerFirst("Паспорт выдан"); got != "паспорт выдан" {
			t.Errorf("получено %q", got)
		}
	})
	t.Run("первая буква в верхний регистр", func(t *testing.T) {
		if got := upperFirst("прошу закрыть счёт"); got != "Прошу закрыть счёт" {
			t.Errorf("получено %q", got)
		}
	})
	t.Run("омоглифы меняют только похожие буквы", func(t *testing.T) {
		g := NewGenerator(1, false)
		const src = "абвгдеж"
		replaced := false
		for i := 0; i < 50; i++ {
			got := g.replaceHomoglyphs(src)
			if len([]rune(got)) != len([]rune(src)) {
				t.Fatalf("число букв изменилось: %q", got)
			}
			for k, r := range []rune(got) {
				orig := []rune(src)[k]
				if r == orig {
					continue
				}
				lat, ok := homoglyphs[orig]
				if !ok || r != lat {
					t.Fatalf("буква %q заменена на %q без латинского двойника", orig, r)
				}
				replaced = true
			}
		}
		if !replaced {
			t.Error("ни одна буква ни разу не заменена")
		}
	})
}

// TestValueShapes — табличная проверка порождаемых значений: длины, наличие
// разделителей, контрольные суммы и верные, и заведомо неверные.
func TestValueShapes(t *testing.T) {
	g := NewGenerator(99, false)
	cases := []struct {
		name  string
		check func(t *testing.T)
	}{
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
		{"почта содержит собаку и точку", func(t *testing.T) {
			v := g.email(g.person())
			if !strings.Contains(v, "@") || !strings.Contains(v, ".") {
				t.Errorf("адрес %q неверной формы", v)
			}
		}},
		{"почта в зоне рф встречается", func(t *testing.T) {
			p := g.person()
			found := false
			for i := 0; i < 200 && !found; i++ {
				found = strings.HasSuffix(g.email(p), ".рф")
			}
			if !found {
				t.Error("адрес в кириллической зоне ни разу не встретился")
			}
		}},
		{"почта с плюсом встречается", func(t *testing.T) {
			p := g.person()
			found := false
			for i := 0; i < 200 && !found; i++ {
				found = strings.Contains(g.email(p), "+")
			}
			if !found {
				t.Error("адрес с плюсом ни разу не встретился")
			}
		}},
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
			if !strings.Contains(v, "г.") && !strings.Contains(v, "обл") &&
				!strings.Contains(v, "край") && !strings.Contains(v, "республик") &&
				!strings.Contains(v, "округ") {
				t.Errorf("орган выдачи %q без географии", v)
			}
		}},
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
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.check(t) })
	}
}
