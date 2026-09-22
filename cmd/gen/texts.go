package main

import (
	"strings"

	"pii-guard/internal/pii"
)

// Виды текста, отличные от анкеты. Проверяющая система подаёт не только
// заполненные формы: приходят расшифровки разговоров, выдержки из заявлений,
// строки выгрузок, письма и заметки сотрудников. Форма подачи меняет
// окружение значения, а значит и работу контекстных правил.

// requests — просьбы, которыми заканчивается заявление.
var requests = []string{
	"прошу перевыпустить карту в связи с утратой",
	"прошу закрыть счёт и выдать остаток наличными",
	"прошу изменить контактные данные в вашей системе",
	"прошу предоставить справку о состоянии задолженности",
	"прошу расторгнуть договор обслуживания",
	"прошу вернуть ошибочно списанные средства",
	"прошу подключить услугу уведомлений об операциях",
	"прошу пересмотреть отказ в выдаче кредита",
}

// letterSubjects — темы писем клиента в банк.
var letterSubjects = []string{
	"Заявка на перевыпуск карты", "Уточнение данных по счёту",
	"Обращение по операции от прошлой недели", "Запрос справки для визы",
	"Жалоба на списание комиссии", "Подтверждение личности для перевода",
	"Заявление на смену тарифа",
}

// specPick выбирает несколько разных спецификаций подряд, не повторяя типы.
func (g *Generator) specPick(count int) []valueSpec {
	specs := valueSpecs()
	order := g.r.Perm(len(specs))
	if count > len(order) {
		count = len(order)
	}
	out := make([]valueSpec, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, specs[order[i]])
	}
	return out
}

// genDialog порождает расшифровку разговора из двух реплик: оператор
// спрашивает, клиент называет данные.
func genDialog(g *Generator) []frag {
	p := g.person()
	questions := []string{
		"Добрый день, банк на связи. Назовите, пожалуйста, ваши данные.",
		"Здравствуйте! Для идентификации назовите данные из анкеты.",
		"Слушаю вас. Подскажите сведения для проверки.",
		"Добрый вечер, чтобы продолжить, продиктуйте данные.",
		"Оператор на линии. Уточните сведения по заявке.",
	}
	marks := []string{"Оператор: ", "— Оператор: ", "[оператор] ", "О: "}
	answers := []string{"Клиент: ", "— Клиент: ", "[клиент] ", "К: "}
	i := g.r.IntN(len(marks))
	out := frags(lit(marks[i]+g.pick(questions)+nl), lit(answers[i]))
	specs := g.specPick(2 + g.r.IntN(3))
	for k, s := range specs {
		if k > 0 {
			out = append(out, lit(", "))
		}
		out = append(out, g.attributeFor(s, p)...)
	}
	return append(out, lit("."))
}

// genStatement порождает выдержку из письменного заявления: первое лицо,
// перечисление документов и просьба в конце.
func genStatement(g *Generator) []frag {
	p := g.person()
	registered := "зарегистрированный по адресу "
	if p.female {
		registered = "зарегистрированная по адресу "
	}
	out := concat(
		frags(lit("Я, "), val(pii.TypeFIO, p.full(caseNom)), lit(", дата рождения ")),
		g.dateFrags(pii.TypeDOB, g.randomDate(1950, 2004)),
		frags(lit(", паспорт ")),
		g.passportFrags(),
		frags(lit(", выдан ")),
		frags(val(pii.TypeIssuer, g.issuer()), lit(", ")),
		frags(lit(registered)),
		g.addressFrags(),
		frags(lit(", "+g.pick(requests)+".")),
	)
	if g.chance(50) {
		out = append(out, lit(" Контакты для ответа: "))
		out = append(out, val(pii.TypePhone, g.phone()), lit(", "), val(pii.TypeEmail, g.email(p)), lit("."))
	}
	return out
}

// exportSeparators — разделители полей в строке выгрузки. Запятая не
// используется: она встречается внутри адреса.
var exportSeparators = []string{";", "|", "\t", " ; ", "||"}

// genExportRow порождает строку выгрузки: значения подряд через разделитель,
// без единого якоря. Тип приходится определять по форме значения.
func genExportRow(g *Generator) []frag {
	p := g.person()
	sep := g.pick(exportSeparators)
	specs := g.specPick(4 + g.r.IntN(4))
	out := make([]frag, 0, len(specs)*3)
	if g.chance(40) {
		out = append(out, lit(g.digitsNonZero(6)+sep))
	}
	for i, s := range specs {
		if i > 0 {
			out = append(out, lit(sep))
		}
		out = append(out, s.value(g, p)...)
	}
	return append(out, lit(sep+g.pick([]string{"OK", "ACTIVE", "new", "1", "closed"})))
}

// tableFieldCount — число полей в таблице из заголовка и строк.
const tableFieldCount = 5

// genTableFive порождает таблицу ровно из пяти полей с заголовком и рамкой.
func genTableFive(g *Generator) []frag {
	p := g.person()
	specs := g.specPick(tableFieldCount)
	style := g.r.IntN(3)
	out := make([]frag, 0, tableFieldCount*4)
	switch style {
	case 0:
		out = append(out, lit("| Поле | Значение |"+nl+"|---|---|"+nl))
	case 1:
		out = append(out, lit("Поле\tЗначение"+nl))
	default:
		out = append(out, lit("Карточка клиента"+nl))
	}
	for _, s := range specs {
		switch style {
		case 0:
			out = append(out, lit("| "+s.label+" | "))
			out = append(out, s.value(g, p)...)
			out = append(out, lit(" |"+nl))
		case 1:
			out = append(out, lit(s.label+"\t"))
			out = append(out, s.value(g, p)...)
			out = append(out, lit(nl))
		default:
			out = append(out, fieldRow(s.label, s.value(g, p))...)
		}
	}
	return out
}

// genEmailLetter порождает электронное письмо с темой, телом и подписью.
// Подпись — отдельный случай: имя и контакты идут списком в конце.
func genEmailLetter(g *Generator) []frag {
	p := g.person()
	out := frags(
		lit("Тема: "+g.pick(letterSubjects)+nl),
		lit("От кого: "), val(pii.TypeEmail, g.email(p)), lit(nl+nl),
		lit(g.pick([]string{"Здравствуйте!", "Добрый день!", "Уважаемые коллеги!"})+nl),
		lit(upperFirst(g.pick(requests))+". Мои данные: "),
	)
	specs := g.specPick(2 + g.r.IntN(3))
	for i, s := range specs {
		if i > 0 {
			out = append(out, lit(", "))
		}
		out = append(out, g.attributeFor(s, p)...)
	}
	out = append(out, lit("."+nl+nl))
	out = append(out, lit(g.pick([]string{"С уважением,", "Спасибо,", "С наилучшими пожеланиями,"})+nl))
	out = append(out, val(pii.TypeFIO, p.full(caseNom)), lit(nl))
	out = append(out, lit("тел. "), val(pii.TypePhone, g.phone()), lit(nl))
	out = append(out, val(pii.TypeEmail, g.email(p)))
	return out
}

// noteOpenings — начала заметки сотрудника, написанной для себя.
var noteOpenings = []string{
	"перезвонить ", "не забыть проверить ", "клиент просил связаться, ",
	"вчера приходил ", "по заявке вопрос, ", "уточнить у ",
	"записал со слов: ", "в чате скинули ",
}

// genFreeNote порождает заметку в свободной форме: строчные буквы, мало знаков
// препинания, якоря встречаются не всегда.
func genFreeNote(g *Generator) []frag {
	p := g.person()
	out := frags(lit(g.pick(noteOpenings)))
	out = append(out, val(pii.TypeFIO, strings.ToLower(g.fioVariant(p, g.anyCase()))))
	specs := g.specPick(2 + g.r.IntN(3))
	for _, s := range specs {
		out = append(out, lit(g.pick([]string{" ", ", ", " и ", " — "})))
		if g.chance(55) {
			out = append(out, lit(lowerFirst(g.anchor(s))+space))
		}
		out = append(out, s.value(g, p)...)
	}
	return append(out, lit(g.pick([]string{"", " срочно", " до конца дня", " перезвонит сам"})))
}

// genTwoPeople порождает одно предложение с двумя разными людьми: типичный
// перевод от одного человека другому.
func genTwoPeople(g *Generator) []frag {
	from, to := g.person(), g.person()
	tmpl := g.r.IntN(4)
	switch tmpl {
	case 0:
		return concat(
			frags(lit("Перевод от "), val(pii.TypeFIO, g.fioVariant(from, caseGen)), lit(" получателю ")),
			frags(val(pii.TypeFIO, g.fioVariant(to, caseDat)), lit(" на карту ")),
			g.cardFrags(), frags(lit(".")),
		)
	case 1:
		return concat(
			frags(lit("Доверенность выдана "), val(pii.TypeFIO, g.fioVariant(from, caseDat)), lit(" от имени ")),
			frags(val(pii.TypeFIO, g.fioVariant(to, caseGen)), lit(", паспорт ")),
			g.passportFrags(), frags(lit(".")),
		)
	case 2:
		return concat(
			frags(lit("В отделение пришли "), val(pii.TypeFIO, g.fioVariant(from, caseNom)), lit(" и ")),
			frags(val(pii.TypeFIO, g.fioVariant(to, caseNom)), lit(", телефоны ")),
			frags(val(pii.TypePhone, g.phone()), lit(" и "), val(pii.TypePhone, g.phone()), lit(".")),
		)
	default:
		return concat(
			frags(lit("Созаёмщики по договору: "), val(pii.TypeFIO, g.fioVariant(from, caseNom)), lit(" (ИНН ")),
			frags(val(pii.TypeINN, g.inn(true, true)), lit(") и "), val(pii.TypeFIO, g.fioVariant(to, caseNom))),
			frags(lit(" (ИНН "), val(pii.TypeINN, g.inn(true, true)), lit(").")),
		)
	}
}

// cardFrags возвращает номер карты как размечаемое значение.
func (g *Generator) cardFrags() []frag {
	return one(pii.TypeCard, g.cardNumber(true))
}

// genThreeMentions порождает текст, где один и тот же человек назван трижды в
// разных падежах. Проверяется, что маскируются все упоминания, а не первое.
func genThreeMentions(g *Generator) []frag {
	p := g.person()
	return concat(
		frags(lit("Заявление принято от гражданина: "), val(pii.TypeFIO, g.fioVariant(p, caseNom)), lit(". Паспорт ")),
		frags(val(pii.TypeFIO, g.fioVariant(p, caseGen)), lit(" приложен к обращению: ")),
		g.passportFrags(),
		frags(lit(". Ответ направлен "), val(pii.TypeFIO, g.fioVariant(p, caseDat)), lit(" на почту ")),
		frags(val(pii.TypeEmail, g.email(p)), lit(".")),
	)
}

// genBilingual порождает текст сразу на двух языках: так выглядят анкеты для
// международных переводов и переписка с зарубежным отделением.
func genBilingual(g *Generator) []frag {
	p := g.person()
	latin := titleLatin(p.name) + space + titleLatin(p.surname)
	switch g.r.IntN(3) {
	case 0:
		return concat(
			frags(lit("Клиент "), val(pii.TypeFIO, p.full(caseNom)), lit(" / ")),
			frags(val(pii.TypeFIO, latin), lit(", address: ")),
			frags(val(pii.TypeAddress, g.addressLatin()), lit(", телефон ")),
			frags(val(pii.TypePhone, g.phone()), lit(".")),
		)
	case 1:
		return concat(
			frags(lit("Please verify the client: ")),
			frags(val(pii.TypeFIO, latin), lit(" (по-русски ")),
			frags(val(pii.TypeFIO, p.full(caseNom)), lit("), passport ")),
			g.passportFrags(),
			frags(lit(", дата рождения ")),
			g.dateFrags(pii.TypeDOB, g.randomDate(1950, 2004)),
			frags(lit(".")),
		)
	default:
		return concat(
			frags(lit("Beneficiary name: "), val(pii.TypeCardHolder, g.holderName(p)), lit(nl)),
			frags(lit("Получатель: "), val(pii.TypeFIO, p.full(caseNom)), lit(nl)),
			frags(lit("Card / карта: "), val(pii.TypeCard, g.cardNumber(true)), lit(nl)),
			frags(lit("E-mail: "), val(pii.TypeEmail, g.email(p))),
		)
	}
}
