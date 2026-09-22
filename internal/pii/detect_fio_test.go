package pii

import (
	"strings"
	"testing"

	"pii-guard/internal/pii/dict"
)

// fioFound прогоняет детектор имён по тексту и возвращает найденные фрагменты
// в виде подстрок исходного текста. Тест не зависит от других детекторов:
// вызывается только детектор ФИО.
func fioFound(text string) []string {
	d := NewDoc(text)
	spans := NewFIODetector().Detect(d)
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, text[s.Start:s.End])
	}
	return out
}

// fioEqual сравнивает списки найденных и ожидаемых фрагментов.
func fioEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestFIODetectorSpans(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		// Полное ФИО в обоих порядках и в падежных формах.
		{"фамилия имя отчество", "Иванов Иван Иванович обратился в банк", []string{"Иванов Иван Иванович"}},
		{"имя отчество фамилия", "Заявление подал Иван Иванович Иванов", []string{"Иван Иванович Иванов"}},
		{"дательный падеж", "Письмо Иванову Ивану Ивановичу отправлено", []string{"Иванову Ивану Ивановичу"}},
		{"творительный падеж", "Подписано Ивановым Иваном Ивановичем", []string{"Ивановым Иваном Ивановичем"}},
		{"женское полное имя", "Клиент Петрова Мария Сергеевна", []string{"Петрова Мария Сергеевна"}},
		{"женское имя в падеже", "Выдано Петровой Марии Сергеевне", []string{"Петровой Марии Сергеевне"}},
		{"весь текст заглавными", "ИВАНОВ ИВАН ИВАНОВИЧ", []string{"ИВАНОВ ИВАН ИВАНОВИЧ"}},
		{"строчными буквами", "плательщик иванов иван иванович", []string{"иванов иван иванович"}},
		{"буква ё в имени", "Клиент Фёдоров Пётр Семёнович", []string{"Фёдоров Пётр Семёнович"}},

		// Пары компонентов.
		{"имя и фамилия", "В офис пришёл Иван Иванов", []string{"Иван Иванов"}},
		{"фамилия и имя", "В списке значится Иванов Иван", []string{"Иванов Иван"}},
		{"имя и отчество", "Здравствуйте, Сергей Владимирович", []string{"Сергей Владимирович"}},
		{"имя и отчество в падеже", "Передайте Сергею Владимировичу документы", []string{"Сергею Владимировичу"}},
		{"фамилия по суффиксу и имя", "Оформлено на Фёдора Артёмова", []string{"Фёдора Артёмова"}},
		{"двойная фамилия", "Клиент Петров-Водкин Кузьма Сергеевич", []string{"Петров-Водкин Кузьма Сергеевич"}},
		{"составное имя", "Счёт открыт на Анна-Мария Иванова", []string{"Анна-Мария Иванова"}},
		{"армянская фамилия", "Договор подписал Хачатрян Армен", []string{"Хачатрян Армен"}},
		{"украинская фамилия", "Обратился Шевченко Тарас Григорьевич", []string{"Шевченко Тарас Григорьевич"}},
		{"тюркское отчество", "Клиент Мамедов Ислам Ахмед оглы", []string{"Мамедов Ислам", "Ахмед оглы"}},

		// Инициалы во всех порядках.
		{"фамилия и два инициала через пробел", "Заявку принял Иванов И. И.", []string{"Иванов И. И"}},
		{"фамилия и два инициала без пробела", "Заявку принял Иванов И.И.", []string{"Иванов И.И"}},
		{"инициалы перед фамилией", "Согласовано: И. И. Иванов", []string{"И. И. Иванов"}},
		{"инициалы слитно перед фамилией", "Согласовано: И.И. Иванов", []string{"И.И. Иванов"}},
		{"фамилия и один инициал", "В деле упомянут Смирнов С.", []string{"Смирнов С"}},
		{"подпись поля фио не попадает во фрагмент", "Ф. И. О. Соколова Ирина Львовна", []string{"Соколова Ирина Львовна"}},
		{"латинское отчество", "Cardholder IVANOV IVAN IVANOVICH", []string{"IVANOV IVAN IVANOVICH"}},
		{"фамилия по суффиксу в середине предложения", "Отчёт подготовил Мурашов лично", []string{"Мурашов"}},

		// Одиночный компонент с якорем и без него.
		{"якорь клиент", "клиент Иванов подтвердил операцию", []string{"Иванов"}},
		{"якорь на имя", "Карта выпущена на имя Петровой", []string{"Петровой"}},
		{"якорь через тире", "заявитель — Сидоров", []string{"Сидоров"}},
		{"якорь с служебным словом", "клиент банка Кузнецова", []string{"Кузнецова"}},
		{"фамилия из словаря без якоря", "Документы передал Морозов вчера", []string{"Морозов"}},
		{"одиночное отчество", "Обратился Иванович лично", []string{"Иванович"}},
		{"имя из словаря без якоря", "Сегодня дежурит Анастасия", []string{"Анастасия"}},

		// Латиница.
		{"латиница капсом", "Cardholder IVAN IVANOV", []string{"IVAN IVANOV"}},
		{"латиница с заглавной", "Держатель Ivan Ivanov", []string{"Ivan Ivanov"}},
		{"латиница одна фамилия с якорем", "Оформлено на имя IVANOV", []string{"IVANOV"}},
		{"латиница фамилия и инициал", "Держатель карты Ivanov I.", []string{"Ivanov I"}},
		{"латиница без якоря и без словаря", "Best regards, John Smith", nil},

		// Адреса и объекты маскировать нельзя.
		{"улица сокращённо", "Адрес: ул. Пушкина, дом 5", nil},
		{"улица полностью", "живёт на улице Гагарина", nil},
		{"площадь", "Встреча на площадь Ленина", nil},
		{"проспект", "Офис на проспект Гагарина", nil},
		{"библиотека имени", "Работает библиотека имени Тургенева", nil},
		{"улица с званием", "Адрес: ул. Академика Павлова, 12", nil},

		// Обычные слова маскировать нельзя.
		{"обычное существительное", "Магазин закрыт на учёт", nil},
		{"родительный падеж множественного", "Клиентов было много", nil},
		{"сумма и заказ", "Сумма заказа 1500 рублей", nil},
		{"город из списка исключений", "Филиал в городе Ростов работает", nil},
		{"короткое слово", "Один из них ушёл", nil},
		{"прилагательное с суффиксом", "Московский филиал закрыт", nil},

		// Границы фрагмента.
		{"перевод строки не пересекается", "Иванов Иван\nПетров Пётр", []string{"Иванов Иван", "Петров Пётр"}},
		{"запятая разрывает цепочку", "В списке: Иванов, Петров", []string{"Иванов", "Петров"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fioFound(c.text)
			if !fioEqual(got, c.want) {
				t.Fatalf("текст %q: получено %q, ожидалось %q", c.text, got, c.want)
			}
		})
	}
}

func TestFIODetectorConfidenceAndReason(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		conf   float64
		reason string
	}{
		{"три компонента", "Клиент Иванов Иван Иванович", ConfHigh, "fio:surname+name+patronymic"},
		{"имя и фамилия из словаря", "Пришёл Иван Иванов", ConfHigh, "fio:name+surname"},
		{"фамилия по суффиксу и имя", "Пришёл Фёдор Артёмов", ConfAnchored, "fio:surname+name"},
		{"инициалы", "Принял Иванов И. И.", ConfHigh, "fio:initials"},
		{"одиночный компонент с якорем", "клиент Иванов", ConfAnchored, "fio:anchor"},
		{"одиночное отчество", "Ответил Петрович", ConfAnchored, "fio:patronymic"},
		{"фамилия по суффиксу без якоря", "Отчёт подготовил Мурашов", ConfMedium, "fio:surname_suffix"},
		{"латиница", "Держатель Ivan Ivanov", ConfHigh, "fio:latin+name+surname"},
		{"латиница с якорем", "на имя IVANOV", ConfAnchored, "fio:latin+anchor"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spans := NewFIODetector().Detect(NewDoc(c.text))
			if len(spans) != 1 {
				t.Fatalf("текст %q: найдено %d фрагментов, ожидался один: %v", c.text, len(spans), spans)
			}
			if spans[0].Type != TypeFIO {
				t.Fatalf("текст %q: тип %s, ожидался %s", c.text, spans[0].Type, TypeFIO)
			}
			if spans[0].Conf != c.conf {
				t.Fatalf("текст %q: уверенность %v, ожидалась %v", c.text, spans[0].Conf, c.conf)
			}
			if spans[0].Reason != c.reason {
				t.Fatalf("текст %q: причина %q, ожидалась %q", c.text, spans[0].Reason, c.reason)
			}
		})
	}
}

func TestFIODictionaryForms(t *testing.T) {
	cases := []struct {
		word    string
		name    bool
		surname bool
	}{
		{"иван", true, false},
		{"ивана", true, false},
		{"ивану", true, false},
		{"иваном", true, false},
		{"сергей", true, false},
		{"сергею", true, false},
		{"сергеем", true, false},
		{"мария", true, false},
		{"марии", true, false},
		{"анна", true, false},
		{"анны", true, false},
		{"любовь", true, false},
		{"любови", true, false},
		{"илья", true, false},
		{"ильи", true, false},
		{"петр", true, false},
		{"иванов", false, true},
		{"иванова", false, true},
		{"иванову", false, true},
		{"ивановым", false, true},
		{"ивановой", false, true},
		{"кузнецов", false, true},
		{"кузнецовой", false, true},
		{"шевченко", false, true},
		{"хачатрян", false, true},
		{"стол", false, false},
		{"договор", false, false},
		{"платеж", false, false},
	}

	for _, c := range cases {
		t.Run(c.word, func(t *testing.T) {
			if _, ok := dict.LookupName(c.word); ok != c.name {
				t.Fatalf("слово %q: имя %v, ожидалось %v", c.word, ok, c.name)
			}
			if got := dict.LookupSurname(c.word); got != c.surname {
				t.Fatalf("слово %q: фамилия %v, ожидалось %v", c.word, got, c.surname)
			}
		})
	}
}

func TestFIOTranslit(t *testing.T) {
	cases := []struct{ latin, want string }{
		{"ivanov", "иванов"},
		{"ivanova", "иванова"},
		{"petrov", "петров"},
		{"kuznetsov", "кузнецов"},
		{"sergey", "сергей"},
		{"dmitry", "дмитрий"},
		{"yuliya", "юлия"},
		{"alexey", "алексей"},
		{"krylov", "крылов"},
		{"shevchenko", "шевченко"},
		{"zhukov", "жуков"},
		{"mikhail", "михаил"},
	}

	for _, c := range cases {
		t.Run(c.latin, func(t *testing.T) {
			if got := dict.TranslitToCyr(c.latin); got != c.want {
				t.Fatalf("транслитерация %q дала %q, ожидалось %q", c.latin, got, c.want)
			}
		})
	}
}

func TestFIODictionariesLoaded(t *testing.T) {
	male, female, surname := dict.Sizes()
	if male < 100 || female < 100 || surname < 300 {
		t.Fatalf("словари загрузились не полностью: %d мужских, %d женских, %d фамилий", male, female, surname)
	}
}

func TestFIODetectorIsStateless(t *testing.T) {
	// Детектор обслуживает запросы параллельно, поэтому повторный разбор
	// одного и того же текста обязан давать тот же результат.
	det := NewFIODetector()
	text := "Клиент Иванов Иван Иванович, телефон +7 916 123-45-67"
	first := det.Detect(NewDoc(text))
	for i := 0; i < 3; i++ {
		again := det.Detect(NewDoc(text))
		if len(again) != len(first) {
			t.Fatalf("повторный разбор дал %d фрагментов вместо %d", len(again), len(first))
		}
		for j := range again {
			if again[j] != first[j] {
				t.Fatalf("повторный разбор дал другой фрагмент: %v вместо %v", again[j], first[j])
			}
		}
	}
	if !strings.Contains(text[first[0].Start:first[0].End], "Иванов") {
		t.Fatalf("первый фрагмент не содержит фамилии: %q", text[first[0].Start:first[0].End])
	}
}
