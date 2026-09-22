package main

import (
	"strings"

	"pii-guard/internal/pii"
)

// valueSpec описывает один тип персональных данных: как его называют в
// текстах (якоря и подпись поля) и как порождается само значение.
// Из спецификаций собираются и простые предложения, и анкеты, и письма:
// один источник правды вместо повторяющихся шаблонов.
type valueSpec struct {
	// name — имя категории набора для простых предложений.
	name string
	// typ — тип персональных данных по перечню технического задания.
	typ pii.Type
	// label — подпись поля в таблице, строке выгрузки и письме.
	label string
	// anchors — слова перед значением, по которым его узнают.
	anchors []string
	// value порождает размечаемое значение. Человек передаётся, потому что
	// почта, имя держателя карты и само имя должны относиться к одному лицу.
	value func(g *Generator, p person) []frag
}

// anchor выбирает случайный якорь спецификации.
func (g *Generator) anchor(s valueSpec) string { return g.pick(s.anchors) }

// one — короткая запись значения из одного фрагмента.
func one(t pii.Type, text string) []frag { return frags(val(t, text)) }

// valueSpecs перечисляет все типы персональных данных набора. Порядок
// фиксирован: от него зависит воспроизводимость выпуска по seed.
func valueSpecs() []valueSpec {
	out := personSpecs()
	out = append(out, documentSpecs()...)
	out = append(out, contactSpecs()...)
	return append(out, cardSpecs()...)
}

// personSpecs — типы, описывающие самого человека.
func personSpecs() []valueSpec {
	return []valueSpec{
		{
			name: "fio", typ: pii.TypeFIO, label: "ФИО",
			anchors: []string{
				"ФИО", "Клиент", "Заявитель", "Держатель счёта", "Плательщик",
				"Получатель", "Представитель", "Ответственный сотрудник",
				"Контактное лицо", "Ф. И. О.", "Владелец счёта",
				"Фамилия, имя, отчество", "На приём записан", "Анкету заполнил",
			},
			value: func(g *Generator, p person) []frag {
				return one(pii.TypeFIO, g.fioVariant(p, g.anyCase()))
			},
		},
		{
			name: "dob", typ: pii.TypeDOB, label: "Дата рождения",
			anchors: []string{
				"Дата рождения", "Д. р.", "ДР", "дата рожд.", "Год рождения",
				"Родился", "Родилась", "Дата рождения клиента", "Рождён",
				"Дата рождения по паспорту", "Дата появления на свет",
			},
			value: func(g *Generator, _ person) []frag {
				return g.dateFrags(pii.TypeDOB, g.randomDate(1945, 2006))
			},
		},
		{
			name: "birth_place", typ: pii.TypeBirthPlace, label: "Место рождения",
			anchors: []string{
				"Место рождения", "Место рожд.", "МР", "Родился в",
				"Место рождения по паспорту", "Населённый пункт рождения",
				"Место рождения заявителя", "Страна и город рождения",
				"Место рождения ребёнка", "Родина",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeBirthPlace, g.birthPlace())
			},
		},
		{
			name: "citizenship", typ: pii.TypeCitizenship, label: "Гражданство",
			anchors: []string{
				"Гражданство", "Подданство", "Гражданство клиента",
				"Гражданство по паспорту", "Страна гражданства",
				"Гражданство заявителя", "Гражданская принадлежность",
				"Nationality", "Гражданство владельца счёта",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeCitizenship, g.citizenship())
			},
		},
		{
			name: "cardholder", typ: pii.TypeCardHolder, label: "Держатель карты",
			anchors: []string{
				"Держатель карты", "Cardholder", "На карте указано",
				"Имя на карте", "Владелец карты", "Cardholder name",
				"Эмбоссировано", "Имя держателя", "Card holder",
				"Имя латиницей на карте",
			},
			value: func(g *Generator, p person) []frag {
				return one(pii.TypeCardHolder, g.holderName(p))
			},
		},
	}
}

// documentSpecs — типы, описывающие документы человека.
func documentSpecs() []valueSpec {
	return []valueSpec{
		{
			name: "passport", typ: pii.TypePassport, label: "Паспорт",
			anchors: []string{
				"Паспорт", "Паспорт РФ", "Паспортные данные", "Документ: паспорт",
				"Удостоверение личности", "Пасп.", "Серия и номер паспорта",
				"Данные документа", "Паспорт гражданина РФ", "Основной документ",
				"Реквизиты паспорта",
			},
			value: func(g *Generator, _ person) []frag { return g.passportFrags() },
		},
		{
			name: "issuer", typ: pii.TypeIssuer, label: "Кем выдан",
			anchors: []string{
				"Паспорт выдан", "Кем выдан", "Орган выдачи", "Выдан",
				"Кем выдан документ", "Наименование органа выдачи",
				"Подразделение выдачи", "Выдавший орган", "Выдан органом",
				"Кем оформлен документ",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeIssuer, g.issuer())
			},
		},
		{
			name: "dept_code", typ: pii.TypeDeptCode, label: "Код подразделения",
			anchors: []string{
				"Код подразделения", "к/п", "Подразделение", "Код подр.",
				"Код органа выдачи", "Код подразделения ОВД", "КП",
				"Код подразделения по паспорту", "Номер подразделения выдачи",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeDeptCode, g.deptCode())
			},
		},
		{
			name: "issue_date", typ: pii.TypeIssueDate, label: "Дата выдачи",
			anchors: []string{
				"Дата выдачи", "Выдан", "Документ выдан", "Дата выдачи паспорта",
				"Дата оформления", "Дата выдачи документа", "Выдано",
				"Дата выдачи по документу", "Оформлен", "Дата начала действия",
			},
			value: func(g *Generator, _ person) []frag {
				return g.dateFrags(pii.TypeIssueDate, g.randomDate(2002, 2024))
			},
		},
		{
			name: "driver_license", typ: pii.TypeDriverLicense, label: "Водительское удостоверение",
			anchors: []string{
				"Водительское удостоверение", "в/у", "ВУ", "Права серии",
				"Номер водительского удостоверения", "Водительские права",
				"Удостоверение водителя", "ВУ серии", "Права", "Номер прав",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeDriverLicense, g.driverLicense())
			},
		},
		{
			name: "snils", typ: pii.TypeSNILS, label: "СНИЛС",
			anchors: []string{
				"СНИЛС", "снилс", "Страховой номер индивидуального лицевого счёта",
				"Номер СНИЛС", "Страховое свидетельство", "СНИЛС клиента",
				"Пенсионное свидетельство", "Номер лицевого счёта в ПФР",
				"Страховой номер",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeSNILS, g.snils(true))
			},
		},
		{
			name: "foreign_passport", typ: pii.TypeForeignPassport, label: "Загранпаспорт",
			anchors: []string{
				"Заграничный паспорт", "Загранпаспорт", "Паспорт для выезда за границу",
				"Загран. паспорт", "Номер загранпаспорта", "Загран",
				"Заграничный паспорт гражданина РФ", "Паспорт для поездок",
				"Документ для выезда",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeForeignPassport, g.foreignPassport())
			},
		},
		{
			name: "residence_permit", typ: pii.TypeResidencePermit, label: "Вид на жительство",
			anchors: []string{
				"Вид на жительство", "ВНЖ", "Документ: вид на жительство",
				"Разрешение на проживание", "Номер ВНЖ", "Вид на жительство в РФ",
				"ВНЖ серии", "Удостоверение ВНЖ", "Документ иностранного гражданина",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeResidencePermit, g.residencePermit())
			},
		},
		{
			name: "birth_cert", typ: pii.TypeBirthCert, label: "Свидетельство о рождении",
			anchors: []string{
				"Свидетельство о рождении", "СоР", "Св-во о рождении",
				"Документ ребёнка: свидетельство о рождении", "Свидетельство",
				"Номер свидетельства о рождении", "Актовая запись о рождении",
				"Свидетельство о рождении ребёнка", "Детский документ",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeBirthCert, g.birthCert())
			},
		},
		{
			name: "military_id", typ: pii.TypeMilitaryID, label: "Военный билет",
			anchors: []string{
				"Военный билет", "Воинский документ", "Военник",
				"Номер военного билета", "Военный билет серии",
				"Удостоверение военнослужащего", "Билет военнообязанного",
				"Воинский учётный документ", "Военно-учётный документ",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeMilitaryID, g.militaryID())
			},
		},
	}
}

// contactSpecs — типы, по которым с человеком связываются.
func contactSpecs() []valueSpec {
	return []valueSpec{
		{
			name: "address", typ: pii.TypeAddress, label: "Адрес регистрации",
			anchors: []string{
				"Адрес регистрации", "Адрес", "Проживает по адресу",
				"Адрес доставки", "Фактический адрес", "Зарегистрирован по адресу",
				"Адрес проживания", "Место жительства", "Адрес по прописке",
				"Почтовый адрес клиента", "Адрес для корреспонденции",
			},
			value: func(g *Generator, _ person) []frag { return g.addressFrags() },
		},
		{
			name: "postcode", typ: pii.TypePostcode, label: "Индекс",
			anchors: []string{
				"Почтовый индекс", "Индекс", "Индекс отправления", "Zip",
				"Почтовый код", "Индекс получателя", "Индекс адреса",
				"Postal code", "Индекс по прописке", "Почтовый индекс клиента",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypePostcode, g.postcode())
			},
		},
		{
			name: "email", typ: pii.TypeEmail, label: "E-mail",
			anchors: []string{
				"Электронная почта", "e-mail", "Email", "Почта для связи",
				"Контактный адрес", "Адрес электронной почты", "Почта",
				"E-mail для уведомлений", "Отправьте документы на",
				"Ящик клиента", "Электронный адрес",
			},
			value: func(g *Generator, p person) []frag {
				return one(pii.TypeEmail, g.email(p))
			},
		},
		{
			name: "phone", typ: pii.TypePhone, label: "Телефон",
			anchors: []string{
				"Телефон", "Контактный телефон", "тел.", "Мобильный",
				"Номер для связи", "Whatsapp", "Сотовый", "Телефон клиента",
				"Номер телефона", "Звонить на", "Телефон для связи",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypePhone, g.phone())
			},
		},
	}
}

// cardSpecs — типы, относящиеся к платёжным реквизитам и налоговому учёту.
func cardSpecs() []valueSpec {
	return []valueSpec{
		{
			name: "inn", typ: pii.TypeINN, label: "ИНН",
			anchors: []string{
				"ИНН", "Идентификационный номер налогоплательщика", "ИНН клиента",
				"инн", "Налоговый номер", "ИНН физлица", "ИНН заявителя",
				"Номер ИНН", "ИНН получателя", "Налоговый идентификатор",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeINN, g.inn(g.chance(60), true))
			},
		},
		{
			name: "card", typ: pii.TypeCard, label: "Номер карты",
			anchors: []string{
				"Номер карты", "Карта", "Списание с карты", "PAN",
				"Перевод на карту", "Реквизиты карты", "Карта получателя",
				"Номер пластиковой карты", "Карта отправителя", "Card number",
			},
			value: func(g *Generator, _ person) []frag {
				number := g.cardNumber(true)
				return one(pii.TypeCard, number)
			},
		},
		{
			name: "cvv", typ: pii.TypeCVV, label: "CVV",
			anchors: []string{
				"CVV", "CVC2", "Код безопасности", "Защитный код", "cvv2",
				"Три цифры с оборота", "CVV/CVC", "Проверочный код карты",
				"Код с оборотной стороны", "CVC",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypeCVV, g.digits(3))
			},
		},
		{
			name: "pin", typ: pii.TypePIN, label: "ПИН-код",
			anchors: []string{
				"ПИН-код", "пин", "PIN", "Пинкод от карты", "Новый пин-код",
				"Секретный код карты", "ПИН", "Код доступа к карте",
				"Пин для банкомата", "PIN-code",
			},
			value: func(g *Generator, _ person) []frag {
				return one(pii.TypePIN, g.digits(4))
			},
		},
	}
}

// holderName возвращает имя держателя карты латиницей заглавными буквами.
func (g *Generator) holderName(p person) string {
	name := translit(p.name) + space + translit(p.surname)
	if g.chance(25) {
		name = translit(p.surname) + space + translit(p.name)
	}
	return strings.ToUpper(name)
}

// anyCase возвращает случайный падеж записи имени.
func (g *Generator) anyCase() gcase {
	switch g.r.IntN(3) {
	case 0:
		return caseGen
	case 1:
		return caseDat
	default:
		return caseNom
	}
}

// specByName ищет спецификацию по имени категории.
func specByName(name string) (valueSpec, bool) {
	for _, s := range valueSpecs() {
		if s.name == name {
			return s, true
		}
	}
	return valueSpec{}, false
}
