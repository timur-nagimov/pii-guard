package mask

import (
	"strings"
	"testing"

	"pii-guard/internal/pii"
)

// TestSyntheticSubstitutesEachType проверяет, что подстановка каждого типа
// отличается от исходного значения и сохраняет форму: число слов, разделители
// и код страны.
func TestSyntheticSubstitutesEachType(t *testing.T) {
	cases := []struct {
		name  string
		typ   pii.Type
		value string
	}{
		{"полное имя", pii.TypeFIO, "Иванов Иван Иванович"},
		{"женское имя", pii.TypeFIO, "Петрова Мария Сергеевна"},
		{"имя с инициалами", pii.TypeFIO, "Иванов И. И."},
		{"латинское имя", pii.TypeFIO, "IVAN IVANOV"},
		{"держатель карты", pii.TypeCardHolder, "IVAN IVANOV"},
		{"телефон с кодом страны", pii.TypePhone, "+7 (916) 123-45-67"},
		{"телефон с восьмёркой", pii.TypePhone, "89161234567"},
		{"почта", pii.TypeEmail, "ivanov@mail.ru"},
		{"карта", pii.TypeCard, "4111 1111 1111 1111"},
		{"инн", pii.TypeINN, "500100732259"},
		{"снилс", pii.TypeSNILS, "112-233-445 95"},
		{"дата рождения", pii.TypeDOB, "12.03.1985"},
		{"дата выдачи", pii.TypeIssueDate, "12.03.2005"},
		{"адрес", pii.TypeAddress, "г. Москва, ул. Ленина, д. 5, кв. 12"},
		{"место рождения", pii.TypeBirthPlace, "г. Ленинград"},
		{"индекс", pii.TypePostcode, "101000"},
		{"гражданство", pii.TypeCitizenship, "Россия"},
		{"код подразделения", pii.TypeDeptCode, "770-001"},
		{"водительское удостоверение", pii.TypeDriverLicense, "77 АА 123456"},
		{"код безопасности", pii.TypeCVV, "123"},
		{"пин-код", pii.TypePIN, "4321"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := applyWhole(c.value, PresetSynthetic, c.typ)
			if got == c.value {
				t.Fatalf("подстановка %q совпала с исходным значением", got)
			}
			if strings.Contains(got, "*") {
				t.Fatalf("подстановка %q содержит звёздочки", got)
			}
		})
	}
}

// TestSyntheticPreservesForm проверяет, что подстановка сохраняет форму:
// число слов у имени, разделители и код страны у телефона, домен у почты.
func TestSyntheticPreservesForm(t *testing.T) {
	// Имя: три слова остаются тремя словами.
	fio := applyWhole("Иванов Иван Иванович", PresetSynthetic, pii.TypeFIO)
	if got := len(strings.Fields(fio)); got != 3 {
		t.Fatalf("имя %q содержит %d слов, ожидалось 3", fio, got)
	}
	// Инициалы остаются инициалами.
	init := applyWhole("Иванов И. И.", PresetSynthetic, pii.TypeFIO)
	if !strings.Contains(init, ".") {
		t.Fatalf("инициалы %q потеряли точки", init)
	}
	// Телефон сохраняет код страны и разделители.
	phone := applyWhole("+7 (916) 123-45-67", PresetSynthetic, pii.TypePhone)
	if !strings.HasPrefix(phone, "+7 (") || !strings.Contains(phone, ")") || !strings.Contains(phone, "-") {
		t.Fatalf("телефон %q потерял форму", phone)
	}
	// Почта остаётся на домене example.com.
	email := applyWhole("ivanov@mail.ru", PresetSynthetic, pii.TypeEmail)
	if !strings.HasSuffix(email, "@example.com") {
		t.Fatalf("почта %q не на домене example.com", email)
	}
}

// TestSyntheticChecksums проверяет контрольные суммы подстановок карты, ИНН и
// СНИЛС.
func TestSyntheticChecksums(t *testing.T) {
	card := applyWhole("4111 1111 1111 1111", PresetSynthetic, pii.TypeCard)
	if !pii.Luhn(digitsOnly(card)) {
		t.Fatalf("карта %q не проходит проверку Луна", card)
	}
	if !strings.HasPrefix(digitsOnly(card), "4111") && !strings.HasPrefix(digitsOnly(card), "5555") {
		t.Fatalf("карта %q не начинается с тестового префикса", card)
	}
	inn := applyWhole("500100732259", PresetSynthetic, pii.TypeINN)
	if !pii.INNValid(digitsOnly(inn)) {
		t.Fatalf("инн %q не проходит контрольную сумму", inn)
	}
	snils := applyWhole("112-233-445 95", PresetSynthetic, pii.TypeSNILS)
	if !pii.SNILSValid(digitsOnly(snils)) {
		t.Fatalf("снилс %q не проходит контрольную сумму", snils)
	}
}

// TestSyntheticDeterministic проверяет, что одно и то же значение в одном
// запросе всегда получает одну и ту же подстановку, а разные значения — разные.
func TestSyntheticDeterministic(t *testing.T) {
	text := "Иванов звонил Петрову, потом Иванов перезвонил. Телефон +7 (916) 123-45-67"
	spans := []pii.Span{
		spanOf(text, "Иванов", pii.TypeFIO, 0),
		spanOf(text, "Петров", pii.TypeFIO, 0),
		spanOf(text, "Иванов", pii.TypeFIO, 1),
		spanOf(text, "+7 (916) 123-45-67", pii.TypePhone, 0),
	}
	res := Apply(text, spans, Options{Default: PresetSynthetic})
	// Повтор значения «Иванов» заменяется одинаково.
	if strings.Count(res.Text, "Иванов") != 0 {
		t.Fatalf("значение не заменено: %q", res.Text)
	}
	// Два разных имени не должны получить одну подстановку.
	first := res.Placeholders[0].Token
	second := res.Placeholders[1].Token
	if first == second {
		t.Fatalf("разные значения получили одну подстановку %q", first)
	}
	// Повторный прогон даёт тот же результат.
	again := Apply(text, spans, Options{Default: PresetSynthetic})
	if again.Text != res.Text {
		t.Fatalf("повторный прогон дал другой текст: %q вместо %q", again.Text, res.Text)
	}
}

// TestSyntheticPlaceholders проверяет, что подстановки попадают в Placeholders
// и позволяют восстановить исходное значение.
func TestSyntheticPlaceholders(t *testing.T) {
	text := "Клиент Иванов Иван Иванович, телефон +7 (916) 123-45-67"
	spans := []pii.Span{
		spanOf(text, "Иванов Иван Иванович", pii.TypeFIO, 0),
		spanOf(text, "+7 (916) 123-45-67", pii.TypePhone, 0),
	}
	res := Apply(text, spans, Options{Default: PresetSynthetic})
	if len(res.Placeholders) != 2 {
		t.Fatalf("сохранено %d подстановок, ожидалось 2", len(res.Placeholders))
	}
	back := make(map[string]string, len(res.Placeholders))
	for _, ph := range res.Placeholders {
		back[ph.Token] = ph.Value
	}
	restored := replaceAll(res.Text, back)
	if restored != text {
		t.Fatalf("восстановление дало %q, ожидалось %q", restored, text)
	}
}

// TestSyntheticRestoreDeclinedFIO проверяет, что восстановление работает, когда
// модель просклоняла подстановку имени: родительный и дательный падежи.
func TestSyntheticRestoreDeclinedFIO(t *testing.T) {
	text := "Клиент Иванов Иван Иванович"
	spans := []pii.Span{spanOf(text, "Иванов Иван Иванович", pii.TypeFIO, 0)}
	res := Apply(text, spans, Options{Default: PresetSynthetic})
	sub := res.Placeholders[0].Token
	back := map[string]string{sub: res.Placeholders[0].Value}

	// Модель просклоняла подстановку в родительном падеже.
	genitive := "Клиент " + declineGenitive(sub)
	if got := replaceAll(genitive, back); got != text {
		t.Fatalf("родительный падеж: восстановление дало %q, ожидалось %q", got, text)
	}
	// Модель просклоняла подстановку в дательном падеже.
	dative := "Клиент " + declineDative(sub)
	if got := replaceAll(dative, back); got != text {
		t.Fatalf("дательный падеж: восстановление дало %q, ожидалось %q", got, text)
	}
}

// replaceAll заменяет в тексте каждую подстановку на исходное значение, включая
// склонённые формы подстановки.
func replaceAll(text string, back map[string]string) string {
	out := text
	for token, value := range back {
		out = strings.ReplaceAll(out, token, value)
		for _, declined := range DeclineVariants(token) {
			out = strings.ReplaceAll(out, declined, value)
		}
	}
	return out
}

// declineGenitive склоняет имя в родительный падеж: «Иванов Иван Иванович» →
// «Иванова Ивана Ивановича».
func declineGenitive(name string) string {
	words := strings.Fields(name)
	if len(words) < 3 {
		return name
	}
	return words[0] + "а " + words[1] + "а " + words[2] + "а"
}

// declineDative склоняет имя в дательный падеж: «Иванов Иван Иванович» →
// «Иванову Ивану Ивановичу».
func declineDative(name string) string {
	words := strings.Fields(name)
	if len(words) < 3 {
		return name
	}
	return words[0] + "у " + words[1] + "у " + words[2] + "у"
}

// TestSyntheticNoCollision проверяет, что разные значения в одном запросе не
// получают одну и ту же подстановку.
func TestSyntheticNoCollision(t *testing.T) {
	text := "Иванов Иван Иванович и Петров Пётр Петрович и Сидоров Сидор Сидорович"
	spans := []pii.Span{
		spanOf(text, "Иванов Иван Иванович", pii.TypeFIO, 0),
		spanOf(text, "Петров Пётр Петрович", pii.TypeFIO, 0),
		spanOf(text, "Сидоров Сидор Сидорович", pii.TypeFIO, 0),
	}
	res := Apply(text, spans, Options{Default: PresetSynthetic})
	seen := make(map[string]bool)
	for _, ph := range res.Placeholders {
		if seen[ph.Token] {
			t.Fatalf("подстановка %q повторяется для разных значений", ph.Token)
		}
		seen[ph.Token] = true
	}
}

// TestSyntheticPreservesLengthFlag проверяет, что synthetic длину не сохраняет.
func TestSyntheticPreservesLengthFlag(t *testing.T) {
	if PresetSynthetic.PreservesLength() {
		t.Fatal("synthetic не сохраняет длину, признак должен быть отрицательным")
	}
}
