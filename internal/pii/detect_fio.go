package pii

import (
	"sort"
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
		// Двойная фамилия со строчной буквы склеивается здесь, а не в
		// fioWordEnd: там часть после дефиса обязана начинаться с заглавной
		// буквы, иначе к любому имени приклеится обычное слово.
		if w.roles == 0 && fioLowerSurnameCandidate(w) {
			if hEnd, hLast, ok := fioLowerHyphenEnd(d, last, end); ok {
				if hw := fioNewWord(d, toks[i].Start, hEnd); hw.roles != 0 || fioLowerSurnameCandidate(hw) {
					w, last = hw, hLast
				}
			}
		}
		switch {
		case w.roles != 0:
			words = append(words, w)
		case fioLowerSurnameCandidate(w):
			lower = append(lower, w)
		}
		i = last
	}
	return words, lower
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
	if fioStopWords[w.norm] || fioCommonWords[w.norm] {
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
		// «Иванов И. И.» и «иванов и. и.» это одно и то же имя. Сокращения
		// вроде «ул.», «пл.», «им.» инициалами не бывают: они помечают адрес,
		// а не человека. Служебные «г.», «т.» инициалами становятся только в
		// паре с другим инициалом, поэтому отдельно они отсекаются в
		// fioInitialsPair.
		if w.capital || (w.dot && !fioAddressMarkers[w.norm]) {
			return fioRoleInitial
		}
		return 0
	}
	// Две заглавные буквы это пара инициалов: «Сорокина ВЯ» значит Сорокина
	// Вера Яковлевна, а «Максимов ДР.» — Максимов Дмитрий Романович. Слово
	// целиком заглавными вроде «ООО» и «ЗАО» сюда не попадает: у него три
	// буквы, а не две. Сокращения «ул.», «пл.», «г.» инициалами не бывают.
	if fioRuneCount(w.norm) == 2 && w.shout && !fioAddressMarkers[w.norm] {
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

// fioExpandSuffixes собирает окончания из основ и падежных хвостов.
func fioExpandSuffixes(stems, tails []string) []string {
	out := make([]string, 0, len(stems)*len(tails))
	for _, s := range stems {
		for _, t := range tails {
			out = append(out, s+t)
		}
	}
	return out
}

// Окончания отчеств вместе с падежными формами. Мужские и женские отчества
// разделены, потому что у них разные падежные хвосты.
var (
	fioPatronymicMale   = fioExpandSuffixes([]string{"ович", "евич"}, []string{"", "а", "у", "ем", "е"})
	fioPatronymicFemale = fioExpandSuffixes([]string{"овн", "евн"}, []string{"а", "ы", "е", "у", "ой"})
	// Окончания «ична» и «инична» совпадают с краткими прилагательными
	// вроде «практична», поэтому им нужна заглавная буква.
	fioPatronymicShortFemale = fioExpandSuffixes([]string{"ичн", "иничн"}, []string{"а", "ы", "е", "у", "ой"})
	// Окончание «ич» без предшествующего «ов» слабое: так заканчиваются и
	// обычные слова, поэтому одного его для маскирования не хватает.
	fioPatronymicWeakSuffixes = fioExpandSuffixes([]string{"ич"}, []string{"", "а", "у", "ем", "е"})
	// Тюркские отчества не склоняются и пишутся отдельным словом.
	fioPatronymicTurkic = []string{"оглы", "кызы", "улы", "уулу"}
)

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

// Окончания фамилий. Сильные окончания сами по себе указывают на фамилию.
// Окончания прилагательных и слабые окончания засчитываются только вместе со
// вторым компонентом имени или с якорем.
var (
	fioSurnameStrongSuffixes = append(
		fioExpandSuffixes([]string{"ов", "ев", "ин", "ын"}, []string{"", "а", "у", "ым", "е", "ой", "ы"}),
		"енко", "швили", "дзе", "ян", "яна", "яну", "яном", "яне", "янц", "оглы")
	// Фамилии на «ский» и «цкий» неотличимы от прилагательных в названиях
	// улиц и районов: Невский проспект, Ленинский район, Тверской бульвар.
	// Поэтому одного такого слова для маскирования недостаточно.
	fioSurnameAdjectiveSuffixes = fioExpandSuffixes([]string{"ск", "цк"},
		[]string{"ий", "ой", "ого", "ому", "им", "ом", "ая", "ую", "ие", "их", "ими"})
	fioSurnameWeakSuffixes = []string{"ко", "ук", "юк", "чук", "их", "ых", "ая", "ой", "ия", "иа", "ли"}
)

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
	if comma && !(a.has(fioRoleAnySurname) && b.has(fioRoleName)) {
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
	// Фамилия из списка исключений это город или обычное слово: «г. Ростов»
	// и «ул. Пушкина» не должны маскироваться. В тройке с двумя инициалами
	// такое слово безопасно, поэтому здесь отсекается только пара.
	if a.has(fioRoleAnySurname) && fioStopWords[a.norm] {
		return false
	}
	if b.has(fioRoleAnySurname) && fioStopWords[b.norm] {
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
	if w.latin || fioStopWords[w.norm] {
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

// Фамилия со строчной буквы.
//
// Люди пишут строчными постоянно, и фамилия без заглавной буквы уходила
// языковой модели открытым текстом: «меня зовут тимур нагимов» маскировалось
// только до имени. Снять требование заглавной буквы целиком нельзя — фамилией
// станет обычное слово «практична» или «слева», — поэтому решение принимается
// только при сильной опоре рядом.

// fioMinLowerSurnameRunes задаёт наименьшую длину фамилии со строчной буквы.
// Порог выше обычного: среди коротких слов с фамильным окончанием почти всё
// это обычные слова вроде «годов» и «снова», а короткие фамилии есть в словаре
// и распознаются независимо от регистра.
const fioMinLowerSurnameRunes = 6

// fioLowerSurnameConf задаёт уверенность находки со строчной буквы. Она ниже
// уверенности находки с заглавной буквы намеренно: решение принято по
// соседству, а не по самому слову, и в профиле точности такой фрагмент
// отсекается порогом, оставляя опознанную часть имени.
const fioLowerSurnameConf = ConfMedium

// fioLowerSurnameCandidate решает, что слово со строчной буквы вообще может
// оказаться фамилией. Проверяется только форма слова: опора ищется дальше.
func fioLowerSurnameCandidate(w fioWord) bool {
	// Слово с признаками разбирается обычным путём, заглавная буква решается
	// без опоры, а строчная латиница это почти всегда идентификатор или адрес.
	if w.roles != 0 || w.capital || w.latin {
		return false
	}
	if fioRuneCount(w.norm) < fioMinLowerSurnameRunes {
		return false
	}
	if !fioHasSuffix(w.norm, fioLowerSurnameSuffixes) {
		return false
	}
	// Ловушка проверяется и по слову целиком, и по основе, поэтому здесь же
	// отсеиваются обычные слова, служебные слова, якоря и маркеры улиц.
	if fioLowerTrap(w.norm) {
		return false
	}
	return !fioGeoWord(w.norm)
}

// fioLowerSurnameSuffixes перечисляет окончания, по которым фамилию опознают
// без заглавной буквы. Набор уже, чем у слова с заглавной буквы: взяты только
// сильные окончания и только прямые падежи.
//
// Творительный и предложный падежи и множественное число отброшены намеренно.
// На «овой», «овым», «овы» кончаются относительные прилагательные — «дебетовой
// карты», «финансовой отчётности», «грузовым транспортом», — а на «ове» и
// «ине» предложный падеж обычных слов: «в основе», «на середине». Фамилия в
// этих формах со строчной буквы встречается редко, а обычных слов в них тысячи.
var fioLowerSurnameSuffixes = append(
	fioExpandSuffixes([]string{"ов", "ев", "ин", "ын"}, []string{"", "а", "у"}),
	"енко", "швили", "дзе", "ян", "яна", "яну", "янц", "оглы")

// fioLowerSurnames разбирает кандидатов в фамилии со строчной буквы и
// возвращает фрагменты, которые накрывают кандидата вместе с опорой.
//
// Фрагмент намеренно строится поверх уже найденного: он длиннее и с меньшей
// уверенностью, поэтому при разрешении пересечений он вытесняет находку с
// заглавной буквы там, где порог его пропускает, и отбрасывается там, где не
// пропускает, оставляя исходную находку нетронутой.
func fioLowerSurnames(d *Doc, words, lower []fioWord, found []Span) []Span {
	if len(lower) == 0 {
		return nil
	}
	var out []Span
	for _, c := range lower {
		if sp, ok := fioLowerSurnameSpan(d, words, found, c); ok {
			out = append(out, sp)
		}
	}
	return out
}

// fioLowerSurnameSpan подбирает опору для одного кандидата.
//
// Опора всегда состоит из двух частей, и это главное ограничение правила.
// Одного соседнего имени не хватает: словарь имён содержит обычные слова
// («сила», «вера», «марта», «август»), а сам кандидат не проверяется ничем,
// кроме списка ловушек, поэтому «Иван контрактов не подписывал» и «в августе
// подарков не будет» превращались в находку. Родительный падеж
// множественного числа по форме неотличим от фамилии, и список исключений
// этот открытый класс слов закрыть не может.
//
// Поэтому решение принимается только там, где родительный падеж
// множественного числа невозможен грамматически:
//
//	«меня зовут тимур нагимов»          — называние + имя рядом с кандидатом
//	«клиент нагимов тимур ринатович»    — кандидат перед именем с отчеством
//	«меня зовут нагимов»                — прямое называние человека
func fioLowerSurnameSpan(d *Doc, words []fioWord, found []Span, c fioWord) (Span, bool) {
	start, end, reason := c.start, c.end, ""
	if from, ok := fioLowerLeftSupport(d, words, found, c); ok {
		start, reason = from, "fio:lower_surname_after_name"
	}
	if reason == "" {
		if to, ok := fioLowerRightSupport(d, words, found, c); ok {
			end, reason = to, "fio:lower_surname_before_name"
		}
	}
	if reason == "" {
		if _, ok := fioMarkerBefore(d, c.start, fioLowerSoloAnchors); !ok {
			return Span{}, false
		}
		reason = "fio:lower_surname_anchor"
	}
	// Отчество справа от фамилии входит во фрагмент: «зовут тимур нагимов
	// ринатович» не должно обрываться на фамилии.
	if end == c.end {
		if tail, ok := fioLowerTail(d, words, found, c.end, fioRoleAnyPatronymic); ok {
			end = tail
		}
	}
	return fioSpanRange(d, start, end, fioLowerSurnameConf, reason)
}

// fioLowerLeftSupport ищет опору слева: «меня зовут тимур нагимов», «на имя
// ивана ивановича нагимова». Возвращает начало будущего фрагмента.
//
// Требований два, и оба обязательны. Слева стоит имя или отчество, а перед
// ними стоит оборот называния человека. Без второго требования правило
// срабатывает на обычной речи: «у Анны купонов не осталось», «Иван Сергеевич
// комментариев не оставлял». Фамилия слева опорой не является, иначе «Петров
// снова подал заявку» превратится в двойную фамилию.
func fioLowerLeftSupport(d *Doc, words []fioWord, found []Span, c fioWord) (int, bool) {
	w, ok := fioWordBefore(words, c.start)
	if !ok || !fioSpaceGap(d, w.end, c.start) {
		return 0, false
	}
	if !w.has(fioRoleName | fioRoleAnyPatronymic) {
		return 0, false
	}
	// Если имя уже вошло в найденный фрагмент, фрагмент продолжается с его
	// начала: иначе получится пересечение, накрывающее часть имени. Якорь
	// ищется от начала всего фрагмента, чтобы «меня зовут тимур ринатович
	// нагимов» опиралось на «зовут», а не на отчество.
	start := w.start
	if sp, ok := fioSpanEndingAt(found, w.end); ok {
		start = sp.Start
	}
	if _, ok := fioMarkerBefore(d, start, fioLowerNameAnchors); !ok {
		return 0, false
	}
	return start, true
}

// fioLowerRightSupport ищет опору справа: «клиент нагимов тимур ринатович».
// Порядок «фамилия имя отчество» встречается в анкетах и в списках.
// Возвращает конец будущего фрагмента.
//
// Требований снова два. Справа стоит имя вместе с отчеством: одного имени мало,
// иначе «менеджер контрактов Мария перезвонит» съедает обычное слово. Слева
// стоит оборот, после которого человека называют по анкете. Без второго
// требования правило ловит вынесенный вперёд родительный падеж при отрицании,
// а это обычная русская речь: «долгов иван иванович не имеет», «голосов иван
// иванович не набрал».
func fioLowerRightSupport(d *Doc, words []fioWord, found []Span, c fioWord) (int, bool) {
	w, i, ok := fioWordAfter(words, c.end)
	if !ok || !fioSpaceGap(d, c.end, w.start) || !w.has(fioRoleName) {
		return 0, false
	}
	end := w.end
	if sp, ok := fioSpanStartingAt(found, w.start); ok && sp.End > end {
		end = sp.End
	}
	patronymic := false
	if tail, ok := fioLowerTail(d, words, found, w.end, fioRoleAnyPatronymic); ok {
		patronymic = true
		if tail > end {
			end = tail
		}
	} else if i+1 < len(words) && words[i+1].end <= end && words[i+1].has(fioRoleAnyPatronymic) {
		patronymic = true
	}
	if !patronymic {
		return 0, false
	}
	if _, ok := fioMarkerBefore(d, c.start, fioLowerFormAnchors); !ok {
		return 0, false
	}
	return end, true
}

// fioLowerTail возвращает конец слова с нужным признаком, стоящего сразу за
// указанным смещением. Если слово уже вошло в найденный фрагмент, возвращается
// конец этого фрагмента: новый фрагмент обязан накрывать старый целиком, иначе
// при разрешении пересечений часть имени останется без маски.
func fioLowerTail(d *Doc, words []fioWord, found []Span, pos int, role fioRole) (int, bool) {
	w, _, ok := fioWordAfter(words, pos)
	if !ok || !fioSpaceGap(d, pos, w.start) || !w.has(role) {
		return 0, false
	}
	if sp, ok := fioSpanStartingAt(found, w.start); ok {
		return sp.End, true
	}
	return w.end, true
}

// fioWordBefore возвращает последнее слово, которое кончается не позже
// указанного смещения. Слова упорядочены по началу и не пересекаются, поэтому
// поиск идёт делением пополам: на длинном тексте перебор обошёлся бы дорого.
func fioWordBefore(words []fioWord, pos int) (fioWord, bool) {
	i := sort.Search(len(words), func(i int) bool { return words[i].end > pos })
	if i == 0 {
		return fioWord{}, false
	}
	return words[i-1], true
}

// fioWordAfter возвращает первое слово, которое начинается не раньше
// указанного смещения, вместе с его номером в списке.
func fioWordAfter(words []fioWord, pos int) (fioWord, int, bool) {
	i := sort.Search(len(words), func(i int) bool { return words[i].start >= pos })
	if i >= len(words) {
		return fioWord{}, 0, false
	}
	return words[i], i, true
}

// fioSpanEndingAt ищет найденный фрагмент, который кончается ровно на указанном
// смещении. Фрагменты упорядочены по началу и не пересекаются, поэтому их концы
// тоже идут по возрастанию.
func fioSpanEndingAt(found []Span, pos int) (Span, bool) {
	i := sort.Search(len(found), func(i int) bool { return found[i].End >= pos })
	if i < len(found) && found[i].End == pos {
		return found[i], true
	}
	return Span{}, false
}

// fioSpanStartingAt ищет найденный фрагмент, который начинается ровно на
// указанном смещении.
func fioSpanStartingAt(found []Span, pos int) (Span, bool) {
	i := sort.Search(len(found), func(i int) bool { return found[i].Start >= pos })
	if i < len(found) && found[i].Start == pos {
		return found[i], true
	}
	return Span{}, false
}

// fioSpaceGap сообщает, что между двумя смещениями стоят только пробелы.
// Точки и запятые фамилию от имени не отделяют, поэтому здесь промежуток
// строже, чем у слов с заглавной буквы.
func fioSpaceGap(d *Doc, from, to int) bool {
	if to <= from || to > len(d.Text) {
		return false
	}
	gap := d.Text[from:to]
	if fioRuneCount(gap) > fioMaxGapRunes {
		return false
	}
	for _, r := range gap {
		if r != ' ' && r != '\t' && r != '\u00a0' {
			return false
		}
	}
	return true
}

// fioLowerTrap сообщает, что слово со строчной буквы это обычное слово, а не
// фамилия. Проверяется и сама форма, и основа с отсечённым падежным
// окончанием: «машина», «машины», «машиной» это одно и то же слово.
func fioLowerTrap(norm string) bool {
	if fioLowerTrapWord(norm) {
		return true
	}
	runes := []rune(norm)
	for n := 1; n <= 2 && n < len(runes); n++ {
		stem := runes[:len(runes)-n]
		if fioLowerTrapStem(string(stem)) {
			return true
		}
		// Беглая гласная: у «напиток» родительный падеж множественного числа
		// «напитков», и основа теряет букву. Она возвращается на место, иначе
		// каждое такое слово пришлось бы перечислять отдельно.
		if len(stem) < 2 {
			continue
		}
		head, tail := string(stem[:len(stem)-1]), string(stem[len(stem)-1:])
		if fioLowerTrapStem(head+"о"+tail) || fioLowerTrapStem(head+"е"+tail) {
			return true
		}
	}
	return false
}

// fioLowerTrapWord сообщает, что слово целиком перечислено как обычное в одном
// из наборов детектора.
func fioLowerTrapWord(w string) bool {
	return fioLowerTrapWords[w] || fioLowerTrapStems[w] || fioStopWords[w] ||
		fioCommonWords[w] || fioHomonyms[w] || fioAnchors[w] ||
		fioFillerWords[w] || fioAddressMarkers[w]
}

// fioLowerTrapStem сообщает, что основа принадлежит обычному слову. Наборы с
// именами и обращениями здесь не проверяются: у фамилии «Романов» основа
// совпадает с именем «Роман», и такая проверка отбросила бы саму фамилию.
func fioLowerTrapStem(stem string) bool {
	return fioLowerTrapStems[stem] || fioLowerTrapWords[stem] ||
		fioStopWords[stem] || fioCommonWords[stem]
}

// fioLowerSoloAnchors перечисляет обороты, после которых слово со строчной
// буквы считается фамилией без всякого соседа. Набор предельно узкий: решение
// принимается по одному слову, поэтому сюда попали только обороты, после
// которых родительный падеж множественного числа невозможен грамматически.
//
// «в лице», «на имя», «от имени», «фио», «за подписью» сюда намеренно не
// входят: «банк действует в лице агентов», «счёт открыт на имя опекунов»,
// «фио сотрудников» — это обычная речь без персональных данных, и по одному
// слову отличить её от фамилии нельзя.
var fioLowerSoloAnchors = fioWordSet(
	"зовут", "меня зовут", "вас зовут", "его зовут", "ее зовут", "их зовут",
	// «моя фамилия нагимов» это обычный способ представиться. Множественное
	// «фамилии» сюда не входит: после него законно стоит родительный падеж
	// множественного числа — «фамилии сотрудников».
	"фамилия", "фамилию", "фамилией", "по фамилии",
	// Обращения: после них стоит человек, а «господин налогов» или
	// «уважаемый клиентов» по-русски не говорят.
	"господин", "господина", "господину", "госпожа", "госпоже", "госпожи",
	"г-н", "г-на", "г-ну", "г-же", "г-жа",
	"уважаемый", "уважаемая", "уважаемому", "уважаемой",
)

// fioLowerFormAnchors перечисляет обороты, после которых человека называют по
// анкете: сначала фамилия, потом имя и отчество. Это fioLowerNameAnchors плюс
// два названия стороны договора.
//
// Названия должностей — «менеджер», «специалист», «директор», «руководитель»,
// «администратор», «представитель» — сюда не входят намеренно: после них
// стоит родительный падеж множественного числа («менеджер контрактов»,
// «специалист по эквайрингу», «сотрудник органов»), и он неотличим от фамилии.
// По той же причине не вошли «владелец», «держатель», «плательщик»,
// «получатель», «должник»: «владелец автомобилей», «плательщик налогов».
var fioLowerFormAnchors = fioMergeWordSets(fioLowerNameAnchors, fioWordSet(
	"клиент", "клиента", "клиенту", "клиентом", "клиентка", "клиентки",
	"заявитель", "заявителя", "заявителю", "заявителем",
))

// fioMergeWordSets собирает один набор слов из нескольких.
func fioMergeWordSets(sets ...map[string]bool) map[string]bool {
	out := make(map[string]bool)
	for _, set := range sets {
		for w := range set {
			out[w] = true
		}
	}
	return out
}

// fioLowerNameAnchors перечисляет обороты, после которых человека называют по
// имени. Они работают только вместе с опознанным именем или отчеством рядом с
// кандидатом, поэтому набор шире узкого: «в лице ивана нагимова» безопасно,
// а «в лице агентов» сюда не попадёт, потому что имени рядом нет.
var fioLowerNameAnchors = fioWordSet(
	"зовут", "меня зовут", "вас зовут", "его зовут", "ее зовут", "их зовут",
	"фио", "на имя", "от имени", "в лице", "за подписью",
	"фамилия", "фамилии", "фамилию", "фамилией", "по фамилии", "фамилия клиента",
	"господин", "господина", "господину", "госпожа", "госпоже", "госпожи",
	"г-н", "г-на", "г-ну", "г-же", "г-жа",
	"уважаемый", "уважаемая", "уважаемому", "уважаемой",
	"доверенность на", "доверенности на", "оформлен на", "оформлена на",
	"зарегистрирован на", "я",
)

// Названия ролей — «клиент», «владелец», «получатель» — в этот набор не
// входят намеренно. После них обычно стоит родительный падеж множественного
// числа: «плательщик налогов», «владелец автомобилей», «получатель
// переводов». Такое слово неотличимо от фамилии по форме, а решение по нему
// принималось бы без второго компонента имени. Рядом с ролью фамилия со
// строчной буквы всё равно находится, но по соседнему имени, а не по якорю:
// «клиент нагимов тимур ринатович».

// fioLowerTrapWords перечисляет обычные слова с сильным фамильным окончанием,
// которые чаще всего стоят рядом с именем: краткие прилагательные, наречия и
// причастия. Именно они дали бы ложные срабатывания после снятия требования
// заглавной буквы.
var fioLowerTrapWords = fioWordSet(
	"готова", "готовы", "готовым", "готовой", "здорова", "знакома", "довольна",
	"снова", "слева", "справа", "заново", "сначала", "сразу", "потому",
	"весьма", "однако", "будучи", "получена", "оплачена", "закрыта",
	"обязана", "должна", "нужна", "видна", "слышна", "согласна", "уверена",
	"понятна", "приятна", "практична", "логична", "типична", "симпатична",
	"отлична", "прилична", "привычна", "публична", "обычна", "удобна",
	"основа", "основы", "основе", "основу", "основой",
	"голова", "головы", "голове", "голову", "головой",
	"корова", "коровы", "корове", "корову",
	"половина", "середина", "дюжина", "полдюжины",
	"украина", "украины", "украине", "украину", "украиной",
	"гражданин", "гражданина", "хозяин", "хозяина", "христианин",
	"дворянин", "мещанин", "крестьянин", "славянин", "англичанин", "россиянин",
	"термин", "термина", "термины", "терминов", "терминал",
	"протеин", "вазелин", "пингвин", "павлин", "мезонин", "ковролин",
	"керосин", "карантин", "витамин", "апельсин", "мандарин", "магазин",
	"стеарин", "маргарин", "лабиринт", "муравьин",
	"деревьев", "листьев", "перьев", "ручьев", "друзей", "судей",
	"годовой", "деловой", "боевой", "мировой", "часовой", "цифровой",
	"основной", "массовой", "торговой", "рисовой", "садовой", "столовой",
	"строевой", "круговой", "угловой", "целевой", "тыловой", "правовой",
	"типовой", "штормовой", "световой", "звуковой", "языковой", "игровой",
	"ледовой", "меловой", "лицевой", "клещевой", "плечевой", "лучевой",
	"ключевой", "ножевой", "душевой", "дождевой", "свиной", "нулевой",
	"бытовой", "почтовой", "сетевой", "стартовой", "страховой", "налоговой",
	"хоровой", "жировой", "носовой", "ротовой", "теневой", "спиртовой",
	"сотовой", "оптовой", "суровой", "цветовой", "береговой", "кормовой",
)

// fioLowerTrapStems перечисляет основы обычных слов с фамильным окончанием.
// Через основу закрываются сразу все падежные формы: «машина», «машины»,
// «машиной», «машинам».
var fioLowerTrapStems = fioWordSet(
	"машин", "картин", "причин", "женщин", "мужчин", "община", "общин",
	"долин", "равнин", "впадин", "котловин", "вершин", "величин", "глубин",
	"ширин", "толщин", "длин", "середин", "половин", "окраин", "руин",
	"дисциплин", "медицин", "кабин", "витрин", "корзин", "истин", "тишин",
	"старин", "родин", "чужбин", "паутин", "скотин", "рутин", "плотин",
	"пружин", "трещин", "развалин", "раковин", "лощин", "пучин", "кручин",
	"доктрин", "десятин", "седин", "година", "перин", "маслин", "малин",
	"смородин", "говядин", "баранин", "свинин", "осетрин", "солонин",
	"калин", "рябин", "осин", "лещин", "ольшин",
	"морщин", "щетин", "дружин", "глин", "льдин", "плешин", "прогалин",
	"проталин", "промоин", "сердцевин", "древесин", "парусин", "резин",
	"бензин", "керосин", "гардин", "перекладин", "трясин", "холстин",
	// Родительный падеж множественного числа совпадает с фамилией на «ов»:
	// «метров», «объектов», «игроков». Основа отсекается на две буквы, поэтому
	// здесь стоит начальная форма существительного. Основы, от которых
	// образуются настоящие фамилии, сюда не попадают: «кузнец», «мороз»,
	// «жук», «ворон» дают Кузнецова, Морозова, Жукова, Воронова.
	"метр", "километр", "сантиметр", "литр", "килограмм", "гектар",
	"миллион", "миллиард", "градус", "доллар", "пункт", "балл", "вид",
	"год", "месяц", "дом", "остров", "полуостров", "берег", "объект",
	"район", "город", "регион", "народ", "язык", "фильм", "альбом",
	"сериал", "трек", "сингл", "концерт", "турнир", "сезон", "этап",
	"тип", "вариант", "элемент", "компонент", "материал", "продукт",
	"товар", "заказ", "платеж", "вклад", "кредит", "вопрос", "ответ",
	"отзыв", "рейс", "билет", "центр", "офис", "филиал", "отдел",
	"сервис", "сервер", "файл", "сайт", "канал", "символ", "байт",
	"стол", "стул", "автор", "критик", "игрок", "спортсмен", "музыкант",
	"художник", "турист", "студент", "ученик", "выпускник", "специалист",
	"участник", "работник", "пассажир", "депутат", "директор", "менеджер",
	"оператор", "инвестор", "кандидат", "конкурент", "поставщик",
	"заказчик", "подрядчик", "пример", "экземпляр", "проект", "процесс",
	"результат", "ресурс", "источник", "инструмент", "механизм", "прибор",
	"аппарат", "агрегат", "завод", "склад", "цех", "отель", "ресторан",
	"налог", "актив", "взнос", "сбор", "штраф", "расход", "доход",
	"убыток", "дивиденд", "акционер", "кредитор", "вкладчик", "должник",
	"собственник", "пайщик", "дольщик", "арендатор", "застройщик",
	"наследник", "родственник", "супруг", "ветеран", "пенсионер", "инвалид",
	"чиновник", "юрист", "адвокат", "эксперт", "курьер", "охранник",
	"фермер", "бизнесмен", "банкир", "брокер", "дилер", "мошенник", "хакер",
	"абонент", "пациент", "болельщик", "артист", "актер", "журналист",
	"блогер", "тренер", "новичок", "подросток", "мальчик", "мужик",
	"бонус", "тариф", "лимит", "баланс", "курс", "обмен", "перевод",
	"банкомат", "терминал", "напиток", "мастер", "нерв", "цвет", "срок",
	"праздник", "размер", "орган", "округ", "член", "столик", "класс",
	"телефон", "поход", "плод", "закон", "приток", "фрукт", "кружок",
	"педагог", "совет", "расчет", "возраст", "комплект", "фрагмент",
	"фактор", "выбор", "двор", "тренажер", "консультант", "предок",
	"халат", "бассейн", "торт", "запрос", "музей", "динозавр", "полет",
	"доброволец", "министр", "компьютер", "сигнал", "препарат", "чемпион",
	"комикс", "союзник", "автомат", "водоем", "кассир", "разработчик",
	"успех", "минус", "плюс", "рисунок", "момент", "разговор", "навык",
	"доктор", "футболист", "маршрут", "оттенок", "приоритет", "склон",
	"параметр", "предмет", "металл", "лидер", "остаток", "электрон",
	"атом", "пожар", "холм", "потомок", "фонд", "салат", "соус", "порог",
	"дефект", "мост", "курорт", "стакан", "театр", "пакет", "экспонат",
	"кадр", "приказ", "лауреат", "труд", "лист", "сосуд", "сувенир",
	"коридор", "порядок", "памятник", "архив", "рынок", "поселок", "пилот",
	"водопад", "интерес", "метод", "выстрел", "вектор", "текст", "враг",
	"залив", "образец", "охотник", "аспект", "владельцев", "жильцов",
	"продавцов", "кружков", "предков", "танцев", "музеев", "героев",
	"осадков", "концов", "отцов", "бойцов", "певцов", "образцов",
	"стульев", "крыльев", "пальцев", "немцев", "самцов", "евреев",
	"славян", "крестьян", "украинцев", "британцев", "арабов", "персов",
	"сербов", "хозяев", "полуслов", "такова", "потеряна", "покров",
	"госпошлин", "логин", "админ", "кофемашин", "пробоин", "кальян",
	"добровольцев", "ролл", "сиквел", "реал",
	// Деепричастия на «ев»: «увидев», «рассмотрев».
	"увидев", "посмотрев", "рассмотрев", "разглядев", "оглядев", "усмотрев",
	"предусмотрев", "преодолев", "одолев", "успев", "сумев", "поспев",
	"потерпев", "прозрев", "поболев", "посидев", "полетев",
	// Остальные частые существительные из замера: родительный падеж
	// множественного числа даёт форму, неотличимую от фамилии.
	"профессионал", "официант", "контрагент", "реквизит", "недостаток",
	"моллюск", "кран", "пруд", "танк", "груз", "гаджет", "бургер",
	"десерт", "изыск", "анализ", "археолог", "юниор", "энтузиаст",
	"чемпионат", "федерал", "уговор", "топоним", "стерлинг", "рецептор",
	"релиз", "пулемет", "прицел", "принцип", "престол", "покемон",
	"подшипник", "пирожок", "оркестр", "нюанс", "наркотик",
	"муниципалитет", "маркетплейс", "лежак", "кубок", "конус",
	"контроллер", "комплекс", "колокол", "катер", "катализатор", "исток",
	"инцидент", "индивид", "император", "иероглиф", "запас", "епископ",
	"деликатес", "декабрист", "дачник", "грызун", "вывод", "ботаник",
	"архитектор", "арест", "активист", "авиабилет", "аборт", "феодал",
	"финал", "финанс", "фунт", "хлыст", "полуфабрикат", "бизон",
	"конституционалист", "операционист", "юнионист",
	"клуб", "стадион", "аэропорт", "вокзал", "поезд", "самолет", "автобус",
	"корабль", "офицер", "полк", "отряд", "экипаж", "приз", "рекорд",
)

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

// fioWordSet собирает набор слов для быстрой проверки по одному слову.
func fioWordSet(words ...string) map[string]bool {
	out := make(map[string]bool, len(words))
	for _, w := range words {
		out[dict.Normalize(w)] = true
	}
	return out
}

// fioAnchors перечисляет слова, после которых идёт имя человека. Падежные формы
// перечислены явно, потому что маркер сравнивается со словом целиком и
// «клиентов» до «клиент» не сокращается.
var fioAnchors = fioWordSet(
	"фио", "на имя", "в лице", "от имени", "фамилия", "фамилии", "имя", "отчество",
	// Местоимение «я» перед именем встречается в заявлениях: «Я Иванов».
	"я",
	"клиент", "клиента", "клиенту", "клиентом", "клиенте", "клиентов", "клиентка", "клиентки",
	"заявитель", "заявителя", "заявителю", "заявителем",
	"заемщик", "заемщика", "заемщику", "созаемщик", "поручитель", "поручителя",
	"плательщик", "плательщика", "получатель", "получателя", "получателю",
	"отправитель", "отправителя", "владелец", "владельца", "держатель", "держателя",
	"гражданин", "гражданина", "гражданке", "гражданка", "гражданки",
	"г-н", "г-на", "г-же", "г-жа", "гражданству",
	// Обращение и падежная форма «фамилию»: «укажите фамилию воробьев»,
	// «госпожа горшкова» разбирались без якоря и терялись на строчной букве.
	"господин", "господина", "господину", "госпожа", "госпоже", "госпожи",
	"фамилию", "фамилией",
	"уважаемый", "уважаемая", "уважаемые", "подпись", "подписал", "подписант",
	"здравствуйте", "здравствуй", "привет", "приветствуем", "приветствую",
	"дорогой", "дорогая", "дорогие", "добрый день", "доброе утро",
	"добрый вечер", "доброго дня", "уведомление для", "уведомление",
	"курьер", "курьера", "координатор", "координатора", "для клиента",
	"представитель", "представителя", "доверенность на", "доверенности на",
	"сотрудник", "сотрудника", "менеджер", "менеджера", "специалист", "специалиста",
	"пациент", "пациента", "абонент", "абонента", "наследник", "наследника", "наследнику",
	"супруг", "супруга", "супруге", "должник", "должника", "истец", "ответчик", "свидетель",
	"директор", "директора", "бухгалтер", "бухгалтера", "руководитель", "руководителя",
	"профессор", "нотариус", "вкладчик", "арендатор", "покупатель", "продавец",
	"заказчик", "исполнитель", "подрядчик", "бенефициар",
	"оформлен на", "оформлена на", "зарегистрирован на", "перевод", "перевести",
	// Падежные формы и обороты, которые встречаются в письмах и заявлениях.
	"зовут", "меня зовут", "вас зовут", "сотруднику", "сотрудником", "сотруднице",
	"студент", "студента", "студенту", "студентка", "студентки",
	"администратор", "администратора", "администратором",
	"менеджеру", "менеджером", "специалисту", "специалистом", "клиентом",
	"абоненту", "пациенту", "покупателю", "покупателем", "продавцу",
	"получателем", "плательщиком", "владельцу", "держателю", "заемщику",
	"поручителю", "курьеру", "координатору", "водитель", "водителя", "водителю",
	"арендатора", "арендатору", "должнику", "наследнице", "супруге",
	"владельца карты", "держателя карты",
	// Английские якоря: анкеты для переводов за рубеж и международные формы.
	"client", "customer", "applicant", "cardholder", "card holder", "holder",
	"name", "beneficiary", "recipient", "payer", "fio",
)

// fioGreetings перечисляет обращения, после которых идёт имя человека даже
// без фамилии: «Здравствуйте Моисей» другого признака имени не содержит.
// Список намеренно короче fioAnchors: по одному незнакомому слову решение
// принимается только после прямого обращения к человеку.
var fioGreetings = fioWordSet(
	"уважаемый", "уважаемая", "уважаемые", "уважаемому", "уважаемой",
	"здравствуйте", "здравствуй", "привет", "приветствуем",
	"дорогой", "дорогая", "дорогие", "дорогому", "дорогой наш",
	"господин", "господину", "господина", "госпожа", "госпоже", "госпожи",
	"г-н", "г-на", "г-ну", "г-же", "г-жа",
	"клиент", "клиента", "клиенту", "клиентка", "клиентки",
	"заявитель", "заявителя", "заявителю",
	"на имя", "фио", "в лице", "от имени",
	"получатель", "получателя", "получателю",
	"плательщик", "плательщика", "плательщику",
	"доверитель", "доверителя", "доверенность на", "доверенности на",
	"абонент", "абонента", "пациент", "пациента",
	"владелец", "владельца", "держатель", "держателя",
	"заемщик", "заемщика", "поручитель", "поручителя",
	// Английские обращения в международных формах.
	"client", "customer", "applicant", "cardholder", "card holder", "holder",
	"name", "beneficiary", "recipient", "payer", "fio",
)

// fioCommonWords перечисляет обычные слова, которые часто стоят с заглавной
// буквы рядом с именем: обращения к группе, названия месяцев, вводные слова.
// Именем такое слово не бывает, поэтому приклеивать его к фамилии нельзя.
var fioCommonWords = fioWordSet(
	"коллега", "коллеги", "коллегам", "партнер", "партнеры", "друг", "друзья",
	"господа", "товарищ", "товарищи", "дамы", "гость", "гости", "жители",
	"пользователь", "пользователи", "подписчик", "подписчики", "читатели",
	"покупатель", "покупатели", "участник", "участники", "родители", "все",
	"внимание", "уведомление", "информация", "напоминание", "спасибо",
	"благодарим", "просим", "пожалуйста", "добрый", "доброе", "доброго",
	"компания", "организация", "общество", "предприятие", "учреждение",
	"банк", "банка", "отделение", "филиал", "офис", "договор", "заявление",
	"справка", "квитанция", "документ", "документы", "паспорт", "карта",
	"счет", "счета", "номер", "адрес", "телефон", "почта", "индекс",
	"январь", "января", "февраль", "февраля", "март", "марта", "апрель",
	"апреля", "май", "мая", "июнь", "июня", "июль", "июля", "август",
	"августа", "сентябрь", "сентября", "октябрь", "октября", "ноябрь",
	"ноября", "декабрь", "декабря",
	"понедельник", "вторник", "среда", "четверг", "пятница", "суббота",
	"воскресенье", "сегодня", "завтра", "вчера", "также", "однако", "итого",
	"викторина", "викторины", "викторине", "лекция", "лекции", "книга",
	"портрет", "памятник", "монета", "улица", "поселок", "деревня", "город",
)

// fioAddressMarkers перечисляет слова, после которых идёт название улицы.
// Названия улиц и памятных объектов персональными данными не являются, и
// лишнее срабатывание на них проверяется отдельно.
var fioAddressMarkers = fioWordSet(
	"ул", "улица", "улице", "улицы", "улицу",
	"пр-т", "пр-кт", "проспект", "проспекте", "проспекта",
	"пер", "переулок", "переулке", "переулка",
	"пл", "площадь", "площади", "наб", "набережная", "набережной",
	"ш", "шоссе", "б-р", "бульвар", "бульваре", "бульвара",
	"проезд", "проезде", "тупик", "имени", "им",
	"метро", "станция", "станции", "музей", "музея", "библиотека", "библиотеки",
	"театр", "театра", "парк", "парка", "мост", "моста",
)

// fioFillerWords перечисляет служебные слова, которые допускаются между
// маркером и именем. Без них якорь «клиент банка Иванов» перестаёт работать, а маркер
// «ул. Академика Павлова» перестаёт защищать название улицы.
var fioFillerWords = fioWordSet(
	"банка", "банке", "по", "от", "для", "на", "в", "у", "с", "к",
	"наш", "нашего", "вашего", "вашей", "нашей", "его", "ее", "их",
	"гр", "г", "тов", "ип", "ооо", "зао",
	"академика", "генерала", "маршала", "адмирала", "профессора", "героя",
	"братьев", "космонавта", "композитора", "писателя", "поэта",
	"лейтенанта", "капитана", "доктора", "сестер", "летчика",
)

// fioStopWords перечисляет обычные слова и названия городов с фамильными
// окончаниями.
// Они отбрасываются только там, где решение принимается по одному слову без
// якоря: вторым компонентом имени такое слово вреда не приносит.
var fioStopWords = fioWordSet(
	"магазин", "господин", "один", "бензин", "карантин", "витамин", "апельсин",
	"мандарин", "кувшин", "аршин", "картин", "машин", "причин", "женщин", "мужчин",
	"долин", "витрин", "корзин", "кабин", "половин",
	"слов", "основ", "банков", "клиентов", "годов", "домов", "счетов", "звонков",
	"документов", "заказов", "договоров", "паспортов", "отделов", "филиалов",
	"сотрудников", "процентов", "номеров", "вкладов", "товаров", "продуктов",
	"часов", "вопросов", "ответов", "сервисов", "серверов", "партнеров",
	"операторов", "случаев", "платежей", "средств", "рублев",
	"ростов", "саратов", "тамбов", "киров", "псков", "азов", "львов", "харьков",
	"чернигов", "кишинев", "ковров", "серпухов", "пушкин", "гагарин",
	// Падежные формы месяцев: «в августе», «к марту» — это не имена.
	"январе", "январю", "феврале", "февралю", "марте", "марту", "апреле",
	"апрелю", "мае", "маю", "июне", "июню", "июле", "июлю", "августе",
	"августу", "сентябре", "сентябрю", "октябре", "октябрю", "ноябре",
	"ноябрю", "декабре", "декабрю",
	// «Марка» и её падежные формы: марка автомобиля, почтовая марка.
	"марк", "марка", "марки", "марку", "марке", "маркой",
	// Слова, совпадающие с редкими именами из словаря.
	"викторина", "викторины", "викторине", "забава", "забавы", "забаву",
	"улита", "улиты", "мина", "мины", "мине", "мину", "сила", "силы", "силу",
	"силе", "любим", "лавр", "лавра", "лавры", "карп", "карпа", "карпы",
	"милан", "милана", "боян", "мир", "мира", "миру", "мире",
)

// fioHomonyms перечисляет имена, которые совпадают с обычными словами. Одного такого
// слова для маскирования мало, нужен второй компонент имени или якорь.
var fioHomonyms = fioWordSet(
	"вера", "любовь", "надежда", "роза", "роман", "майя", "лада", "рада",
	"марта", "ада", "ия", "злата", "август", "августа", "искра", "воля",
	"мир", "лев", "марс",
	// Редкие имена, совпадающие с обычными словами.
	"сила", "любим", "мина", "лавр", "карп", "милан", "боян", "забава",
	"улита", "викторина", "милица", "наина", "мавра", "прокл", "изот",
)

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
