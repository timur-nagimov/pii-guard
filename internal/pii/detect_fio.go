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
	fioRolePatronymic
	fioRolePatronymicWeak
	fioRoleInitial
)

// Наборы признаков, которые в правилах используются вместе.
const (
	fioRoleAnySurname    = fioRoleSurnameDict | fioRoleSurnameStrong | fioRoleSurnameAdjective | fioRoleSurnameWeak
	fioRoleAnyPatronymic = fioRolePatronymic | fioRolePatronymicWeak
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

// fioMaxGapRunes задаёт наибольшую длину промежутка между соседними
// компонентами имени. Промежуток состоит только из пробелов и точек после инициалов.
const fioMaxGapRunes = 3

// fioWord описывает слово-кандидат в компоненты имени вместе с разобранными
// признаками. Слово хранится в байтовых границах исходного текста.
type fioWord struct {
	start   int
	end     int
	norm    string
	key     string
	capital bool
	latin   bool
	dot     bool
	roles   fioRole
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
	words := fioCollectWords(d)
	var out []Span
	start := 0
	for i := 1; i <= len(words); i++ {
		if i < len(words) && fioAdjacent(d, words[i-1], words[i]) {
			continue
		}
		out = append(out, fioScanRun(d, words[start:i])...)
		start = i
	}
	return out
}

// fioCollectWords собирает слова, у которых есть хотя бы один признак
// компонента имени. Слова без признаков пропускаются: они всё равно разрывают
// цепочку, потому что промежуток с буквами не считается соседством.
func fioCollectWords(d *Doc) []fioWord {
	out := make([]fioWord, 0, 8)
	toks := d.Tokens
	for i := 0; i < len(toks); i++ {
		if toks[i].Kind != KindCyr && toks[i].Kind != KindLat {
			continue
		}
		end, last := fioWordEnd(d, i)
		if w := fioNewWord(d, toks[i].Start, end); w.roles != 0 {
			out = append(out, w)
		}
		i = last
	}
	return out
}

// fioWordEnd находит конец слова, приклеивая части через дефис: двойные
// фамилии вроде Петров-Водкин и составные имена вроде Анна-Мария это один
// компонент. Часть после дефиса обязана начинаться с заглавной буквы, иначе
// к имени приклеится обычное слово.
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
			break
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
	w.latin = d.Tokens[d.TokenIndexAt(start)].Kind == KindLat
	w.dot = end < len(d.Text) && d.Text[end] == '.'
	w.key = w.norm
	if w.latin {
		w.key = dict.TranslitToCyr(w.norm)
	}
	w.roles = fioWordRoles(w)
	return w
}

// fioWordRoles определяет признаки слова. Части двойной фамилии разбираются
// по отдельности: достаточно, чтобы имени соответствовала любая из них.
func fioWordRoles(w fioWord) fioRole {
	if fioRuneCount(w.norm) == 1 {
		if w.capital {
			return fioRoleInitial
		}
		return 0
	}
	var roles fioRole
	for _, part := range strings.Split(w.key, "-") {
		roles |= fioPartRoles(part, w.capital)
	}
	return roles
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
func fioAdjacent(d *Doc, a, b fioWord) bool {
	if b.start < a.end {
		return false
	}
	gap := d.Text[a.end:b.start]
	if fioRuneCount(gap) > fioMaxGapRunes {
		return false
	}
	for _, r := range gap {
		// Неразрывный пробел встречается в тексте из офисных редакторов.
		if r != ' ' && r != '\t' && r != '.' && r != '\u00a0' {
			return false
		}
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
	}
	return 0, "", false
}

// fioInitialsPair проверяет запись «Иванов И.» и «И. Иванов».
func fioInitialsPair(a, b fioWord) bool {
	if a.has(fioRoleAnySurname) && b.isInitial() {
		return true
	}
	return a.isInitial() && b.has(fioRoleAnySurname)
}

// fioSingleShape разбирает одиночный компонент. Одного слова достаточно,
// если это отчество, если рядом стоит якорь или если слово известно словарю.
func fioSingleShape(d *Doc, w fioWord) (float64, string, bool) {
	if w.has(fioRolePatronymic) && w.capital && !w.latin {
		return ConfAnchored, "fio:patronymic", true
	}
	if _, ok := fioMarkerBefore(d, w.start, fioAnchors); ok {
		return ConfAnchored, "fio:anchor", true
	}
	// Дальше решение принимается без якоря, поэтому одиночная латиница и
	// слова со строчной буквы отбрасываются, а обычные слова с фамильными
	// окончаниями отсекаются списком исключений.
	if !w.capital || w.latin || fioStopWords[w.norm] {
		return 0, "", false
	}
	switch {
	case w.has(fioRoleSurnameDict):
		return ConfAnchored, "fio:surname", true
	case w.has(fioRoleName) && !fioHomonyms[w.norm]:
		return ConfAnchored, "fio:name", true
	case w.has(fioRoleSurnameStrong) && !fioAtSentenceStart(d, w.start):
		return ConfMedium, "fio:surname_suffix", true
	}
	return 0, "", false
}

// fioMakeSpan собирает фрагмент, отсекая случаи, когда слева стоит название
// улицы или объекта, и не давая фрагменту выйти за строку.
func fioMakeSpan(d *Doc, first, last fioWord, conf float64, reason string) (Span, bool) {
	if _, blocked := fioMarkerBefore(d, first.start, fioAddressMarkers); blocked {
		return Span{}, false
	}
	if first.latin && last.latin {
		reason = "fio:latin+" + strings.TrimPrefix(reason, "fio:")
	}
	// Границы нормализуются на самом фрагменте, а не на всём тексте: разбор
	// текста целиком ради обрезки нескольких знаков стоит слишком дорого.
	lead, tail := NormalizeSpan(d.Text[first.start:last.end], 0, last.end-first.start)
	start, end := first.start+lead, first.start+tail
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
	"клиент", "клиента", "клиенту", "клиентом", "клиенте", "клиентов", "клиентка", "клиентки",
	"заявитель", "заявителя", "заявителю", "заявителем",
	"заемщик", "заемщика", "заемщику", "созаемщик", "поручитель", "поручителя",
	"плательщик", "плательщика", "получатель", "получателя", "получателю",
	"отправитель", "отправителя", "владелец", "владельца", "держатель", "держателя",
	"гражданин", "гражданина", "гражданке", "гражданка", "гражданки",
	"г-н", "г-на", "г-же", "г-жа", "гражданству",
	"уважаемый", "уважаемая", "уважаемые", "подпись", "подписал", "подписант",
	"представитель", "представителя", "доверенность на", "доверенности на",
	"сотрудник", "сотрудника", "менеджер", "менеджера", "специалист", "специалиста",
	"пациент", "пациента", "абонент", "абонента", "наследник", "наследника", "наследнику",
	"супруг", "супруга", "супруге", "должник", "должника", "истец", "ответчик", "свидетель",
	"директор", "директора", "бухгалтер", "бухгалтера", "руководитель", "руководителя",
	"профессор", "нотариус", "вкладчик", "арендатор", "покупатель", "продавец",
	"заказчик", "исполнитель", "подрядчик", "бенефициар",
	"оформлен на", "оформлена на", "зарегистрирован на", "перевод", "перевести",
	"владельца карты", "держателя карты",
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
)

// fioHomonyms перечисляет имена, которые совпадают с обычными словами. Одного такого
// слова для маскирования мало, нужен второй компонент имени или якорь.
var fioHomonyms = fioWordSet(
	"вера", "любовь", "надежда", "роза", "роман", "майя", "лада", "рада",
	"марта", "ада", "ия", "злата", "август", "августа", "искра", "воля",
	"мир", "лев", "марс",
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
