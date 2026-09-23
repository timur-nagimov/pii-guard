package pii

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"pii-guard/internal/pii/dict"
)

// fioRole хранит признак, по которому слово может быть компонентом имени.
// Одно слово несёт сразу несколько признаков: «Романов» это и фамилия из
// словаря, и слово с фамильным окончанием.
type fioRole uint16

// Признаки компонентов имени.
const (
	fioRoleName fioRole = 1 << iota
	fioRoleSurnameDict
	fioRoleSurnameStrong
	fioRoleSurnameWeak
	fioRoleSurnameAdjective
	fioRoleSurnameLatin
	fioRolePatronymic
	fioRolePatronymicWeak
	fioRoleInitial
	fioRoleUnknownCap
	fioRoleInitialsPair
)

// Наборы признаков, которые в правилах используются вместе.
const (
	fioRoleAnySurname    = fioRoleSurnameDict | fioRoleSurnameStrong | fioRoleSurnameAdjective | fioRoleSurnameWeak | fioRoleSurnameLatin
	fioRoleAnyPatronymic = fioRolePatronymic | fioRolePatronymicWeak
	// fioRoleNameCore — признаки, которые сами по себе опознают компонент
	// имени. К такому компоненту разрешено приклеить незнакомое слово с
	// заглавной буквы: словарь не знает всех имён и фамилий.
	fioRoleNameCore = fioRoleName | fioRoleSurnameDict | fioRoleSurnameStrong | fioRolePatronymic
)

// fioAnchorWindow задаёт окно поиска якоря слева от имени, в рунах. Якорь
// обычно стоит вплотную, но между ним и именем помещается служебное слово
// вроде «банка», поэтому окна в одно слово не хватает.
const fioAnchorWindow = 32

// fioMinSurnameRunes задаёт наименьшую длину слова, при которой фамильное
// окончание считается признаком фамилии. Короткие слова дают слишком много случайных
// совпадений, а короткие фамилии вроде Цой берутся из словаря.
const fioMinSurnameRunes = 5

// fioMinPatronymicRunes задаёт наименьшую длину отчества.
const fioMinPatronymicRunes = 5

// fioMinUnknownRunes задаёт наименьшую длину незнакомого слова с заглавной
// буквы, которое разрешено приклеить к опознанному компоненту имени.
// Слова короче трёх букв это почти всегда сокращения вроде «Ул» и «Об».
const fioMinUnknownRunes = 3

// fioMaxAbbrevRunes задаёт наибольшую длину сокращения с точкой.
const fioMaxAbbrevRunes = 5

// fioMaxGapRunes задаёт наибольшую длину промежутка между соседними
// компонентами имени. Промежуток состоит только из пробелов и точек после инициалов.
const fioMaxGapRunes = 3

// fioWord описывает слово-кандидат в компоненты имени вместе с разобранными
// признаками. Слово хранится в байтовых границах исходного текста.
type fioWord struct {
	start int
	end   int
	norm  string
	key   string
	// latinKey хранит исходную латинскую запись слова. Для латинских слов
	// словарь проверяется и по ней: обратная транслитерация теряет мягкий
	// знак («olga» → «олга») и путает «ья» и «я» («tatiana» → «татяна»).
	latinKey string
	capital  bool
	latin    bool
	dot      bool
	// shout означает, что слово написано целиком заглавными: так пишут
	// сокращения вроде ООО и ЗАО, а не имена людей.
	shout bool
	// sentStart означает, что слово открывает предложение. Заполняется только
	// для незнакомых слов, остальным признак не нужен.
	sentStart bool
	roles     fioRole
}

// has сообщает, что у слова есть хотя бы один из перечисленных признаков.
func (w fioWord) has(r fioRole) bool { return w.roles&r != 0 }

// isInitial сообщает, что слово это инициал с точкой. Инициал без точки
// принимается только рядом с другим инициалом, поэтому точка проверяется
// отдельно от самого признака.
func (w fioWord) isInitial() bool { return w.has(fioRoleInitial) && w.dot }

// fioDetector находит имена людей: полное ФИО, пары из имени и фамилии,
// одиночные компоненты рядом с якорем, инициалы и транслитерацию латиницей.
//
// Детектор не хранит состояния между вызовами и безопасен для параллельной
// работы: все словари только читаются.
type fioDetector struct{}

// NewFIODetector создаёт детектор имён людей.
func NewFIODetector() Detector { return fioDetector{} }

// Types перечисляет типы, которые находит детектор.
func (fioDetector) Types() []Type { return []Type{TypeFIO} }

// Detect разбирает текст на слова-кандидаты, собирает их в цепочки соседних
// слов и подбирает для каждой цепочки самую длинную комбинацию компонентов.
func (fioDetector) Detect(d *Doc) []Span {
	words, lower := fioCollectWords(d)
	var out []Span
	start := 0
	for i := 1; i <= len(words); i++ {
		if i < len(words) && fioAdjacent(d, words[i-1], words[i]) {
			continue
		}
		out = append(out, fioScanRun(d, words[start:i])...)
		start = i
	}
	// Фамилии со строчной буквы разбираются последним шагом: им нужна опора
	// на уже найденные фрагменты.
	out = append(out, fioLowerSurnames(d, words, lower, out)...)
	SortSpans(out)
	return out
}

// fioCollectWords собирает слова, у которых есть хотя бы один признак
// компонента имени. Слова без признаков пропускаются: они всё равно разрывают
// цепочку, потому что промежуток с буквами не считается соседством.
//
// Вторым списком возвращаются кандидаты в фамилии со строчной буквы: у них
// признаков нет, потому что одного фамильного окончания без заглавной буквы
// для решения не хватает. Они разбираются отдельным шагом по опоре на соседей.
func fioCollectWords(d *Doc) (words, lower []fioWord) {
	words = make([]fioWord, 0, 8)
	toks := d.Tokens
	for i := 0; i < len(toks); i++ {
		if toks[i].Kind != KindCyr && toks[i].Kind != KindLat {
			continue
		}
		end, last := fioWordEnd(d, i)
		w := fioNewWord(d, toks[i].Start, end)
		if w.roles == 0 && fioLowerSurnameCandidate(w) {
			w, last = fioGlueLowerSurname(d, w, last, end)
		}
		switch {
		case w.roles != 0:
			words = append(words, w)
		case fioLowerSurnameCandidate(w):
			lower = append(lower, w)
		default:
			// Слово без признаков не запоминается ни одним списком, но
			// цепочку соседей оно всё равно разрывает: промежуток с буквами
			// соседством не считается.
		}
		i = last
	}
	return words, lower
}

// fioGlueLowerSurname приклеивает к кандидату в фамилию вторую часть через
// дефис, написанную со строчной буквы. Двойная фамилия со строчной буквы
// склеивается здесь, а не в fioWordEnd: там часть после дефиса обязана
// начинаться с заглавной буквы, иначе к любому имени приклеится обычное
// слово. Склейка принимается, только если целиком она сама остаётся
// кандидатом: иначе к фамилии прилипнет соседнее слово через дефис.
func fioGlueLowerSurname(d *Doc, w fioWord, last, end int) (word fioWord, wordLast int) {
	hEnd, hLast, ok := fioLowerHyphenEnd(d, last, end)
	if !ok {
		return w, last
	}
	hw := fioNewWord(d, w.start, hEnd)
	if hw.roles == 0 && !fioLowerSurnameCandidate(hw) {
		return w, last
	}
	return hw, hLast
}

// fioWordEnd находит конец слова, приклеивая части через дефис: двойные
// фамилии вроде Петров-Водкин и составные имена вроде Анна-Мария это один
// компонент. Часть после дефиса обязана начинаться с заглавной буквы либо
// быть похожей на фамилию: «Петров-Водкин» и «петров-водкин» это одна
// фамилия, а «иванов-то» и «нагимов-банк» — нет.
func fioWordEnd(d *Doc, i int) (int, int) {
	toks := d.Tokens
	end, last := toks[i].End, i
	for last+2 < len(toks) {
		hyphen, next := toks[last+1], toks[last+2]
		if hyphen.Kind != KindPunct || d.Text[hyphen.Start:hyphen.End] != "-" {
			break
		}
		if hyphen.Start != end || next.Start != hyphen.End || next.Kind != toks[i].Kind {
			break
		}
		if !fioStartsUpper(d.Text[next.Start:next.End]) {
			part := fioNewWord(d, next.Start, next.End)
			if part.capital || !fioLowerSurnamePart(part) {
				break
			}
		}
		end, last = next.End, last+2
	}
	return end, last
}

// fioNewWord разбирает слово и определяет его признаки. Латинская запись
// переводится в кириллицу, чтобы сравниваться с тем же словарём.
func fioNewWord(d *Doc, start, end int) fioWord {
	w := fioWord{start: start, end: end}
	w.norm = dict.Normalize(d.Lower[start:end])
	w.capital = fioStartsUpper(d.Text[start:end])
	w.shout = w.capital && fioAllUpper(d.Text[start:end])
	w.latin = d.Tokens[d.TokenIndexAt(start)].Kind == KindLat
	w.dot = end < len(d.Text) && d.Text[end] == '.'
	w.key = w.norm
	if w.latin {
		w.key = dict.TranslitToCyr(w.norm)
		w.latinKey = w.norm
	}
	w.roles = fioWordRoles(w)
	if w.roles == 0 && fioUnknownCapable(w) {
		w.roles = fioRoleUnknownCap
		w.sentStart = fioAtSentenceStart(d, start)
	}
	return w
}

// fioUnknownCapable решает, что незнакомое слово с заглавной буквы может
// оказаться редким именем или фамилией. Словари не покрывают всех имён, и
// именно на границе словаря фрагмент обрывался посреди ФИО.
//
// Слово отбрасывается, если это сокращение заглавными, служебное слово,
// маркер улицы, должность, известное географическое название или обычное
// слово из списка исключений: такие слова именем не бывают.
func fioUnknownCapable(w fioWord) bool {
	if !w.capital || w.latin || w.shout {
		return false
	}
	n := fioRuneCount(w.norm)
	if n < fioMinUnknownRunes {
		return false
	}
	// Короткое слово с точкой это сокращение вроде «Акад.» или «Респ.»,
	// а не имя.
	if w.dot && n <= fioMaxAbbrevRunes {
		return false
	}
	if fioAddressMarkers[w.norm] || fioAnchors[w.norm] || fioFillerWords[w.norm] {
		return false
	}
	if fioStopWord(w.norm) || fioCommonWords[w.norm] {
		return false
	}
	return !fioGeoWord(w.norm)
}

// fioGeoWord сообщает, что слово это известный город или страна. Кроме самой
// формы проверяется начальная: словарь стран падежных форм не хранит, а в
// тексте встречается «гражданин России».
func fioGeoWord(norm string) bool {
	if _, ok := dict.Place(norm); ok {
		return true
	}
	runes := []rune(norm)
	if len(runes) < 4 {
		return false
	}
	base := string(runes[:len(runes)-1])
	for _, tail := range []string{"а", "я", "ь", ""} {
		if _, ok := dict.Place(base + tail); ok {
			return true
		}
	}
	return false
}

// fioLowerHyphenEnd приклеивает к кандидату со строчной буквы вторую часть
// двойной фамилии: «нагимов-хайруллин». Обе части обязаны быть фамильными по
// окончанию, иначе к фамилии приклеится обычное слово через дефис вроде
// «иванов-то» или «нагимов-банк».
func fioLowerHyphenEnd(d *Doc, last, end int) (int, int, bool) {
	toks := d.Tokens
	ok := false
	for last+2 < len(toks) {
		hyphen, next := toks[last+1], toks[last+2]
		if hyphen.Kind != KindPunct || d.Text[hyphen.Start:hyphen.End] != "-" {
			break
		}
		if hyphen.Start != end || next.Start != hyphen.End || next.Kind != KindCyr {
			break
		}
		part := fioNewWord(d, next.Start, next.End)
		if part.capital || !fioLowerSurnamePart(part) {
			break
		}
		end, last, ok = next.End, last+2, true
	}
	return end, last, ok
}

// fioLowerSurnamePart проверяет часть двойной фамилии по тем же правилам, что
// и одиночного кандидата, но без требования к длине: вторая часть бывает
// короткой, а решение всё равно принимается по фрагменту целиком.
func fioLowerSurnamePart(w fioWord) bool {
	return fioHasSuffix(w.norm, fioLowerSurnameSuffixes) && !fioLowerTrap(w.norm) && !fioGeoWord(w.norm)
}

// fioWordRoles определяет признаки слова. Части двойной фамилии разбираются
// по отдельности: достаточно, чтобы имени соответствовала любая из них.
func fioWordRoles(w fioWord) fioRole {
	if fioRuneCount(w.norm) == 1 {
		// Инициалом считается одиночная буква с точкой независимо от регистра:
		// «Иванов И. И.» и «иванов и. и.» это одно и то же имя. В переписке
		// пишут «лебедев л. л.», и без строчных инициалов вся связка с именем
		// терялась. Одинокая строчная буква без точки — предлог или начало
		// слова.
		//
		// Адресные маркеры («ш.», «пл.», «им.») инициалами не бывают вовсе:
		// они помечают улицу, а не человека. Служебные буквы «г.», «д.»,
		// «с.», «п.» со строчной буквы это тоже адресные сокращения — «г.
		// Ростов», «д. Михайловка», — но рядом со вторым инициалом они
		// остаются настоящим инициалом: «николаева а.г.». Поэтому признак им
		// выдаётся здесь, а отсекаются они там, где решение принимается по
		// одному слову или по паре: fioSingleShape и fioInitialsPair.
		if w.capital || (w.dot && !fioAddressMarkers[w.norm]) {
			return fioRoleInitial
		}
		return 0
	}
	// Две заглавные буквы это пара инициалов: «Сорокина ВЯ» значит Сорокина
	// Вера Яковлевна, а «Максимов ДР.» — Максимов Дмитрий Романович. Слово
	// целиком заглавными вроде «ООО» и «ЗАО» сюда не попадает: у него три
	// буквы, а не две. Сокращения «ул.», «пл.», «г.» инициалами не бывают, а
	// правовые формы вроде «ИП» и «АО» стоят перед именем или названием, но
	// инициалами тоже не бывают.
	if fioRuneCount(w.norm) == 2 && w.shout && !fioAddressMarkers[w.norm] && !fioOrgAbbrev(w.norm) {
		return fioRoleInitialsPair
	}
	var roles fioRole
	for _, part := range strings.Split(w.key, "-") {
		roles |= fioPartRoles(part, w.capital)
	}
	// Латинская запись проверяется по латинскому словарю напрямую: обратная
	// транслитерация теряет мягкий знак и путает «ья» и «я», поэтому русский
	// словарь не находит «olga» и «tatiana». Английские имена и фамилии в
	// русском словаре отсутствуют вовсе.
	if w.latin {
		for _, part := range strings.Split(w.latinKey, "-") {
			roles |= fioLatinPartRoles(part)
		}
	}
	return roles
}

// fioOrgAbbrev сообщает, что сокращение заглавными это правовая форма, а не
// пара инициалов: «АО Полюс» это организация, а «ИП Новиков Сергей Петрович»
// — человек с полным именем, у которого фамилия, имя и отчество написаны
// целиком. «ИП» и «ЧП» проверяются отдельно от fioOrgForms: в том наборе их
// нет намеренно, потому что имя после них принадлежит живому человеку и
// маскироваться должно.
func fioOrgAbbrev(norm string) bool {
	return fioOrgForms[norm] || norm == "ип" || norm == "чп"
}

// fioLowerServiceInitial сообщает, что одиночная буква со строчной это
// служебное адресное сокращение: «г. Ростов», «д. Михайловка», «с.
// Петровское», «п. Ленино». Инициалом такая буква становится только рядом со
// вторым инициалом, поэтому там, где решение принимается по одному слову или
// по паре, она отбрасывается.
func fioLowerServiceInitial(w fioWord) bool {
	return w.has(fioRoleInitial) && !w.capital && fioLowerAbbrev[w.norm]
}

// fioLatinPartRoles определяет признаки латинской части слова по латинскому
// словарю и по транслитерированным окончаниям русских фамилий.
func fioLatinPartRoles(p string) fioRole {
	if fioRuneCount(p) < 2 {
		return 0
	}
	var roles fioRole
	if dict.LookupLatinName(p) {
		roles |= fioRoleName
	}
	if dict.LookupLatinSurname(p) {
		roles |= fioRoleSurnameDict
	}
	if fioLatinSurnameSuffix(p) {
		roles |= fioRoleSurnameLatin
	}
	return roles
}

// fioLatinSurnameSuffix сообщает, что латинское слово похоже на фамилию по
// транслитерированному русскому окончанию: -ov, -ova, -ev, -eva, -in, -ina,
// -sky, -skaya, -enko, -uk. Такие слова встречаются в латинской записи
// русских фамилий, которых нет в словаре.
func fioLatinSurnameSuffix(p string) bool {
	lower := strings.ToLower(p)
	for _, suf := range fioLatinSurnameSuffixes {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	return false
}

// fioLatinSurnameSuffixes — транслитерированные окончания русских фамилий.
var fioLatinSurnameSuffixes = []string{
	"ov", "ova", "ev", "eva", "in", "ina", "yn", "yna",
	"sky", "skaya", "skiy", "skoy", "enko", "uk", "yuk", "chuk",
	"shvili", "dze", "yan", "yanova",
}

// fioPartRoles определяет признаки одной части слова.
func fioPartRoles(p string, capital bool) fioRole {
	if fioRuneCount(p) < 2 {
		return 0
	}
	var roles fioRole
	if _, ok := dict.LookupName(p); ok {
		roles |= fioRoleName
	}
	if dict.LookupSurname(p) {
		roles |= fioRoleSurnameDict
	}
	roles |= fioPatronymicRole(p, capital)
	// Фамилия по окончанию требует заглавной буквы: иначе фамилией
	// становится обычное слово «клиентов» или «магазин».
	if capital {
		roles |= fioSurnameSuffixRole(p)
	}
	return roles
}

// fioPatronymicRole определяет, что слово похоже на отчество. Окончания
// «ович», «евич», «овна», «евна» настолько характерны, что заглавная буква
// для них не нужна. Окончания «ична» и «ич» встречаются и у обычных слов,
// поэтому они засчитываются только с заглавной буквы.
func fioPatronymicRole(p string, capital bool) fioRole {
	if fioHasSuffix(p, fioPatronymicTurkic) {
		return fioRolePatronymic
	}
	if fioRuneCount(p) < fioMinPatronymicRunes {
		return 0
	}
	if fioHasSuffix(p, fioPatronymicMale) || fioHasSuffix(p, fioPatronymicFemale) {
		return fioRolePatronymic
	}
	if !capital {
		return 0
	}
	if fioHasSuffix(p, fioPatronymicShortFemale) {
		return fioRolePatronymic
	}
	if fioHasSuffix(p, fioPatronymicWeakSuffixes) {
		return fioRolePatronymicWeak
	}
	return 0
}

// fioSurnameSuffixRole определяет, что слово похоже на фамилию по окончанию.
func fioSurnameSuffixRole(p string) fioRole {
	if fioRuneCount(p) < fioMinSurnameRunes {
		return 0
	}
	if fioHasSuffix(p, fioSurnameStrongSuffixes) {
		return fioRoleSurnameStrong
	}
	if fioHasSuffix(p, fioSurnameAdjectiveSuffixes) {
		return fioRoleSurnameAdjective
	}
	if fioHasSuffix(p, fioSurnameWeakSuffixes) {
		return fioRoleSurnameWeak
	}
	return 0
}

// fioAdjacent сообщает, что два слова идут подряд и относятся к одному имени.
// Между компонентами допускаются только пробелы и точки после инициалов,
// перевод строки и запятая цепочку разрывают.
//
// Исключение — запятая между фамилией и именем: «Иванов, Иван Иванович» это
// один человек, а не два. Запятая допускается только когда слева фамилия, а
// справа имя: «Иванов, Петров» остаётся списком двух людей.
func fioAdjacent(d *Doc, a, b fioWord) bool {
	if b.start < a.end {
		return false
	}
	gap := d.Text[a.end:b.start]
	if fioRuneCount(gap) > fioMaxGapRunes {
		return false
	}
	comma := false
	for _, r := range gap {
		// Неразрывный пробел встречается в тексте из офисных редакторов.
		if r == ',' {
			comma = true
			continue
		}
		if r != ' ' && r != '\t' && r != '.' && r != '\u00a0' {
			return false
		}
	}
	if comma && (!a.has(fioRoleAnySurname) || !b.has(fioRoleName)) {
		return false
	}
	return true
}

// fioScanRun подбирает комбинации компонентов в цепочке соседних слов.
// Разбор идёт слева направо и предпочитает более длинную комбинацию, поэтому
// полное ФИО не распадается на пару и одиночное слово.
func fioScanRun(d *Doc, run []fioWord) []Span {
	var out []Span
	loose := false
	for i := 0; i < len(run); {
		conf, reason, n := fioShape(d, run, i, loose)
		if n == 0 {
			// Инициал, который никуда не вошёл, запоминается: следующий за ним
			// инициал почти всегда часть подписи вроде «Ф.И.О.».
			loose = run[i].has(fioRoleInitial)
			i++
			continue
		}
		if sp, ok := fioMakeSpan(d, run[i], run[i+n-1], conf, reason); ok {
			out = append(out, sp)
		}
		loose = false
		i += n
	}
	return out
}

// fioShape подбирает комбинацию, начинающуюся с указанного слова, и
// возвращает уверенность, причину и число занятых слов. Признак loose
// сообщает, что слева остался неиспользованный инициал.
func fioShape(d *Doc, run []fioWord, i int, loose bool) (float64, string, int) {
	// Цепочка из трёх и более инициалов подряд это не имя, а подпись поля:
	// начинать с такого инициала разбор нельзя.
	if loose && run[i].has(fioRoleInitial) {
		return 0, "", 0
	}
	conf, reason, n := fioCoreShape(d, run, i)
	if n == 0 {
		return 0, "", 0
	}
	// Незнакомое слово справа от опознанного компонента входит во фрагмент:
	// «Иван Гремиславский» не должен обрываться на имени.
	if fioTakesUnknown(run, i+n) {
		n++
	}
	return conf, reason, n
}

// fioCoreShape подбирает комбинацию без учёта незнакомых слов справа.
func fioCoreShape(d *Doc, run []fioWord, i int) (float64, string, int) {
	if run[i].has(fioRoleUnknownCap) {
		return fioUnknownLead(d, run, i)
	}
	return fioKnownShape(d, run, i)
}

// fioUnknownLead разбирает цепочку, которая начинается с незнакомого слова с
// заглавной буквы. Слово входит во фрагмент, если сразу за ним стоит
// опознанный компонент имени; в одиночку оно засчитывается только после
// обращения вроде «уважаемый» или «на имя».
func fioUnknownLead(d *Doc, run []fioWord, i int) (float64, string, int) {
	// В начале предложения заглавная буква ничего не значит: там стоит любое
	// слово, а не только имя.
	if run[i].sentStart {
		return 0, "", 0
	}
	if i+1 < len(run) && run[i+1].has(fioRoleNameCore) {
		if conf, reason, n := fioKnownShape(d, run, i+1); n > 0 {
			return conf, "fio:cap+" + strings.TrimPrefix(reason, "fio:"), n + 1
		}
	}
	if _, ok := fioMarkerBefore(d, run[i].start, fioGreetings); ok {
		return ConfAnchored, "fio:greeting", 1
	}
	return 0, "", 0
}

// fioTakesUnknown сообщает, что справа от разобранной комбинации стоит
// незнакомое слово с заглавной буквы, примыкающее к опознанному компоненту.
func fioTakesUnknown(run []fioWord, j int) bool {
	if j <= 0 || j >= len(run) {
		return false
	}
	// Слово в начале предложения не принимается: точка после фамилии
	// заканчивает предложение, а не разделяет компоненты имени.
	return run[j].has(fioRoleUnknownCap) && !run[j].sentStart &&
		run[j-1].has(fioRoleNameCore)
}

// fioKnownShape подбирает комбинацию из слов, опознанных словарём или
// окончанием.
func fioKnownShape(d *Doc, run []fioWord, i int) (float64, string, int) {
	if i+2 < len(run) {
		if conf, reason, ok := fioTripleShape(run[i], run[i+1], run[i+2]); ok {
			return conf, reason, 3
		}
	}
	if i+1 < len(run) {
		if conf, reason, ok := fioPairShape(run[i], run[i+1]); ok {
			return conf, reason, 2
		}
	}
	if conf, reason, ok := fioSingleShape(d, run[i]); ok {
		return conf, reason, 1
	}
	return 0, "", 0
}

// fioTripleShape разбирает три компонента: полное ФИО в любом порядке и
// фамилию с двумя инициалами.
func fioTripleShape(a, b, c fioWord) (float64, string, bool) {
	switch {
	case a.has(fioRoleAnySurname) && b.has(fioRoleName) && c.has(fioRoleAnyPatronymic),
		a.has(fioRoleName) && b.has(fioRoleAnyPatronymic) && c.has(fioRoleAnySurname),
		a.has(fioRoleName) && b.has(fioRoleAnySurname) && c.has(fioRoleAnyPatronymic):
		return ConfHigh, "fio:surname+name+patronymic", true
	case fioInitialsTriple(a, b, c):
		return ConfHigh, "fio:initials", true
	default:
		// Прочие сочетания трёх слов на ФИО не похожи: решение о них
		// принимается ниже, по паре и по одиночному слову.
	}
	return 0, "", false
}

// fioInitialsTriple проверяет запись «Иванов И. И.» и «И. И. Иванов».
// Хотя бы у одного инициала обязана быть точка, иначе одиночная заглавная
// буква рядом с фамилией принимается за инициал.
func fioInitialsTriple(a, b, c fioWord) bool {
	if a.has(fioRoleAnySurname) && b.has(fioRoleInitial) && c.has(fioRoleInitial) {
		return b.dot || c.dot
	}
	if a.has(fioRoleInitial) && b.has(fioRoleInitial) && c.has(fioRoleAnySurname) {
		return a.dot || b.dot
	}
	return false
}

// fioPairShape разбирает два компонента: имя с отчеством, имя с фамилией,
// фамилию с инициалом.
func fioPairShape(a, b fioWord) (float64, string, bool) {
	switch {
	case a.has(fioRoleName) && b.has(fioRoleAnyPatronymic):
		return ConfHigh, "fio:name+patronymic", true
	case a.has(fioRoleAnyPatronymic) && b.has(fioRoleSurnameDict):
		return ConfHigh, "fio:patronymic+surname", true
	case fioInitialsPair(a, b):
		return ConfHigh, "fio:initials", true
	case a.has(fioRoleName) && b.has(fioRoleSurnameDict),
		a.has(fioRoleSurnameDict) && b.has(fioRoleName):
		return ConfHigh, "fio:name+surname", true
	case a.has(fioRoleName) && b.has(fioRoleAnySurname),
		a.has(fioRoleAnySurname) && b.has(fioRoleName):
		return ConfAnchored, "fio:surname+name", true
	case a.has(fioRoleSurnameDict) && b.has(fioRoleInitialsPair),
		a.has(fioRoleInitialsPair) && b.has(fioRoleSurnameDict):
		return ConfHigh, "fio:surname+initials", true
	default:
		// Прочие пары слов именем не считаются: пара без признаков имени
		// разбирается дальше как одиночное слово.
	}
	return 0, "", false
}

// fioInitialsPair проверяет запись «Иванов И.» и «И. Иванов».
func fioInitialsPair(a, b fioWord) bool {
	// Инициал и фамилия обязаны быть одной письменности. Без этого «S. Сведения»
	// в хвосте VIN принималось за имя: латинская буква с точкой склеивалась с
	// обычным русским словом, и ложное имя вытесняло настоящий номер.
	if a.latin != b.latin {
		return false
	}
	// Служебная буква со строчной в паре с одним словом это адресное
	// сокращение, а не инициал: «г. Ростов» и «д. Михайловка» помечают место.
	// В тройке с двумя инициалами такая буква остаётся инициалом, поэтому
	// отсекается только пара.
	if fioLowerServiceInitial(a) || fioLowerServiceInitial(b) {
		return false
	}
	// Фамилия из списка исключений это город или обычное слово: «г. Ростов»
	// и «ул. Пушкина» не должны маскироваться. В тройке с двумя инициалами
	// такое слово безопасно, поэтому здесь отсекается только пара.
	if a.has(fioRoleAnySurname) && fioStopWord(a.norm) {
		return false
	}
	if b.has(fioRoleAnySurname) && fioStopWord(b.norm) {
		return false
	}
	if a.has(fioRoleAnySurname) && b.isInitial() {
		return true
	}
	return a.isInitial() && b.has(fioRoleAnySurname)
}

// fioSingleShape разбирает одиночный компонент. Одного слова достаточно,
// если это отчество, если рядом стоит якорь или если слово известно словарю.
func fioSingleShape(d *Doc, w fioWord) (float64, string, bool) {
	// Одна заглавная буква без точки это не инициал, а предлог или начало
	// слова: «В общем», «С уважением».
	if w.has(fioRoleInitial) && !w.dot {
		return 0, "", false
	}
	// Служебная буква со строчной одиночным компонентом имени не бывает даже
	// рядом с якорем: «клиент г. Москва» это адрес, а не человек.
	if fioLowerServiceInitial(w) {
		return 0, "", false
	}
	if w.has(fioRolePatronymic) && w.capital && !w.latin {
		return ConfAnchored, "fio:patronymic", true
	}
	if _, ok := fioMarkerBefore(d, w.start, fioAnchors); ok {
		return ConfAnchored, "fio:anchor", true
	}
	// Дальше решение принимается без якоря. Одиночная латиница и обычные
	// слова с фамильными окончаниями отсекаются списком исключений. Имя из
	// словаря опознаётся независимо от регистра: «анастасия» в чате это то же
	// имя, что «Анастасия» в анкете. Фамилия по одному слову требует заглавной
	// буквы: иначе фамилией станет «иванов» в «сто иванов».
	if w.latin || fioStopWord(w.norm) {
		return 0, "", false
	}
	switch {
	case w.has(fioRoleSurnameDict):
		if !w.capital {
			return 0, "", false
		}
		return ConfAnchored, "fio:surname", true
	case w.has(fioRoleName) && !fioHomonyms[w.norm]:
		return ConfAnchored, "fio:name", true
	case w.has(fioRoleSurnameStrong) && w.capital && !fioAtSentenceStart(d, w.start):
		return ConfMedium, "fio:surname_suffix", true
	default:
		// Одиночное слово без словарного признака и без сильного окончания
		// именем не считается: одной заглавной буквы для решения мало.
	}
	return 0, "", false
}

// fioMakeSpan собирает фрагмент, отсекая случаи, когда слева стоит название
// улицы или объекта, и не давая фрагменту выйти за строку.
func fioMakeSpan(d *Doc, first, last fioWord, conf float64, reason string) (Span, bool) {
	if first.latin && last.latin {
		reason = "fio:latin+" + strings.TrimPrefix(reason, "fio:")
	}
	sp, ok := fioSpanRange(d, first.start, last.end, conf, reason)
	if !ok {
		return Span{}, false
	}
	// Точка после последнего инициала входит во фрагмент: «Иванов И. И.» это
	// одно имя, и эталон в наборе данных включает точку. Нормализация границ
	// отрезает её, поэтому она возвращается на место.
	if last.isInitial() && last.end < len(d.Text) && d.Text[last.end] == '.' && sp.End == last.end {
		sp.End++
	}
	// То же для пары инициалов без точек: «Максимов ДР.» включает точку.
	if last.has(fioRoleInitialsPair) && last.end < len(d.Text) && d.Text[last.end] == '.' && sp.End == last.end {
		sp.End++
	}
	return sp, true
}

// fioSpanRange собирает фрагмент по границам в байтах: обрезает знаки по
// краям, отсекает названия улиц слева и не даёт фрагменту выйти за строку.
func fioSpanRange(d *Doc, from, to int, conf float64, reason string) (Span, bool) {
	if fioStreetBefore(d, from) {
		return Span{}, false
	}
	if fioOrgFormBefore(d, from) {
		return Span{}, false
	}
	// Границы нормализуются на самом фрагменте, а не на всём тексте: разбор
	// текста целиком ради обрезки нескольких знаков стоит слишком дорого.
	lead, tail := NormalizeSpan(d.Text[from:to], 0, to-from)
	start, end := from+lead, from+tail
	if start >= end {
		return Span{}, false
	}
	// Компоненты имени разделяются только пробелами и точками, поэтому
	// перевод строки внутри фрагмента означает ошибку разбора.
	if strings.ContainsAny(d.Text[start:end], "\n\r") {
		return Span{}, false
	}
	return Span{Start: start, End: end, Type: TypeFIO, Conf: conf, Reason: reason}, true
}

// fioStreetBefore сообщает, что слева от фрагмента стоит указание на улицу
// или памятный объект.
//
// Слово «имени» попадает и в название улицы, и в оборот «от имени Иванова»,
// поэтому выигрывает более длинный маркер: если слева нашлось указание на
// человека длиннее, чем указание на улицу, фрагмент остаётся именем.
func fioStreetBefore(d *Doc, start int) bool {
	street, blocked := fioMarkerBefore(d, start, fioAddressMarkers)
	if !blocked {
		return false
	}
	person, ok := fioMarkerBefore(d, start, fioAnchors)
	return !ok || len(person) <= len(street)
}

// fioOrgFormBefore сообщает, что слева от фрагмента стоит правовая форма и
// открывающая кавычка: «ООО «Новиков Трейд»». Фамилия в наименовании
// юридического лица человека не обозначает, и маскировать её нельзя — иначе
// из договора исчезает название контрагента.
//
// Форма «ИП» сюда не входит намеренно: за ней стоит имя живого человека.
func fioOrgFormBefore(d *Doc, start int) bool {
	head := d.Lower[:start]
	// Кавычка обязательна. Без неё «ООО Иванов и партнеры» — обычное
	// перечисление, где фамилия принадлежит человеку и маскироваться должна;
	// кавычки же обрамляют именно наименование: ООО «Новиков Трейд».
	quoted := strings.TrimRight(head, " \t\u00a0")
	if quoted == "" {
		return false
	}
	trimmed := strings.TrimRight(quoted, "«\"'")
	if len(trimmed) == len(quoted) {
		return false
	}
	trimmed = strings.TrimRight(trimmed, " \t\u00a0")
	i := strings.LastIndexAny(trimmed, " \t\n\r(.,;:")
	return fioOrgForms[dict.Normalize(trimmed[i+1:])]
}

// fioStopWordsExtra дополняет перечень fioStopWords словами, на которых
// детектор ошибался: падежные формы месяцев («в августе», «к марту») и
// «марка» с падежными формами — это не имена. Сам перечень fioStopWords
// вынесен в detect_fio_words.go вместе с остальными словарями, поэтому
// дополнение живёт здесь, рядом с проверками, и подмешивается к нему в
// fioStopWord.
var fioStopWordsExtra = fioWordSet(
	// Падежные формы месяцев: «в августе», «к марту» — это не имена.
	"январе", "январю", "феврале", "февралю", "марте", "марту", "апреле",
	"апрелю", "мае", "маю", "июне", "июню", "июле", "июлю", "августе",
	"августу", "сентябре", "сентябрю", "октябре", "октябрю", "ноябре",
	"ноябрю", "декабре", "декабрю",
	// «Марка» и её падежные формы: марка автомобиля, почтовая марка.
	"марк", "марка", "марки", "марку", "марке", "маркой",
)

// fioStopWord сообщает, что слово стоит в списке исключений: обычное слово,
// название города или падежная форма, совпадающая с именем. Проверяются оба
// перечня — основной и дополнение.
func fioStopWord(norm string) bool {
	return fioStopWords[norm] || fioStopWordsExtra[norm]
}

// fioMarkerBefore ищет слово-маркер слева от фрагмента. Между маркером и
// фрагментом допускаются только знаки препинания и служебные слова: иначе
// маркер из другой части предложения помечает чужое слово.
//
// Сравнение идёт по целым словам окна, а не поиском подстроки: так «клиент»
// не находится внутри «клиентов», а перебор всех маркеров не тратит время на
// каждое слово текста.
func fioMarkerBefore(d *Doc, start int, markers map[string]bool) (string, bool) {
	words := fioWindowWords(d, start)
	i := len(words) - 1
	for i >= 0 && fioFillerWords[words[i]] {
		i--
	}
	if i < 0 {
		return "", false
	}
	for _, cand := range fioMarkerVariants(words, i) {
		if markers[cand] {
			return cand, true
		}
	}
	return "", false
}

// fioMarkerVariants собирает варианты записи маркера из хвостовых слов окна:
// одно слово, два слова через пробел и слитную запись вроде «ф. и. о».
func fioMarkerVariants(words []string, i int) []string {
	out := []string{words[i]}
	if i >= 1 {
		out = append(out, words[i-1]+" "+words[i], words[i-1]+words[i])
	}
	if i >= 2 {
		out = append(out, words[i-2]+words[i-1]+words[i])
	}
	return out
}

// fioWindowWords разбирает окно слева от фрагмента на слова. Окно обрезается
// по переводу строки и по началу обрезанного слова: маркер, от которого в
// окно попал только хвост, засчитывать нельзя.
func fioWindowWords(d *Doc, start int) []string {
	lo, _ := d.WindowRunes(start, start, fioAnchorWindow, 0)
	window := d.Lower[lo:start]
	if i := strings.LastIndexAny(window, "\n\r"); i >= 0 {
		window = window[i+1:]
		lo += i + 1
	}
	words := fioSplitWords(window)
	if lo > 0 && len(words) > 0 {
		// Окно началось посреди слова, значит первое слово неполное.
		if r, size := fioLastRune(d.Lower, lo); size > 0 && fioIsWordRune(r) {
			words = words[1:]
		}
	}
	return words
}

// fioSplitWords делит строку на слова из букв, цифр и внутренних дефисов.
// Одиночные дефисы и тире словами не считаются: запись «заявитель — Сидоров»
// обязана работать так же, как «заявитель Сидоров».
func fioSplitWords(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		w := strings.Trim(cur.String(), "-")
		cur.Reset()
		if w != "" {
			out = append(out, dict.Normalize(w))
		}
	}
	for _, r := range s {
		if fioIsWordRune(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// fioIsWordRune сообщает, что руна может стоять внутри слова.
func fioIsWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-'
}

// fioAtSentenceStart сообщает, что слово открывает предложение. В начале
// предложения заглавная буква ничего не значит, поэтому решение по одному
// только окончанию там не принимается.
func fioAtSentenceStart(d *Doc, start int) bool {
	i := start
	for i > 0 {
		r, size := fioLastRune(d.Text, i)
		if size == 0 {
			return true
		}
		if unicode.IsSpace(r) {
			i -= size
			continue
		}
		return strings.ContainsRune(".!?;:", r)
	}
	return true
}

// fioAllUpper сообщает, что в слове нет строчных букв. Так пишут сокращения
// вроде ООО и ЗАО, поэтому такое слово не принимается за незнакомое имя.
func fioAllUpper(s string) bool {
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

// fioStartsUpper сообщает, что слово начинается с заглавной буквы.
func fioStartsUpper(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

// fioRuneCount возвращает число рун в строке.
func fioRuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// fioLastRune возвращает руну, которая заканчивается на указанном смещении.
// Разбор идёт по последним байтам, а не с начала строки: на длинном тексте
// проход с начала на каждом слове обходится слишком дорого.
func fioLastRune(s string, end int) (rune, int) {
	if end <= 0 || end > len(s) {
		return 0, 0
	}
	from := end - utf8.UTFMax
	if from < 0 {
		from = 0
	}
	return utf8.DecodeLastRuneInString(s[from:end])
}

// fioHasSuffix сообщает, что строка заканчивается одним из окончаний.
func fioHasSuffix(s string, suffixes []string) bool {
	for _, suf := range suffixes {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	return false
}
