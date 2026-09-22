package main

import (
	"fmt"
	"strings"
)

// Отрицательные категории. Текст в них состоит только из обрамляющих кусков,
// поэтому разметка пустая. Эти примеры нужны, чтобы измерять ложные
// срабатывания: жюри отдельно проверяет, что суммы, номера заказов, известные
// люди и адреса отделений банка остаются нетронутыми.

// negFamous порождает упоминание известного человека с ролью. Публичная
// персона в справочном тексте не является персональными данными клиента.
func negFamous(g *Generator) []frag {
	f := famousPeople[g.r.IntN(len(famousPeople))]
	tmpl := []string{
		"В холле отделения висит портрет: %s %s.",
		"Лекция посвящена теме «%s %s и его эпоха».",
		"Викторина: кем был %s %s?",
		"На памятной монете изображён %s %s.",
		"Книга рассказывает про то, как жил %s %s.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), f.role, f.name)))
}

// negMemorialStreet порождает упоминание улицы, названной в честь человека.
// Фамилия в названии улицы не указывает на клиента.
func negMemorialStreet(g *Generator) []frag {
	tmpl := []string{
		"Ближайшее отделение находится на %s.",
		"Маршрут автобуса проходит через %s.",
		"Ремонт дороги затронет %s.",
		"Новый филиал откроется на %s в следующем квартале.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), g.pick(memorialStreets))))
}

// negBankOffice порождает адрес отделения банка: это адрес организации, а не
// адрес проживания клиента.
func negBankOffice(g *Generator) []frag {
	tmpl := []string{
		"Отделение банка: %s. Режим работы с 9:00 до 20:00.",
		"Документы можно подать в офисе по адресу %s.",
		"Банкомат расположен по адресу %s.",
		"Головной офис: %s.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), g.pick(bankOffices))))
}

// negOrgRequisites порождает реквизиты организации: расчётный счёт, БИК, КПП и
// ОГРН относятся к юридическому лицу.
func negOrgRequisites(g *Generator) []frag {
	text := fmt.Sprintf(
		"%s, КПП %s, ОГРН %s, р/с %s в банке, БИК %s.",
		g.pick(orgNames), g.digitsNonZero(9), g.digitsNonZero(13),
		"407028"+g.digits(14), "0445"+g.digits(5),
	)
	return frags(lit(text))
}

// negMoney порождает денежную сумму: число рядом со словом о деньгах не
// является персональными данными.
func negMoney(g *Generator) []frag {
	amount := fmt.Sprintf("%d %03d,%02d", 1+g.r.IntN(900), g.r.IntN(1000), g.r.IntN(100))
	if g.chance(25) {
		return frags(lit(fmt.Sprintf("Оплата за %s принята, сумма %s руб.", g.pick(goods), amount)))
	}
	tmpl := []string{
		"Сумма перевода %s руб. списана со счёта.",
		"Стоимость услуги составляет %s рублей.",
		"Остаток на счёте: %s ₽.",
		"Комиссия за операцию удержана, итого %s руб.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), amount)))
}

// negOrder порождает номер заказа или договора: длинное число рядом со словом
// «заказ» похоже на паспорт или карту, но персональными данными не является.
func negOrder(g *Generator) []frag {
	tmpl := []string{
		"Номер заказа %s, статус «в пути».",
		"Договор № %s от прошлого года продлён автоматически.",
		"Обращение зарегистрировано под номером %s.",
		"Накладная %s передана в бухгалтерию.",
		"Номер операции в чеке: %s.",
	}
	numbers := []string{
		g.digitsNonZero(10), g.digitsNonZero(4) + " " + g.digits(6),
		g.digitsNonZero(2) + "-" + g.digits(4) + "/" + g.digits(2),
		g.digitsNonZero(12), g.digitsNonZero(16),
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), g.pick(numbers))))
}

// negPaymentDate порождает дату платежа: дата без связи с человеком не
// является персональными данными.
func negPaymentDate(g *Generator) []frag {
	d := g.randomDate(2018, 2026)
	tmpl := []string{
		"Платёж проведён %s, средства зачислены на следующий рабочий день.",
		"Срок оплаты по счёту — до %s.",
		"Следующее списание по подписке состоится %s.",
		"Отчётный период закрыт %s.",
		"Заявка создана %s и передана оператору.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), d.format(g.r.IntN(formatCount)))))
}

// negCardExpiry порождает срок действия карты: месяц и год не являются
// персональными данными сами по себе.
func negCardExpiry(g *Generator) []frag {
	exp := fmt.Sprintf("%02d/%02d", 1+g.r.IntN(12), 25+g.r.IntN(6))
	tmpl := []string{
		"Срок действия карты %s, перевыпуск произойдёт автоматически.",
		"Карта действительна до %s.",
		"На лицевой стороне указан срок %s.",
		"Valid thru %s.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), exp)))
}

// negSMSCode порождает одноразовый код из сообщения: он живёт несколько минут
// и не является персональными данными.
func negSMSCode(g *Generator) []frag {
	tmpl := []string{
		"Код из СМС: %s. Никому не сообщайте его.",
		"Одноразовый код подтверждения %s действует пять минут.",
		"Код для входа в приложение — %s.",
		"Введите код %s на странице подтверждения.",
	}
	code := g.digits(4 + g.r.IntN(3))
	return frags(lit(fmt.Sprintf(g.pick(tmpl), code)))
}

// negNetwork порождает сетевые адреса: они относятся к оборудованию.
func negNetwork(g *Generator) []frag {
	ip := fmt.Sprintf("%d.%d.%d.%d", 10+g.r.IntN(200), g.r.IntN(256), g.r.IntN(256), 1+g.r.IntN(254))
	tmpl := []string{
		"Запрос пришёл с адреса %s, регион определён по сети.",
		"Сервер приложения доступен по %s:8080.",
		"В журнале зафиксирован вход с %s.",
		"Балансировщик перенаправил трафик на узел %s.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), ip)))
}

// negVersion порождает номер версии или сборки.
func negVersion(g *Generator) []frag {
	v := fmt.Sprintf("%d.%d.%d", g.r.IntN(10), g.r.IntN(30), g.r.IntN(20))
	tmpl := []string{
		"Обновите приложение до версии %s.",
		"Сборка %s выложена в магазин приложений.",
		"В релизе %s исправлена ошибка входа.",
		"Версия ядра %s, протокол обмена не менялся.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), v)))
}

// negLaw порождает ссылку на закон: номер закона похож на номер документа, но
// персональными данными не является.
func negLaw(g *Generator) []frag {
	laws := []string{
		"Федеральный закон № 152-ФЗ «О персональных данных»",
		"ФЗ-115 о противодействии отмыванию доходов",
		"статья 9 ФЗ-152", "постановление Правительства № 1119",
		"Указание Банка России № 5599-У", "статья 857 ГК РФ",
	}
	tmpl := []string{
		"Обработка ведётся в соответствии с требованиями: %s.",
		"Основание отказа — %s.",
		"Согласие оформлено по форме, предусмотренной документом %s.",
		"Подробнее смотрите %s.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), g.pick(laws))))
}

// negPINWord порождает упоминание пин-кода без самого кода: якорь есть,
// значения нет, маскировать нечего.
func negPINWord(g *Generator) []frag {
	tmpl := []string{
		"Введите пин на терминале и дождитесь чека.",
		"Пин-код не сообщайте сотрудникам банка ни при каких условиях.",
		"Если вы забыли пин, закажите его повторно в приложении.",
		"Сменить пин можно в любом банкомате банка.",
		"PIN is required for contactless payments above the limit.",
	}
	return frags(lit(g.pick(tmpl)))
}

// negThreeDigits порождает упоминание кода безопасности без числа.
func negThreeDigits(g *Generator) []frag {
	tmpl := []string{
		"Назовите три цифры с оборотной стороны карты для подтверждения.",
		"Код безопасности напечатан на обороте карты рядом с подписью.",
		"Сотрудник банка никогда не спрашивает cvv по телефону.",
		"Три цифры с обратной стороны нужны только при оплате в интернете.",
	}
	return frags(lit(g.pick(tmpl)))
}

// negSupportPhone порождает телефон службы поддержки банка. Справочный номер
// организации печатают в каждом письме, и маскировать его нельзя.
func negSupportPhone(g *Generator) []frag {
	tmpl := []string{
		"Телефон службы поддержки банка: %s, звонок по России бесплатный.",
		"По вопросам обслуживания звоните на %s.",
		"Горячая линия %s работает круглосуточно.",
		"Если карта утеряна, наберите %s с любого телефона.",
		"Контакт-центр: %s, среднее время ожидания — две минуты.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), g.pick(supportPhones))))
}

// negBankRequisites порождает подпись письма с ИНН и КПП самого банка. Это
// реквизиты организации, а не налоговый номер клиента.
func negBankRequisites(g *Generator) []frag {
	text := fmt.Sprintf(
		"С уважением, %s. ИНН %s, КПП %s, лицензия Банка России № %s.",
		g.pick(bankNames), g.digitsNonZero(10), g.digitsNonZero(9), g.digitsNonZero(4),
	)
	if g.chance(40) {
		text = fmt.Sprintf(
			"Реквизиты банка: %s, ИНН %s, КПП %s, БИК %s.",
			g.pick(bankNames), g.digitsNonZero(10), g.digitsNonZero(9), "0445"+g.digits(5),
		)
	}
	return frags(lit(text))
}

// negBranchAddressTail порождает адрес отделения в конце письма: подпись
// организации, а не адрес проживания клиента.
func negBranchAddressTail(g *Generator) []frag {
	tmpl := []string{
		"Благодарим за обращение.\n%s, адрес отделения: %s.",
		"Ответ подготовлен отделением банка.\nАдрес для визита: %s, %s.",
		"Ждём вас в офисе.\n%s\n%s\nРежим работы: будни с 9 до 20.",
		"Документы можно забрать лично.\n%s, %s.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), g.pick(bankNames), g.pick(bankOffices))))
}

// negRateDate порождает дату курса валют: дата справочного показателя не
// связана с человеком.
func negRateDate(g *Generator) []frag {
	d := g.randomDate(2019, 2026)
	rate := fmt.Sprintf("%d,%02d", 30+g.r.IntN(70), g.r.IntN(100))
	tmpl := []string{
		"Курс %s на %s составляет %s рубля.",
		"На %s официальный курс %s установлен на уровне %s.",
		"Котировка %s от %s: %s.",
		"Курс продажи %s действует с %s и равен %s.",
	}
	t := g.pick(tmpl)
	// Только в одном шаблоне дата стоит первой, остальные начинаются с валюты.
	if strings.HasPrefix(t, "На %s") {
		return frags(lit(fmt.Sprintf(t, d.format(g.r.IntN(formatCount)), g.pick(currencyPairs), rate)))
	}
	return frags(lit(fmt.Sprintf(t, g.pick(currencyPairs), d.format(g.r.IntN(formatCount)), rate)))
}

// negBranchNumber порождает номер отделения банка.
func negBranchNumber(g *Generator) []frag {
	num := fmt.Sprintf("%04d/%04d", 1+g.r.IntN(9000), 1+g.r.IntN(9000))
	if g.chance(40) {
		num = fmt.Sprintf("№ %d", 1+g.r.IntN(9000))
	}
	tmpl := []string{
		"Счёт открыт в отделении %s.",
		"Обратитесь в отделение %s, там есть сейфовые ячейки.",
		"Отделение %s временно не обслуживает юридических лиц.",
		"Документы переданы в отделение %s для хранения.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), num)))
}

// negAccountNumber порождает номер счёта без персональных данных рядом.
// Двадцать цифр похожи на связку карты и паспорта, но это реквизит счёта.
func negAccountNumber(g *Generator) []frag {
	acc := "408178" + g.digits(14)
	if g.chance(35) {
		acc = "40817" + g.digits(3) + " " + g.digits(4) + " " + g.digits(8)
	}
	tmpl := []string{
		"Средства зачислены на счёт %s.",
		"Номер счёта для пополнения: %s.",
		"Счёт %s закрыт по заявлению, остаток переведён.",
		"В платёжном поручении укажите счёт %s.",
		"Корреспондентский счёт %s используется для межбанковских переводов.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), acc)))
}

// negCompanyLikeSurname порождает название компании, образованное от фамилии.
// Фамилия в наименовании юридического лица не является персональными данными.
func negCompanyLikeSurname(g *Generator) []frag {
	tmpl := []string{
		"Договор заключён с %s на поставку оборудования.",
		"Подрядчиком выступает %s.",
		"Счёт выставлен от %s, оплата в течение десяти дней.",
		"%s подтвердила готовность выполнить работы.",
		"Аудит провела %s по заказу банка.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), g.pick(companyLikeSurname))))
}

// negNamedPosition порождает должность или звание, в название которых входит
// фамилия известного человека.
func negNamedPosition(g *Generator) []frag {
	tmpl := []string{
		"Приглашённый эксперт — %s.",
		"На конференции выступит %s.",
		"Программу ведёт %s.",
		"Отзыв подготовил %s.",
		"Комиссию возглавляет %s.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), g.pick(namedPositions))))
}

// negLawQuote порождает цитату из закона с номером и датой принятия. Номер и
// дата нормативного акта похожи на реквизиты документа человека.
func negLawQuote(g *Generator) []frag {
	d := g.randomDate(1995, 2024)
	acts := []string{
		"Федеральный закон от %s № 152-ФЗ",
		"Федеральный закон от %s № 115-ФЗ",
		"Постановление Правительства РФ от %s № 1119",
		"Указание Банка России от %s № 5599-У",
		"Приказ ФНС России от %s № ММВ-7-14/502@",
	}
	act := fmt.Sprintf(g.pick(acts), d.format(g.r.IntN(formatCount)))
	tmpl := []string{
		"Согласно документу %s обработка данных допускается с согласия субъекта.",
		"В соответствии с актом %s банк обязан хранить сведения пять лет.",
		"Основание: %s.",
		"Цитата: «оператор обязан обеспечить конфиденциальность» — %s.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), act)))
}

// negArticleCode порождает артикул товара из букв и цифр.
func negArticleCode(g *Generator) []frag {
	tmpl := []string{
		"Артикул товара %s, остаток на складе четыре штуки.",
		"В чеке указан код позиции %s.",
		"Товар %s снят с продажи.",
		"Проверьте артикул %s перед оформлением возврата.",
		"Позиция %s входит в акцию до конца месяца.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), g.pick(articleCodes))))
}

// negConfirmCode порождает код подтверждения из сообщения в приложении.
func negConfirmCode(g *Generator) []frag {
	code := g.digits(4 + g.r.IntN(3))
	tmpl := []string{
		"Push-уведомление: код подтверждения операции %s.",
		"Бот прислал код %s, введите его в форме.",
		"Проверочный код %s истекает через десять минут.",
		"Для подтверждения входа используйте код %s.",
		"Код подтверждения платежа: %s. Не передавайте его третьим лицам.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), code)))
}

// negFlightNumber порождает номер авиарейса: буквы и цифры рядом с датой
// похожи на реквизиты документа.
func negFlightNumber(g *Generator) []frag {
	flight := fmt.Sprintf("%s %d", g.pick(airlineCodes), 100+g.r.IntN(9000))
	tmpl := []string{
		"Рейс %s вылетает по расписанию.",
		"Посадка на рейс %s завершится за двадцать минут до вылета.",
		"Билет оформлен на рейс %s, выход у стойки регистрации.",
		"Рейс %s задержан на полтора часа.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), flight)))
}

// negTrackNumber порождает почтовый трек-номер отправления.
func negTrackNumber(g *Generator) []frag {
	track := fmt.Sprintf("%s%sRU", g.pick(trackPrefixes), g.digits(9))
	if g.chance(35) {
		track = g.digits(14)
	}
	tmpl := []string{
		"Трек-номер отправления %s, посылка в пути.",
		"Отследить доставку карты можно по номеру %s.",
		"Почтовый идентификатор %s присвоен отправлению.",
		"Документы отправлены заказным письмом, трек %s.",
	}
	return frags(lit(fmt.Sprintf(g.pick(tmpl), track)))
}
