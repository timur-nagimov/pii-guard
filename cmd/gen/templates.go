package main

import (
	"strings"

	"pii-guard/internal/pii"
)

// Обрамляющий текст для шаблонов. В литералах не должно быть персональных
// данных: всё, что похоже на них, обязано идти через размечаемое значение,
// иначе набор будет наказывать детектор за верное срабатывание.
var (
	fioPrefixesNom = []string{
		"Клиент: ", "Заявитель: ", "ФИО: ", "Держатель счёта: ",
		"Анкету заполнил ", "На приём записан ", "Ответственный сотрудник: ",
		"В отделение обратился ", "Представился как ",
	}
	fioPrefixesGen = []string{
		"Заявление от ", "Доверенность на имя ", "Счёт открыт на имя ",
		"Данные владельца карты ", "Копия паспорта ", "Запрос от ",
	}
	fioPrefixesDat = []string{
		"Перевод получателю ", "Направить уведомление ", "Кредит одобрен ",
		"Выдать справку ", "Позвонить клиенту ",
	}
	neutralTails = []string{
		".", ", проверьте данные.", " — данные взяты из анкеты.", ";",
		". Обращение принято в работу.", " (заявка принята).",
		". Ответ направим в течение трёх рабочих дней.",
	}
)

// tail возвращает нейтральное окончание предложения.
func (g *Generator) tail() frag { return lit(g.pick(neutralTails)) }

// anchored собирает фразу «вступление, значение, окончание». Вступление
// работает якорем, по которому детектор обязан узнать значение даже при
// выдуманном номере.
func (g *Generator) anchored(prefixes []string, value []frag) []frag {
	return concat(frags(lit(g.pick(prefixes))), value, frags(g.tail()))
}

// simple собирает фразу с одним якорем и одним значением.
func (g *Generator) simple(prefixes []string, t pii.Type, value string) []frag {
	return g.anchored(prefixes, frags(val(t, value)))
}

// genFIO порождает предложение с именем человека в разных падежах и записях.
func genFIO(g *Generator) []frag {
	p := g.person()
	switch g.r.IntN(3) {
	case 0:
		return g.simple(fioPrefixesNom, pii.TypeFIO, g.fioVariant(p, caseNom))
	case 1:
		return g.simple(fioPrefixesGen, pii.TypeFIO, g.fioVariant(p, caseGen))
	default:
		return g.simple(fioPrefixesDat, pii.TypeFIO, g.fioVariant(p, caseDat))
	}
}

// genDOB порождает предложение с датой рождения.
func genDOB(g *Generator) []frag {
	prefixes := []string{
		"Дата рождения: ", "родился ", "родилась ", "Д. р. ", "дата рожд. ",
		"Клиент рождён ", "Год и дата рождения: ", "ДР: ",
	}
	return g.anchored(prefixes, g.dateFrags(pii.TypeDOB, g.randomDate(1945, 2006)))
}

// genBirthPlace порождает предложение с местом рождения.
func genBirthPlace(g *Generator) []frag {
	prefixes := []string{
		"Место рождения: ", "Родился в ", "Место рожд. ", "МР: ",
		"Место рождения по паспорту: ",
	}
	return g.simple(prefixes, pii.TypeBirthPlace, g.birthPlace())
}

// genPassport порождает предложение с серией и номером паспорта.
func genPassport(g *Generator) []frag {
	prefixes := []string{
		"Паспорт ", "Паспорт РФ ", "Документ: паспорт ", "Паспортные данные: ",
		"Удостоверение личности: паспорт ", "Пасп. ", "Данные документа: ",
	}
	return g.anchored(prefixes, g.passportFrags())
}

// genCitizenship порождает предложение с гражданством.
func genCitizenship(g *Generator) []frag {
	prefixes := []string{"Гражданство: ", "Гражданство клиента — ", "Подданство: ", "Гражданство по паспорту: "}
	return g.simple(prefixes, pii.TypeCitizenship, g.citizenship())
}

// genIssuer порождает предложение с органом выдачи паспорта.
func genIssuer(g *Generator) []frag {
	prefixes := []string{"Паспорт выдан ", "Кем выдан: ", "Орган выдачи: ", "выдан "}
	return g.simple(prefixes, pii.TypeIssuer, g.issuer())
}

// genDeptCode порождает предложение с кодом подразделения.
func genDeptCode(g *Generator) []frag {
	prefixes := []string{"Код подразделения ", "код подразделения: ", "к/п ", "Подразделение: "}
	return g.simple(prefixes, pii.TypeDeptCode, g.deptCode())
}

// genIssueDate порождает предложение с датой выдачи документа.
func genIssueDate(g *Generator) []frag {
	prefixes := []string{"Дата выдачи: ", "выдан ", "Документ выдан ", "Дата выдачи паспорта — "}
	return g.anchored(prefixes, g.dateFrags(pii.TypeIssueDate, g.randomDate(2002, 2024)))
}

// genDriverLicense порождает предложение с водительским удостоверением.
func genDriverLicense(g *Generator) []frag {
	prefixes := []string{
		"Водительское удостоверение ", "в/у ", "Права серии ",
		"ВУ: ", "Номер водительского удостоверения ",
	}
	return g.simple(prefixes, pii.TypeDriverLicense, g.driverLicense())
}

// genAddress порождает предложение с адресом и индексом.
func genAddress(g *Generator) []frag {
	prefixes := []string{
		"Адрес регистрации: ", "Проживает по адресу ", "Адрес доставки: ",
		"Зарегистрирован по адресу: ", "Фактический адрес — ", "Адрес: ",
	}
	return g.anchored(prefixes, g.addressFrags())
}

// genPostcode порождает предложение с почтовым индексом.
func genPostcode(g *Generator) []frag {
	prefixes := []string{"Почтовый индекс: ", "индекс ", "Индекс отправления: ", "Zip: "}
	return g.simple(prefixes, pii.TypePostcode, g.postcode())
}

// genEmail порождает предложение с адресом почты.
func genEmail(g *Generator) []frag {
	prefixes := []string{
		"Электронная почта: ", "e-mail: ", "Почта для связи — ", "Email: ",
		"Отправьте документы на ", "Контактный адрес: ",
	}
	return g.simple(prefixes, pii.TypeEmail, g.email(g.person()))
}

// genPhone порождает предложение с номером телефона.
func genPhone(g *Generator) []frag {
	prefixes := []string{
		"Телефон: ", "Контактный телефон ", "тел. ", "Мобильный: ",
		"Звонить на ", "Номер для связи — ", "Whatsapp: ",
	}
	return g.simple(prefixes, pii.TypePhone, g.phone())
}

// genINN порождает предложение с ИНН человека или организации.
func genINN(g *Generator) []frag {
	prefixes := []string{"ИНН ", "ИНН: ", "Идентификационный номер налогоплательщика ", "инн клиента "}
	return g.simple(prefixes, pii.TypeINN, g.inn(g.chance(60), true))
}

// genCard порождает предложение с номером карты.
func genCard(g *Generator) []frag {
	prefixes := []string{
		"Номер карты ", "Карта ", "Списание с карты ", "PAN: ",
		"Перевод на карту ", "Реквизиты карты: ",
	}
	_, number := g.cardNumber(true)
	return g.simple(prefixes, pii.TypeCard, number)
}

// genCVV порождает предложение с кодом безопасности карты.
func genCVV(g *Generator) []frag {
	prefixes := []string{"CVV ", "CVC2 ", "код безопасности ", "Защитный код: ", "cvv2: ", "три цифры с оборота — "}
	return g.simple(prefixes, pii.TypeCVV, g.digits(3))
}

// genPIN порождает предложение с пин-кодом.
func genPIN(g *Generator) []frag {
	prefixes := []string{"ПИН-код ", "пин ", "PIN: ", "пинкод от карты ", "Новый пин-код: "}
	return g.simple(prefixes, pii.TypePIN, g.digits(4))
}

// genCardHolder порождает предложение с именем держателя карты латиницей.
func genCardHolder(g *Generator) []frag {
	p := g.person()
	name := strings.ToUpper(translit(p.name) + " " + translit(p.surname))
	prefixes := []string{"Держатель карты ", "Cardholder: ", "На карте указано ", "Имя на карте: "}
	return g.simple(prefixes, pii.TypeCardHolder, name)
}

// genSNILS порождает предложение со страховым номером.
func genSNILS(g *Generator) []frag {
	prefixes := []string{"СНИЛС ", "снилс: ", "Страховой номер индивидуального лицевого счёта ", "Номер СНИЛС — "}
	return g.simple(prefixes, pii.TypeSNILS, g.snils(true))
}

// genForeignPassport порождает предложение с заграничным паспортом.
func genForeignPassport(g *Generator) []frag {
	prefixes := []string{"Заграничный паспорт ", "Загранпаспорт: ", "Паспорт для выезда за границу ", "Загран. паспорт "}
	return g.simple(prefixes, pii.TypeForeignPassport, g.foreignPassport())
}

// genResidencePermit порождает предложение с видом на жительство.
func genResidencePermit(g *Generator) []frag {
	prefixes := []string{"Вид на жительство ", "ВНЖ: ", "Документ: вид на жительство № ", "Разрешение на проживание "}
	return g.simple(prefixes, pii.TypeResidencePermit, g.residencePermit())
}

// genBirthCert порождает предложение со свидетельством о рождении.
func genBirthCert(g *Generator) []frag {
	prefixes := []string{"Свидетельство о рождении ", "СоР: ", "Документ ребёнка: свидетельство о рождении "}
	return g.simple(prefixes, pii.TypeBirthCert, g.birthCert())
}

// genMilitaryID порождает предложение с военным билетом.
func genMilitaryID(g *Generator) []frag {
	prefixes := []string{"Военный билет ", "Воинский документ: ", "Военник "}
	return g.simple(prefixes, pii.TypeMilitaryID, g.militaryID())
}

// fieldRow возвращает строку таблицы вида «Поле: значение».
func fieldRow(label string, value []frag) []frag {
	return concat(frags(lit(label+": ")), value, frags(lit("\n")))
}
