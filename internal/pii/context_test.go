package pii

import (
	"strings"
	"testing"
)

// ctxExtra — дополнительный фрагмент, который кладётся в текст рядом с
// проверяемым: он изображает работу других детекторов, но от них не зависит.
type ctxExtra struct {
	value string
	typ   Type
}

// ctxCase — один случай табличного теста контекстного фильтра.
type ctxCase struct {
	name        string
	text        string
	value       string
	typ         Type
	extra       []ctxExtra
	opts        ContextOptions
	wantDropped bool
	wantReason  string
}

// ctxDefaultOpts — настройки, при которых включены оба контекстных правила.
func ctxDefaultOpts() ContextOptions {
	return ContextOptions{PublicFigures: true, OrgAddresses: true}
}

// ctxSpanFor собирает фрагмент по подстроке исходного текста.
func ctxSpanFor(t *testing.T, text, value string, typ Type) Span {
	t.Helper()
	idx := strings.Index(text, value)
	if idx < 0 {
		t.Fatalf("подстрока %q не найдена в тексте %q", value, text)
	}
	return Span{Start: idx, End: idx + len(value), Type: typ, Conf: ConfAnchored}
}

func TestContextFilterApply(t *testing.T) {
	cases := []ctxCase{
		{
			name:        "поэт с известным именем снимается",
			text:        "Поэт Александр Пушкин родился в Москве.",
			value:       "Александр Пушкин",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:  "тёзка в банковском контексте маскируется",
			text:  "Клиент Пушкин Александр Сергеевич, паспорт 4509 123456.",
			value: "Пушкин Александр Сергеевич",
			typ:   TypeFIO,
			extra: []ctxExtra{{value: "4509 123456", typ: TypePassport}},
			opts:  ctxDefaultOpts(),
		},
		{
			name:  "держатель карты с известной фамилией маскируется",
			text:  "Держатель карты Пушкин Александр Сергеевич.",
			value: "Пушкин Александр Сергеевич",
			typ:   TypeFIO,
			opts:  ctxDefaultOpts(),
		},
		{
			name:  "ролевой признак не спасает при банковском якоре",
			text:  "Поэт Александр Пушкин подал заявку на кредит.",
			value: "Александр Пушкин",
			typ:   TypeFIO,
			opts:  ctxDefaultOpts(),
		},
		{
			name:        "полное имя из словаря без роли снимается",
			text:        "Александр Пушкин упомянут в школьной программе.",
			value:       "Александр Пушкин",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:  "одна известная фамилия без роли остаётся",
			text:  "Гагарина ждали в аэропорту весь вечер.",
			value: "Гагарина",
			typ:   TypeFIO,
			opts:  ctxDefaultOpts(),
		},
		{
			name:        "одна известная фамилия с ролью снимается",
			text:        "Космонавта Гагарина встречали в Москве.",
			value:       "Гагарина",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:        "падежная форма после слова памятник снимается",
			text:        "Памятник Пушкину стоит в центре города.",
			value:       "Пушкину",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:        "годы жизни в скобках считаются ролевым признаком",
			text:        "Чайковский (1840-1893) известен всему миру.",
			value:       "Чайковский",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:        "верхний регистр не мешает правилу",
			text:        "ПОЭТ АЛЕКСАНДР ПУШКИН РОДИЛСЯ В МОСКВЕ",
			value:       "АЛЕКСАНДР ПУШКИН",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:        "буква ё в роли не мешает правилу",
			text:        "Актёр Олег Янковский снялся в фильме.",
			value:       "Олег Янковский",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:        "роль без словаря снимается в чистом абзаце",
			text:        "Композитор Иван Петров написал симфонию.",
			value:       "Иван Петров",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:  "обычное имя без роли и словаря остаётся",
			text:  "Вчера Иван Петров зашёл к соседу.",
			value: "Иван Петров",
			typ:   TypeFIO,
			opts:  ctxDefaultOpts(),
		},
		{
			name:  "слово поэтому не является ролевым признаком",
			text:  "Поэтому Сергей Никитенко подошёл позже.",
			value: "Сергей Никитенко",
			typ:   TypeFIO,
			opts:  ctxDefaultOpts(),
		},
		{
			name:  "другой персональный фрагмент в абзаце сохраняет имя",
			text:  "Поэт Александр Пушкин, телефон +7 916 123-45-67.",
			value: "Александр Пушкин",
			typ:   TypeFIO,
			extra: []ctxExtra{{value: "+7 916 123-45-67", typ: TypePhone}},
			opts:  ctxDefaultOpts(),
		},
		{
			name:  "имя из другого абзаца не считается контекстом",
			text:  "Поэт Александр Пушкин родился в Москве.\n\nКлиент Смирнов, паспорт 4509 123456.",
			value: "Александр Пушкин",
			typ:   TypeFIO,
			extra: []ctxExtra{{value: "4509 123456", typ: TypePassport}},
			opts:  ctxDefaultOpts(),
			// Банковский контекст лежит в соседнем абзаце и правилу не мешает.
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:        "слово картина не является банковским якорем",
			text:        "Картина Пушкина висит в музее уже сто лет.",
			value:       "Пушкина",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonPublicFigure,
		},
		{
			name:  "правило известных людей выключено настройкой",
			text:  "Поэт Александр Пушкин родился в Москве.",
			value: "Александр Пушкин",
			typ:   TypeFIO,
			opts:  ContextOptions{OrgAddresses: true},
		},
		{
			name:        "улица Пушкина не персональные данные",
			text:        "Доставка на улицу Пушкина, дом 5.",
			value:       "Пушкина",
			typ:         TypeFIO,
			opts:        ContextOptions{},
			wantDropped: true,
			wantReason:  reasonStreetName,
		},
		{
			name:        "сокращение ул. тоже считается адресным словом",
			text:        "Адрес: ул. Лермонтова, д. 12, кв. 3.",
			value:       "Лермонтова",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonStreetName,
		},
		{
			name:        "проспект перед фамилией снимает фрагмент",
			text:        "Встреча на проспекте Гагарина возле дома 7.",
			value:       "Гагарина",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonStreetName,
		},
		{
			name:        "сокращение пр-т перед фамилией снимает фрагмент",
			text:        "Офис на пр-т Королева, 14.",
			value:       "Королева",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonStreetName,
		},
		{
			name:        "станция метро с фамилией снимается",
			text:        "Выход со станции Маяковская направо.",
			value:       "Маяковская",
			typ:         TypeFIO,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonStreetName,
		},
		{
			name:        "слово имени перед фамилией снимает фрагмент",
			text:        "Театр имени Вахтангова закрыт на ремонт.",
			value:       "Вахтангова",
			typ:         TypeFIO,
			opts:        ContextOptions{},
			wantDropped: true,
			wantReason:  reasonStreetName,
		},
		{
			name:  "адресное слово далеко слева не снимает фрагмент",
			text:  "Улица была пустая, потому что все разошлись, а Соколов Пётр ждал.",
			value: "Соколов Пётр",
			typ:   TypeFIO,
			opts:  ctxDefaultOpts(),
		},
		{
			name:        "адрес отделения снимается",
			text:        "Отделение банка: г. Москва, ул. Тверская, д. 1.",
			value:       "г. Москва, ул. Тверская, д. 1",
			typ:         TypeAddress,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonOrgAddress,
		},
		{
			name:        "юридический адрес снимается",
			text:        "Юридический адрес: г. Казань, ул. Кремлёвская, д. 10.",
			value:       "г. Казань, ул. Кремлёвская, д. 10",
			typ:         TypeAddress,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonOrgAddress,
		},
		{
			name:        "организационная форма считается признаком организации",
			text:        "ПАО Сбербанк, г. Москва, ул. Вавилова, д. 19.",
			value:       "г. Москва, ул. Вавилова, д. 19",
			typ:         TypeAddress,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonOrgAddress,
		},
		{
			name:        "филиал слева от адреса снимает его",
			text:        "Филиал в Твери: г. Тверь, ул. Мира, д. 4.",
			value:       "г. Тверь, ул. Мира, д. 4",
			typ:         TypeAddress,
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonOrgAddress,
		},
		{
			name:  "адрес клиента рядом с адресом отделения остаётся",
			text:  "Адрес клиента: г. Казань, ул. Баумана, д. 3. Адрес отделения: г. Казань, ул. Кремлёвская, д. 10.",
			value: "г. Казань, ул. Баумана, д. 3",
			typ:   TypeAddress,
			extra: []ctxExtra{{value: "г. Казань, ул. Кремлёвская, д. 10", typ: TypeAddress}},
			opts:  ctxDefaultOpts(),
		},
		{
			name:        "адрес отделения рядом с адресом клиента снимается",
			text:        "Адрес клиента: г. Казань, ул. Баумана, д. 3. Адрес отделения: г. Казань, ул. Кремлёвская, д. 10.",
			value:       "г. Казань, ул. Кремлёвская, д. 10",
			typ:         TypeAddress,
			extra:       []ctxExtra{{value: "г. Казань, ул. Баумана, д. 3", typ: TypeAddress}},
			opts:        ctxDefaultOpts(),
			wantDropped: true,
			wantReason:  reasonOrgAddress,
		},
		{
			name:  "далёкий признак организации адрес не трогает",
			text:  "Ближайшее отделение работает до восьми вечера, а посылку мы доставим по любому удобному вам адресу в пределах города, например: г. Тула, ул. Ленина, д. 3.",
			value: "г. Тула, ул. Ленина, д. 3",
			typ:   TypeAddress,
			opts:  ctxDefaultOpts(),
		},
		{
			name:  "адрес клиента без признака организации остаётся",
			text:  "Адрес регистрации: г. Тула, ул. Ленина, д. 3, кв. 7.",
			value: "г. Тула, ул. Ленина, д. 3, кв. 7",
			typ:   TypeAddress,
			opts:  ctxDefaultOpts(),
		},
		{
			name:  "правило адресов организаций выключено настройкой",
			text:  "Отделение банка: г. Москва, ул. Тверская, д. 1.",
			value: "г. Москва, ул. Тверская, д. 1",
			typ:   TypeAddress,
			opts:  ContextOptions{PublicFigures: true},
		},
		{
			name:        "телефон из списка разрешённых снимается",
			text:        "Звоните на горячую линию 8 800 100-10-10.",
			value:       "8 800 100-10-10",
			typ:         TypePhone,
			opts:        ContextOptions{AllowValues: []string{"8 800 100-10-10"}},
			wantDropped: true,
			wantReason:  reasonAllowList,
		},
		{
			name:        "почта банка из списка разрешённых снимается",
			text:        "Пишите на help@bank.ru в любое время.",
			value:       "help@bank.ru",
			typ:         TypeEmail,
			opts:        ContextOptions{AllowValues: []string{"HELP@BANK.RU"}},
			wantDropped: true,
			wantReason:  reasonAllowList,
		},
		{
			name:        "имя из списка разрешённых снимается",
			text:        "Заявку принял Пётр Семёнов, менеджер офиса.",
			value:       "Пётр Семёнов",
			typ:         TypeFIO,
			opts:        ContextOptions{AllowPersons: []string{"Петр   Семенов"}},
			wantDropped: true,
			wantReason:  reasonAllowList,
		},
		{
			name:        "адрес из списка разрешённых снимается",
			text:        "Головной офис расположен по адресу г. Москва, ул. Каланчёвская, д. 27.",
			value:       "г. Москва, ул. Каланчёвская, д. 27",
			typ:         TypeAddress,
			opts:        ContextOptions{AllowAddresses: []string{"г. москва, ул. каланчевская, д. 27"}},
			wantDropped: true,
			wantReason:  reasonAllowList,
		},
		{
			name:  "похожее, но не совпадающее значение не снимается",
			text:  "Звоните на номер 8 800 100-10-11.",
			value: "8 800 100-10-11",
			typ:   TypePhone,
			opts:  ContextOptions{AllowValues: []string{"8 800 100-10-10"}},
		},
		{
			name:  "телефон не снимается правилом известных людей",
			text:  "Поэт Александр Пушкин, телефон +7 916 123-45-67.",
			value: "+7 916 123-45-67",
			typ:   TypePhone,
			opts:  ctxDefaultOpts(),
		},
		{
			name:  "паспорт рядом с известным именем не снимается",
			text:  "Поэт Александр Пушкин, паспорт 4509 123456.",
			value: "4509 123456",
			typ:   TypePassport,
			opts:  ctxDefaultOpts(),
		},
		{
			name:  "адрес с улицей имени человека остаётся адресом",
			text:  "Доставка: г. Москва, ул. Пушкина, д. 5.",
			value: "г. Москва, ул. Пушкина, д. 5",
			typ:   TypeAddress,
			opts:  ctxDefaultOpts(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDoc(tc.text)
			spans := []Span{ctxSpanFor(t, tc.text, tc.value, tc.typ)}
			for _, e := range tc.extra {
				spans = append(spans, ctxSpanFor(t, tc.text, e.value, e.typ))
			}
			target := spans[0]
			kept, dropped := NewContextFilter(tc.opts).Apply(d, spans)
			got, reason := ctxLookup(target, kept, dropped)
			if got != tc.wantDropped {
				t.Fatalf("фрагмент %q: снят=%v, ожидалось снят=%v (причина %q)",
					tc.value, got, tc.wantDropped, reason)
			}
			if tc.wantDropped && reason != tc.wantReason {
				t.Fatalf("фрагмент %q: причина %q, ожидалась %q", tc.value, reason, tc.wantReason)
			}
		})
	}
}

// ctxLookup находит проверяемый фрагмент среди оставленных и снятых.
func ctxLookup(target Span, kept, dropped []Span) (bool, string) {
	for _, s := range dropped {
		if s.Start == target.Start && s.End == target.End && s.Type == target.Type {
			return true, s.Reason
		}
	}
	for _, s := range kept {
		if s.Start == target.Start && s.End == target.End && s.Type == target.Type {
			return false, s.Reason
		}
	}
	return false, "фрагмент потерян"
}

// TestContextFilterKeepsInputUntouched проверяет, что фильтр не меняет входной
// срез: движок отдаёт его дальше в журнал и метрики.
func TestContextFilterKeepsInputUntouched(t *testing.T) {
	text := "Поэт Александр Пушкин родился в Москве."
	d := NewDoc(text)
	spans := []Span{ctxSpanFor(t, text, "Александр Пушкин", TypeFIO)}
	spans[0].Reason = "fio:anchor"
	kept, dropped := NewContextFilter(ctxDefaultOpts()).Apply(d, spans)
	if len(kept) != 0 || len(dropped) != 1 {
		t.Fatalf("ожидался один снятый фрагмент, получено kept=%d dropped=%d", len(kept), len(dropped))
	}
	if spans[0].Reason != "fio:anchor" {
		t.Fatalf("входной фрагмент изменён: %q", spans[0].Reason)
	}
	if dropped[0].Reason != "fio:anchor+"+reasonPublicFigure {
		t.Fatalf("причина снятия %q", dropped[0].Reason)
	}
}

// TestContextFilterEmptyInput проверяет вырожденные входы.
func TestContextFilterEmptyInput(t *testing.T) {
	f := NewContextFilter(ctxDefaultOpts())
	if kept, dropped := f.Apply(nil, nil); len(kept) != 0 || len(dropped) != 0 {
		t.Fatalf("на пустом входе фильтр вернул kept=%d dropped=%d", len(kept), len(dropped))
	}
	d := NewDoc("")
	if kept, dropped := f.Apply(d, nil); len(kept) != 0 || len(dropped) != 0 {
		t.Fatalf("на пустом документе фильтр вернул kept=%d dropped=%d", len(kept), len(dropped))
	}
}

// TestCtxStem проверяет сравнение фамилий по основе: без него падежные формы
// не находятся в словаре.
func TestCtxStem(t *testing.T) {
	cases := []struct{ word, want string }{
		{"пушкин", "пушкин"},
		{"пушкина", "пушкин"},
		{"пушкину", "пушкин"},
		{"пушкиным", "пушкин"},
		{"толстой", "толст"},
		{"толстого", "толст"},
		{"чайковский", "чайковск"},
		{"чайковского", "чайковск"},
		{"гоголь", "гогол"},
		{"гоголя", "гогол"},
		{"глинка", "глинк"},
		{"глинки", "глинк"},
		{"иван", "иван"},
		{"цой", "цой"},
	}
	for _, c := range cases {
		if got := ctxStem(c.word); got != c.want {
			t.Errorf("ctxStem(%q) = %q, ожидалось %q", c.word, got, c.want)
		}
	}
}
