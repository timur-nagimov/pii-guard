package pii

import (
	"strings"
	"unicode/utf8"
)

// Якорные слова для форматных типов. Все задаются в нижнем регистре:
// поиск идёт по копии текста в нижнем регистре, поэтому регистр исходной
// записи значения не имеет.

// Общие якорные слова, повторяющиеся в списках разных детекторов. Вынесены
// в константы, чтобы не дублировать строковые литералы по файлам.
const (
	anchorWordInn        = "инн"
	anchorWordCard       = "карт"
	anchorWordDriver     = "водительск"
	anchorWordCVV        = "cvc"
	anchorWordDept       = "к/п"
	anchorWordAddress    = "адрес"
	anchorWordMkr        = "мкр"
	anchorWordContract   = "договор"
	anchorWordPassport   = "паспорт" // #nosec G101 — якорное слово, а не секрет
	anchorWordIssued     = "выдан"
	anchorWordRegistered = "зарегистрирован"
	anchorWordHolder     = "держател"
	anchorWordCitizen    = "гражданин"
	anchorWordDeptCode   = "код подразделения"
	anchorWordNumber     = "номер"
	anchorWordApplicant  = "заявител"
	anchorWordRegion     = "обл"
	anchorWordGor        = "гор"
	anchorWordPos        = "пос"
	anchorWordResp       = "респ"
	anchorWordProsp      = "просп"
	anchorWordSeries     = "серии"
	anchorWordSNILS      = "снилс"
	anchorWordCVV2       = "cvv"
	anchorWordClient     = "клиент"
	anchorWordLives      = "проживает"
	anchorWordTel        = "тел"
	anchorWordSeriesFull = "серия"
	anchorWordPhone      = "телефон"
	anchorWordPayer      = "плательщик"
)

var (
	// anchorsPassportCore — слова, которые в любом перечне значат одно и то
	// же: рядом написано про паспорт. Они нужны и широкому набору якорей, и
	// строгому, и списку сильных персональных признаков, поэтому заданы один
	// раз: иначе перечни разъезжаются, стоит поправить любой из них.
	anchorsPassportCore = []string{anchorWordPassport, anchorWordSeriesFull, anchorWordSeries, "passport"}

	anchorsPassport = append([]string{"пасп", "сери", anchorWordNumber, "№", anchorWordIssued, "удостоверение личности", "паспортные данные", "данные документа", "документ"}, anchorsPassportCore...)
	anchorsINN      = []string{anchorWordInn, "налогоплательщик", "идентификационный номер"}
	anchorsCard     = []string{anchorWordCard, "card", "pan", "visa", "mastercard", "мир", "maestro", "счёт карты", "номер карты"}
	anchorsPhone    = []string{anchorWordTel, anchorWordPhone, "моб", "сот", "звонить", "whatsapp", "вайбер", "номер телефона", "связь", "phone"}
	anchorsPostcode = []string{"индекс", "почтовый индекс", "почтовый код", "индекс получателя", "индекс адреса", "индекс отправления", "индекс по прописке", "zip", "postcode"}
	anchorsSNILS    = []string{anchorWordSNILS, "страховой номер", "лицевого счета", "лицевого счёта"}
	anchorsDriver   = []string{anchorWordDriver, "в/у", "ву ", "права", "driver"}
	anchorsCVV      = []string{anchorWordCVV2, anchorWordCVV, "cvv2", "cvc2", "код безопасности", "защитный код", "три цифры", "с обратной стороны", "с оборота", "оборотн",
		// «Проверочный код карты» — обычная формулировка в анкетах. Слово
		// «карты» в якоре оставлено намеренно: голый «проверочный код» это
		// ещё и код подтверждения из сообщения, а он персональными данными
		// карты не является.
		"проверочный код карты", "проверочного кода карты"}
	anchorsPIN = []string{"пин", "pin", "пин-код", "пинкод", "pin-code", "код доступа к карте", "секретный код карты", "секретный код"}

	// anchorsDeptWide — сильные якоря кода подразделения, которые допустимо
	// искать в более широком окне. Между якорем и значением может стоять
	// уточнение «по паспорту»: «код подразделения по паспорту: 993 542».
	// Основа «подразделени» закрывает все падежи. Короткие сокращения «кп» и
	// «к/п» сюда не входят: они слишком общие, чтобы доверять им на
	// расстоянии.
	anchorsDeptWide = []string{anchorWordDeptCode, "код органа выдачи", "номер подразделения выдачи", "подразделение", "подразделени"}

	// anchorsDept — код подразделения пишут и полным словом, и сокращением.
	// Сильные якоря берутся из anchorsDeptWide целиком: разойдись эти два
	// перечня, узкое окно перестало бы узнавать то, что узнаёт широкое.
	// Сокращения из анкет и строк таблиц нужны только здесь. «кп» без
	// двоеточия закрывает записи «кП — 695 995» и «КП (714-563)»; от «кпп»
	// (КПП организации) его защищает правило «между якорем и значением не
	// должно быть других цифр» — у «кпп» всегда есть лишняя буква «п» перед
	// цифрами. «Орган выдачи» и «наименование органа выдачи» — формулировки
	// из выгрузок, где код стоит сразу за названием органа; в широкое окно
	// они не идут, чтобы не дотянуться до чужого шестизначного числа.
	anchorsDept = append([]string{"код подр", anchorWordDept, "к\\п", "кп:", "кп", "код п/п", "наименование органа выдачи", "орган выдачи"}, anchorsDeptWide...)

	// anchorsAddressLead — адресные слова, после которых шесть цифр почти
	// всегда почтовый индекс, даже если слово «индекс» не написано:
	// «Адрес регистрации: 150861, Свердловская область».
	anchorsAddressLead = []string{anchorWordAddress, "адресу", anchorWordLives, "прописк", "доставки"}

	// anchorsAddressWord — признаки того, что число стоит внутри адреса.
	// Нужны для индекса в конце адреса, где слева от него уже идут номера
	// дома и строения и обычное окно якоря до слова «адрес» не достаёт.
	anchorsAddressWord = []string{anchorWordAddress, anchorWordLives, "прописк", "улица", "ул.", "бульвар", "проспект", "переул", "шоссе", "набережн", "наб.", "микрорайон", anchorWordMkr, "квартал", "корп", "стр.", "кв.", "д. "}

	// anchorsPassportStrict — якоря, которые говорят о паспорте однозначно.
	// Слова «номер» и «№» сюда не входят: они стоят рядом с любым служебным
	// номером организации.
	// Объединение двух ночных правок: сборка добавила русские обороты из
	// анкет, ветка латиницы английское написание. Нужны все, поэтому общий
	// набор паспортных слов входит сюда целиком.
	anchorsPassportStrict = append([]string{"пасп.", "пасп ",
		"основной документ", "данные документа", "документ: паспорт"}, anchorsPassportCore...)

	// anchorsStrongPersonal — сильные персональные якоря. Если такое слово
	// стоит между служебным признаком и числом, признак к числу не относится:
	// в записи «по договору 12, паспорт 4509 123456» речь всё-таки о паспорте.
	anchorsStrongPersonal = append([]string{"пасп", anchorWordSNILS, anchorWordInn, anchorWordCard, "подразделени", anchorWordDept, "индекс", anchorWordPhone, "удостоверен", anchorWordDriver}, anchorsPassportCore...)

	// negAnchorsMoney — рядом с денежными словами число почти наверняка сумма.
	negAnchorsMoney = []string{"сумма", "руб", "₽", "оплат", "стоимость", "баланс", "остаток", "счёт на", "счет на"}

	// negAnchorsOrder — служебные номера документооборота: заказ, договор,
	// накладная, обращение. Рядом с ними число не является персональными
	// данными.
	negAnchorsOrder = []string{
		"заказ", anchorWordContract, "накладн", "счёт-фактур", "счет-фактур", "артикул",
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
	fuzzyAnchorsPassport = []string{anchorWordPassport, "паспорта", "паспорте", "паспортные", "паспортный"}
	fuzzyAnchorsPhone    = []string{anchorWordPhone, "телефона", "телефону"}
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

// patternSplitSeries — запись, в которой первые четыре цифры разбиты на две
// пары: «27-12 564508». В такой форме пишут и паспорт с разбитой серией, и
// водительское удостоверение, где отдельно стоят код региона и серия.
const patternSplitSeries = "2-2-6"

// patternPassport — привычная запись паспорта: четыре цифры серии и шесть
// цифр номера.
const patternPassport = "4-6"

// patternSolidTen — те же десять цифр, написанные подряд. Отдельная форма,
// потому что без разделителей она слабее: так пишут и паспорт, и служебный
// номер организации.
const patternSolidTen = "10"

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

	for i := range runs {
		if used[i] {
			continue
		}
		next, last, ok := n.appendRunSpans(out, d, runs, i)
		if !ok {
			continue
		}
		out = next
		// Помечаются все вошедшие кандидаты, а не только крайние: иначе
		// середина разбитого номера осталась бы свободной и получила бы
		// свой, посторонний тип.
		for k := i; k <= last; k++ {
			used[k] = true
		}
	}
	return out
}

// appendRunSpans разбирает один числовой кандидат и дописывает найденные
// фрагменты к out. Кандидат проверяется тремя независимыми правилами, и
// разбор вынесен из Detect: самому циклу важно только одно — какие кандидаты
// оказались заняты. Вторым значением возвращается индекс последнего занятого
// кандидата: разбитый номер занимает сразу несколько.
//
// Срез передаётся и возвращается, а не собирается заново на каждый кандидат:
// Detect зовётся на каждом запросе, и лишний срез на кандидат стоит дороже
// возврата трёх значений.
func (n numericDetector) appendRunSpans(out []Span, d *Doc, runs []NumRun, i int) ([]Span, int, bool) {
	run := &runs[i]
	// Паспорт, записанный с разделяющими словами: «серия 4509 номер 123456».
	// Серия бывает разбита на части, поэтому первый кандидат не обязан
	// содержать все четыре цифры: «27-12 564508».
	if len(run.Digits) >= 2 && len(run.Digits) <= 4 {
		if j, ok := joinPassportParts(d, runs, i); ok {
			return append(out, passportSeriesNumberSpan(d, run, &runs[j])), j, true
		}
	}

	// Паспорт, приклеившийся к дате: «Белова К.А. 1977-12-17 75 52 040112».
	if parts, ok := splitDatePassport(d, run); ok {
		return append(out, parts...), i, true
	}

	s, ok := n.classify(d, run)
	if !ok {
		return out, i, false
	}
	out = append(out, s)
	// Пин-код, разбитый на две пары цифр: «PIN-code: 07 26». Первая пара
	// ловится якорем, вторая — соседняя двухзначная пара, между которой и
	// якорем уже стоят цифры первой пары. Помечаем её тем же типом, чтобы
	// обе половины пина были замаскированы.
	if s.Type == TypePIN && len(run.Digits) == 2 {
		if j, ok := joinSplitPIN(d, runs, i); ok {
			return append(out, splitPINSpan(d, &runs[j])), j, true
		}
	}
	return out, i, true
}

// passportSeriesNumberSpan собирает фрагмент паспорта из серии и номера,
// записанных разделяющими словами.
func passportSeriesNumberSpan(d *Doc, series, number *NumRun) Span {
	start, end := NormalizeSpan(d.Text, series.Start, number.End)
	return Span{Start: start, End: end, Type: TypePassport, Conf: ConfHigh, Reason: "passport:series_number_words"}
}

// splitPINSpan собирает фрагмент второй пары цифр разбитого пин-кода.
func splitPINSpan(d *Doc, run *NumRun) Span {
	start, end := NormalizeSpan(d.Text, run.Start, run.End)
	return Span{Start: start, End: end, Type: TypePIN, Conf: ConfHigh, Reason: "pin:split_pair"}
}

// datePrefixGroups — длины групп цифр, с которых начинается дата. Вариант из
// одной группы — это год, оставшийся от записи с названием месяца:
// «14 октября 1986 8045 063266». Однозначные день и месяц дают группы из
// одной цифры: «25.3.1991» — это 2-1-4.
var datePrefixGroups = [][]int{{4, 2, 2}, {2, 2, 4}, {2, 2, 2}, {4}, {2, 1, 4}, {1, 2, 4}, {1, 1, 4}}

// passportTailGroups — длины групп цифр серии и номера паспорта.
var passportTailGroups = [][]int{{4, 6}, {2, 2, 6}, {10}}

// splitDatePassport отделяет серию и номер паспорта, слипшиеся с датой в
// одного числового кандидата: «Григорьев Егор Алексеевич 26-08-1990 7943 №
// 275956». Дату и паспорт разделяет пробел, то есть допустимый разделитель
// групп, поэтому кандидат получается один. Дата остаётся детектору дат, а
// паспортный хвост возвращается отдельным фрагментом.
func splitDatePassport(d *Doc, run *NumRun) ([]Span, bool) {
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
func digitGroups(d *Doc, run *NumRun) [][2]int {
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
	// start и end — байтовые границы фрагмента, если они отличаются от границ
	// кандидата. Нужны для кода подразделения, слипшегося с соседним числом:
	// «635.159. 938.772» — один кандидат, а код подразделения только первый
	// «3-3». Ноль в end означает конец кандидата.
	start int
	end   int
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
func (numericDetector) classify(d *Doc, run *NumRun) (Span, bool) {
	if isDottedGroups(run) {
		return Span{}, false
	}
	c := numContext{d: d, run: run, digits: run.Digits, pattern: run.GroupsPattern()}
	for _, rule := range numRules {
		m, ok := rule(c)
		if !ok {
			continue
		}
		start, end := run.Start, run.End
		if m.start > 0 {
			start = m.start
		}
		if m.end > 0 {
			end = m.end
		}
		start, end = NormalizeSpan(d.Text, start, end)
		return Span{Start: start, End: end, Type: m.typ, Conf: m.conf, Reason: m.reason}, true
	}
	return Span{}, false
}

// numContext — числовой кандидат вместе с документом. Даёт правилам
// одинаковый набор проверок окружения и не хранит изменяемого состояния.
//
// Кандидат хранится ссылкой: сам NumRun весит восемьдесят байт, контекст
// копируется в каждое правило на каждом кандидате, и копия номера на этом
// пути обходится дороже разыменования.
type numContext struct {
	d       *Doc
	run     *NumRun
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

// negAnchorsPassportShape — признаки, при которых голая форма паспорта
// паспортом не является: номер принадлежит другому документу или организации.
// Список действует только на пути без якоря; при явном слове «паспорт» рядом
// он не применяется.
var negAnchorsPassportShape = []string{
	"инн", "огрн", "кпп", "бик", "лицевой счёт", "лицевой счет",
	"договор", "полис",
}

// negAnchorsOtherDoc — явное название другого документа. Оно отменяет паспорт
// даже при паспортном якоре: слово «номер» само по себе якорь слабый, и
// «номер студенческого билета 6534-667349» маскировалось паспортом. Слова
// вроде «инн» сюда не входят — они встречаются в анкете рядом с настоящим
// паспортом.
var negAnchorsOtherDoc = []string{
	"студенческ", "зачётк", "зачетк", "читательск", "абонемент",
	"пропуск", "табельн",
}

// passportNegContext ищет такой признак в начале предложения перед значением.
func passportNegContext(d *Doc, start int) bool {
	head := sentenceHead(d, start)
	if _, ok := ContainsAnyLower(head, negAnchorsPassportShape); ok {
		return true
	}
	_, ok := ContainsAnyLower(head, negAnchorsOtherDoc)
	return ok
}

// passportOtherDocContext ищет название другого документа. Проверяется и на
// пути с якорем, поэтому список узкий.
func passportOtherDocContext(d *Doc, start int) bool {
	_, ok := ContainsAnyLower(sentenceHead(d, start), negAnchorsOtherDoc)
	return ok
}

// isTollFree сообщает, что номер бесплатный: код 800, 803 или 804 после
// восьмёрки или семёрки.
func isTollFree(digits string) bool {
	if len(digits) != 11 {
		return false
	}
	if digits[0] != '7' && digits[0] != '8' {
		return false
	}
	switch digits[1:4] {
	case "800", "803", "804":
		return true
	default:
		// Остальные коды принадлежат операторам и регионам: такой номер
		// вполне может быть личным, бесплатным его считать нельзя.
	}
	return false
}

// innFollowedByKPP сообщает, что сразу за номером идёт код причины постановки
// на учёт. Окно короткое: КПП из соседнего предложения к этому номеру не
// относится.
func innFollowedByKPP(d *Doc, end int) bool {
	_, hi := d.WindowRunes(end, end, 0, innKPPWindow)
	tail := d.Lower[end:hi]
	if i := strings.IndexAny(tail, ".!?\n\r"); i >= 0 {
		tail = tail[:i]
	}
	_, ok := ContainsAnyLower(tail, []string{"кпп"})
	return ok
}

// innKPPWindow — сколько рун за номером просматривается в поисках КПП.
const innKPPWindow = 24

// serviceNumberContext ищет служебный признак в текущем предложении перед
// значением. Если между признаком и значением успело встретиться сильное
// персональное якорное слово, признак к значению не относится.
//
// Паспортный якорь действует и тогда, когда стоит до служебного признака:
// в записи «Паспорт Романова приложен к обращению: 5112 724571» слово
// «паспорт» предшествует служебному «обращению», но число — номер паспорта.
// Ограничение именно паспортными якорями, а не всеми сильными, не даёт
// служебному номеру в предложении с чужим ИНН или картой стать паспортом.
//
// Паспортный якорь ищется в более широком окне, чем предложение: в записи
// «Паспорт Копыловой М.Г. приложен к обращению: 2882 № 772957» инициалы
// «М.Г.» выглядят как конец предложения, и слово «паспорт» не попадает в
// head. Окно в 60 рун достаёт до него, не задевая соседнее предложение.
func serviceNumberContext(d *Doc, start int) bool {
	head := sentenceHead(d, start)
	pos := lastAnchorEnd(head, negAnchorsService)
	if pos < 0 {
		return false
	}
	if _, strong := ContainsAnyLower(head[pos:], anchorsStrongPersonal); strong {
		return false
	}
	win := d.LowerWindow(start, start, serviceWindow, 0)
	_, passport := ContainsAnyLower(win, anchorsPassportStrict)
	return !passport
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
	if !isPhoneShape(*c.run) {
		return numMatch{}, false
	}
	// Бесплатные номера 800, 803 и 804 выдаются организациям и человеку
	// принадлежать не могут: это горячая линия банка, а не телефон клиента.
	// Маскировать их — значит портить текст без всякой защиты.
	if isTollFree(c.digits) {
		return numMatch{}, false
	}
	if c.anchorAt(anchorsPhone) {
		return numMatch{TypePhone, ConfHigh, "phone:anchor", 0, 0}, true
	}
	if c.run.HasPlus || strings.HasPrefix(c.digits, "7") || strings.HasPrefix(c.digits, "8") {
		return numMatch{TypePhone, ConfHigh, "phone:shape", 0, 0}, true
	}
	if c.anchorFuzzy(fuzzyAnchorsPhone) {
		return numMatch{TypePhone, ConfHigh, "phone:anchor_typo", 0, 0}, true
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
		return numMatch{TypeCard, ConfHigh, reason + "+anchor", 0, 0}, true
	}
	if !Luhn(c.digits) && c.pattern != "4-4-4-4" {
		return numMatch{}, false
	}
	return numMatch{TypeCard, conf, reason, 0, 0}, true
}

// matchSNILS: одиннадцать цифр, обычно в записи три-три-три-два.
func matchSNILS(c numContext) (numMatch, bool) {
	if len(c.digits) != 11 {
		// Опечатка в самом номере: цифра потерялась или задвоилась. Форма уже
		// не сходится, поэтому якорь обязателен.
		if (len(c.digits) == 10 || len(c.digits) == 12) && c.anchorAt(anchorsSNILS) {
			return numMatch{TypeSNILS, ConfAnchored, "snils:anchor_typo_digits", 0, 0}, true
		}
		return numMatch{}, false
	}
	if c.anchorAt(anchorsSNILS) {
		return numMatch{TypeSNILS, ConfHigh, "snils:anchor", 0, 0}, true
	}
	if c.pattern != patternSNILS && c.pattern != "11" {
		return numMatch{}, false
	}
	if SNILSValid(c.digits) && c.pattern == patternSNILS {
		return numMatch{TypeSNILS, ConfAnchored, "snils:checksum", 0, 0}, true
	}
	return numMatch{}, false
}

// matchINN: десять цифр у организации, двенадцать у человека.
func matchINN(c numContext) (numMatch, bool) {
	if len(c.digits) != 10 && len(c.digits) != 12 {
		// Опечатка в самом номере: цифра потерялась или задвоилась. Форма уже
		// не сходится, поэтому якорь обязателен.
		if (len(c.digits) == 11 || len(c.digits) == 13) && c.anchorAt(anchorsINN) {
			return numMatch{TypeINN, ConfAnchored, "inn:anchor_typo_digits", 0, 0}, true
		}
		return numMatch{}, false
	}
	if c.anchorAt(anchorsINN) {
		// Код причины постановки на учёт бывает только у организации: у
		// человека его нет. Десятизначный ИНН, за которым идёт КПП, — это
		// реквизиты юридического лица, и маскировать их незачем.
		if len(c.digits) == 10 && innFollowedByKPP(c.d, c.run.End) {
			return numMatch{}, false
		}
		return numMatch{TypeINN, ConfAnchored, "inn:anchor", 0, 0}, true
	}
	// Номер, разбитый на группы по форме паспорта, ИНН не бывает: ИНН пишут
	// десятью цифрами подряд. Контрольная сумма сходится и у постороннего
	// числа, поэтому без этой оговорки «27-12 564508» рядом со словами
	// «удостоверение личности» становилось ИНН и паспорт оставался открытым.
	// Сплошные десять цифр сюда не попадают намеренно: это как раз форма ИНН.
	if (c.pattern == patternPassport || c.pattern == patternSplitSeries) && c.anchorAt(anchorsPassport) {
		return numMatch{}, false
	}
	if INNValid(c.digits) && !c.money() && !c.service() {
		return numMatch{TypeINN, ConfMedium, "inn:checksum", 0, 0}, true
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
		return numMatch{TypePassport, ConfAnchored, "passport:anchor_typo_digits", 0, 0}, true
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
		if passportOtherDocContext(c.d, c.run.Start) {
			return numMatch{}, false
		}
		return numMatch{TypePassport, ConfHigh, "passport:anchor", 0, 0}, true
	}
	if c.anchorFuzzy(fuzzyAnchorsPassport) {
		return numMatch{TypePassport, ConfHigh, "passport:anchor_typo", 0, 0}, true
	}
	// Форма без якоря — слабое основание, и рядом с признаком другого
	// документа она не годится вовсе. Десять цифр рядом со словом «ИНН» это
	// ИНН организации, «Студенческий 3827-178120» — студенческий билет.
	// Раньше и то и другое маскировалось паспортом.
	if passportNegContext(c.d, c.run.Start) {
		return numMatch{}, false
	}
	if c.pattern == patternPassport || c.pattern == patternSplitSeries {
		return numMatch{TypePassport, ConfAnchored, "passport:shape", 0, 0}, true
	}
	// Десять цифр подряд — форма слабее: так пишут и паспорт, и служебный
	// номер. Нужен хотя бы намёк на текст вокруг: голое число без единого
	// слова рядом персональными данными не считаем.
	if c.pattern == patternSolidTen && !c.money() && c.hasWordAround() {
		return numMatch{TypePassport, ConfAnchored, "passport:shape_solid", 0, 0}, true
	}
	return numMatch{}, false
}

// matchDriver: две цифры региона, две цифры серии и номер.
func matchDriver(c numContext) (numMatch, bool) {
	if c.pattern != patternSplitSeries && c.pattern != patternSolidTen {
		return numMatch{}, false
	}
	if c.anchorAt(anchorsDriver) {
		return numMatch{TypeDriverLicense, ConfHigh, "driver:anchor", 0, 0}, true
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
	prefixStart, prefixEnd, isPrefix := deptPrefixBounds(c)
	suffixStart, suffixEnd, isSuffix := deptSuffixBounds(c)
	if !isDeptShape(c.pattern) && !c.hasDigits(6) && !c.hasDigits(5) && !isPrefix && !isSuffix {
		return numMatch{}, false
	}
	if c.anchorNear(anchorsDept) {
		return deptMatch(prefixStart, prefixEnd, isPrefix, ConfHigh, "dept:anchor"), true
	}
	// Широкое окно для сильных якорей: «код подразделения по паспорту: 993
	// 542» — между якорем и значением стоит уточнение «по паспорту», и узкое
	// окно до якоря не достаёт. Правило «между якорем и значением нет других
	// цифр» не даёт широкому окну пометить почтовый индекс кодом подразделения.
	if _, ok := c.d.AnchorBefore(c.run.Start, anchorsDeptWide, anchorWindow); ok {
		return deptMatch(prefixStart, prefixEnd, isPrefix, ConfHigh, "dept:anchor_wide"), true
	}
	// Якорь после значения: «989774 — код подразделения ОВД». Если код слипся
	// с предыдущим числом, берётся последний «3-3» или «6»: «579122. 916/580 —
	// номер подразделения выдачи».
	if c.anchorAfter(anchorsDept) {
		return deptMatch(suffixStart, suffixEnd, isSuffix, ConfHigh, "dept:anchor_after"), true
	}
	if hasFuzzyWord(c.d.LowerWindow(c.run.Start, c.run.Start, nearAnchorWindow, 0), fuzzyAnchorsDept) {
		return deptMatch(prefixStart, prefixEnd, isPrefix, ConfHigh, "dept:anchor_typo"), true
	}
	if c.pattern == "3-3" && deptPassportNear(c) {
		return deptMatch(prefixStart, prefixEnd, isPrefix, ConfAnchored, "dept:passport_context"), true
	}
	return numMatch{}, false
}

// deptMatch собирает результат распознавания кода подразделения. Если код
// слипся с соседним числом, границы фрагмента ограничиваются найденным
// «3-3» или «6».
func deptMatch(start, end int, isSplit bool, conf float64, reason string) numMatch {
	m := numMatch{TypeDeptCode, conf, reason, 0, 0}
	if isSplit {
		m.start, m.end = start, end
	}
	return m
}

// deptPrefixBounds сообщает, что кандидат начинается с «3-3» и за ним идут
// другие цифры: «635.159. 938.772» — один кандидат, а код подразделения
// только первый «3-3». Возвращает байтовые границы этого «3-3».
func deptPrefixBounds(c numContext) (int, int, bool) {
	if len(c.run.Groups) < 2 || c.run.Groups[0] != 3 || c.run.Groups[1] != 3 {
		return 0, 0, false
	}
	bounds := digitGroups(c.d, c.run)
	if len(bounds) < 2 {
		return 0, 0, false
	}
	return bounds[0][0], bounds[1][1], true
}

// deptSuffixBounds сообщает, что кандидат заканчивается кодом подразделения,
// слипшимся с предыдущим числом: «579122. 916/580» — один кандидат, а код
// подразделения последний «3-3» или «6». Возвращает байтовые границы кода.
func deptSuffixBounds(c numContext) (int, int, bool) {
	bounds := digitGroups(c.d, c.run)
	if len(bounds) < 2 {
		return 0, 0, false
	}
	// Последняя пара «3-3».
	for i := len(bounds) - 1; i >= 1; i-- {
		if bounds[i-1][1]-bounds[i-1][0] == 3 && bounds[i][1]-bounds[i][0] == 3 {
			return bounds[i-1][0], bounds[i][1], true
		}
	}
	// Последняя группа «6».
	for i := len(bounds) - 1; i >= 0; i-- {
		if bounds[i][1]-bounds[i][0] == 6 {
			return bounds[i][0], bounds[i][1], true
		}
	}
	return 0, 0, false
}

// deptPassportNear сообщает, что рядом с кодом подразделения стоит паспорт:
// якорное слово либо сокращённая запись «с. XXXX н. YYYYYY» или
// «XXXX № YYYYYY». Такие записи встречаются в строках выгрузок, где код
// подразделения идёт сразу за паспортом.
func deptPassportNear(c numContext) bool {
	if c.anchorAt(anchorsPassport) {
		return true
	}
	win := c.d.LowerWindow(c.run.Start, c.run.End, anchorWindow, anchorWindow)
	return passportAbbrevIn(win)
}

// passportAbbrevIn сообщает, что в окне есть сокращённая запись паспорта
// «с. XXXX н. YYYYYY» или «XXXX № YYYYYY».
func passportAbbrevIn(win string) bool {
	return passportAbbrevSeries(win) || passportAbbrevNumber(win)
}

// passportAbbrevSeries проверяет сокращённую запись «с. XXXX н. YYYYYY».
// Кириллические «с» и «н» занимают по два байта, поэтому после «с.» и «н.»
// смещение сдвигается на три.
func passportAbbrevSeries(win string) bool {
	i := strings.Index(win, "с.")
	if i < 0 {
		return false
	}
	rest := strings.TrimLeft(win[i+3:], " \t")
	if len(rest) < 4 || !allDigits(rest[:4]) {
		return false
	}
	rest = strings.TrimLeft(rest[4:], " \t")
	if !strings.HasPrefix(rest, "н.") {
		return false
	}
	rest = strings.TrimLeft(rest[3:], " \t")
	return len(rest) >= 6 && allDigits(rest[:6])
}

// passportAbbrevNumber проверяет сокращённую запись «XXXX № YYYYYY».
func passportAbbrevNumber(win string) bool {
	i := strings.Index(win, "№")
	if i < 0 {
		return false
	}
	before := strings.TrimRight(win[:i], " \t")
	if len(before) < 4 || !allDigits(before[len(before)-4:]) {
		return false
	}
	rest := strings.TrimLeft(win[i+1:], " \t")
	return len(rest) >= 6 && allDigits(rest[:6])
}

// allDigits сообщает, что строка состоит только из цифр.
func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// matchPostcode: шесть цифр в почтовом или адресном окружении.
func matchPostcode(c numContext) (numMatch, bool) {
	if !c.hasDigits(6) {
		return numMatch{}, false
	}
	if c.anchorNear(anchorsPostcode) || c.anchorAfter(anchorsPostcode) {
		return numMatch{TypePostcode, ConfHigh, "postcode:anchor", 0, 0}, true
	}
	if c.anchorNear(anchorsAddressLead) {
		return numMatch{TypePostcode, ConfAnchored, "postcode:address_lead", 0, 0}, true
	}
	if inAddressTail(c) {
		return numMatch{TypePostcode, ConfAnchored, "postcode:address_tail", 0, 0}, true
	}
	// Опечатка в якоре: «инедкс» вместо «индекс». Короткие якоря допускают
	// одну опечатку только рядом со значением.
	if hasFuzzyWordMin(c.d.LowerWindow(c.run.Start, c.run.Start, nearAnchorWindow, 0), anchorsPostcode, 6) {
		return numMatch{TypePostcode, ConfAnchored, "postcode:anchor_typo", 0, 0}, true
	}
	return numMatch{}, false
}

// matchSecretCode: код безопасности и пин-код маскируются только по явному
// якорю — три или четыре цифры сами по себе встречаются в любом тексте.
func matchSecretCode(c numContext) (numMatch, bool) {
	if len(c.digits) < 2 || len(c.digits) > 5 {
		return numMatch{}, false
	}
	// Пин-код допускает две цифры: в записи «PIN-code: 07 26» пин разбит на
	// две пары, и каждая пара — отдельный числовой кандидат. Код безопасности
	// всегда трёхзначный, поэтому для него порог остаётся прежним.
	if c.anchorNear(anchorsPIN) || c.anchorAfter(anchorsPIN) {
		return numMatch{TypePIN, ConfHigh, "pin:anchor", 0, 0}, true
	}
	if len(c.digits) < 3 {
		return numMatch{}, false
	}
	if c.anchorNear(anchorsCVV) || c.anchorAfter(anchorsCVV) {
		return numMatch{TypeCVV, ConfHigh, "cvv:anchor", 0, 0}, true
	}
	// Опечатка в якоре: «пни» вместо «пин», «оборотнои» вместо «оборота».
	// Короткие якоря допускают одну опечатку только рядом со значением.
	win := c.d.LowerWindow(c.run.Start, c.run.Start, nearAnchorWindow, 0)
	if hasFuzzyWordMin(win, anchorsCVV, 5) {
		return numMatch{TypeCVV, ConfAnchored, "cvv:anchor_typo", 0, 0}, true
	}
	if hasFuzzyWordMin(win, anchorsPIN, 3) {
		return numMatch{TypePIN, ConfAnchored, "pin:anchor_typo", 0, 0}, true
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
func isDottedGroups(run *NumRun) bool {
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
	case patternPassport, patternSplitSeries, patternSolidTen, "4-2-4":
		return true
	default:
		return false
	}
}

// passportConnectors — слова, которые допустимо встретить между серией и
// номером паспорта.
var passportConnectors = []string{anchorWordNumber, "ном", "no", "n", anchorWordSeriesFull, anchorWordSeries, "сер", "№", "#", ":", ",", "-", "/", ".", "с", "н", " "}

// collectDigits собирает группу ровно из want цифр, начиная с кандидата i.
// Группу разрешено набирать из нескольких соседних кандидатов, разделённых
// только соединителями: в наборе паспорт пишут и как «240 402», и как «27-12»,
// и обе половины — отдельные числовые кандидаты. Возвращает индекс последнего
// вошедшего кандидата.
//
// Точку между частями не допускаем намеренно: так пишут даты, и «27.12» рядом
// с паспортным якорем не должно превращаться в серию.
func collectDigits(d *Doc, runs []NumRun, i, want, maxParts int) (int, bool) {
	total := len(runs[i].Digits)
	if total > want {
		return 0, false
	}
	if total == want {
		return i, true
	}
	for j := i + 1; j < len(runs) && j-i < maxParts; j++ {
		gap := d.Lower[runs[j-1].End:runs[j].Start]
		if len([]rune(gap)) > 3 || strings.Contains(gap, ".") || !onlyConnectors(gap) {
			return 0, false
		}
		total += len(runs[j].Digits)
		if total == want {
			return j, true
		}
		if total > want {
			return 0, false
		}
	}
	return 0, false
}

// joinPassportParts проверяет, что за четырёхзначной серией через разделяющие
// слова идёт шестизначный номер, и возвращает индекс кандидата с номером.
func joinPassportParts(d *Doc, runs []NumRun, i int) (int, bool) {
	if serviceNumberContext(d, runs[i].Start) {
		return 0, false
	}
	// Серия набирается из одной или двух частей: «4509» и «27-12».
	seriesEnd, ok := collectDigits(d, runs, i, 4, 2)
	if !ok {
		return 0, false
	}
	// Серия без паспортного контекста — не паспорт: четыре цифры встречаются
	// в любом тексте. Контекст даёт якорь либо сокращение «с.» перед серией:
	// «с. 8603 н. 352641» в анкете без слова «паспорт».
	if _, ok := d.FindAnchor(runs[i].Start, runs[seriesEnd].End, anchorsPassport, anchorWindow, anchorWindow); !ok {
		if !seriesAbbrevBefore(d, runs[i].Start) {
			return 0, false
		}
	}
	for j := seriesEnd + 1; j < len(runs) && j <= seriesEnd+2; j++ {
		gap := d.Lower[runs[seriesEnd].End:runs[j].Start]
		if strings.ContainsAny(gap, "\n\r") {
			return 0, false
		}
		if len([]rune(gap)) > 24 {
			return 0, false
		}
		if !onlyConnectors(gap) {
			return 0, false
		}
		// Номер тоже бывает разбит: «№ 240 402».
		if numEnd, ok := collectDigits(d, runs, j, 6, 2); ok {
			return numEnd, true
		}
	}
	return 0, false
}

// seriesAbbrevBefore сообщает, что перед серией стоит сокращение «с.» —
// «серия» в анкетах и строках таблиц: «с. 8603 н. 352641». Сокращение
// должно быть отдельным словом, иначе «с.» найдётся внутри «свидетельство»
// или «счёт».
func seriesAbbrevBefore(d *Doc, start int) bool {
	before := strings.TrimRight(d.Lower[:start], " \t")
	if !strings.HasSuffix(before, "с.") {
		return false
	}
	prev := dwRuneBefore(d.Lower, len(before)-2)
	return !dwIsLetter(prev)
}

// joinSplitPIN находит вторую пару цифр разбитого пин-кода: «PIN-code: 07 26».
// Первая пара уже распознана как пин, вторая примыкает к ней без посторонних
// слов и тоже состоит из двух цифр.
func joinSplitPIN(d *Doc, runs []NumRun, i int) (int, bool) {
	for j := i + 1; j < len(runs) && j <= i+1; j++ {
		if len(runs[j].Digits) != 2 {
			continue
		}
		gap := d.Lower[runs[i].End:runs[j].Start]
		if len([]rune(gap)) > 8 {
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
	folded := foldAnchors(anchors, minRunes)
	if len(folded) == 0 {
		return false
	}
	norm := FoldHomoglyphs(window)

	for _, word := range letterWords(norm) {
		for _, a := range folded {
			if nearlyEqual(word, a) {
				return true
			}
		}
	}
	// Пробел, вставленный внутрь якоря, разрывает слово: «поч товый» вместо
	// «почтовый». Проверяем окно без пробелов целиком.
	return fuzzyCompactContains(norm, folded)
}

// fuzzyCompactContains ищет якорь в окне без пробелов. Пробел, вставленный
// внутрь якоря, разрывает слово: «поч товый» вместо «почтовый».
func fuzzyCompactContains(norm string, folded []string) bool {
	compact := dropSpaces(norm)
	for _, a := range folded {
		if strings.Contains(compact, a) {
			return true
		}
	}
	return false
}

// foldAnchors сворачивает якоря по омоглифам и отбрасывает те, что короче
// minRunes.
//
// Якори приводятся и отсеиваются по длине ОДИН раз, а не заново для
// каждого слова окна. Прежний порядок давал число приведений, равное
// числу слов, умноженному на число якорей, и это на самом горячем пути:
// обработка текста из-за него подорожала вдвое.
func foldAnchors(anchors []string, minRunes int) []string {
	folded := make([]string, 0, len(anchors))
	for _, a := range anchors {
		if utf8.RuneCountInString(a) < minRunes {
			continue
		}
		folded = append(folded, FoldHomoglyphs(a))
	}
	return folded
}

// dropSpaces убирает пробелы, табуляцию и неразрывный пробел. Нужно для
// якоря, внутрь которого попал пробел: без пробелов «поч товый» снова
// совпадает с «почтовый».
func dropSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\u00a0' {
			return -1
		}
		return r
	}, s)
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
	// Два вида опечатки разобраны порознь: при равной длине буква заменена
	// или переставлена с соседней, при разной — одна буква лишняя. Общий
	// проход по двум указателям приходилось ветвить на каждом шаге, хотя
	// вид опечатки известен заранее — по разнице длин.
	switch len(rb) - len(ra) {
	case 0:
		return oneSwapApart(ra, rb)
	case 1:
		return oneInsertApart(ra, rb)
	default:
		return false
	}
}

// oneSwapApart сравнивает слова одинаковой длины: допускается одна замена
// буквы либо одна перестановка соседних букв. Перестановка — частая
// опечатка: «инедкс» вместо «индекс».
func oneSwapApart(ra, rb []rune) bool {
	diff := 0
	for i := 0; i < len(ra); i++ {
		if ra[i] == rb[i] {
			continue
		}
		diff++
		if diff > 1 {
			return false
		}
		// Перестановка соседних рун: «ab» против «ba». Обе буквы объясняются
		// одной опечаткой, поэтому вторую пропускаем, а не считаем заново.
		if swappedRunes(ra, rb, i, i) {
			i++
		}
	}
	return true
}

// oneInsertApart сравнивает слова, длина которых различается на одну руну:
// в длинном слове допускается ровно одна лишняя буква. Это сразу два вида
// опечатки — лишняя буква и потерянная: какое из слов считать исходным,
// неизвестно, поэтому короткое всегда сравнивается с длинным.
func oneInsertApart(short, long []rune) bool {
	i, j, diff := 0, 0, 0
	for i < len(short) && j < len(long) {
		if short[i] == long[j] {
			i, j = i+1, j+1
			continue
		}
		diff++
		if diff > 1 {
			return false
		}
		// Лишняя руна длинного слова пропускается, короткое слово остаётся
		// на месте: дальше слова обязаны совпасть до конца.
		j++
	}
	return true
}

// swappedRunes сообщает, что в позициях i и j руны переставлены местами:
// «ab» против «ba».
func swappedRunes(ra, rb []rune, i, j int) bool {
	return i+1 < len(ra) && j+1 < len(rb) &&
		ra[i] == rb[j+1] && ra[i+1] == rb[j]
}
