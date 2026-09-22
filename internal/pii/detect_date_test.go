package pii

import "testing"

// dateWant — ожидаемый фрагмент: его текст и тип.
type dateWant struct {
	text string
	typ  Type
}

// dateCase — случай табличного теста детектора дат.
type dateCase struct {
	name string
	mode string
	text string
	want []dateWant
}

// runDateDetector прогоняет только детектор дат, чтобы тест не зависел от
// остальных детекторов пакета.
func runDateDetector(t *testing.T, mode, text string) []dateWant {
	t.Helper()
	d := NewDoc(text)
	spans := NewDateDetector(mode).Detect(d)
	SortSpans(spans)
	got := make([]dateWant, 0, len(spans))
	for _, s := range spans {
		if !s.Valid() {
			t.Fatalf("детектор вернул некорректные границы: %+v", s)
		}
		got = append(got, dateWant{text: text[s.Start:s.End], typ: s.Type})
	}
	return got
}

func TestDateDetector(t *testing.T) {
	cases := []dateCase{
		{
			name: "числовая дата с явным якорем рождения",
			text: "Дата рождения: 05.03.1990",
			want: []dateWant{{"05.03.1990", TypeDOB}},
		},
		{
			name: "сокращение д.р. и дефисы",
			text: "д.р. 07-11-1975",
			want: []dateWant{{"07-11-1975", TypeDOB}},
		},
		{
			name: "сокращение д/р и дроби",
			text: "д/р 05/03/1990",
			want: []dateWant{{"05/03/1990", TypeDOB}},
		},
		{
			name: "заглавные буквы не мешают",
			text: "ДАТА РОЖДЕНИЯ: 05.03.1990",
			want: []dateWant{{"05.03.1990", TypeDOB}},
		},
		{
			name: "порядок год месяц день",
			text: "Дата рождения 1990.03.05",
			want: []dateWant{{"1990.03.05", TypeDOB}},
		},
		{
			name: "порядок год день месяц",
			text: "Дата рождения 1990.25.03",
			want: []dateWant{{"1990.25.03", TypeDOB}},
		},
		{
			name: "двузначный год",
			text: "д.р. 05.03.90",
			want: []dateWant{{"05.03.90", TypeDOB}},
		},
		{
			name: "день без ведущего нуля",
			text: "Родился 5.3.1990 в Твери",
			want: []dateWant{{"5.3.1990", TypeDOB}},
		},
		{
			name: "месяц больше двенадцати на первом месте",
			text: "Дата рождения 25.03.1990",
			want: []dateWant{{"25.03.1990", TypeDOB}},
		},
		{
			name: "дата словами с годом",
			text: "Дата рождения 12 марта 1985 года",
			want: []dateWant{{"12 марта 1985", TypeDOB}},
		},
		{
			name: "дата словами с окончанием порядкового числительного",
			text: "Родилась 5-го марта 1990",
			want: []dateWant{{"5-го марта 1990", TypeDOB}},
		},
		{
			name: "именительный падеж месяца",
			text: "Родился 1 май 2001",
			want: []dateWant{{"1 май 2001", TypeDOB}},
		},
		{
			name: "предложный падеж месяца",
			text: "Рождён 1 мае 2001",
			want: []dateWant{{"1 мае 2001", TypeDOB}},
		},
		{
			name: "английская запись месяц день год",
			text: "Клиент Ivanov, March 12, 1985",
			want: []dateWant{{"March 12, 1985", TypeDOB}},
		},
		{
			name: "английская запись день месяц год",
			text: "д/р 12 March 1985",
			want: []dateWant{{"12 March 1985", TypeDOB}},
		},
		{
			name: "год рождения сокращением",
			text: "Петров П.П., 1985 г.р.",
			want: []dateWant{{"1985", TypeDOB}},
		},
		{
			name: "год рождения словами",
			text: "Заявитель 1985 года рождения",
			want: []dateWant{{"1985", TypeDOB}},
		},
		{
			name: "год рождения с сокращением род",
			text: "Уточнение: род. 1985",
			want: []dateWant{{"1985", TypeDOB}},
		},
		{
			name: "сокращение др без точки",
			text: "ФИО: Петров Пётр Петрович, ДР 14.07.1982",
			want: []dateWant{{"14.07.1982", TypeDOB}},
		},
		{
			name: "дата выдачи по якорю выдан",
			text: "Паспорт 4509 123456, выдан 12.05.2010",
			want: []dateWant{{"12.05.2010", TypeIssueDate}},
		},
		{
			name: "дата выдачи по якорю дата выдачи",
			text: "Дата выдачи 01.02.2015",
			want: []dateWant{{"01.02.2015", TypeIssueDate}},
		},
		{
			name: "дата выдачи словами",
			text: "Выдана 3 апреля 2018 года",
			want: []dateWant{{"3 апреля 2018", TypeIssueDate}},
		},
		{
			name: "дата после упоминания органа выдачи",
			text: "Паспорт 4509 123456 ОВД г. Москвы 12.05.2010",
			want: []dateWant{{"12.05.2010", TypeIssueDate}},
		},
		{
			name: "ближний якорь рождения побеждает якорь выдачи",
			text: "Паспорт выдан УФМС, Иванов 05.03.1990 г.р.",
			want: []dateWant{{"05.03.1990", TypeDOB}},
		},
		{
			name: "режим pii_context ловит дату рядом с отчеством и паспортом",
			text: "Иванов Иван Иванович, 05.03.1990, паспорт 4509 123456",
			want: []dateWant{{"05.03.1990", TypeDOB}},
		},
		{
			name: "режим pii_context ловит дату рядом с банковским якорем",
			text: "Клиент, 12/03/1985, обращение принято",
			want: []dateWant{{"12/03/1985", TypeDOB}},
		},
		{
			name: "режим pii_context ловит дату рядом с длинным номером",
			text: "Анкета: 05.03.1990, 4276 1600 1234 5678",
			want: []dateWant{{"05.03.1990", TypeDOB}},
		},
		{
			name: "дата без контекста в режиме по умолчанию не маскируется",
			text: "Встреча состоится 05.03.1990 в офисе",
			want: nil,
		},
		{
			name: "режим anchor_only не маскирует дату по контексту",
			mode: DateModeAnchorOnly,
			text: "Иванов Иван Иванович, 05.03.1990, паспорт 4509 123456",
			want: nil,
		},
		{
			name: "режим any маскирует дату без контекста",
			mode: DateModeAny,
			text: "Встреча состоится 05.03.1990 в офисе",
			want: []dateWant{{"05.03.1990", TypeDOB}},
		},
		{
			name: "срок действия карты из двух групп не маскируется",
			text: "Срок действия карты 12/27",
			want: nil,
		},
		{
			name: "срок действия карты с четырёхзначным годом не маскируется",
			text: "Карта действует до 12/2027",
			want: nil,
		},
		{
			name: "отрицательный якорь срока действия",
			text: "Срок действия 12.03.2025",
			want: nil,
		},
		{
			name: "отрицательный якорь даты платежа",
			text: "Дата платежа 05.03.2024, клиент Петров",
			want: nil,
		},
		{
			name: "отрицательный якорь операции",
			text: "Дата операции 05.03.2024 по карте клиента",
			want: nil,
		},
		{
			name: "отрицательный якорь оплаты",
			text: "Оплата прошла 05.03.2024, заявитель уведомлён",
			want: nil,
		},
		{
			name: "отрицательный якорь курса",
			text: "Курс на 05.03.2024 для клиента",
			want: nil,
		},
		{
			name: "отчётный период не маскируется",
			text: "Отчётный период 01.01.2020, клиент Сидоров",
			want: nil,
		},
		{
			name: "несуществующее тридцатое февраля",
			text: "Дата рождения 30.02.1990",
			want: nil,
		},
		{
			name: "двадцать девятое февраля невисокосного года",
			text: "Дата рождения 29.02.1991",
			want: nil,
		},
		{
			name: "двадцать девятое февраля високосного года",
			text: "Дата рождения 29.02.1992",
			want: []dateWant{{"29.02.1992", TypeDOB}},
		},
		{
			name: "невозможные день и месяц",
			text: "Дата рождения 45.13.1990",
			want: nil,
		},
		{
			name: "год раньше тысяча девятисотого",
			text: "Дата рождения 05.03.1890",
			want: nil,
		},
		{
			name: "год из будущего",
			text: "Дата рождения 05.03.2099",
			want: nil,
		},
		{
			name: "номер версии датой не является",
			text: "Версия 1.2.3 выпущена",
			want: nil,
		},
		{
			name: "телефон датой не является",
			text: "Телефон клиента +7 916 123-45-67",
			want: nil,
		},
		{
			name: "серия и номер паспорта датой не являются",
			text: "Паспорт 4509 123456 клиента",
			want: nil,
		},
		{
			name: "число без якоря рождения годом рождения не считается",
			text: "Сумма 1985 рублей списана",
			want: nil,
		},
		{
			name: "обычный текст без дат",
			text: "Заявление принято в отделении банка",
			want: nil,
		},
		{
			name: "дата рождения и дата выдачи в одном тексте",
			text: "Дата рождения 05.03.1990. Паспорт выдан 12.05.2010.",
			want: []dateWant{{"05.03.1990", TypeDOB}, {"12.05.2010", TypeIssueDate}},
		},
		{
			name: "фрагмент не выходит за границы строки",
			text: "Дата рождения:\n05.03.1990\nПаспорт 4509 123456",
			want: []dateWant{{"05.03.1990", TypeDOB}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runDateDetector(t, tc.mode, tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("найдено %d фрагментов %v, ожидалось %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("фрагмент %d: получено %v, ожидалось %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestDateDetectorConfidence проверяет уровни уверенности: явный якорь даёт
// высокую уверенность, контекст персональных данных — среднюю.
func TestDateDetectorConfidence(t *testing.T) {
	cases := []struct {
		name string
		mode string
		text string
		conf float64
		typ  Type
	}{
		{"явный якорь рождения", "", "Дата рождения 05.03.1990", ConfHigh, TypeDOB},
		{"явный якорь выдачи", "", "Дата выдачи 12.05.2010", ConfHigh, TypeIssueDate},
		{"упоминание документа слева", "", "Паспорт 4509 123456 ОВД Москвы 12.05.2010", ConfAnchored, TypeIssueDate},
		{"контекст персональных данных", "", "Иванов Иван Иванович, 05.03.1990", ConfMedium, TypeDOB},
		{"режим any", DateModeAny, "Событие 05.03.1990", ConfMedium, TypeDOB},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spans := NewDateDetector(tc.mode).Detect(NewDoc(tc.text))
			if len(spans) != 1 {
				t.Fatalf("ожидался один фрагмент, получено %d: %+v", len(spans), spans)
			}
			if spans[0].Conf != tc.conf {
				t.Errorf("уверенность %.2f, ожидалась %.2f (%s)", spans[0].Conf, tc.conf, spans[0].Reason)
			}
			if spans[0].Type != tc.typ {
				t.Errorf("тип %s, ожидался %s", spans[0].Type, tc.typ)
			}
			if spans[0].Reason == "" {
				t.Error("причина срабатывания не заполнена")
			}
		})
	}
}

// TestDateDetectorModeFallback проверяет, что неизвестный режим приводится к
// режиму по умолчанию и тип персональных данных не выключается.
func TestDateDetectorModeFallback(t *testing.T) {
	text := "Иванов Иван Иванович, 05.03.1990"
	for _, mode := range []string{"", "неизвестный режим", DateModePIIContext} {
		spans := NewDateDetector(mode).Detect(NewDoc(text))
		if len(spans) != 1 {
			t.Fatalf("режим %q: ожидался один фрагмент, получено %d", mode, len(spans))
		}
	}
}

// TestDateDetectorTypes фиксирует список типов детектора: по нему движок
// строит отчёт о покрытии.
func TestDateDetectorTypes(t *testing.T) {
	types := NewDateDetector(DateModePIIContext).Types()
	if len(types) != 2 || types[0] != TypeDOB || types[1] != TypeIssueDate {
		t.Fatalf("неожиданный список типов: %v", types)
	}
}

// TestDateValidDayMonth проверяет длину месяцев отдельно от разбора текста.
func TestDateValidDayMonth(t *testing.T) {
	cases := []struct {
		day, month string
		year       int
		want       bool
	}{
		{"31", "01", 1990, true},
		{"31", "04", 1990, false},
		{"29", "02", 2000, true},
		{"29", "02", 1900, false},
		{"00", "05", 1990, false},
		{"01", "13", 1990, false},
	}
	for _, tc := range cases {
		if got := dateValidDayMonth(tc.day, tc.month, tc.year); got != tc.want {
			t.Errorf("%s.%s.%d: получено %v, ожидалось %v", tc.day, tc.month, tc.year, got, tc.want)
		}
	}
}
