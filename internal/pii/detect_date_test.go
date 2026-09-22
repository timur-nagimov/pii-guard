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
			name: "дата через пробел с якорем рождения",
			text: "Дата рождения: 12 03 1985",
			want: []dateWant{{"12 03 1985", TypeDOB}},
		},
		{
			name: "дата через пробел с якорем выдачи",
			text: "Дата выдачи: 01 08 2020 г.",
			want: []dateWant{{"01 08 2020", TypeIssueDate}},
		},
		{
			name: "дата через пробел без ведущих нулей датой не считается",
			text: "Дата рождения: 1 3 1985",
			want: nil,
		},
		{
			name: "дата внутри длинной числовой записи",
			text: "Белова К.А. 1977-12-17 75 52 040112.",
			want: []dateWant{{"1977-12-17", TypeDOB}},
		},
		{
			name: "дата перед серией и номером паспорта",
			text: "Андреев Е. О. 18 10 1963 7956 689649",
			want: []dateWant{{"18 10 1963", TypeDOB}},
		},
		{
			name: "серия и номер после даты второй датой не становятся",
			text: "Ефимов В. К. 25/04/2004 29 47 475159",
			want: []dateWant{{"25/04/2004", TypeDOB}},
		},
		{
			name: "цифры серии и номера считаются вместе через слово",
			text: "Вишневская В. А. | 08.05.1954 | серия 3321 номер 649566.",
			want: []dateWant{{"08.05.1954", TypeDOB}},
		},
		{
			name: "сетевой адрес датой не является",
			text: "Запрос пришёл с адреса 195.12.1.20, регион определён по сети.",
			want: nil,
		},
		{
			name: "дата через пробел без контекста не маскируется",
			text: "Заявка создана 12 03 2024 и передана оператору.",
			want: nil,
		},
		{
			name: "телефон через пробелы датой не является",
			text: "Телефон клиента 8 916 123 45 67",
			want: nil,
		},
		{
			name: "снилс через пробел датой не является",
			text: "СНИЛС клиента 011-228-332 86",
			want: nil,
		},
		{
			name: "сокращение дата рожд с двузначным годом",
			text: "дата рожд. 21.06.75 г.",
			want: []dateWant{{"21.06.75", TypeDOB}},
		},
		{
			name: "якорь года рождения справа",
			text: "Клиент 12.03.1985 года рождения",
			want: []dateWant{{"12.03.1985", TypeDOB}},
		},
		{
			name: "английское сокращение dob",
			text: "DOB: 12/03/1985",
			want: []dateWant{{"12/03/1985", TypeDOB}},
		},
		{
			name: "дата прописью целиком",
			text: "Дата рождения: двенадцатое марта тысяча девятьсот восемьдесят пятого года",
			want: []dateWant{{"двенадцатое марта тысяча девятьсот восемьдесят пятого", TypeDOB}},
		},
		{
			name: "порядковое числительное словом и числовой год",
			text: "Родился пятого марта 1990 года",
			want: []dateWant{{"пятого марта 1990", TypeDOB}},
		},
		{
			name: "составной день и год две тысячи",
			text: "Клиент родился двадцать первого июня две тысячи второго года",
			want: []dateWant{{"двадцать первого июня две тысячи второго", TypeDOB}},
		},
		{
			name: "числительные без месяца датой не являются",
			text: "Родился в тысяча девятьсот восемьдесят пятом",
			want: nil,
		},
		{
			name: "дата одной группой цифр с якорем рождения",
			text: "Дата рождения 12031985",
			want: []dateWant{{"12031985", TypeDOB}},
		},
		{
			name: "дата одной группой цифр с якорем выдачи",
			text: "Паспорт выдан 15062010",
			want: []dateWant{{"15062010", TypeIssueDate}},
		},
		{
			name: "дата одной группой цифр без явного якоря не маскируется",
			text: "Клиент Иванов Иван Иванович, 12031985, обращение принято",
			want: nil,
		},
		{
			name: "месяц с опечаткой разбирается",
			text: "выдан 9 сентбяря 2010",
			want: []dateWant{{"9 сентбяря 2010", TypeIssueDate}},
		},
		{
			name: "опечатка в анкете рождения",
			text: "ДР: 12 янвая 1995 г.",
			want: []dateWant{{"12 янвая 1995", TypeDOB}},
		},
		{
			name: "похожее на месяц слово датой не делает, остаётся год",
			text: "Дата рождения: 12 мартышка 1985",
			want: []dateWant{{"1985", TypeDOB}},
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

// TestDateWordNumber проверяет сборку числа из числительных: на ней держится
// разбор даты, записанной словами целиком.
func TestDateWordNumber(t *testing.T) {
	cases := []struct {
		words []string
		want  int
		ok    bool
	}{
		{[]string{"двенадцатое"}, 12, true},
		{[]string{"двадцать", "первого"}, 21, true},
		{[]string{"тысяча", "девятьсот", "восемьдесят", "пятого"}, 1985, true},
		{[]string{"две", "тысячи", "второго"}, 2002, true},
		{[]string{"две", "тысячи", "двадцать", "четвёртого"}, 2024, true},
		{[]string{"тысяча", "девятьсот", "девяностого"}, 1990, true},
		{[]string{"марта"}, 0, false},
		{nil, 0, false},
	}
	for _, tc := range cases {
		got, ok := dateWordNumber(tc.words)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%v: получено %d %v, ожидалось %d %v", tc.words, got, ok, tc.want, tc.ok)
		}
	}
}

// TestDateNearWord проверяет допуск на одну опечатку в названии месяца.
func TestDateNearWord(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"сентбяря", "сентября", true},
		{"янвая", "января", true},
		{"аавгуста", "августа", true},
		{"мартв", "марта", true},
		{"сентября", "сентября", true},
		{"сентяб", "сентября", false},
		{"мартышка", "марта", false},
	}
	for _, tc := range cases {
		if got := dateNearWord(tc.a, tc.b); got != tc.want {
			t.Errorf("%q и %q: получено %v, ожидалось %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestDateGroupLengths фиксирует требования к длинам групп цифр: запись через
// пробел разбирается строже, чем запись через разделитель.
func TestDateGroupLengths(t *testing.T) {
	cases := []struct {
		parts  []string
		spaced bool
		want   bool
	}{
		{[]string{"12", "03", "1985"}, true, true},
		{[]string{"1985", "03", "12"}, true, true},
		{[]string{"1", "3", "1985"}, true, false},
		{[]string{"12", "03", "85"}, true, false},
		{[]string{"1", "3", "1985"}, false, true},
		{[]string{"12", "03", "85"}, false, true},
		{[]string{"4509", "1234", "56"}, false, false},
		{[]string{"916", "123", "45"}, false, false},
	}
	for _, tc := range cases {
		if got := dateGroupLengths(tc.parts, tc.spaced); got != tc.want {
			t.Errorf("%v (через пробел %v): получено %v, ожидалось %v", tc.parts, tc.spaced, got, tc.want)
		}
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
