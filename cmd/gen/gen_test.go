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
	first := NewGenerator(42, false).Generate(120)
	second := NewGenerator(42, false).Generate(120)
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

// checkRecord проверяет одну запись: границы в пределах текста, фрагмент
// совпадает с подставленным значением, фрагменты не пересекаются и не
// захватывают перевод строки.
func checkRecord(t *testing.T, r Record, types map[string]bool) {
	t.Helper()
	if !utf8.ValidString(r.Text) {
		t.Fatalf("%s: текст не является корректным UTF-8", r.ID)
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
		if i < len(r.Values) && got != r.Values[i] {
			t.Fatalf("%s: фрагмент %d указывает на %q вместо подставленного %q", r.ID, i, got, r.Values[i])
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Fatalf("%s: фрагмент %d пересекает перевод строки: %q", r.ID, i, got)
		}
		if strings.TrimSpace(got) != got {
			t.Fatalf("%s: фрагмент %d захватил пробелы по краям: %q", r.ID, i, got)
		}
	}
	if len(r.Spans) != len(r.Values) {
		t.Fatalf("%s: %d фрагментов при %d значениях", r.ID, len(r.Spans), len(r.Values))
	}
}

// TestSpansPointToValues проверяет разметку всего набора: смещения байтовые и
// указывают ровно на подставленное значение.
func TestSpansPointToValues(t *testing.T) {
	types := knownTypes()
	for _, r := range NewGenerator(7, false).Generate(600) {
		checkRecord(t, r, types)
	}
}

// TestNegativeCategoriesEmpty проверяет, что отрицательные категории не несут
// разметки: по ним измеряются ложные срабатывания.
func TestNegativeCategoriesEmpty(t *testing.T) {
	seen := make(map[string]int)
	for _, r := range NewGenerator(11, false).Generate(800) {
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

// TestPlanCoversAllCategories проверяет, что в выпуске есть каждая категория,
// включая длинные тексты и категорию со сложными предложениями.
func TestPlanCoversAllCategories(t *testing.T) {
	counts := planCounts(8000)
	total := 0
	for i, c := range allCategories() {
		if counts[i] == 0 {
			t.Errorf("категория %s не попала в выпуск", c.name)
		}
		total += counts[i]
	}
	if total != 8000 {
		t.Errorf("сумма по категориям %d вместо 8000", total)
	}
	if got := negativeTotal(8000); got < minNegativeRecords {
		t.Errorf("отрицательных строк %d, требуется не меньше %d", got, minNegativeRecords)
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
	defer f.Close()

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
			_, number := g.cardNumber(true)
			if !pii.Luhn(pii.DigitsOnly(number)) {
				t.Errorf("номер %q не проходит алгоритм Луна", number)
			}
		}},
		{"испорченный номер карты не проходит алгоритм Луна", func(t *testing.T) {
			_, number := g.cardNumber(false)
			if pii.Luhn(pii.DigitsOnly(number)) {
				t.Errorf("номер %q неожиданно верен", number)
			}
		}},
		{"номер карты содержит шестнадцать цифр", func(t *testing.T) {
			_, number := g.cardNumber(true)
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
			if v := g.birthCert(); !strings.Contains(v, "-") || !strings.Contains(v, "№") {
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
		{"восемь форматов даты различаются", func(t *testing.T) {
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
			if !strings.Contains(v, "г.") && !strings.Contains(v, "обл") && !strings.Contains(v, "край") && !strings.Contains(v, "республик") {
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
