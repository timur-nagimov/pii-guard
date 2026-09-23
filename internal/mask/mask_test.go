package mask

import (
	"strings"
	"testing"
	"unicode/utf8"

	"pii-guard/internal/pii"
)

// applyWhole накладывает пресет на весь текст целиком. Так проверяется именно
// публичный путь Apply, а не внутренняя функция маскирования.
func applyWhole(text string, preset Preset, t pii.Type) string {
	spans := []pii.Span{{Start: 0, End: len(text), Type: t, Conf: pii.ConfHigh}}
	return Apply(text, spans, Options{Default: preset}).Text
}

// TestPresetsOnTypicalValues проверяет каждый пресет на характерном значении
// каждого типа. Формат маски выбирает участник, поэтому он зафиксирован тестом:
// расхождение с описанием в документации сразу видно.
func TestPresetsOnTypicalValues(t *testing.T) {
	cases := []struct {
		name   string
		typ    pii.Type
		value  string
		preset Preset
		want   string
	}{
		{"имя целиком", pii.TypeFIO, "Иванов Иван Иванович", PresetFull, "********************"},
		{"имя с пробелами", pii.TypeFIO, "Иванов Иван Иванович", PresetFullWS, "****** **** ********"},
		{"имя частично", pii.TypeFIO, "Иванов Иван Иванович", PresetPartial, "Ив**** **** ******ич"},
		{"имя инициалами", pii.TypeFIO, "Иванов Иван Иванович", PresetInitials, "И. И. И."},
		{"имя плейсхолдером", pii.TypeFIO, "Иванов Иван Иванович", PresetToken, "[FIO_1]"},

		{"телефон целиком", pii.TypePhone, "+7 (916) 123-45-67", PresetFull, "******************"},
		{"телефон с пробелами", pii.TypePhone, "+7 (916) 123-45-67", PresetFullWS, "** ***** *********"},
		{"телефон частично", pii.TypePhone, "+7 (916) 123-45-67", PresetPartial, "+7 (9**) ***-**-67"},
		{"телефон плейсхолдером", pii.TypePhone, "+7 (916) 123-45-67", PresetToken, "[PHONE_1]"},

		{"карта целиком", pii.TypeCard, "4111 1111 1111 1111", PresetFull, "*******************"},
		{"карта с пробелами", pii.TypeCard, "4111 1111 1111 1111", PresetFullWS, "**** **** **** ****"},
		{"карта частично", pii.TypeCard, "4111 1111 1111 1111", PresetPartial, "41** **** **** **11"},
		{"карта плейсхолдером", pii.TypeCard, "4111 1111 1111 1111", PresetToken, "[CARD_1]"},

		{"почта целиком", pii.TypeEmail, "ivan@example.com", PresetFull, "****************"},
		{"почта частично", pii.TypeEmail, "ivan@example.com", PresetPartial, "iv**@*******.*om"},
		{"почта инициалами", pii.TypeEmail, "ivan@example.com", PresetInitials, "I. E. C."},
		{"почта плейсхолдером", pii.TypeEmail, "ivan@example.com", PresetToken, "[EMAIL_1]"},

		{"паспорт целиком", pii.TypePassport, "4509 123456", PresetFull, "***********"},
		{"паспорт с пробелами", pii.TypePassport, "4509 123456", PresetFullWS, "**** ******"},
		{"паспорт частично", pii.TypePassport, "4509 123456", PresetPartial, "45** ****56"},
		{"паспорт плейсхолдером", pii.TypePassport, "4509 123456", PresetToken, "[PASSPORT_1]"},

		{"код безопасности целиком", pii.TypeCVV, "123", PresetFull, "***"},
		{"короткое значение частично маскируется целиком", pii.TypeCVV, "123", PresetPartial, "***"},
		{"код безопасности плейсхолдером", pii.TypeCVV, "123", PresetToken, "[CVV_1]"},

		{"адрес целиком", pii.TypeAddress, "г. Москва, ул. Ленина, д. 1, кв. 2", PresetFull, "**********************************"},
		{"адрес с пробелами", pii.TypeAddress, "г. Москва, ул. Ленина, д. 1, кв. 2", PresetFullWS, "** ******* *** ******* ** ** *** *"},
		{"адрес частично", pii.TypeAddress, "г. Москва, ул. Ленина, д. 1, кв. 2", PresetPartial, "г. М*****, **. ******, *. *, *в. 2"},

		{"снилс целиком", pii.TypeSNILS, "112-233-445 95", PresetFull, "**************"},
		{"снилс частично", pii.TypeSNILS, "112-233-445 95", PresetPartial, "11*-***-*** 95"},
		{"одна буква", pii.TypeFIO, "И", PresetFull, "*"},
		{"одна буква инициалом", pii.TypeFIO, "И", PresetInitials, "И."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := applyWhole(c.value, c.preset, c.typ); got != c.want {
				t.Fatalf("маска %q, ожидалась %q", got, c.want)
			}
		})
	}
}

// TestLengthPreservingPresets проверяет, что сохраняющие длину пресеты не
// меняют числа рун. Сдвиг границ портит выравнивание соседних фрагментов и
// обнуляет оценку проверяющей системы.
func TestLengthPreservingPresets(t *testing.T) {
	values := []string{
		"Иванов Иван Иванович",
		"+7 (916) 123-45-67",
		"4111 1111 1111 1111",
		"ivan@example.com",
		"4509 123456",
		"123",
		"г. Москва, ул. Ленина, д. 1, кв. 2",
		"112-233-445 95",
		"И",
	}
	presets := []Preset{PresetFull, PresetFullWS, PresetPartial}
	for _, p := range presets {
		if !p.PreservesLength() {
			t.Fatalf("пресет %q должен сохранять длину", p)
		}
		for _, v := range values {
			t.Run(string(p)+"/"+v, func(t *testing.T) {
				got := applyWhole(v, p, pii.TypeFIO)
				if utf8.RuneCountInString(got) != utf8.RuneCountInString(v) {
					t.Fatalf("маска %q содержит %d рун, исходное значение %d", got, utf8.RuneCountInString(got), utf8.RuneCountInString(v))
				}
			})
		}
	}
}

// TestInitialsDoesNotPreserveLength проверяет, что инициалы длину не сохраняют.
// Именно поэтому валидатор настроек запрещает их для системы без ключа.
func TestInitialsDoesNotPreserveLength(t *testing.T) {
	if PresetInitials.PreservesLength() {
		t.Fatal("инициалы не сохраняют длину, признак должен быть отрицательным")
	}
	value := "Иванов Иван Иванович"
	got := applyWhole(value, PresetInitials, pii.TypeFIO)
	if utf8.RuneCountInString(got) == utf8.RuneCountInString(value) {
		t.Fatalf("маска %q неожиданно совпала по длине с исходным значением", got)
	}
}

// TestPresetValidAndPreservesLength проверяет признаки пресетов.
func TestPresetValidAndPreservesLength(t *testing.T) {
	cases := []struct {
		preset    Preset
		valid     bool
		preserves bool
	}{
		{PresetFull, true, true},
		{PresetFullWS, true, true},
		{PresetPartial, true, true},
		{PresetInitials, true, false},
		{PresetToken, true, false},
		{PresetSynthetic, true, false},
		{Preset(""), false, false},
		{Preset("неизвестный"), false, false},
		{Preset("FULL"), false, false},
	}
	for _, c := range cases {
		t.Run(string(c.preset), func(t *testing.T) {
			if got := c.preset.Valid(); got != c.valid {
				t.Errorf("Valid = %v, ожидалось %v", got, c.valid)
			}
			if got := c.preset.PreservesLength(); got != c.preserves {
				t.Errorf("PreservesLength = %v, ожидалось %v", got, c.preserves)
			}
		})
	}
}

// TestPresetFor проверяет выбор пресета с учётом переопределений по типам.
func TestPresetFor(t *testing.T) {
	opts := Options{
		Default: PresetFull,
		PerType: map[pii.Type]Preset{
			pii.TypeFIO:   PresetInitials,
			pii.TypeEmail: Preset("мусор"),
		},
	}
	if got := opts.PresetFor(pii.TypeFIO); got != PresetInitials {
		t.Errorf("переопределение по типу не применилось: %q", got)
	}
	if got := opts.PresetFor(pii.TypePhone); got != PresetFull {
		t.Errorf("пресет по умолчанию не применился: %q", got)
	}
	if got := opts.PresetFor(pii.TypeEmail); got != PresetFull {
		t.Errorf("негодное переопределение должно откатываться к умолчанию, получено %q", got)
	}
	empty := Options{}
	if got := empty.PresetFor(pii.TypeFIO); got != PresetFull {
		t.Errorf("без настроек должен применяться полный пресет, получено %q", got)
	}
}

// TestTokenNumbering проверяет, что одинаковые значения получают один и тот же
// плейсхолдер. Иначе обратное преобразование ответа языковой модели невозможно.
func TestTokenNumbering(t *testing.T) {
	text := "Иванов звонил Петрову, потом Иванов перезвонил. Почта ivan@example.com"
	spans := []pii.Span{
		spanOf(text, "Иванов", pii.TypeFIO, 0),
		spanOf(text, "Петров", pii.TypeFIO, 0),
		spanOf(text, "Иванов", pii.TypeFIO, 1),
		spanOf(text, "ivan@example.com", pii.TypeEmail, 0),
	}
	res := Apply(text, spans, Options{Default: PresetToken})

	if strings.Count(res.Text, "[FIO_1]") != 2 {
		t.Fatalf("повтор значения получил разные плейсхолдеры: %q", res.Text)
	}
	if !strings.Contains(res.Text, "[FIO_2]") {
		t.Fatalf("разные значения получили один плейсхолдер: %q", res.Text)
	}
	if !strings.Contains(res.Text, "[EMAIL_1]") {
		t.Fatalf("счётчик плейсхолдеров не ведётся по типам: %q", res.Text)
	}
	if len(res.Placeholders) != 3 {
		t.Fatalf("сохранено %d подстановок, ожидалось 3: %+v", len(res.Placeholders), res.Placeholders)
	}
	want := map[string]string{"[FIO_1]": "Иванов", "[FIO_2]": "Петров", "[EMAIL_1]": "ivan@example.com"}
	for _, p := range res.Placeholders {
		if want[p.Token] != p.Value {
			t.Errorf("подстановка %q указывает на %q, ожидалось %q", p.Token, p.Value, want[p.Token])
		}
	}
}

// TestTokenSameValueDifferentTypes проверяет, что счётчики плейсхолдеров у
// разных типов независимы, а одинаковая строка разных типов не склеивается.
func TestTokenSameValueDifferentTypes(t *testing.T) {
	text := "123456 и 123456"
	spans := []pii.Span{
		spanOf(text, "123456", pii.TypePostcode, 0),
		spanOf(text, "123456", pii.TypeCVV, 1),
	}
	res := Apply(text, spans, Options{Default: PresetToken})
	if res.Text != "[POSTCODE_1] и [CVV_1]" {
		t.Fatalf("получено %q", res.Text)
	}
}

// TestApplyKeepsBytesOutsideSpans проверяет, что за пределами фрагментов не
// меняется ни один байт. Метрика проверяющей системы считается внутри эталонных
// фрагментов, но сдвиг соседнего текста ломает их выравнивание.
func TestApplyKeepsBytesOutsideSpans(t *testing.T) {
	// Текст латиницей: при полном маскировании длина в байтах не меняется,
	// поэтому можно сравнивать побайтово.
	text := "call 89161234567 or write ivan@example.com now"
	spans := []pii.Span{
		spanOf(text, "89161234567", pii.TypePhone, 0),
		spanOf(text, "ivan@example.com", pii.TypeEmail, 0),
	}
	got := Apply(text, spans, Options{Default: PresetFull}).Text
	if len(got) != len(text) {
		t.Fatalf("длина изменилась: %d байт вместо %d", len(got), len(text))
	}
	inside := func(i int) bool {
		for _, s := range spans {
			if i >= s.Start && i < s.End {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(text); i++ {
		if inside(i) {
			if got[i] != '*' {
				t.Fatalf("байт %d внутри фрагмента не замаскирован: %q", i, got[i])
			}
			continue
		}
		if got[i] != text[i] {
			t.Fatalf("байт %d вне фрагмента изменён: %q вместо %q", i, got[i], text[i])
		}
	}
}

// TestApplyKeepsSurroundingText проверяет сохранение окружающего текста на
// кириллице, где число байт при маскировании меняется.
func TestApplyKeepsSurroundingText(t *testing.T) {
	text := "Клиент Иванов Иван, телефон 89161234567, спасибо"
	spans := []pii.Span{
		spanOf(text, "Иванов Иван", pii.TypeFIO, 0),
		spanOf(text, "89161234567", pii.TypePhone, 0),
	}
	got := Apply(text, spans, Options{Default: PresetFull}).Text
	want := "Клиент ***********, телефон ***********, спасибо"
	if got != want {
		t.Fatalf("получено %q, ожидалось %q", got, want)
	}
}

// TestApplyEdgeCases проверяет поведение на негодных наборах фрагментов.
// Такие наборы не должны приводить ни к панике, ни к потере текста.
func TestApplyEdgeCases(t *testing.T) {
	text := "телефон 89161234567 здесь"
	phone := spanOf(text, "89161234567", pii.TypePhone, 0)
	inner := pii.Span{Start: phone.Start + 2, End: phone.Start + 7, Type: pii.TypeCVV, Conf: pii.ConfHigh}
	cases := []struct {
		name  string
		spans []pii.Span
		want  string
	}{
		{"нет фрагментов", nil, text},
		{"пустой фрагмент пропускается", []pii.Span{{Start: 5, End: 5, Type: pii.TypePhone}}, text},
		{"перевёрнутый фрагмент пропускается", []pii.Span{{Start: 10, End: 4, Type: pii.TypePhone}}, text},
		{"фрагмент за пределами текста пропускается", []pii.Span{{Start: 5, End: len(text) + 10, Type: pii.TypePhone}}, text},
		{
			name:  "пересекающийся фрагмент пропускается",
			spans: []pii.Span{phone, inner},
			want:  "телефон *********** здесь",
		},
		{
			name:  "весь текст",
			spans: []pii.Span{{Start: 0, End: len(text), Type: pii.TypePhone, Conf: pii.ConfHigh}},
			want:  strings.Repeat("*", len([]rune(text))),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Apply(text, c.spans, Options{Default: PresetFull}).Text
			if got != c.want {
				t.Fatalf("получено %q, ожидалось %q", got, c.want)
			}
		})
	}
}

// TestApplyEmptyText проверяет пустой текст.
func TestApplyEmptyText(t *testing.T) {
	res := Apply("", nil, Options{Default: PresetFull})
	if res.Text != "" || len(res.Placeholders) != 0 {
		t.Fatalf("получено %q и %d подстановок", res.Text, len(res.Placeholders))
	}
}

// spanOf строит фрагмент по номеру вхождения значения в текст. Нумерация
// вхождений начинается с нуля.
func spanOf(text, value string, t pii.Type, occurrence int) pii.Span {
	from := 0
	for i := 0; i <= occurrence; i++ {
		idx := strings.Index(text[from:], value)
		if idx < 0 {
			panic("значение не найдено в тексте: " + value)
		}
		from += idx
		if i < occurrence {
			from += len(value)
		}
	}
	return pii.Span{Start: from, End: from + len(value), Type: t, Conf: pii.ConfHigh}
}
