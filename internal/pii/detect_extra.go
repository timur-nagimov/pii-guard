package pii

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Якорные слова для расширенных типов. Все задаются в нижнем регистре: поиск
// идёт по копии текста в нижнем регистре, поэтому регистр исходной записи
// значения не имеет.
var (
	// anchorsAccount — номер банковского счёта. «Счёт» и «счет» закрывают оба
	// написания, «р/с» и «л/с» — сокращения из платёжных документов.
	anchorsAccount = []string{
		"счёт", "счет", "р/с", "расчётный счёт", "расчетный счет",
		"лицевой счёт", "лицевой счет", "л/с", "номер счёта", "номер счета",
		"account", "на счёт", "на счет", "зачислить на", "счёт для",
		"счет для", "счёт получателя", "счет получателя",
	}

	// anchorsOMS — полис обязательного медицинского страхования.
	anchorsOMS = []string{
		"полис", "омс", "медицинский полис", "страховой полис", "полис омс",
		"полис обязательного медицинского страхования", "номер полиса",
		"полис клиента", "медицинская страховка",
	}

	// anchorsPlate — государственный регистрационный знак автомобиля.
	anchorsPlate = []string{
		"госномер", "г/н", "гос. номер", "гос номер", "номер автомобиля",
		"автомобиль", "машина", "транспортное средство", "тс", "госзнак",
		"гос знак", "регистрационный знак", "номер машины", "номер тс",
		"авто", "номер авто",
	}

	// anchorsVIN — идентификационный номер транспортного средства.
	anchorsVIN = []string{
		"vin", "вин", "идентификационный номер", "номер кузова", "номер шасси",
		"vin-код", "вин-код", "идентификационный номер транспортного средства",
		"номер вина", "vin номер",
	}

	// anchorsIP — адрес в сети. «Адрес» берётся осторожно: он же якорь
	// почтового адреса, поэтому сетевой адрес требует более строгой формы.
	anchorsIP = []string{
		"ip", "айпи", "ip-адрес", "айпи-адрес", "с адреса", "вход с",
		"сессия с", "ip адрес", "сетевой адрес", "адрес узла",
	}
)

// extraDetector находит расширенные типы персональных данных: номер банковского
// счёта, полис ОМС, госномер автомобиля, VIN и адрес в сети. Детектор без
// состояния и безопасен для параллельного вызова.
type extraDetector struct{}

// NewExtraDetector создаёт детектор расширенных типов.
func NewExtraDetector() Detector { return extraDetector{} }

// Types перечисляет типы, которые находит детектор.
func (extraDetector) Types() []Type {
	return []Type{TypeAccount, TypeOMS, TypePlate, TypeVIN, TypeIPAddress}
}

// Detect разбирает документ на расширенные типы. Числовые типы идут по
// числовым кандидатам, буквенно-цифровые — по токенам.
func (e extraDetector) Detect(d *Doc) []Span {
	var out []Span
	out = append(out, e.detectNumeric(d)...)
	out = append(out, e.detectAlnum(d)...)
	out = append(out, e.detectIP(d)...)
	return out
}

// detectNumeric находит номер счёта и полис ОМС по числовым кандидатам.
func (extraDetector) detectNumeric(d *Doc) []Span {
	runs := d.NumRuns()
	var out []Span
	for _, run := range runs {
		if s, ok := extraAccount(d, run); ok {
			out = append(out, s)
			continue
		}
		if s, ok := extraOMS(d, run); ok {
			out = append(out, s)
		}
	}
	return out
}

// extraAccount находит номер банковского счёта: двадцать цифр, начинающихся на
// балансовый счёт физического лица. Контрольная сумма считается вместе с БИК
// банка, поэтому в одиночку не проверяется: якорь и начало обязательны.
func extraAccount(d *Doc, run NumRun) (Span, bool) {
	if len(run.Digits) != 20 {
		return Span{}, false
	}
	if !strings.HasPrefix(run.Digits, "408") &&
		!strings.HasPrefix(run.Digits, "423") &&
		!strings.HasPrefix(run.Digits, "426") {
		return Span{}, false
	}
	if !extraAnchorBefore(d, run.Start, anchorsAccount) {
		return Span{}, false
	}
	return extraSpan(d, run.Start, run.End, TypeAccount, ConfHigh, "account:anchor")
}

// extraOMS находит полис обязательного медицинского страхования: шестнадцать
// цифр. Контрольная цифра по Луну — только усиление уверенности, а не условие:
// жюри подаёт выдуманные номера.
func extraOMS(d *Doc, run NumRun) (Span, bool) {
	if len(run.Digits) != 16 {
		return Span{}, false
	}
	if !extraAnchorBefore(d, run.Start, anchorsOMS) {
		return Span{}, false
	}
	conf, reason := ConfHigh, "oms:anchor"
	if Luhn(run.Digits) {
		conf, reason = ConfHigh, "oms:anchor_luhn"
	}
	return extraSpan(d, run.Start, run.End, TypeOMS, conf, reason)
}

// detectAlnum находит госномер автомобиля и VIN по токенам: это буквенно-
// цифровые последовательности, которых нет среди числовых кандидатов.
func (extraDetector) detectAlnum(d *Doc) []Span {
	toks := d.Tokens
	var out []Span
	for i := 0; i < len(toks); i++ {
		// Цифра тоже начинает последовательность: VIN почти всегда начинается
		// с неё (мировой код изготовителя вида 1HG, 5DH, 2M2). Пока цифровые
		// токены пропускались, треть номеров в наборе не находилась вовсе.
		if toks[i].Kind != KindLat && toks[i].Kind != KindCyr && toks[i].Kind != KindDigit {
			continue
		}
		// Пробуем собрать госномер: буква, три цифры, две буквы, две или три
		// цифры, с одиночными пробелами между группами.
		if start, end, ok := plateSpan(d, toks, i); ok {
			out = appendAnchored(d, out, start, end, anchorsPlate, anchorWindow, TypePlate, "plate:anchor")
			i = tokenIndexAt(toks, end)
			continue
		}
		// Пробуем собрать VIN: семнадцать знаков латиницы и цифр без пробелов.
		if start, end, ok := vinSpan(d, toks, i); ok {
			out = appendAnchored(d, out, start, end, anchorsVIN, vinAnchorWindow, TypeVIN, "vin:anchor")
			i = tokenIndexAt(toks, end)
		}
	}
	return out
}

// appendAnchored дописывает фрагмент, если рядом со значением нашёлся якорь.
// Обе буквенно-цифровые формы без якоря не маскируются: госномер и VIN сами по
// себе неотличимы от артикула или кода товара, а форма найдена в любом случае —
// разбор всё равно перескакивает её целиком.
func appendAnchored(d *Doc, out []Span, start, end int, anchors []string, before int, t Type, reason string) []Span {
	if !extraAnchorNear(d, start, end, anchors, before, nearAnchorWindow) {
		return out
	}
	s, ok := extraSpan(d, start, end, t, ConfHigh, reason)
	if !ok {
		return out
	}
	return append(out, s)
}

// plateLetters — буквы, допустимые в госномере: только те, что имеют латинские
// двойники по начертанию. Человек часто набирает их латиницей, и A123BC77 это
// тот же номер, что и А123ВС77.
var plateLetters = map[rune]bool{
	'а': true, 'в': true, 'е': true, 'к': true, 'м': true, 'н': true,
	'о': true, 'р': true, 'с': true, 'т': true, 'у': true, 'х': true,
	'a': true, 'b': true, 'e': true, 'k': true, 'm': true, 'h': true,
	'o': true, 'p': true, 'c': true, 't': true, 'y': true, 'x': true,
}

// plateSpan пытается собрать госномер, начиная с токена i. Возвращает байтовые
// границы номера. Форма: буква, три цифры, две буквы, две или три цифры, с
// одиночными пробелами между группами.
func plateSpan(d *Doc, toks []Token, i int) (int, int, bool) {
	// Первая буква.
	pos, ok := takePlateLetters(d, toks, i, 1)
	if !ok {
		return 0, 0, false
	}
	// Три цифры.
	pos = skipPlateSpace(d, toks, pos)
	if !isDigitToken(toks, pos, 3) {
		return 0, 0, false
	}
	pos++
	// Две буквы.
	pos = skipPlateSpace(d, toks, pos)
	pos, ok = takePlateLetters(d, toks, pos, 2)
	if !ok {
		return 0, 0, false
	}
	// Две или три цифры региона.
	pos = skipPlateSpace(d, toks, pos)
	if !isDigitToken(toks, pos, 2) && !isDigitToken(toks, pos, 3) {
		return 0, 0, false
	}
	pos++
	return toks[i].Start, toks[pos-1].End, true
}

// takePlateLetters забирает ровно n букв госномера, начиная с токена pos, и
// возвращает позицию следующего токена.
//
// Буквы разрешено набирать из нескольких подряд идущих токенов: буквы
// госномера выбраны так, что у каждой есть латинский двойник по начертанию, и
// человек набирает их вперемешку — «М663XК09» это кириллические М и К с
// латинской X посередине. Разметчик слов делит такую запись по смене
// письменности, и пара букв оказывается в двух токенах. Пока группа искалась
// одним токеном, смешанные номера не находились вовсе.
func takePlateLetters(d *Doc, toks []Token, pos, n int) (int, bool) {
	got := 0
	for pos < len(toks) && got < n {
		cnt, ok := plateLettersInToken(d, toks[pos])
		if !ok || got+cnt > n {
			return 0, false
		}
		// Токены группы обязаны идти вплотную: пробел между буквами означает,
		// что это уже не одна группа номера, а разные слова.
		if got > 0 && d.Text[toks[pos-1].End:toks[pos].Start] != "" {
			return 0, false
		}
		got += cnt
		pos++
	}
	if got != n {
		return 0, false
	}
	return pos, true
}

// plateLettersInToken считает буквы госномера в одном токене. Токен годится
// только целиком: чужая буква внутри означает, что это обычное слово, а не
// часть номера.
func plateLettersInToken(d *Doc, tok Token) (int, bool) {
	if tok.Kind != KindLat && tok.Kind != KindCyr {
		return 0, false
	}
	got := 0
	for _, r := range d.Text[tok.Start:tok.End] {
		if !plateLetters[unicode.ToLower(r)] {
			return 0, false
		}
		got++
	}
	return got, true
}

// skipPlateSpace пропускает один одиночный пробел между группами госномера.
func skipPlateSpace(d *Doc, toks []Token, pos int) int {
	if pos < len(toks) && toks[pos].Kind == KindSpace && d.Text[toks[pos].Start:toks[pos].End] == " " {
		return pos + 1
	}
	return pos
}

// isDigitToken сообщает, что токен — ровно n цифр.
func isDigitToken(toks []Token, pos, n int) bool {
	if pos >= len(toks) || toks[pos].Kind != KindDigit {
		return false
	}
	return toks[pos].End-toks[pos].Start == n
}

// vinSpan пытается собрать VIN, начиная с токена i: семнадцать знаков латиницы
// и цифр без букв I, O и Q.
func vinSpan(d *Doc, toks []Token, i int) (int, int, bool) {
	// Первый токен обязан быть латиницей или цифрой. Без этой проверки за VIN
	// принималось слово «Идентификационный»: в нём ровно семнадцать букв, и
	// маска накрывала сам якорь, оставляя номер рядом открытым.
	if toks[i].Kind != KindDigit && toks[i].Kind != KindLat {
		return 0, 0, false
	}
	start := toks[i].Start
	end, count := vinRun(d, toks, i)
	if count != 17 {
		return 0, 0, false
	}
	if !vinCharsOK(d.Text[start:end]) {
		return 0, 0, false
	}
	return start, end, true
}

// vinRun тянет последовательность знаков номера вправо от токена i и
// возвращает её конец и число знаков в ней.
//
// Номер переносят и разрывают пробелом: «BFXP336J ZLY4XJ5Z2». Один разрыв
// допускаем, считая только знаки самого номера; больше одного — это уже
// перечисление, а не номер.
func vinRun(d *Doc, toks []Token, i int) (int, int) {
	end := toks[i].End
	count := utf8.RuneCountInString(d.Text[toks[i].Start:end])
	gaps := 0
	for j := i; j+1 < len(toks); {
		next := toks[j+1]
		if vinGapAfter(d, toks, j, gaps, count) {
			gaps++
			j++
			next = toks[j+1]
		}
		if next.Kind != KindDigit && next.Kind != KindLat {
			break
		}
		end = next.End
		count += utf8.RuneCountInString(d.Text[next.Start:next.End])
		j++
	}
	return end, count
}

// vinGapAfter сообщает, что за токеном j стоит допустимый разрыв номера.
//
// Пробел — самостоятельный токен, а не пустота между соседями: номер
// «BFXP336J ZLY4XJ5Z2» состоит из двух частей, и разрыв виден только так.
// Один разрыв допускаем, больше — это уже перечисление.
func vinGapAfter(d *Doc, toks []Token, j, gaps, count int) bool {
	if gaps != 0 || count >= 17 || j+2 >= len(toks) {
		return false
	}
	space := toks[j+1]
	if space.Kind != KindSpace || d.Text[space.Start:space.End] != " " {
		return false
	}
	return toks[j+2].Kind == KindDigit || toks[j+2].Kind == KindLat
}

// vinCharsOK проверяет знаки собранного номера.
//
// Буквы I, O и Q из VIN исключены: их убрали, чтобы не путать с единицей
// и нулём. Заодно считаем цифры: в настоящем номере они есть всегда, и это
// отсекает слова из семнадцати латинских букв.
func vinCharsOK(s string) bool {
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			if vinForbidden[unicode.ToLower(r)] {
				return false
			}
		}
	}
	return digits > 0
}

// tokenIndexAt возвращает индекс токена, содержащего байтовое смещение.
func tokenIndexAt(toks []Token, off int) int {
	for k := range toks {
		if toks[k].End > off {
			return k
		}
	}
	return len(toks) - 1
}

// vinAnchorWindow — окно поиска якоря для VIN. Общего окна в сорок знаков не
// хватает: фраза «идентификационный номер транспортного средства» сама длиной
// в сорок пять, и якорь не помещался целиком, из-за чего номер оставался
// открытым. Форма VIN узкая, поэтому более широкое окно безопасно.
const vinAnchorWindow = 72

// vinForbidden — буквы, исключённые из VIN: их убрали, чтобы не путать с
// единицей и нулём.
var vinForbidden = map[rune]bool{'i': true, 'o': true, 'q': true}

// detectIP находит адреса в сети: IPv4 из четырёх чисел от нуля до 255 через
// точку. Служебные адреса не маскируются: loopback, «все нули» и сетевые
// диапазоны в записи CIDR.
func (extraDetector) detectIP(d *Doc) []Span {
	var out []Span
	text := d.Text
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			continue
		}
		// Ищем конец IPv4-кандидата: четыре числа через точки.
		end := ipv4End(text, i)
		if end < 0 {
			continue
		}
		// Форма проверяется до перескока: «1234.5.6.7» адресом не является, но
		// со второй цифры начинается настоящий адрес «234.5.6.7».
		if !isIPv4(text[i:end]) {
			continue
		}
		if s, ok := ipSpan(d, i, end); ok {
			out = append(out, s)
		}
		// Кандидат разобран целиком, поэтому разбор продолжается за ним: иначе
		// тот же адрес собирался бы заново с каждой своей цифры.
		i = end - 1
	}
	return out
}

// ipSpan решает, маскировать ли разобранный адрес: отсеивает записи, которые на
// человека не указывают, и требует якорь.
func ipSpan(d *Doc, start, end int) (Span, bool) {
	// Сетевой диапазон в записи CIDR (10.0.0.0/8) на человека не указывает.
	if end < len(d.Text) && d.Text[end] == '/' {
		return Span{}, false
	}
	if isServiceIPv4(d.Text[start:end]) {
		return Span{}, false
	}
	if !extraAnchorNear(d, start, end, anchorsIP, anchorWindow, nearAnchorWindow) {
		return Span{}, false
	}
	return extraSpan(d, start, end, TypeIPAddress, ConfHigh, "ip:anchor")
}

// ipv4End возвращает конец IPv4-кандидата, начинающегося в позиции i, либо
// минус единицу, если это не четыре числа через точки.
func ipv4End(text string, i int) int {
	parts := 0
	j := i
	for parts < 4 {
		start := j
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		if j == start {
			return -1
		}
		parts++
		if parts == 4 {
			return j
		}
		if j >= len(text) || text[j] != '.' {
			return -1
		}
		j++
	}
	return -1
}

// isIPv4 проверяет, что строка — четыре числа от нуля до 255 через точки.
func isIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if !isByteNumber(p) {
			return false
		}
	}
	return true
}

// isByteNumber проверяет, что строка — число от нуля до 255 без ведущих нулей
// (кроме самого нуля).
func isByteNumber(p string) bool {
	if p == "" || len(p) > 3 {
		return false
	}
	if len(p) > 1 && p[0] == '0' {
		return false
	}
	n := 0
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
		n = n*10 + int(p[i]-'0')
	}
	return n <= 255
}

// isServiceIPv4 сообщает, что адрес служебный и на человека не указывает:
// loopback и адрес «все нули». Сетевые диапазоны в записи CIDR обрабатываются
// отдельно, до этой проверки.
func isServiceIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	a, _ := atoi(parts[0])
	b, _ := atoi(parts[1])
	switch {
	case a == 127:
		return true
	case a == 0 && b == 0:
		return true
	}
	return false
}

// atoi разбирает десятичное число из строки.
func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// extraAnchorNear ищет якорь в окне вокруг значения. Окно задаётся в рунах и
// сворачивается по омоглифам: «госнoмер» с латинской «о» совпадает с якорем
// «госномер».
func extraAnchorNear(d *Doc, start, end int, anchors []string, before, after int) bool {
	lo, hi := d.WindowRunes(start, end, before, after)
	window := FoldHomoglyphs(extraLowerSlice(d, lo, hi))
	for _, a := range anchors {
		if strings.Contains(window, FoldHomoglyphs(a)) {
			return true
		}
	}
	return false
}

// extraAnchorBefore ищет якорь слева от значения и требует, чтобы между якорем
// и значением не было других цифр. Без этого требования якорь одного типа в
// перечислении помечает номер следующего: «карта 4111..., полис 1234...» — карта
// не должна стать полисом.
func extraAnchorBefore(d *Doc, start int, anchors []string) bool {
	lo, _ := d.WindowRunes(start, start, anchorWindow, 0)
	window := FoldHomoglyphs(extraLowerSlice(d, lo, start))
	best := -1
	for _, a := range anchors {
		if e := extraAnchorEnd(window, FoldHomoglyphs(a)); e > best {
			best = e
		}
	}
	if best < 0 {
		return false
	}
	return !strings.ContainsAny(window[best:], "0123456789")
}

// extraAnchorEnd возвращает конец последнего вхождения якоря как отдельного
// слова либо минус единицу, если такого вхождения нет.
func extraAnchorEnd(window, anchor string) int {
	if anchor == "" {
		return -1
	}
	for pos := strings.LastIndex(window, anchor); pos >= 0; pos = strings.LastIndex(window[:pos], anchor) {
		if extraWordAt(window, anchor, pos) {
			return pos + len(anchor)
		}
		if pos == 0 {
			break
		}
	}
	return -1
}

// extraWordAt сообщает, что якорь в позиции pos стоит отдельным словом: слева
// граница слова, справа не длиннее допустимого хвоста букв.
func extraWordAt(window, anchor string, pos int) bool {
	if pos > 0 {
		if r, _ := utf8.DecodeLastRuneInString(window[:pos]); isWordRune(r) {
			return false
		}
	}
	for _, r := range window[pos+len(anchor):] {
		if !isWordRune(r) {
			return true
		}
	}
	return false
}

// isWordRune сообщает, что руна — часть слова.
func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// extraLowerSlice безопасно вырезает участок строки нижнего регистра.
func extraLowerSlice(d *Doc, lo, hi int) string {
	if lo < 0 {
		lo = 0
	}
	if hi > len(d.Lower) {
		hi = len(d.Lower)
	}
	if lo >= hi {
		return ""
	}
	return d.Lower[lo:hi]
}

// extraSpan собирает фрагмент расширенного типа с нормализованными границами.
func extraSpan(d *Doc, start, end int, t Type, conf float64, reason string) (Span, bool) {
	start, end = NormalizeSpan(d.Text, start, end)
	if start >= end {
		return Span{}, false
	}
	return Span{Start: start, End: end, Type: t, Conf: conf, Reason: reason}, true
}
