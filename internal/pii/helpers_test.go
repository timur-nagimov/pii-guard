package pii

import (
	"strings"
	"testing"
)

// TestLuhn проверяет контрольную сумму номеров карт. Суммы нужны движку как
// усилитель уверенности, поэтому важно, чтобы верные номера не отбраковывались,
// а изменение одной цифры ломало сумму.
func TestLuhn(t *testing.T) {
	cases := []struct {
		name   string
		digits string
		want   bool
	}{
		{"visa 16 знаков", "4111111111111111", true},
		{"visa с испорченной последней цифрой", "4111111111111112", false},
		{"mastercard", "5555555555554444", true},
		{"amex 15 знаков", "378282246310005", true},
		{"visa тестовая", "4012888888881881", true},
		{"discover", "6011111111111117", true},
		{"все нули 16 знаков", "0000000000000000", true},
		{"произвольные 16 знаков без суммы", "5469380011223456", false},
		{"12 знаков без суммы", "411111111111", false},
		{"слишком коротко", "4111111111", false},
		{"слишком длинно", "41111111111111111111", false},
		{"пустая строка", "", false},
		{"буква внутри", "41111111111111a1", false},
		{"пробел внутри", "4111 1111 1111 1111", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Luhn(c.digits); got != c.want {
				t.Fatalf("Luhn(%q) = %v, ожидалось %v", c.digits, got, c.want)
			}
		})
	}
}

// TestINNValid проверяет контрольные суммы ИНН обеих длин. Десять знаков
// принадлежат организации, двенадцать человеку, и схемы проверки у них разные.
func TestINNValid(t *testing.T) {
	cases := []struct {
		name   string
		digits string
		want   bool
	}{
		{"десять знаков, верная сумма", "7707083893", true},
		{"десять знаков, испорченная сумма", "7707083894", false},
		{"десять знаков без суммы", "1234567890", false},
		{"двенадцать знаков, верная сумма", "500100732259", true},
		{"двенадцать знаков, испорченная сумма", "500100732258", false},
		{"двенадцать знаков, другой верный номер", "772816897110", true},
		{"двенадцать знаков без суммы", "123456789012", false},
		{"девять знаков", "770708389", false},
		{"одиннадцать знаков", "77070838930", false},
		{"пустая строка", "", false},
		{"буква внутри", "77070838a3", false},
		{"разделители внутри", "7707-083893", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := INNValid(c.digits); got != c.want {
				t.Fatalf("INNValid(%q) = %v, ожидалось %v", c.digits, got, c.want)
			}
		})
	}
}

// TestSNILSValid проверяет контрольную сумму СНИЛС. Сумма считается только по
// первым девяти знакам, последние два знака являются самой суммой.
func TestSNILSValid(t *testing.T) {
	cases := []struct {
		name   string
		digits string
		want   bool
	}{
		{"классический пример", "11223344595", true},
		{"испорченная сумма", "11223344596", false},
		{"все нули", "00000000000", true},
		{"произвольные знаки", "12345678901", false},
		{"десять знаков", "1122334459", false},
		{"двенадцать знаков", "112233445950", false},
		{"пустая строка", "", false},
		{"буква внутри", "1122334459a", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SNILSValid(c.digits); got != c.want {
				t.Fatalf("SNILSValid(%q) = %v, ожидалось %v", c.digits, got, c.want)
			}
		})
	}
}

// TestDigitsOnly проверяет вытаскивание цифр из записи с разделителями.
func TestDigitsOnly(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"+7 (916) 123-45-67", "79161234567"},
		{"4509 123456", "4509123456"},
		{"112-233-445 95", "11223344595"},
		{"без цифр", ""},
		{"", ""},
		{"Иванов 1985", "1985"},
		{"a1b2c3", "123"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := DigitsOnly(c.in); got != c.want {
				t.Fatalf("DigitsOnly(%q) = %q, ожидалось %q", c.in, got, c.want)
			}
		})
	}
}

// TestNormalizeSpan проверяет обрезку краёв фрагмента. Лишний изменённый байт
// за границей настоящего фрагмента сбивает выравнивание соседних фрагментов,
// поэтому пунктуация и пробелы по краям обязаны отрезаться.
func TestNormalizeSpan(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"пробелы по краям", "   4509 123456   ", "4509 123456"},
		{"скобки и точка", "(4509 123456).", "4509 123456"},
		{"кавычки ёлочки", "«Иванов Иван»", "Иванов Иван"},
		{"запятая и точка с запятой", ",ivan@example.com;", "ivan@example.com"},
		{"номерной знак и решётка", "№#770-001#№", "770-001"},
		{"перевод строки по краям", "\n4509123456\n", "4509123456"},
		{"внутренние разделители не трогаем", "4509 123456", "4509 123456"},
		{"только пунктуация", "...", ""},
		{"только пробелы", "   ", ""},
		{"пустая строка", "", ""},
		{"кириллица без краевого мусора", "Иванов", "Иванов"},
		{"длинное тире слева", "—123456—", "123456"},
		{"плюс не обрезается", "+79161234567", "+79161234567"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end := NormalizeSpan(c.text, 0, len(c.text))
			if start > end {
				t.Fatalf("NormalizeSpan вернула перевёрнутые границы %d:%d", start, end)
			}
			if got := c.text[start:end]; got != c.want {
				t.Fatalf("NormalizeSpan(%q) = %q, ожидалось %q", c.text, got, c.want)
			}
		})
	}
}

// TestNormalizeSpanClampsBounds проверяет, что выход за границы текста не
// приводит к панике: границы приводятся к допустимым.
func TestNormalizeSpanClampsBounds(t *testing.T) {
	text := "  4509  "
	start, end := NormalizeSpan(text, -10, len(text)+10)
	if text[start:end] != "4509" {
		t.Fatalf("получено %q, ожидалось %q", text[start:end], "4509")
	}
}

// digitRunCase — случай табличного теста числовых кандидатов. На один текст
// их может прийтись несколько, поэтому ожидание задано параллельными
// списками: i-й кандидат сверяется с i-м элементом каждого из них.
type digitRunCase struct {
	name     string
	text     string
	digits   []string
	patterns []string
	hasPlus  []bool
}

// checkDigitRun сверяет одного числового кандидата. Границы проверяются у
// каждого: по ним форматные детекторы режут исходный текст, и выход за его
// пределы уронил бы разбор целиком, а не испортил один фрагмент.
func checkDigitRun(t *testing.T, c digitRunCase, i int, r NumRun) {
	t.Helper()
	if r.Digits != c.digits[i] {
		t.Errorf("последовательность %d: цифры %q, ожидались %q", i, r.Digits, c.digits[i])
	}
	if got := r.GroupsPattern(); got != c.patterns[i] {
		t.Errorf("последовательность %d: форма %q, ожидалась %q", i, got, c.patterns[i])
	}
	if r.HasPlus != c.hasPlus[i] {
		t.Errorf("последовательность %d: признак плюса %v, ожидался %v", i, r.HasPlus, c.hasPlus[i])
	}
	if r.Start < 0 || r.End > len(c.text) || r.Start >= r.End {
		t.Errorf("последовательность %d: испорченные границы %d:%d", i, r.Start, r.End)
	}
}

// TestDigitRuns проверяет разбор текста на числовых кандидатов. Именно на этом
// разборе стоят все форматные детекторы, поэтому важны и склейка групп через
// допустимые разделители, и отказ от склейки через запятую и перевод строки.
func TestDigitRuns(t *testing.T) {
	cases := []digitRunCase{
		{
			name:     "телефон со скобками и дефисами",
			text:     "+7 (916) 123-45-67",
			digits:   []string{"79161234567"},
			patterns: []string{"1-3-3-2-2"},
			hasPlus:  []bool{true},
		},
		{
			name:     "паспорт серия и номер через пробел",
			text:     "4509 123456",
			digits:   []string{"4509123456"},
			patterns: []string{"4-6"},
			hasPlus:  []bool{false},
		},
		{
			name:     "паспорт серия из двух пар",
			text:     "45 09 123456",
			digits:   []string{"4509123456"},
			patterns: []string{"2-2-6"},
			hasPlus:  []bool{false},
		},
		{
			name:     "цифра приклеена к буквам, две отдельные последовательности",
			text:     "CVC2 987",
			digits:   []string{"2", "987"},
			patterns: []string{"1", "3"},
			hasPlus:  []bool{false, false},
		},
		{
			name:     "дата через точки",
			text:     "12.03.1985",
			digits:   []string{"12031985"},
			patterns: []string{"2-2-4"},
			hasPlus:  []bool{false},
		},
		{
			name:     "код подразделения через дефис",
			text:     "770-001",
			digits:   []string{"770001"},
			patterns: []string{"3-3"},
			hasPlus:  []bool{false},
		},
		{
			name:     "числа через запятую не склеиваются",
			text:     "сумма 100, 200 рублей",
			digits:   []string{"100", "200"},
			patterns: []string{"3", "3"},
			hasPlus:  []bool{false, false},
		},
		{
			name:     "перевод строки не склеивает числа",
			text:     "первый 123\nвторой 456",
			digits:   []string{"123", "456"},
			patterns: []string{"3", "3"},
			hasPlus:  []bool{false, false},
		},
		{
			name:     "карта четырьмя группами",
			text:     "4276 1600 1234 5678",
			digits:   []string{"4276160012345678"},
			patterns: []string{"4-4-4-4"},
			hasPlus:  []bool{false},
		},
		{
			name:     "карта слитно",
			text:     "4276160012345678",
			digits:   []string{"4276160012345678"},
			patterns: []string{"16"},
			hasPlus:  []bool{false},
		},
		{
			name:     "телефон с восьмёркой через дефисы",
			text:     "8-916-123-45-67",
			digits:   []string{"89161234567"},
			patterns: []string{"1-3-3-2-2"},
			hasPlus:  []bool{false},
		},
		{
			name:     "текст без цифр",
			text:     "совсем без чисел",
			digits:   nil,
			patterns: nil,
			hasPlus:  nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runs := NewDoc(c.text).NumRuns()
			if len(runs) != len(c.digits) {
				t.Fatalf("найдено %d последовательностей, ожидалось %d: %+v", len(runs), len(c.digits), runs)
			}
			for i, r := range runs {
				checkDigitRun(t, c, i, r)
			}
		})
	}
}

// TestDigitRunsCached проверяет, что числовые кандидаты считаются один раз:
// документ живёт в пределах запроса и все детекторы получают один и тот же срез.
func TestDigitRunsCached(t *testing.T) {
	d := NewDoc("паспорт 4509 123456")
	first := d.NumRuns()
	second := d.NumRuns()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("ожидалась одна последовательность, получено %d и %d", len(first), len(second))
	}
	if &first[0] != &second[0] {
		t.Fatal("NumRuns пересчитала кандидатов вместо того, чтобы вернуть сохранённых")
	}
}

// TestAnchorBefore проверяет поиск якоря слева от значения. Правило про цифры
// между якорем и значением защищает от того, чтобы якорь из одной части
// предложения помечал число из другой его части.
func TestAnchorBefore(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		value    string
		anchors  []string
		maxRunes int
		want     string
		wantOK   bool
	}{
		{
			name:     "выбирается более длинный якорь",
			text:     "cvc2 987",
			value:    "987",
			anchors:  []string{"cvc", "cvc2"},
			maxRunes: 24,
			want:     "cvc2",
			wantOK:   true,
		},
		{
			name:     "короткий якорь оставляет цифру между собой и значением",
			text:     "cvc2 987",
			value:    "987",
			anchors:  []string{"cvc"},
			maxRunes: 24,
			wantOK:   false,
		},
		{
			name:     "чужая цифра между якорем и значением",
			text:     "инн 12 7707083893",
			value:    "7707083893",
			anchors:  []string{"инн"},
			maxRunes: 24,
			wantOK:   false,
		},
		{
			name:     "якорь вплотную к значению",
			text:     "индекс 190000",
			value:    "190000",
			anchors:  []string{"индекс"},
			maxRunes: 24,
			want:     "индекс",
			wantOK:   true,
		},
		{
			name:     "якорь в верхнем регистре ищется по нижнему",
			text:     "ИНДЕКС 190000",
			value:    "190000",
			anchors:  []string{"индекс"},
			maxRunes: 24,
			want:     "индекс",
			wantOK:   true,
		},
		{
			name:     "якорь за пределами окна",
			text:     "индекс, о котором мы говорили в прошлом письме, равен 190000",
			value:    "190000",
			anchors:  []string{"индекс"},
			maxRunes: 10,
			wantOK:   false,
		},
		{
			name:     "якоря нет вовсе",
			text:     "просто число 190000",
			value:    "190000",
			anchors:  []string{"индекс"},
			maxRunes: 24,
			wantOK:   false,
		},
		{
			name:     "якорь справа не считается",
			text:     "190000 индекс",
			value:    "190000",
			anchors:  []string{"индекс"},
			maxRunes: 24,
			wantOK:   false,
		},
		{
			name:     "пустой список якорей",
			text:     "индекс 190000",
			value:    "190000",
			anchors:  []string{""},
			maxRunes: 24,
			wantOK:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := NewDoc(c.text)
			at := strings.Index(c.text, c.value)
			if at < 0 {
				t.Fatalf("значение %q не найдено в тексте", c.value)
			}
			got, ok := d.AnchorBefore(at, c.anchors, c.maxRunes)
			if ok != c.wantOK {
				t.Fatalf("AnchorBefore вернула признак %v, ожидался %v (якорь %q)", ok, c.wantOK, got)
			}
			if ok && got != c.want {
				t.Fatalf("AnchorBefore нашла якорь %q, ожидался %q", got, c.want)
			}
		})
	}
}

// TestAnchorBeforeWindowInRunes проверяет, что окно поиска якоря считается в
// рунах. Для кириллицы байтовое окно вдвое короче задуманного, и якорь
// перестаёт доставать до значения.
func TestAnchorBeforeWindowInRunes(t *testing.T) {
	// Между якорем и значением ровно двенадцать кириллических рун, то есть
	// двадцать четыре байта.
	text := "индекс города такой 190000"
	d := NewDoc(text)
	at := strings.Index(text, "190000")
	if _, ok := d.AnchorBefore(at, []string{"индекс"}, 26); !ok {
		t.Fatal("якорь не найден в окне из двадцати шести рун: окно считается в байтах")
	}
	if _, ok := d.AnchorBefore(at, []string{"индекс"}, 5); ok {
		t.Fatal("якорь найден в окне из пяти рун, хотя он стоит дальше")
	}
}

// TestContainsAnyLower проверяет поиск первого совпавшего слова из списка.
func TestContainsAnyLower(t *testing.T) {
	cases := []struct {
		name     string
		haystack string
		needles  []string
		want     string
		wantOK   bool
	}{
		{"первое совпадение", "сумма заказа", []string{"сумма", "заказ"}, "сумма", true},
		{"совпадение во втором слове", "номер заказа", []string{"сумма", "заказ"}, "заказ", true},
		{"нет совпадений", "просто текст", []string{"сумма", "заказ"}, "", false},
		{"пустые слова пропускаются", "текст", []string{"", "текст"}, "текст", true},
		{"пустой список", "текст", nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ContainsAnyLower(c.haystack, c.needles)
			if ok != c.wantOK || got != c.want {
				t.Fatalf("ContainsAnyLower = (%q, %v), ожидалось (%q, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestGroupsPattern проверяет строковое описание формы числовой записи.
func TestGroupsPattern(t *testing.T) {
	cases := []struct {
		groups []int
		want   string
	}{
		{[]int{4, 6}, "4-6"},
		{[]int{2, 2, 6}, "2-2-6"},
		{[]int{16}, "16"},
		{[]int{1, 3, 3, 2, 2}, "1-3-3-2-2"},
		{nil, ""},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := (NumRun{Groups: c.groups}).GroupsPattern(); got != c.want {
				t.Fatalf("GroupsPattern = %q, ожидалось %q", got, c.want)
			}
		})
	}
}
