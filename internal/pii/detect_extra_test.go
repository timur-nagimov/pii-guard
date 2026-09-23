package pii

import (
	"testing"
)

// extraCase описывает один случай для детектора расширенных типов.
type extraCase struct {
	name string
	text string
	want []numericHit
}

// runExtraCases прогоняет табличный набор через детектор расширенных типов.
func runExtraCases(t *testing.T, cases []extraCase) {
	t.Helper()
	det := NewExtraDetector()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := NewDoc(c.text)
			got := det.Detect(d)
			if len(got) != len(c.want) {
				t.Fatalf("найдено %d фрагментов, ожидалось %d: %s", len(got), len(c.want), describeSpans(c.text, got))
			}
			for i, s := range got {
				if s.Type != c.want[i].typ {
					t.Errorf("фрагмент %d: тип %s, ожидался %s", i, s.Type, c.want[i].typ)
				}
				if value := c.text[s.Start:s.End]; value != c.want[i].value {
					t.Errorf("фрагмент %d: значение %q, ожидалось %q", i, value, c.want[i].value)
				}
				if s.Conf < ConfMedium {
					t.Errorf("фрагмент %d: уверенность %.2f ниже средней", i, s.Conf)
				}
				if s.Reason == "" {
					t.Errorf("фрагмент %d: не заполнена причина срабатывания", i)
				}
			}
		})
	}
}

// TestExtraAccount проверяет номер банковского счёта: форму, якорь и ловушки.
func TestExtraAccount(t *testing.T) {
	runExtraCases(t, []extraCase{
		{
			name: "слитная запись с якорем",
			text: "Номер счёта 40817810099910004312",
			want: []numericHit{{TypeAccount, "40817810099910004312"}},
		},
		{
			name: "запись группами",
			text: "Счёт 40817 8100 9991 0004 312",
			want: []numericHit{{TypeAccount, "40817 8100 9991 0004 312"}},
		},
		{
			name: "сокращение р/с",
			text: "р/с 40817810099910004312 в банке",
			want: []numericHit{{TypeAccount, "40817810099910004312"}},
		},
		{
			name: "лицевой счёт",
			text: "Лицевой счёт 42301810400000000001",
			want: []numericHit{{TypeAccount, "42301810400000000001"}},
		},
		{
			name: "вклад физического лица",
			text: "Счёт вклада 42601810400000000001",
			want: []numericHit{{TypeAccount, "42601810400000000001"}},
		},
		{
			name: "английский якорь",
			text: "account 40817810099910004312",
			want: []numericHit{{TypeAccount, "40817810099910004312"}},
		},
		{
			name: "без якоря не маскируется",
			text: "40817810099910004312",
			want: nil,
		},
		{
			name: "не тот балансовый счёт",
			text: "Счёт 40702810099910004312",
			want: nil,
		},
		{
			name: "не двадцать цифр",
			text: "Счёт 4081781009991000431",
			want: nil,
		},
	})
}

// TestExtraOMS проверяет полис ОМС: форму, якорь и различие с картой.
func TestExtraOMS(t *testing.T) {
	runExtraCases(t, []extraCase{
		{
			name: "слитная запись с якорем",
			text: "Полис ОМС 1234567890123456",
			want: []numericHit{{TypeOMS, "1234567890123456"}},
		},
		{
			name: "запись по четыре",
			text: "Медицинский полис 1234 5678 9012 3456",
			want: []numericHit{{TypeOMS, "1234 5678 9012 3456"}},
		},
		{
			name: "страховой полис",
			text: "Страховой полис 1234567890123456",
			want: []numericHit{{TypeOMS, "1234567890123456"}},
		},
		{
			name: "без якоря не маскируется",
			text: "1234567890123456",
			want: nil,
		},
	})
}

// TestExtraOMSCardMixed проверяет, что карта и полис различаются по якорю в
// смешанном тексте.
func TestExtraOMSCardMixed(t *testing.T) {
	runExtraCases(t, []extraCase{
		{
			name: "карта и полис в одном тексте",
			text: "Номер карты 4111111111111111, полис ОМС 1234567890123456",
			want: []numericHit{{TypeOMS, "1234567890123456"}},
		},
		{
			name: "полис и карта в обратном порядке",
			text: "Полис ОМС 1234567890123456, карта 4111111111111111",
			want: []numericHit{{TypeOMS, "1234567890123456"}},
		},
	})
}

// TestExtraPlate проверяет госномер автомобиля: форму, якорь и ловушки.
func TestExtraPlate(t *testing.T) {
	runExtraCases(t, []extraCase{
		{
			name: "стандартный номер",
			text: "Госномер А123ВС77",
			want: []numericHit{{TypePlate, "А123ВС77"}},
		},
		{
			name: "трёхзначный регион",
			text: "Госномер А123ВС777",
			want: []numericHit{{TypePlate, "А123ВС777"}},
		},
		{
			name: "с пробелами",
			text: "Госномер А 123 ВС 77",
			want: []numericHit{{TypePlate, "А 123 ВС 77"}},
		},
		{
			name: "латиница вместо кириллицы",
			text: "Госномер A123BC77",
			want: []numericHit{{TypePlate, "A123BC77"}},
		},
		{
			name: "якорь автомобиль",
			text: "Автомобиль А123ВС77 припаркован",
			want: []numericHit{{TypePlate, "А123ВС77"}},
		},
		{
			name: "якорь машина",
			text: "Машина А123ВС77 у отделения",
			want: []numericHit{{TypePlate, "А123ВС77"}},
		},
		{
			name: "без якоря не маскируется",
			text: "А123ВС77",
			want: nil,
		},
		{
			name: "недопустимая буква",
			text: "Госномер Я123ВС77",
			want: nil,
		},
	})
}

// TestExtraVIN проверяет идентификационный номер транспортного средства.
func TestExtraVIN(t *testing.T) {
	runExtraCases(t, []extraCase{
		{
			name: "стандартный VIN",
			text: "VIN XTA21099062134567",
			want: []numericHit{{TypeVIN, "XTA21099062134567"}},
		},
		{
			name: "якорь номер кузова",
			text: "Номер кузова XTA21099062134567",
			want: []numericHit{{TypeVIN, "XTA21099062134567"}},
		},
		{
			name: "якорь номер шасси",
			text: "Номер шасси XTA21099062134567",
			want: []numericHit{{TypeVIN, "XTA21099062134567"}},
		},
		{
			name: "кириллический якорь",
			text: "ВИН XTA21099062134567",
			want: []numericHit{{TypeVIN, "XTA21099062134567"}},
		},
		{
			name: "без якоря не маскируется",
			text: "XTA21099062134567",
			want: nil,
		},
		{
			name: "с запрещённой буквой O",
			text: "VIN XTA2109906213456O",
			want: nil,
		},
		{
			name: "не семнадцать знаков",
			text: "VIN XTA2109906213456",
			want: nil,
		},
	})
}

// TestExtraIP проверяет адреса в сети: форму, якорь и служебные адреса.
func TestExtraIP(t *testing.T) {
	runExtraCases(t, []extraCase{
		{
			name: "IPv4 с якорем",
			text: "Вход с адреса 192.168.1.42",
			want: []numericHit{{TypeIPAddress, "192.168.1.42"}},
		},
		{
			name: "якорь IP",
			text: "IP 8.8.8.8",
			want: []numericHit{{TypeIPAddress, "8.8.8.8"}},
		},
		{
			name: "якорь сессия с",
			text: "Сессия с 203.0.113.5",
			want: []numericHit{{TypeIPAddress, "203.0.113.5"}},
		},
		{
			name: "без якоря не маскируется",
			text: "192.168.1.42",
			want: nil,
		},
		{
			name: "служебный адрес не маскируется",
			text: "Вход с адреса 10.0.0.0/8",
			want: nil,
		},
		{
			name: "loopback не маскируется",
			text: "Вход с адреса 127.0.0.1",
			want: nil,
		},
		{
			name: "сетевой диапазон не маскируется",
			text: "Вход с адреса 192.168.0.0/16",
			want: nil,
		},
		{
			name: "все нули не маскируются",
			text: "Вход с адреса 0.0.0.0",
			want: nil,
		},
		{
			name: "номер версии не принимается за адрес",
			text: "Обновите приложение до версии 1.2.3.4",
			want: nil,
		},
	})
}

// TestExtraTypes проверяет перечень типов детектора.
func TestExtraTypes(t *testing.T) {
	got := NewExtraDetector().Types()
	want := []Type{TypeAccount, TypeOMS, TypePlate, TypeVIN, TypeIPAddress}
	if len(got) != len(want) {
		t.Fatalf("типов %d, ожидалось %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("тип %d: %s, ожидался %s", i, got[i], want[i])
		}
	}
}

// TestVINShapeFixes закрепляет разбор VIN на случаях, которые набор показал
// как ошибочные. Каждый случай здесь — след настоящей ошибки, а не догадка.
func TestVINShapeFixes(t *testing.T) {
	runExtraCases(t, []extraCase{
		{
			// Номер начинается с цифры: мировой код изготовителя почти всегда
			// такой. Пока цифровые токены не начинали разбор, треть номеров
			// в наборе не находилась.
			name: "номер начинается с цифры",
			text: "VIN: 8MT77X3CZG4JDVZNL.",
			want: []numericHit{{typ: TypeVIN, value: "8MT77X3CZG4JDVZNL"}},
		},
		{
			// В слове «Идентификационный» ровно семнадцать букв, и оно
			// принималось за номер: маска накрывала якорь, оставляя значение
			// открытым.
			name: "слово из семнадцати букв не номер",
			text: "Идентификационный номер: 3A942B0K1RYLBV2ED;",
			want: []numericHit{{typ: TypeVIN, value: "3A942B0K1RYLBV2ED"}},
		},
		{
			// Кириллица с цифрами набирает семнадцать знаков и проходила бы
			// проверку на цифры: отсекает её только вид первого токена.
			name: "кириллица с цифрами не номер",
			text: "VIN: Кузовной123456789 принят",
			want: nil,
		},
		{
			// Полная фраза якоря длиннее сорока знаков, и в общее окно поиска
			// она не помещалась целиком.
			name: "длинный якорь достаёт до значения",
			text: "Идентификационный номер транспортного средства: ZRFWBH50AP7F4PLU0!",
			want: []numericHit{{typ: TypeVIN, value: "ZRFWBH50AP7F4PLU0"}},
		},
		{
			// Номер переносят и разрывают пробелом. Пробел — самостоятельный
			// токен, поэтому разрыв не виден как пустота между соседями.
			name: "номер разорван пробелом",
			text: "Идентификационный номер: BFXP336J ZLY4XJ5Z2 ",
			want: []numericHit{{typ: TypeVIN, value: "BFXP336J ZLY4XJ5Z2"}},
		},
		{
			// Два разрыва это уже перечисление, а не один номер.
			name: "два разрыва номером не считаются",
			text: "Идентификационный номер: BFXP336 J ZLY4XJ 5Z2",
			want: nil,
		},
		{
			// Семнадцать латинских букв без единой цифры настоящим номером
			// не бывают.
			name: "без цифр не номер",
			text: "VIN: ABCDEFGHJKLMNPRST.",
			want: nil,
		},
	})
}

// TestPlateMixedScript закрепляет разбор госномера, набранного вперемешку
// кириллицей и латиницей. Буквы госномера выбраны так, что у каждой есть
// латинский двойник, и человек набирает их как придётся; разметчик слов делит
// такую запись по смене письменности.
func TestPlateMixedScript(t *testing.T) {
	runExtraCases(t, []extraCase{
		{
			name: "латинская буква в паре",
			text: "Госзнак: «М663XК09».",
			want: []numericHit{{typ: TypePlate, value: "М663XК09"}},
		},
		{
			name: "латинская буква и длинное тире",
			text: "В анкете указано госномер — У565XН81.",
			want: []numericHit{{typ: TypePlate, value: "У565XН81"}},
		},
		{
			name: "буквы вне набора госномера",
			text: "Госномер: Ж123ЩФ77",
			want: nil,
		},
		{
			name: "три буквы подряд номером не считаются",
			text: "Госномер: М663ХКВ09",
			want: nil,
		},
	})
}
