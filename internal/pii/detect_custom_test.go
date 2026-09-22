package pii

import (
	"strings"
	"sync"
	"testing"
)

// customFound — упрощённый вид найденного фрагмента для сравнения в таблице:
// текст фрагмента читается нагляднее, чем пара байтовых смещений.
type customFound struct {
	Text string
	Type Type
	Conf float64
}

// customRuleSNILS — правило из примера конфигурации: СНИЛС с контрольной суммой.
func customRuleSNILS() CustomRule {
	return CustomRule{
		Name:      TypeSNILS,
		Pattern:   `(\d{3}-\d{3}-\d{3}[ -]?\d{2})`,
		Group:     1,
		Validator: CustomValidatorSNILS,
		Anchors:   []string{"снилс", "страховой номер"},
	}
}

// customDetect прогоняет правила по тексту и приводит результат к customFound.
func customDetect(t *testing.T, rules []CustomRule, text string) []customFound {
	t.Helper()
	det, err := NewCustomDetector(rules)
	if err != nil {
		t.Fatalf("конструктор вернул ошибку: %v", err)
	}
	d := NewDoc(text)
	var out []customFound
	for _, s := range det.Detect(d) {
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			t.Fatalf("некорректные границы фрагмента: %+v", s)
		}
		out = append(out, customFound{Text: text[s.Start:s.End], Type: s.Type, Conf: s.Conf})
	}
	return out
}

// TestCustomDetectorTable проверяет поведение правил из настройки: форму,
// якоря, группы, контрольные суммы и отрицательные случаи.
func TestCustomDetectorTable(t *testing.T) {
	snils := customRuleSNILS()

	requireAnchor := CustomRule{
		Name:          Type("STAFF_ID"),
		Pattern:       `\d{6}`,
		Anchors:       []string{"табельный", "пропуск"},
		RequireAnchor: true,
	}
	groupRule := CustomRule{
		Name:    Type("ORDER_PIN"),
		Pattern: `номер:\s*(\d{4})`,
		Group:   1,
	}
	wholeMatchRule := CustomRule{
		Name:    Type("ORDER_PIN"),
		Pattern: `номер:\s*(\d{4})`,
	}
	cardRule := CustomRule{
		Name:      Type("LOYALTY_CARD"),
		Pattern:   `\d{16}`,
		Validator: CustomValidatorLuhn,
	}
	innRule := CustomRule{
		Name:      Type("PARTNER_INN"),
		Pattern:   `\d{10}`,
		Validator: CustomValidatorINN,
		Anchors:   []string{"инн"},
	}
	noneRule := CustomRule{
		Name:      Type("ACCOUNT_CODE"),
		Pattern:   `\d{5}`,
		Validator: CustomValidatorNone,
		Anchors:   []string{"код"},
	}
	emptyValidatorRule := CustomRule{
		Name:    Type("ACCOUNT_CODE"),
		Pattern: `\d{5}`,
		Anchors: []string{"код"},
	}
	latinRule := CustomRule{
		Name:    Type("CONTRACT_ID"),
		Pattern: `[a-z]{2}\d{5}`,
	}
	cyrRule := CustomRule{
		Name:    Type("SERIES"),
		Pattern: `серия\s+[а-яё]{2}`,
	}
	trailingPunctRule := CustomRule{
		Name:    Type("ACCOUNT_CODE"),
		Pattern: `\d{4},`,
	}
	multilineRule := CustomRule{
		Name:    Type("ACCOUNT_CODE"),
		Pattern: `ключ\s+\d{4}`,
	}
	emptyGroupRule := CustomRule{
		Name:    Type("ACCOUNT_CODE"),
		Pattern: `(\d{3})(x*)`,
		Group:   2,
	}
	narrowWindowRule := CustomRule{
		Name:         Type("ACCOUNT_CODE"),
		Pattern:      `\d{11}`,
		Anchors:      []string{"снилс"},
		AnchorWindow: 5,
	}
	wideWindowRule := CustomRule{
		Name:    Type("ACCOUNT_CODE"),
		Pattern: `\d{11}`,
		Anchors: []string{"снилс"},
	}
	runeWindowRule := CustomRule{
		Name:          Type("STAFF_ID"),
		Pattern:       `\d{6}`,
		Anchors:       []string{"пропуск"},
		RequireAnchor: true,
		AnchorWindow:  20,
	}
	anchoredStartRule := CustomRule{
		Name:    Type("ACCOUNT_CODE"),
		Pattern: `^\d{4}`,
	}

	cases := []struct {
		name  string
		rules []CustomRule
		text  string
		want  []customFound
	}{
		{
			name:  "снилс с якорем и верной контрольной суммой",
			rules: []CustomRule{snils},
			text:  "СНИЛС 112-233-445 95 выдан",
			want:  []customFound{{"112-233-445 95", TypeSNILS, ConfHigh}},
		},
		{
			name:  "снилс без якоря, контрольная сумма сходится",
			rules: []CustomRule{snils},
			text:  "значение 112-233-445 95 в анкете",
			want:  []customFound{{"112-233-445 95", TypeSNILS, ConfAnchored}},
		},
		{
			name:  "снилс с якорем, контрольная сумма не сходится",
			rules: []CustomRule{snils},
			text:  "СНИЛС 112-233-445 96",
			want:  []customFound{{"112-233-445 96", TypeSNILS, ConfAnchored}},
		},
		{
			name:  "снилс без якоря и без контрольной суммы",
			rules: []CustomRule{snils},
			text:  "в поле указано 112-233-445 96",
			want:  []customFound{{"112-233-445 96", TypeSNILS, ConfMedium}},
		},
		{
			name:  "снилс через дефис в последней группе",
			rules: []CustomRule{snils},
			text:  "снилс 112-233-445-95",
			want:  []customFound{{"112-233-445-95", TypeSNILS, ConfHigh}},
		},
		{
			name:  "снилс слитно в последней группе",
			rules: []CustomRule{snils},
			text:  "Страховой номер 112-233-44595",
			want:  []customFound{{"112-233-44595", TypeSNILS, ConfHigh}},
		},
		{
			name:  "якорь стоит после значения",
			rules: []CustomRule{snils},
			text:  "112-233-445 95 (снилс)",
			want:  []customFound{{"112-233-445 95", TypeSNILS, ConfHigh}},
		},
		{
			name:  "снилс без разделителей выражению не подходит",
			rules: []CustomRule{snils},
			text:  "снилс 11223344595",
			want:  nil,
		},
		{
			name:  "обязательный якорь есть",
			rules: []CustomRule{requireAnchor},
			text:  "Табельный номер 123456",
			want:  []customFound{{"123456", Type("STAFF_ID"), ConfAnchored}},
		},
		{
			name:  "обязательный якорь есть, регистр другой",
			rules: []CustomRule{requireAnchor},
			text:  "ТАБЕЛЬНЫЙ 123456",
			want:  []customFound{{"123456", Type("STAFF_ID"), ConfAnchored}},
		},
		{
			name:  "обязательного якоря нет, фрагмент не возвращается",
			rules: []CustomRule{requireAnchor},
			text:  "Заказ 123456 оплачен",
			want:  nil,
		},
		{
			name:  "границы берутся по указанной группе",
			rules: []CustomRule{groupRule},
			text:  "Номер: 1234 в заявке",
			want:  []customFound{{"1234", Type("ORDER_PIN"), ConfMedium}},
		},
		{
			name:  "без группы берётся всё совпадение",
			rules: []CustomRule{wholeMatchRule},
			text:  "Номер: 1234 в заявке",
			want:  []customFound{{"Номер: 1234", Type("ORDER_PIN"), ConfMedium}},
		},
		{
			name:  "группа совпала с пустой строкой, фрагмент пропущен",
			rules: []CustomRule{emptyGroupRule},
			text:  "код 123 дальше",
			want:  nil,
		},
		{
			name:  "валидатор luhn сошёлся",
			rules: []CustomRule{cardRule},
			text:  "4111111111111111",
			want:  []customFound{{"4111111111111111", Type("LOYALTY_CARD"), ConfAnchored}},
		},
		{
			name:  "валидатор luhn не сошёлся, фрагмент всё равно возвращается",
			rules: []CustomRule{cardRule},
			text:  "4111111111111112",
			want:  []customFound{{"4111111111111112", Type("LOYALTY_CARD"), ConfMedium}},
		},
		{
			name:  "валидатор inn и якорь",
			rules: []CustomRule{innRule},
			text:  "ИНН 7707083893",
			want:  []customFound{{"7707083893", Type("PARTNER_INN"), ConfHigh}},
		},
		{
			name:  "валидатор inn не сошёлся, но якорь есть",
			rules: []CustomRule{innRule},
			text:  "ИНН 7707083890",
			want:  []customFound{{"7707083890", Type("PARTNER_INN"), ConfAnchored}},
		},
		{
			name:  "валидатор inn не сошёлся и якоря нет",
			rules: []CustomRule{innRule},
			text:  "значение 7707083890",
			want:  []customFound{{"7707083890", Type("PARTNER_INN"), ConfMedium}},
		},
		{
			name:  "валидатор none с якорем",
			rules: []CustomRule{noneRule},
			text:  "код 12345",
			want:  []customFound{{"12345", Type("ACCOUNT_CODE"), ConfAnchored}},
		},
		{
			name:  "пустой валидатор работает как none",
			rules: []CustomRule{emptyValidatorRule},
			text:  "код 12345",
			want:  []customFound{{"12345", Type("ACCOUNT_CODE"), ConfAnchored}},
		},
		{
			name:  "валидатор none без якоря",
			rules: []CustomRule{noneRule},
			text:  "остаток 12345",
			want:  []customFound{{"12345", Type("ACCOUNT_CODE"), ConfMedium}},
		},
		{
			name:  "латиница в верхнем регистре",
			rules: []CustomRule{latinRule},
			text:  "договор AB12345 подписан",
			want:  []customFound{{"AB12345", Type("CONTRACT_ID"), ConfMedium}},
		},
		{
			name:  "кириллица в верхнем регистре",
			rules: []CustomRule{cyrRule},
			text:  "Серия АБ выдана",
			want:  []customFound{{"Серия АБ", Type("SERIES"), ConfMedium}},
		},
		{
			name:  "пунктуация по краям обрезается",
			rules: []CustomRule{trailingPunctRule},
			text:  "значения 1234, 5678",
			want:  []customFound{{"1234", Type("ACCOUNT_CODE"), ConfMedium}},
		},
		{
			name:  "фрагмент не пересекает перевод строки",
			rules: []CustomRule{multilineRule},
			text:  "ключ\n1234",
			want:  nil,
		},
		{
			name:  "узкое окно якоря в рунах якорь не достаёт",
			rules: []CustomRule{narrowWindowRule},
			text:  "снилс это значение 11223344595",
			want:  []customFound{{"11223344595", Type("ACCOUNT_CODE"), ConfMedium}},
		},
		{
			name:  "окно по умолчанию якорь достаёт",
			rules: []CustomRule{wideWindowRule},
			text:  "снилс это значение 11223344595",
			want:  []customFound{{"11223344595", Type("ACCOUNT_CODE"), ConfAnchored}},
		},
		{
			name:  "окно считается в рунах, а не в байтах",
			rules: []CustomRule{runeWindowRule},
			text:  "пропуск сотрудника 123456",
			want:  []customFound{{"123456", Type("STAFF_ID"), ConfAnchored}},
		},
		{
			name:  "выражение с началом строки берёт только первое число",
			rules: []CustomRule{anchoredStartRule},
			text:  "1234 5678",
			want:  []customFound{{"1234", Type("ACCOUNT_CODE"), ConfMedium}},
		},
		{
			name:  "несколько совпадений одного правила",
			rules: []CustomRule{latinRule},
			text:  "ab12345 и cd67890",
			want: []customFound{
				{"ab12345", Type("CONTRACT_ID"), ConfMedium},
				{"cd67890", Type("CONTRACT_ID"), ConfMedium},
			},
		},
		{
			name:  "два правила находят разные типы",
			rules: []CustomRule{snils, latinRule},
			text:  "снилс 112-233-445 95 и ab12345",
			want: []customFound{
				{"112-233-445 95", TypeSNILS, ConfHigh},
				{"ab12345", Type("CONTRACT_ID"), ConfMedium},
			},
		},
		{
			name:  "правил нет, фрагментов нет",
			rules: nil,
			text:  "снилс 112-233-445 95",
			want:  nil,
		},
		{
			name:  "текст без совпадений",
			rules: []CustomRule{snils, requireAnchor, latinRule},
			text:  "обычный текст без чисел",
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := customDetect(t, tc.rules, tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("получено %d фрагментов %+v, ожидалось %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("фрагмент %d: получено %+v, ожидалось %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestNewCustomDetectorErrors проверяет, что неверная настройка отклоняется
// при создании детектора, а сообщение называет правило.
func TestNewCustomDetectorErrors(t *testing.T) {
	cases := []struct {
		name string
		rule CustomRule
		want string
	}{
		{
			name: "опережающая проверка",
			rule: CustomRule{Name: Type("A"), Pattern: `(?=\d)\d{4}`},
			want: "(?=",
		},
		{
			name: "отрицательная опережающая проверка",
			rule: CustomRule{Name: Type("B"), Pattern: `(?!\d)[а-яё]{4}`},
			want: "(?!",
		},
		{
			name: "ретроспективная проверка",
			rule: CustomRule{Name: Type("C"), Pattern: `(?<=инн )\d{10}`},
			want: "(?<=",
		},
		{
			name: "обратная ссылка",
			rule: CustomRule{Name: Type("D"), Pattern: `(\d)\1{3}`},
			want: `\1`,
		},
		{
			name: "выражение совпадает с пустой строкой",
			rule: CustomRule{Name: Type("E"), Pattern: `\d*`},
			want: "пустой строкой",
		},
		{
			name: "необязательная группа совпадает с пустой строкой",
			rule: CustomRule{Name: Type("F"), Pattern: `(abc)?`},
			want: "пустой строкой",
		},
		{
			name: "выражение не разбирается",
			rule: CustomRule{Name: Type("G"), Pattern: `\d{3}(`},
			want: "не удалось разобрать",
		},
		{
			name: "пустое выражение",
			rule: CustomRule{Name: Type("H"), Pattern: "   "},
			want: "не задано выражение",
		},
		{
			name: "нет имени типа",
			rule: CustomRule{Pattern: `\d{4}`},
			want: "не задано имя типа",
		},
		{
			name: "неизвестный валидатор",
			rule: CustomRule{Name: Type("J"), Pattern: `\d{4}`, Validator: "crc32"},
			want: "неизвестный валидатор",
		},
		{
			name: "группы с таким номером нет",
			rule: CustomRule{Name: Type("K"), Pattern: `(\d{3})`, Group: 5},
			want: "группа 5",
		},
		{
			name: "отрицательный номер группы",
			rule: CustomRule{Name: Type("L"), Pattern: `(\d{3})`, Group: -1},
			want: "отрицательный",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			det, err := NewCustomDetector([]CustomRule{tc.rule})
			if err == nil {
				t.Fatalf("ожидалась ошибка, получен детектор %v", det)
			}
			if det != nil {
				t.Errorf("при ошибке детектор должен быть пустым, получено %v", det)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("сообщение %q не содержит %q", err.Error(), tc.want)
			}
			if tc.rule.Name != "" && !strings.Contains(err.Error(), string(tc.rule.Name)) {
				t.Errorf("сообщение %q не называет правило %q", err.Error(), tc.rule.Name)
			}
		})
	}
}

// TestCustomDetectorRuleOrderIndependent проверяет, что порядок правил в
// настройке не меняет результат: оператор дописывает правила в конец файла и
// не должен получать другой ответ.
func TestCustomDetectorRuleOrderIndependent(t *testing.T) {
	first := customRuleSNILS()
	second := CustomRule{
		Name:      Type("PARTNER_INN"),
		Pattern:   `\d{10}`,
		Validator: CustomValidatorINN,
		Anchors:   []string{"инн"},
	}
	third := CustomRule{
		Name:    Type("CONTRACT_ID"),
		Pattern: `[a-z]{2}\d{5}`,
	}
	text := "СНИЛС 112-233-445 95, ИНН 7707083893, договор ab12345"

	direct := customDetect(t, []CustomRule{first, second, third}, text)
	reverse := customDetect(t, []CustomRule{third, second, first}, text)
	mixed := customDetect(t, []CustomRule{second, first, third}, text)

	if len(direct) != 3 {
		t.Fatalf("ожидалось 3 фрагмента, получено %+v", direct)
	}
	for i := range direct {
		if direct[i] != reverse[i] || direct[i] != mixed[i] {
			t.Fatalf("порядок правил повлиял на результат: %+v, %+v, %+v", direct, reverse, mixed)
		}
	}
}

// TestCustomDetectorSameTypeTwiceOrder проверяет устойчивость порядка, когда
// два правила описывают один тип и совпадают на одном и том же месте.
func TestCustomDetectorSameTypeTwiceOrder(t *testing.T) {
	wide := CustomRule{Name: Type("ACCOUNT_CODE"), Pattern: `\d{6}`}
	narrow := CustomRule{Name: Type("ACCOUNT_CODE"), Pattern: `\d{3}`}
	text := "код 123456"

	direct := customDetect(t, []CustomRule{wide, narrow}, text)
	reverse := customDetect(t, []CustomRule{narrow, wide}, text)

	if len(direct) != len(reverse) || len(direct) == 0 {
		t.Fatalf("разное число фрагментов: %+v и %+v", direct, reverse)
	}
	for i := range direct {
		if direct[i] != reverse[i] {
			t.Fatalf("порядок правил повлиял на результат: %+v и %+v", direct, reverse)
		}
	}
	if direct[0].Text != "123456" {
		t.Errorf("первым должен идти самый длинный фрагмент, получено %+v", direct)
	}
}

// TestCustomDetectorTypes проверяет перечень типов без повторов.
func TestCustomDetectorTypes(t *testing.T) {
	det, err := NewCustomDetector([]CustomRule{
		{Name: Type("A"), Pattern: `\d{4}`},
		{Name: Type("B"), Pattern: `\d{5}`},
		{Name: Type("A"), Pattern: `\d{6}`},
	})
	if err != nil {
		t.Fatalf("конструктор вернул ошибку: %v", err)
	}
	types := det.Types()
	if len(types) != 2 || types[0] != Type("A") || types[1] != Type("B") {
		t.Fatalf("ожидались типы [A B], получено %v", types)
	}
}

// TestCustomDetectorMatchLimit проверяет защиту от разрушительной настройки:
// выражение из одной цифры не должно выдавать фрагмент на каждый байт текста.
func TestCustomDetectorMatchLimit(t *testing.T) {
	det, err := NewCustomDetector([]CustomRule{{Name: Type("DIGIT"), Pattern: `\d`}})
	if err != nil {
		t.Fatalf("конструктор вернул ошибку: %v", err)
	}
	spans := det.Detect(NewDoc(strings.Repeat("7", customSpanLimit*2)))
	if len(spans) != customSpanLimit {
		t.Fatalf("ожидалось %d фрагментов, получено %d", customSpanLimit, len(spans))
	}
}

// TestCustomDetectorParallel проверяет, что один детектор безопасно
// обслуживает параллельные запросы: собственного изменяемого состояния у него
// нет, а Doc у каждого запроса свой.
func TestCustomDetectorParallel(t *testing.T) {
	det, err := NewCustomDetector([]CustomRule{customRuleSNILS()})
	if err != nil {
		t.Fatalf("конструктор вернул ошибку: %v", err)
	}
	text := "СНИЛС 112-233-445 95"
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spans := det.Detect(NewDoc(text))
			if len(spans) != 1 || spans[0].Conf != ConfHigh {
				t.Errorf("неожиданный результат параллельного запуска: %+v", spans)
			}
		}()
	}
	wg.Wait()
}
