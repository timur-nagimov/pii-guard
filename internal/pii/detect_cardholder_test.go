package pii

import "testing"

// cardHolderCase — случай табличного теста: текст, ожидаемый фрагмент и его тип.
// Пустое поле want означает, что детектор не должен ничего найти.
type cardHolderCase struct {
	name string
	text string
	want string
	typ  Type
	conf float64
}

// runCardHolderCases прогоняет случаи через один детектор. Тест не зависит от
// других детекторов: регистр не используется, детектор вызывается напрямую.
func runCardHolderCases(t *testing.T, det Detector, cases []cardHolderCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDoc(tc.text)
			spans := det.Detect(d)
			if tc.want == "" {
				for _, s := range spans {
					if tc.typ == "" || s.Type == tc.typ {
						t.Fatalf("лишнее срабатывание %s на %q: %q", s.Type, tc.text, d.Text[s.Start:s.End])
					}
				}
				return
			}
			for _, s := range spans {
				if s.Type != tc.typ {
					continue
				}
				got := d.Text[s.Start:s.End]
				if got != tc.want {
					continue
				}
				if tc.conf > 0 && s.Conf < tc.conf {
					t.Fatalf("уверенность %v ниже ожидаемой %v", s.Conf, tc.conf)
				}
				return
			}
			t.Fatalf("не найден фрагмент %q типа %s в %q, получено %v", tc.want, tc.typ, tc.text, cardHolderDump(d, spans))
		})
	}
}

// cardHolderDump печатает найденные фрагменты для сообщения об ошибке.
func cardHolderDump(d *Doc, spans []Span) []string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, string(s.Type)+":"+d.Text[s.Start:s.End])
	}
	return out
}

func TestCardHolderDetector(t *testing.T) {
	det := NewCardHolderDetector()
	cases := []cardHolderCase{
		{"якорь держатель карты", "Держатель карты: IVAN IVANOV, счёт открыт вчера", "IVAN IVANOV", TypeCardHolder, ConfHigh},
		{"якорь cardholder", "Cardholder: John Smith", "John Smith", TypeCardHolder, ConfHigh},
		{"якорь card holder", "CARD HOLDER IVAN PETROV", "IVAN PETROV", TypeCardHolder, ConfHigh},
		{"якорь holder name", "holder name: JOHN SMITH", "JOHN SMITH", TypeCardHolder, ConfHigh},
		{"якорь имя держателя с тире", "Имя держателя — Пётр Сидоров", "Пётр Сидоров", TypeCardHolder, ConfHigh},
		{"якорь имя на карте", "имя на карте: ALEKSANDR", "ALEKSANDR", TypeCardHolder, ConfHigh},
		{"якорь на карте указано", "На карте указано SERGEY IVANOV", "SERGEY IVANOV", TypeCardHolder, ConfHigh},
		{"якорь в родительном падеже", "Подпись держателя: ANNA SOKOLOVA", "ANNA SOKOLOVA", TypeCardHolder, ConfHigh},
		{"три слова после якоря", "Держатель: Иванов Иван Иванович", "Иванов Иван Иванович", TypeCardHolder, ConfHigh},
		{"имя в кавычках", "Имя держателя: \"IVAN IVANOV\"", "IVAN IVANOV", TypeCardHolder, ConfHigh},
		{"имя на следующей строке", "Держатель карты:\nIVAN IVANOV", "IVAN IVANOV", TypeCardHolder, ConfHigh},
		{"перенос внутри имени обрывает фрагмент", "Держатель карты:\nIVAN\nIVANOV", "IVAN", TypeCardHolder, ConfHigh},
		{"владелец карты", "Владелец карты Петров Пётр", "Петров Пётр", TypeCardHolder, ConfHigh},
		{"бренд после якоря пропускается", "Держатель карты VISA GOLD IVAN IVANOV", "IVAN IVANOV", TypeCardHolder, ConfHigh},
		{"нижний регистр якоря и значения", "держатель карты: ivan ivanov", "ivan ivanov", TypeCardHolder, ConfHigh},
		{"якорь без двоеточия и заглавных", "держатель карты Ivan Ivanov", "Ivan Ivanov", TypeCardHolder, ConfHigh},

		{"рядом с номером карты", "4111 1111 1111 1111 IVAN IVANOV 12/25", "IVAN IVANOV", TypeCardHolder, ConfAnchored},
		{"имя перед номером карты", "Ivan Ivanov, card 5555 6666 7777 8888", "Ivan Ivanov", TypeCardHolder, ConfAnchored},
		{"смешанный регистр рядом с картой", "Карта 4276380012345678, PETR petrov", "PETR petrov", TypeCardHolder, ConfAnchored},
		{"срок действия как контекст", "VALID THRU 12/25 ANNA SOKOLOVA", "ANNA SOKOLOVA", TypeCardHolder, ConfAnchored},
		{"код безопасности как контекст", "CVV 123, SERGEI KUZNETSOV", "SERGEI KUZNETSOV", TypeCardHolder, ConfAnchored},
		{"три слова рядом с картой", "5536 9137 9876 5432 IVAN IVANOVICH PETROV", "IVAN IVANOVICH PETROV", TypeCardHolder, ConfAnchored},
		{"бренды вокруг имени не мешают", "MASTERCARD WORLD 5536913798765432 PETR SMIRNOV VALID THRU 01/27", "PETR SMIRNOV", TypeCardHolder, ConfAnchored},

		{"только бренды", "VISA GOLD 4111 1111 1111 1111", "", TypeCardHolder, 0},
		{"бренд и категория", "MASTERCARD WORLD 5536913798765432", "", TypeCardHolder, 0},
		{"срок действия без имени", "VALID THRU 12/25", "", TypeCardHolder, 0},
		{"название банка", "ALFA BANK CREDIT CARD", "", TypeCardHolder, 0},
		{"имя без карточного контекста", "Ivan Ivanov прислал документы", "", TypeCardHolder, 0},
		{"кириллица без якоря рядом с картой", "4111 1111 1111 1111 Иван Иванов", "", TypeCardHolder, 0},
		{"после якоря только бренд", "Держатель карты: VISA GOLD", "", TypeCardHolder, 0},
		{"якорь внутри другого слова", "Держательница абонемента Мария Иванова", "", TypeCardHolder, 0},
		{"обычная фраза про держателей", "Держатели карт получают бонусы каждый месяц", "", TypeCardHolder, 0},
		{"английская фраза после якоря", "The cardholder will receive a new card soon", "", TypeCardHolder, 0},
		{"почта рядом с картой", "Карта 4111111111111111, почта ivan.ivanov@mail.ru", "", TypeCardHolder, 0},
		{"цифры внутри слова рвут имя", "Карта 4111111111111111 IVAN2 IVANOV3", "", TypeCardHolder, 0},
		{"служебная английская фраза", "Card 4111111111111111 please check your account limit", "", TypeCardHolder, 0},
		{"пин без имени", "PIN 1234", "", TypeCardHolder, 0},
		{"строчные слова рядом с картой", "Card 4111111111111111 money transfer", "", TypeCardHolder, 0},
		{"имя строчными без якоря не берём", "4111 1111 1111 1111 ivan ivanov", "", TypeCardHolder, 0},
		{"пустой текст", "", "", TypeCardHolder, 0},
	}
	runCardHolderCases(t, det, cases)
}

func TestExtraDocumentsDetector(t *testing.T) {
	det := NewExtraDocumentsDetector()
	cases := []cardHolderCase{
		{"снилс с семёрки", "СНИЛС 789-012-345 00", "789-012-345 00", TypeSNILS, ConfHigh},
		{"снилс с восьмёрки через пробелы", "Страховой номер 890 123 456 78", "890 123 456 78", TypeSNILS, ConfHigh},
		{"снилс слитно с семёрки", "снилс: 78901234500", "78901234500", TypeSNILS, ConfHigh},
		{"снилс со знаком номера", "СНИЛС № 799 123 456 88", "799 123 456 88", TypeSNILS, ConfHigh},
		{"снилс уже найден числовым детектором", "СНИЛС 123-456-789 00", "", TypeSNILS, 0},
		{"телефон без якоря снилс", "Телефон 8 901 234 56 78", "", TypeSNILS, 0},
		{"якорь снилс без номера", "СНИЛС не указан", "", TypeSNILS, 0},

		{"загранпаспорт серия и номер", "Загранпаспорт 75 1234567", "75 1234567", TypeForeignPassport, ConfHigh},
		{"загранпаспорт слитно", "Заграничный паспорт № 751234567", "751234567", TypeForeignPassport, ConfHigh},
		{"загранпаспорт со знаком номера", "Passport No 75 № 1234567", "75 № 1234567", TypeForeignPassport, ConfHigh},
		{"загранпаспорт со словом номер", "Загранпаспорт серия 75 номер 1234567", "75 номер 1234567", TypeForeignPassport, ConfHigh},
		{"travel document", "travel document 12 3456789", "12 3456789", TypeForeignPassport, ConfHigh},
		{"обычный паспорт не загран", "Паспорт 4509 123456", "", TypeForeignPassport, 0},
		{"номер заказа не загран", "Заказ 75 1234567 отправлен", "", TypeForeignPassport, 0},

		{"внж со знаком номера", "Вид на жительство 82 № 1234567", "82 № 1234567", TypeResidencePermit, ConfAnchored},
		{"внж сокращение", "ВНЖ 123456789", "123456789", TypeResidencePermit, ConfAnchored},
		{"residence permit", "residence permit 9876543", "9876543", TypeResidencePermit, ConfAnchored},
		{"внж без номера", "ВНЖ оформлен в прошлом году", "", TypeResidencePermit, 0},
		{"чужое число после якоря внж", "ВНЖ продлён 12 месяцев назад, сумма 123456 рублей", "", TypeResidencePermit, 0},

		{"свидетельство о рождении", "Свидетельство о рождении II-МЮ 123456", "II-МЮ 123456", TypeBirthCert, ConfHigh},
		{"свидетельство строчными латиницей", "свид. о рождении iii-ан 654321", "iii-ан 654321", TypeBirthCert, ConfHigh},
		{"свидетельство слитно с номером", "Свидетельство о рождении: IV-МЮ123456", "IV-МЮ123456", TypeBirthCert, ConfHigh},
		{"свидетельство с пробелами вокруг дефиса", "Свидетельство о рождении V - АБ 112233", "V - АБ 112233", TypeBirthCert, ConfHigh},
		{"свидетельство без серии", "Свидетельство о рождении № 123456", "123456", TypeBirthCert, ConfAnchored},
		{"серия без якоря", "II-МЮ 123456", "", TypeBirthCert, 0},
		{"дата выдачи не номер свидетельства", "Свидетельство о рождении выдано 15.06.2010", "", TypeBirthCert, 0},

		{"военный билет с серией", "Военный билет АБ 1234567", "АБ 1234567", TypeMilitaryID, ConfHigh},
		{"военник через дефис", "военник ВС-1234567", "ВС-1234567", TypeMilitaryID, ConfHigh},
		{"военный билет латиницей", "Military ID AB 1234567", "AB 1234567", TypeMilitaryID, ConfHigh},
		{"военный билет без серии", "Военный билет № 1234567", "1234567", TypeMilitaryID, ConfAnchored},
		{"билет на поезд", "Билет на поезд 1234567", "", TypeMilitaryID, 0},
		{"номер заказа не военный билет", "Заказ 1234567 оплачен", "", TypeMilitaryID, 0},
		{"военный билет строчными", "военный билет аб 1234567", "аб 1234567", TypeMilitaryID, ConfHigh},
		{"снилс на новой строке от якоря", "СНИЛС\n789-012-345 00", "789-012-345 00", TypeSNILS, ConfHigh},
		{"загранпаспорт и внж рядом", "Загранпаспорт 75 1234567, ВНЖ 821234567", "821234567", TypeResidencePermit, ConfAnchored},
		{"пустой текст", "", "", "", 0},
	}
	runCardHolderCases(t, det, cases)
}
