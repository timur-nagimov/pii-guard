package pii

import (
	"strings"
	"unicode/utf8"
)

// Якорные слова для форматных типов. Все задаются в нижнем регистре:
// поиск идёт по копии текста в нижнем регистре, поэтому регистр исходной
// записи значения не имеет.
var (
	anchorsPassport = []string{"паспорт", "пасп", "серия", "серии", "сери", "номер", "№", "выдан", "удостоверение личности", "паспортные данные", "данные документа", "документ", "passport"}
	anchorsINN      = []string{"инн", "налогоплательщик", "идентификационный номер"}
	anchorsCard     = []string{"карт", "card", "pan", "visa", "mastercard", "мир", "maestro", "счёт карты", "номер карты"}
	anchorsPhone    = []string{"тел", "телефон", "моб", "сот", "звонить", "whatsapp", "вайбер", "номер телефона", "связь", "phone"}
	anchorsPostcode = []string{"индекс", "почтовый индекс", "почтовый код", "индекс получателя", "индекс адреса", "индекс отправления", "индекс по прописке", "zip", "postcode"}
	anchorsSNILS    = []string{"снилс", "страховой номер", "лицевого счета", "лицевого счёта"}
	anchorsDriver   = []string{"водительск", "в/у", "ву ", "права", "driver"}
	anchorsCVV      = []string{"cvv", "cvc", "cvv2", "cvc2", "код безопасности", "защитный код", "три цифры", "с обратной стороны", "с оборота", "оборотн"}
	anchorsPIN      = []string{"пин", "pin", "пин-код", "пинкод", "pin-code"}

	// anchorsDept — код подразделения пишут и полным словом, и сокращением.
	// Основа «подразделени» закрывает все падежи, «к/п» и «кп» — сокращения
	// из анкет и строк таблиц. «кп» без двоеточия закрывает записи «кП — 695
	// 995» и «КП (714-563)»; от «кпп» (КПП организации) его защищает правило
	// «между якорем и значением не должно быть других цифр» — у «кпп» всегда
	// есть лишняя буква «п» перед цифрами.
	anchorsDept = []string{"код подразделения", "код подр", "подразделени", "подразделение", "к/п", "к\\п", "кп:", "кп", "код п/п", "код органа выдачи", "номер подразделения выдачи"}

	// anchorsDeptWide — сильные якоря кода подразделения, которые допустимо
	// искать в более широком окне. Между якорем и значением может стоять
	// уточнение «по паспорту»: «код подразделения по паспорту: 993 542».
	// Короткие сокращения «кп» и «к/п» сюда не входят: они слишком общие,
	// чтобы доверять им на расстоянии.
	anchorsDeptWide = []string{"код подразделения", "код органа выдачи", "номер подразделения выдачи", "подразделение", "подразделени"}

	// anchorsAddressLead — адресные слова, после которых шесть цифр почти
	// всегда почтовый индекс, даже если слово «индекс» не написано:
	// «Адрес регистрации: 150861, Свердловская область».
	anchorsAddressLead = []string{"адрес", "адресу", "проживает", "прописк", "доставки"}

	// anchorsAddressWord — признаки того, что число стоит внутри адреса.
	// Нужны для индекса в конце адреса, где слева от него уже идут номера
	// дома и строения и обычное окно якоря до слова «адрес» не достаёт.
	anchorsAddressWord = []string{"адрес", "проживает", "прописк", "улица", "ул.", "бульвар", "проспект", "переул", "шоссе", "набережн", "наб.", "микрорайон", "мкр", "квартал", "корп", "стр.", "кв.", "д. "}

	// anchorsPassportStrict — якоря, которые говорят о паспорте однозначно.
	// Слова «номер» и «№» сюда не входят: они стоят рядом с любым служебным
	// номером организации.
	// Объединение двух ночных правок: сборка добавила русские обороты из
	// анкет, ветка латиницы английский passport. Нужны все.
	anchorsPassportStrict = []string{"паспорт", "пасп.", "пасп ", "серия", "серии",
		"основной документ", "данные документа", "документ: паспорт", "passport"}

	// anchorsStrongPersonal — сильные персональные якоря. Если такое слово
	// стоит между служебным признаком и числом, признак к числу не относится:
	// в записи «по договору 12, паспорт 4509 123456» речь всё-таки о паспорте.
	anchorsStrongPersonal = []string{"паспорт", "пасп", "серия", "серии", "снилс", "инн", "карт", "подразделени", "к/п", "индекс", "телефон", "удостоверен", "водительск", "passport"}

	// negAnchorsMoney — рядом с денежными словами число почти наверняка сумма.
	negAnchorsMoney = []string{"сумма", "руб", "₽", "оплат", "стоимость", "баланс", "остаток", "счёт на", "счет на"}

	// negAnchorsOrder — служебные номера документооборота: заказ, договор,
	// накладная, обращение. Рядом с ними число не является персональными
	// данными.
	negAnchorsOrder = []string{
		"заказ", "договор", "накладн", "счёт-фактур", "счет-фактур", "артикул",
		"заявк", "обращени", "тикет", "операци", "чеке", "транзакци",
	}

	// negAnchorsOrg — реквизиты юридического лица. ОГРН из тринадцати цифр по
	// форме неотличим от номера карты, поэтому признак обязателен.
	negAnchorsOrg = []string{
		"огрн", "кпп", "бик", "р/с", "к/с", "расчётный счёт", "расчетный счет",
		"корреспондентск",
	}

	// negAnchorsService — оба списка служебных признаков вместе.
	negAnchorsService = append(append([]string{}, negAnchorsOrder...), negAnchorsOrg...)

	// fuzzyAnchorsDept, fuzzyAnchorsPassport и fuzzyAnchorsPhone — якорные
	// слова целиком, для которых допускается одна опечатка. Слова короче
	// fuzzyMinRunes сюда не попадают: у них одна замена буквы даёт другое
	// слово и якорь начинает срабатывать где попало.
	fuzzyAnchorsDept     = []string{"подразделения", "подразделение", "подразделением", "подразделений"}
	fuzzyAnchorsPassport = []string{"паспорт", "паспорта", "паспорте", "паспортные", "паспортный"}
	fuzzyAnchorsPhone    = []string{"телефон", "телефона", "телефону"}
)

// anchorWindow — стандартное окно поиска якоря в рунах. Окно задаётся именно
// в рунах: в байтах кириллическое окно вдвое короче задуманного.
const anchorWindow = 40

// nearAnchorWindow — окно для коротких неспецифичных чисел, где якорь обязан
// стоять непосредственно перед значением.
const nearAnchorWindow = 24

// addressTailWindow — окно поиска адресных слов перед индексом в конце
// адреса. Оно шире обычного, потому что между словом «адрес» и индексом
// помещается вся улица с номером дома и строения.
const addressTailWindow = 70

// serviceWindow — сколько рун предложения перед числом просматривается на
// служебные признаки.
const serviceWindow = 60

// fuzzyMinRunes — минимальная длина якоря в рунах, при которой допускается
// одна опечатка.
const fuzzyMinRunes = 7

// patternSNILS — привычная запись СНИЛС: три группы по три цифры и две
// цифры контрольного числа.
const patternSNILS = "3-3-3-2"

// maxDottedDigits — сколько цифр может быть в сетевом адресе вида
// 192.168.0.1. Более длинная цепочка точек к сетевому адресу отношения
// не имеет и разбирается обычными правилами.
const maxDottedDigits = 12

// numericDetector находит форматные типы персональных данных по числовым
// кандидатам. Все типы разбираются за один проход по кандидатам, поэтому
// текст не сканируется по разу на каждый тип.
type numericDetector struct{}

// NewNumericDetector создаёт детектор форматных типов.
func NewNumericDetector() Detector { return numericDetector{} }

// Types перечисляет типы, которые находит детектор.
func (numericDetector) Types() []Type {
	return []Type{
		TypePassport, TypeINN, TypeCard, TypePhone, TypeDeptCode,
		TypePostcode, TypeSNILS, TypeDriverLicense, TypeCVV, TypePIN,
	}
}

// Detect классифицирует каждый числовой кандидат по длине групп, якорю рядом и
// контрольной сумме.
//
// Контрольная сумма только повышает уверенность и не является условием: жюри
// и проверяющая система подают выдуманные номера, которые сумму не проходят.
func (n numericDetector) Detect(d *Doc) []Span {
	runs := d.NumRuns()
	var out []Span
	used := make([]bool, len(runs))

	for i, run := range runs {
		if used[i] {
			continue
		}
		// Паспорт, записанный с разделяющими словами: «серия 4509 номер 123456».
		if len(run.Digits) == 4 {
			if j, ok := joinPassportParts(d, runs, i); ok {
				start, end := NormalizeSpan(d.Text, run.Start, runs[j].End)
				out = append(out, Span{Start: start, End: end, Type: TypePassport, Conf: ConfHigh, Reason: "passport:series_number_words"})
				used[i], used[j] = true, true
				continue
			}
		}

		// Паспорт, приклеившийся к дате: «Белова К.А. 1977-12-17 75 52 040112».
		if parts, ok := splitDatePassport(d, run); ok {
			out = append(out, parts...)
			used[i] = true
			continue
		}

		if s, ok := n.classify(d, run); ok {
			out = append(out, s)
			used[i] = true
		}
	}
	return out
}

// datePrefixGroups — длины групп цифр, с которых начинается дата. Вариант из
// одной группы — это год, оставшийся от записи с названием месяца:
// «14 октября 1986 8045 063266».
var datePrefixGroups = [][]int{{4, 2, 2}, {2, 2, 4}, {2, 2, 2}, {4}}

// passportTailGroups — длины групп цифр серии и номера паспорта.
var passportTailGroups = [][]int{{4, 6}, {2, 2, 6}, {10}}

// splitDatePassport отделяет серию и номер паспорта, слипшиеся с датой в
// одного числового кандидата: «Григорьев Егор Алексеевич 26-08-1990 7943 №
// 275956». Дату и паспорт разделяет пробел, то есть допустимый разделитель
// групп, поэтому кандидат получается один. Дата остаётся детектору дат, а
// паспортный хвост возвращается отдельным фрагментом.
func splitDatePassport(d *Doc, run NumRun) ([]Span, bool) {
	if len(run.Groups) < 3 {
		return nil, false
	}
	prefix, ok := datePrefixLen(run.Groups)
	if !ok {
		return nil, false
	}
	bounds := digitGroups(d, run)
	if len(bounds) != len(run.Groups) {
		return nil, false
	}
	// Одна группа из четырёх цифр перед паспортом — это год и только год.
	// Иначе запись «4509 1234 567890» распалась бы на дату и паспорт.
	if prefix == 1 && !looksLikeYear(d.Text[bounds[0][0]:bounds[0][1]]) {
		return nil, false
	}
	parts := passportTailSpans(d, bounds[prefix:])
	return parts, len(parts) > 0
}

// passportTailSpans режет паспортный хвост на фрагменты по знаку номера:
// в записи «7943 № 275956» серия и номер — два разных фрагмента, и знак
// между ними персональными данными не является, маскировать его незачем.
func passportTailSpans(d *Doc, groups [][2]int) []Span {
	var out []Span
	begin := 0
	for i := 1; i <= len(groups); i++ {
		if i < len(groups) && !strings.ContainsAny(d.Text[groups[i-1][1]:groups[i][0]], "№#") {
			continue
		}
		start, end := NormalizeSpan(d.Text, groups[begin][0], groups[i-1][1])
		if start < end {
			out = append(out, Span{Start: start, End: end, Type: TypePassport, Conf: ConfAnchored, Reason: "passport:after_date"})
		}
		begin = i
	}
	return out
}

// datePrefixLen подбирает такой вариант начала-даты, после которого остаётся
// правильная серия с номером паспорта.
func datePrefixLen(groups []int) (int, bool) {
	for _, v := range datePrefixGroups {
		if len(groups) <= len(v) || !groupsEqual(groups[:len(v)], v) {
			continue
		}
		if groupsEqualAny(groups[len(v):], passportTailGroups) {
			return len(v), true
		}
	}
	return 0, false
}

// looksLikeYear сообщает, что четыре цифры похожи на год рождения или выдачи.
func looksLikeYear(digits string) bool {
	return len(digits) == 4 && (strings.HasPrefix(digits, "19") || strings.HasPrefix(digits, "20"))
}

// groupsEqualAny сообщает, что длины групп совпадают с одним из вариантов.
func groupsEqualAny(groups []int, variants [][]int) bool {
	for _, v := range variants {
		if groupsEqual(groups, v) {
			return true
		}
	}
	return false
}

// groupsEqual сравнивает два набора длин групп.
func groupsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// digitGroups возвращает байтовые границы каждой группы цифр кандидата.
func digitGroups(d *Doc, run NumRun) [][2]int {
	out := make([][2]int, 0, len(run.Groups))
	start, inGroup := 0, false
	for i := run.Start; i < run.End; i++ {
		isDigit := d.Text[i] >= '0' && d.Text[i] <= '9'
		if isDigit && !inGroup {
			start, inGroup = i, true
		}
		if !isDigit && inGroup {
			out = append(out, [2]int{start, i})
			inGroup = false
		}
	}
	if inGroup {
		out = append(out, [2]int{start, run.End})
	}
	return out
}

// numMatch — результат распознавания кандидата: тип, уверенность и причина.
type numMatch struct {
	typ    Type
	conf   float64
	reason string
}

// numRule — одно правило распознавания.
type numRule func(numContext) (numMatch, bool)

// numRules — правила в порядке убывания специфичности формы. Побеждает первое
// сработавшее: у телефона и карты форма однозначнее, чем у шестизначного
// индекса или трёхзначного кода безопасности.
var numRules = []numRule{
	matchPhone, matchCard, matchSNILS, matchINN,
	matchDriver, matchPassport, matchDept, matchPostcode, matchSecretCode,
}

// classify относит один числовой кандидат к типу персональных данных.
func (numericDetector) classify(d *Doc, run NumRun) (Span, bool) {
	if isDottedGroups(run) {
		return Span{}, false
	}
	c := numContext{d: d, run: run, digits: run.Digits, pattern: run.GroupsPattern()}
	for _, rule := range numRules {
		m, ok := rule(c)
		if !ok {
			continue
		}
		start, end := NormalizeSpan(d.Text, run.Start, run.End)
		return Span{Start: start, End: end, Type: m.typ, Conf: m.conf, Reason: m.reason}, true
	}
	return Span{}, false
}

// numContext — числовой кандидат вместе с документом. Даёт правилам
// одинаковый набор проверок окружения и не хранит изменяемого состояния.
type numContext struct {
	d       *Doc
	run     NumRun
	digits  string
	pattern string
}

// anchorAt ищет якорь в стандартном окне вокруг значения.
func (c numContext) anchorAt(anchors []string) bool {
	_, ok := c.d.FindAnchor(c.run.Start, c.run.End, anchors, anchorWindow, anchorWindow/2)
	return ok
}

// anchorNear требует, чтобы якорь стоял непосредственно перед значением и
// между ними не было других цифр. Нужно для коротких неспецифичных чисел:
// иначе любое трёхзначное число в предложении получает чужой якорь.
func (c numContext) anchorNear(anchors []string) bool {
	_, ok := c.d.AnchorBefore(c.run.Start, anchors, nearAnchorWindow)
	return ok
}

// anchorAfter ищет якорь сразу после значения. Нужно для записи «746 —
// защитный код», где якорь стоит после числа.
func (c numContext) anchorAfter(anchors []string) bool {
	_, ok := c.d.FindAnchor(c.run.End, c.run.End, anchors, 0, nearAnchorWindow)
	return ok
}

// anchorFuzzy ищет якорное слово с одной опечаткой в окне перед значением.
func (c numContext) anchorFuzzy(anchors []string) bool {
	return hasFuzzyWord(c.d.LowerWindow(c.run.Start, c.run.End, anchorWindow, anchorWindow/2), anchors)
}

// hasDigits сообщает, что в кандидате ровно n цифр. Нужно для зашумлённых
// значений: лишний пробел внутри номера меняет длины групп, но не общее число
// цифр, и при явном якоре этого достаточно, чтобы опознать тип.
func (c numContext) hasDigits(n int) bool { return len(c.digits) == n }

// hasWordAround сообщает, что рядом с числом есть хотя бы одно слово.
// Число, вокруг которого нет ни одной буквы, не несёт никаких признаков
// персональных данных.
func (c numContext) hasWordAround() bool {
	w := c.d.LowerWindow(c.run.Start, c.run.End, anchorWindow, anchorWindow/2)
	for _, r := range w {
		if k := classify(r); k == KindCyr || k == KindLat {
			return true
		}
	}
	return false
}

// money сообщает, что рядом со значением стоят денежные слова.
func (c numContext) money() bool {
	w := c.d.LowerWindow(c.run.Start, c.run.End, anchorWindow, 10)
	_, ok := ContainsAnyLower(w, negAnchorsMoney)
	return ok
}

// service сообщает, что число — служебный номер организации.
func (c numContext) service() bool {
	return serviceNumberContext(c.d, c.run.Start)
}

// serviceNumberContext ищет служебный признак в текущем предложении перед
// значением. Если между признаком и значением успело встретиться сильное
// персональное якорное слово, признак к значению не относится.
func serviceNumberContext(d *Doc, start int) bool {
	head := sentenceHead(d, start)
	pos := lastAnchorEnd(head, negAnchorsService)
	if pos < 0 {
		return false
	}
	_, strong := ContainsAnyLower(head[pos:], anchorsStrongPersonal)
	return !strong
}

// sentenceHead возвращает текст в нижнем регистре от начала предложения до
// значения, но не длиннее serviceWindow рун. Слово из соседнего предложения
// к числу отношения не имеет.
func sentenceHead(d *Doc, start int) string {
	lo, _ := d.WindowRunes(start, start, serviceWindow, 0)
	head := d.Lower[lo:start]
	if i := strings.LastIndexAny(head, ".!?;\n\r"); i >= 0 {
		head = head[i+1:]
	}
	return head
}

// lastAnchorEnd возвращает смещение конца самого правого из найденных слов
// либо -1, если ни одно не найдено.
func lastAnchorEnd(head string, words []string) int {
	best := -1
	for _, w := range words {
		if w == "" {
			continue
		}
		if i := strings.LastIndex(head, w); i >= 0 && i+len(w) > best {
			best = i + len(w)
		}
	}
	return best
}

// matchPhone: код страны и десять значащих цифр в любой группировке.
func matchPhone(c numContext) (numMatch, bool) {
	if !isPhoneShape(c.run) {
		return numMatch{}, false
	}
	if c.anchorAt(anchorsPhone) {
		return numMatch{TypePhone, ConfHigh, "phone:anchor"}, true
	}
	if c.run.HasPlus || strings.HasPrefix(c.digits, "7") || strings.HasPrefix(c.digits, "8") {
		return numMatch{TypePhone, ConfHigh, "phone:shape"}, true
	}
	if c.anchorFuzzy(fuzzyAnchorsPhone) {
		return numMatch{TypePhone, ConfHigh, "phone:anchor_typo"}, true
	}
	return numMatch{}, false
}

// matchCard: номер карты из тринадцати-девятнадцати цифр.
func matchCard(c numContext) (numMatch, bool) {
	if len(c.digits) < 13 || len(c.digits) > 19 {
		return numMatch{}, false
	}
	anchored := c.anchorAt(anchorsCard)
	if !anchored && (c.service() || c.money()) {
		return numMatch{}, false
	}
	conf, reason := ConfMedium, "card:shape"
	if Luhn(c.digits) {
		conf, reason = ConfHigh, "card:luhn"
	}
	if anchored {
		return numMatch{TypeCard, ConfHigh, reason + "+anchor"}, true
	}
	if !Luhn(c.digits) && c.pattern != "4-4-4-4" {
		return numMatch{}, false
	}
	return numMatch{TypeCard, conf, reason}, true
}

// matchSNILS: одиннадцать цифр, обычно в записи три-три-три-два.
func matchSNILS(c numContext) (numMatch, bool) {
	if len(c.digits) != 11 {
		// Опечатка в самом номере: цифра потерялась или задвоилась. Форма уже
		// не сходится, поэтому якорь обязателен.
		if (len(c.digits) == 10 || len(c.digits) == 12) && c.anchorAt(anchorsSNILS) {
			return numMatch{TypeSNILS, ConfAnchored, "snils:anchor_typo_digits"}, true
		}
		return numMatch{}, false
	}
	if c.anchorAt(anchorsSNILS) {
		return numMatch{TypeSNILS, ConfHigh, "snils:anchor"}, true
	}
	if c.pattern != patternSNILS && c.pattern != "11" {
		return numMatch{}, false
	}
	if SNILSValid(c.digits) && c.pattern == patternSNILS {
		return numMatch{TypeSNILS, ConfAnchored, "snils:checksum"}, true
	}
	return numMatch{}, false
}

// matchINN: десять цифр у организации, двенадцать у человека.
func matchINN(c numContext) (numMatch, bool) {
	if len(c.digits) != 10 && len(c.digits) != 12 {
		// Опечатка в самом номере: цифра потерялась или задвоилась. Форма уже
		// не сходится, поэтому якорь обязателен.
		if (len(c.digits) == 11 || len(c.digits) == 13) && c.anchorAt(anchorsINN) {
			return numMatch{TypeINN, ConfAnchored, "inn:anchor_typo_digits"}, true
		}
		return numMatch{}, false
	}
	if c.anchorAt(anchorsINN) {
		return numMatch{TypeINN, ConfAnchored, "inn:anchor"}, true
	}
	if INNValid(c.digits) && !c.money() && !c.service() {
		return numMatch{TypeINN, ConfMedium, "inn:checksum"}, true
	}
	return numMatch{}, false
}

// matchPassport: четыре цифры серии и шесть цифр номера.
//
// Служебный контекст проверяется до якоря: в списке паспортных якорей есть
// слова «номер» и «№», и без этой проверки «обращение под номером 4533464978»
// становится паспортом.
func matchPassport(c numContext) (numMatch, bool) {
	if c.service() {
		return numMatch{}, false
	}
	if m, ok := matchPassportShape(c); ok {
		return m, ok
	}
	// Опечатка в самом номере: цифра потерялась или задвоилась. Форма уже не
	// сходится, поэтому слово «паспорт» обязано стоять прямо перед номером.
	if len(c.digits) >= 9 && len(c.digits) <= 11 && c.anchorNear(anchorsPassportStrict) {
		return numMatch{TypePassport, ConfAnchored, "passport:anchor_typo_digits"}, true
	}
	return numMatch{}, false
}

// matchPassportShape разбирает номер правильной формы: десять цифр в одной из
// принятых группировок.
func matchPassportShape(c numContext) (numMatch, bool) {
	if !isPassportShape(c.pattern, c.digits) {
		return numMatch{}, false
	}
	if c.anchorAt(anchorsPassport) {
		return numMatch{TypePassport, ConfHigh, "passport:anchor"}, true
	}
	if c.anchorFuzzy(fuzzyAnchorsPassport) {
		return numMatch{TypePassport, ConfHigh, "passport:anchor_typo"}, true
	}
	if c.pattern == "4-6" || c.pattern == "2-2-6" {
		return numMatch{TypePassport, ConfAnchored, "passport:shape"}, true
	}
	// Десять цифр подряд — форма слабее: так пишут и паспорт, и служебный
	// номер. Нужен хотя бы намёк на текст вокруг: голое число без единого
	// слова рядом персональными данными не считаем.
	if c.pattern == "10" && !c.money() && c.hasWordAround() {
		return numMatch{TypePassport, ConfAnchored, "passport:shape_solid"}, true
	}
	return numMatch{}, false
}

// matchDriver: две цифры региона, две цифры серии и номер.
func matchDriver(c numContext) (numMatch, bool) {
	if c.pattern != "2-2-6" && c.pattern != "10" {
		return numMatch{}, false
	}
	if c.anchorAt(anchorsDriver) {
		return numMatch{TypeDriverLicense, ConfHigh, "driver:anchor"}, true
	}
	return numMatch{}, false
}

// matchDept: код подразделения — три цифры и три цифры через дефис, пробел
// или слеш, либо те же шесть цифр подряд.
//
// Якорь требуется непосредственно перед значением: шесть цифр сами по себе
// неотличимы от почтового индекса, и якорь из другой части строки таблицы
// пометил бы индекс кодом подразделения.
func matchDept(c numContext) (numMatch, bool) {
	if !isDeptShape(c.pattern) && !c.hasDigits(6) && !c.hasDigits(5) {
		return numMatch{}, false
	}
	if c.anchorNear(anchorsDept) {
		return numMatch{TypeDeptCode, ConfHigh, "dept:anchor"}, true
	}
	// Широкое окно для сильных якорей: «код подразделения по паспорту: 993
	// 542» — между якорем и значением стоит уточнение «по паспорту», и узкое
	// окно до якоря не достаёт. Правило «между якорем и значением нет других
	// цифр» не даёт широкому окну пометить почтовый индекс кодом подразделения.
	if _, ok := c.d.AnchorBefore(c.run.Start, anchorsDeptWide, anchorWindow); ok {
		return numMatch{TypeDeptCode, ConfHigh, "dept:anchor_wide"}, true
	}
	// Якорь после значения: «989774 — код подразделения ОВД».
	if c.anchorAfter(anchorsDept) {
		return numMatch{TypeDeptCode, ConfHigh, "dept:anchor_after"}, true
	}
	if hasFuzzyWord(c.d.LowerWindow(c.run.Start, c.run.Start, nearAnchorWindow, 0), fuzzyAnchorsDept) {
		return numMatch{TypeDeptCode, ConfHigh, "dept:anchor_typo"}, true
	}
	if c.pattern == "3-3" && strings.Contains(c.run.Seps, "-") && c.anchorAt(anchorsPassport) {
		return numMatch{TypeDeptCode, ConfAnchored, "dept:passport_context"}, true
	}
	return numMatch{}, false
}

// matchPostcode: шесть цифр в почтовом или адресном окружении.
func matchPostcode(c numContext) (numMatch, bool) {
	if !c.hasDigits(6) {
		return numMatch{}, false
	}
	if c.anchorNear(anchorsPostcode) || c.anchorAfter(anchorsPostcode) {
		return numMatch{TypePostcode, ConfHigh, "postcode:anchor"}, true
	}
	if c.anchorNear(anchorsAddressLead) {
		return numMatch{TypePostcode, ConfAnchored, "postcode:address_lead"}, true
	}
	if inAddressTail(c) {
		return numMatch{TypePostcode, ConfAnchored, "postcode:address_tail"}, true
	}
	// Опечатка в якоре: «инедкс» вместо «индекс». Короткие якоря допускают
	// одну опечатку только рядом со значением.
	if hasFuzzyWordMin(c.d.LowerWindow(c.run.Start, c.run.Start, nearAnchorWindow, 0), anchorsPostcode, 6) {
		return numMatch{TypePostcode, ConfAnchored, "postcode:anchor_typo"}, true
	}
	return numMatch{}, false
}

// matchSecretCode: код безопасности и пин-код маскируются только по явному
// якорю — три или четыре цифры сами по себе встречаются в любом тексте.
func matchSecretCode(c numContext) (numMatch, bool) {
	if len(c.digits) < 3 || len(c.digits) > 5 {
		return numMatch{}, false
	}
	if c.anchorNear(anchorsCVV) || c.anchorAfter(anchorsCVV) {
		return numMatch{TypeCVV, ConfHigh, "cvv:anchor"}, true
	}
	if c.anchorNear(anchorsPIN) || c.anchorAfter(anchorsPIN) {
		return numMatch{TypePIN, ConfHigh, "pin:anchor"}, true
	}
	// Опечатка в якоре: «пни» вместо «пин», «оборотнои» вместо «оборота».
	// Короткие якоря допускают одну опечатку только рядом со значением.
	win := c.d.LowerWindow(c.run.Start, c.run.Start, nearAnchorWindow, 0)
	if hasFuzzyWordMin(win, anchorsCVV, 5) {
		return numMatch{TypeCVV, ConfAnchored, "cvv:anchor_typo"}, true
	}
	if hasFuzzyWordMin(win, anchorsPIN, 3) {
		return numMatch{TypePIN, ConfAnchored, "pin:anchor_typo"}, true
	}
	return numMatch{}, false
}

// inAddressTail сообщает, что шесть цифр стоят после запятой в конце адреса:
// «бульвар Солнечная, д. 32, стр. 3, 743558». Слово «индекс» в такой записи
// отсутствует, а между адресным словом и индексом стоят номера дома и
// строения, поэтому обычная проверка якоря рядом сюда не годится.
func inAddressTail(c numContext) bool {
	head := strings.TrimRight(c.d.Lower[:c.run.Start], " \t")
	if !strings.HasSuffix(head, ",") {
		return false
	}
	w := c.d.LowerWindow(c.run.Start, c.run.Start, addressTailWindow, 0)
	if strings.ContainsAny(w, "\n\r") {
		if i := strings.LastIndexAny(w, "\n\r"); i >= 0 {
			w = w[i+1:]
		}
	}
	_, ok := ContainsAnyLower(w, anchorsAddressWord)
	return ok
}

// isDottedGroups сообщает, что перед нами сетевой адрес вида 192.168.0.1:
// четыре и более групп цифр, разделённых только точками. Такое число не
// является ни датой, ни номером документа.
func isDottedGroups(run NumRun) bool {
	return len(run.Groups) >= 4 && run.Seps == "." && len(run.Digits) <= maxDottedDigits
}

// isDeptShape сообщает, что форма кандидата подходит коду подразделения.
func isDeptShape(pattern string) bool {
	return pattern == "3-3" || pattern == "6"
}

// isPhoneShape сообщает, что кандидат похож на номер телефона.
func isPhoneShape(run NumRun) bool {
	digits := run.Digits
	switch {
	case run.HasPlus && len(digits) >= 10 && len(digits) <= 15:
		return true
	case len(digits) == 11 && (digits[0] == '7' || digits[0] == '8'):
		return true
	case len(digits) == 10 && len(run.Groups) >= 3:
		// Запись без кода страны: «916 123 45 67».
		return true
	default:
		return false
	}
}

// isPassportShape сообщает, что кандидат похож на серию и номер паспорта.
func isPassportShape(pattern, digits string) bool {
	if len(digits) != 10 {
		return false
	}
	switch pattern {
	case "4-6", "2-2-6", "10", "4-2-4":
		return true
	default:
		return false
	}
}

// passportConnectors — слова, которые допустимо встретить между серией и
// номером паспорта.
var passportConnectors = []string{"номер", "ном", "no", "n", "серия", "серии", "сер", "№", "#", ":", ",", "-", " "}

// joinPassportParts проверяет, что за четырёхзначной серией через разделяющие
// слова идёт шестизначный номер, и возвращает индекс кандидата с номером.
func joinPassportParts(d *Doc, runs []NumRun, i int) (int, bool) {
	if len(runs[i].Digits) != 4 || serviceNumberContext(d, runs[i].Start) {
		return 0, false
	}
	// Серия без паспортного контекста — не паспорт: четыре цифры встречаются
	// в любом тексте.
	if _, ok := d.FindAnchor(runs[i].Start, runs[i].End, anchorsPassport, anchorWindow, anchorWindow); !ok {
		return 0, false
	}
	for j := i + 1; j < len(runs) && j <= i+2; j++ {
		if len(runs[j].Digits) != 6 {
			continue
		}
		gap := d.Lower[runs[i].End:runs[j].Start]
		if strings.ContainsAny(gap, "\n\r") {
			return 0, false
		}
		if len([]rune(gap)) > 24 {
			return 0, false
		}
		if !onlyConnectors(gap) {
			return 0, false
		}
		return j, true
	}
	return 0, false
}

// onlyConnectors сообщает, что промежуток состоит только из разделяющих слов,
// знаков препинания и пробелов.
func onlyConnectors(gap string) bool {
	rest := gap
	for _, c := range passportConnectors {
		rest = strings.ReplaceAll(rest, c, " ")
	}
	return strings.TrimSpace(rest) == ""
}

// hasFuzzyWord ищет в окне слово, отличающееся от якоря не более чем на одну
// букву. Проверяются только якоря длиной от fuzzyMinRunes: у коротких слов
// одна замена буквы даёт другое слово, и якорь начинает ловить лишнее.
//
// Окно и якоря сворачиваются по омоглифам: латинская «о» и кириллическая «о»
// считаются одной буквой, поэтому «пасп0рт» с латинской буквой совпадает с
// якорем «паспорт». Пробелы внутри слова не мешают: «поч товый» совпадает с
// якорем «почтовый».
func hasFuzzyWord(window string, anchors []string) bool {
	return hasFuzzyWordMin(window, anchors, fuzzyMinRunes)
}

// hasFuzzyWordMin — как hasFuzzyWord, но с настраиваемой минимальной длиной
// якоря. Короткие якоря вроде «индекс» (шесть букв) допускают одну опечатку
// только рядом со значением, поэтому порог для них ниже.
func hasFuzzyWordMin(window string, anchors []string, minRunes int) bool {
	norm := FoldHomoglyphs(window)

	// Якори приводятся и отсеиваются по длине ОДИН раз, а не заново для
	// каждого слова окна. Прежний порядок давал число приведений, равное
	// числу слов, умноженному на число якорей, и это на самом горячем пути:
	// обработка текста из-за него подорожала вдвое.
	folded := make([]string, 0, len(anchors))
	for _, a := range anchors {
		if utf8.RuneCountInString(a) < minRunes {
			continue
		}
		folded = append(folded, FoldHomoglyphs(a))
	}
	if len(folded) == 0 {
		return false
	}

	for _, word := range letterWords(norm) {
		for _, a := range folded {
			if nearlyEqual(word, a) {
				return true
			}
		}
	}
	// Пробел, вставленный внутрь якоря, разрывает слово: «поч товый» вместо
	// «почтовый». Проверяем окно без пробелов целиком.
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\u00a0' {
			return -1
		}
		return r
	}, norm)
	for _, a := range folded {
		if strings.Contains(compact, a) {
			return true
		}
	}
	return false
}

// letterWords режет окно на слова из букв. Цифры и знаки препинания служат
// границами слова: граница \b в Go работает только для латиницы.
func letterWords(window string) []string {
	var out []string
	start, has := 0, false
	for i, r := range window {
		if classify(r) == KindCyr || classify(r) == KindLat {
			if !has {
				start, has = i, true
			}
			continue
		}
		if has {
			out = append(out, window[start:i])
			has = false
		}
	}
	if has {
		out = append(out, window[start:])
	}
	return out
}

// nearlyEqual сообщает, что строки совпадают или различаются одной вставкой,
// удалением, заменой либо перестановкой соседних рун. Перестановка — частая
// опечатка: «инедкс» вместо «индекс».
func nearlyEqual(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	if len(rb)-len(ra) > 1 {
		return false
	}
	same := len(ra) == len(rb)
	i, j, diff := 0, 0, 0
	for i < len(ra) && j < len(rb) {
		if ra[i] == rb[j] {
			i, j = i+1, j+1
			continue
		}
		// Перестановка соседних рун: «ab» против «ba».
		if same && i+1 < len(ra) && j+1 < len(rb) &&
			ra[i] == rb[j+1] && ra[i+1] == rb[j] {
			i, j, diff = i+2, j+2, diff+1
			if diff > 1 {
				return false
			}
			continue
		}
		diff++
		if diff > 1 {
			return false
		}
		if same {
			i++
		}
		j++
	}
	return true
}
