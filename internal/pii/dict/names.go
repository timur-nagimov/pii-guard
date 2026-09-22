// Package dict содержит встроенные словари русских имён и фамилий и правила
// работы с ними: приведение слова к сравнимому виду, разбор падежных форм и
// транслитерацию латиницы в кириллицу.
//
// Словари хранятся только в начальной форме. Падежные формы не перечисляются
// списком, а получаются отсечением падежного окончания и проверкой усечённой
// основы, поэтому объём данных остаётся небольшим, а инициализация занимает
// доли миллисекунды.
package dict

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed names_male.txt
var rawMaleNames string

//go:embed names_female.txt
var rawFemaleNames string

//go:embed surnames.txt
var rawSurnames string

// Gender обозначает род, к которому относится имя. Род повышает точность
// разбора отчеств и падежных форм, но решения о маскировании не меняет.
type Gender uint8

// Значения рода.
const (
	GenderUnknown Gender = iota
	GenderMale
	GenderFemale
)

// tables хранит загруженные словари. Структура создаётся один раз и дальше
// только читается, поэтому безопасна для параллельного доступа из запросов.
type tables struct {
	male     map[string]struct{}
	female   map[string]struct{}
	surnames map[string]struct{}
}

// load собирает словари из встроенных файлов. Вызывается не более одного раза.
func load() *tables {
	return &tables{
		male:     parseList(rawMaleNames),
		female:   parseList(rawFemaleNames),
		surnames: parseList(rawSurnames),
	}
}

// parseList разбирает встроенный список слов по одному в строке.
func parseList(raw string) map[string]struct{} {
	out := make(map[string]struct{}, strings.Count(raw, "\n")+1)
	for _, line := range strings.Split(raw, "\n") {
		w := Normalize(line)
		if w == "" {
			continue
		}
		out[w] = struct{}{}
	}
	return out
}

// data отдаёт словари, загружая их при первом обращении.
var data = sync.OnceValue(load)

// Normalize приводит слово к виду, в котором оно сравнивается со словарём:
// нижний регистр, без обрамляющих пробелов, буква ё заменена на е.
//
// Замена ё на е нужна потому, что в текстах одно и то же имя пишется обоими
// способами, а различать их смысла нет.
func Normalize(w string) string {
	w = strings.TrimSpace(strings.ToLower(w))
	if strings.ContainsRune(w, 'ё') {
		w = strings.ReplaceAll(w, "ё", "е")
	}
	return w
}

// caseEndings перечисляет окончания косвенных падежей, которые отсекаются при
// поиске начальной формы. Порядок важен: сначала длинные окончания, иначе от слова
// «ивановой» останется «иванов» только после отсечения одной буквы.
var caseEndings = []string{"ой", "ей", "ом", "ем", "ым", "им", "ах", "ях", "а", "я", "у", "ю", "ы", "и", "е"}

// stemTails перечисляет буквы, которые возвращаются усечённой основе, чтобы
// получить начальную форму: «серге» плюс й даёт «сергей», «мари» плюс я даёт «мария».
var stemTails = []string{"", "й", "ь", "а", "я"}

// minStemBytes задаёт наименьшую длину основы в байтах, при которой отсечение
// окончания считается осмысленным. Три кириллические буквы занимают шесть
// байт, более короткие основы дают слишком много случайных совпадений.
const minStemBytes = 6

// lookup ищет слово в наборе сначала как есть, затем по усечённой основе.
func lookup(set map[string]struct{}, w string) bool {
	if _, ok := set[w]; ok {
		return true
	}
	for _, e := range caseEndings {
		if !strings.HasSuffix(w, e) {
			continue
		}
		stem := w[:len(w)-len(e)]
		if len(stem) < minStemBytes {
			continue
		}
		for _, tail := range stemTails {
			if _, ok := set[stem+tail]; ok {
				return true
			}
		}
	}
	return false
}

// LookupName сообщает, что слово является русским именем в начальной или в
// падежной форме, и возвращает род имени.
func LookupName(w string) (Gender, bool) {
	w = Normalize(w)
	t := data()
	if lookup(t.male, w) {
		return GenderMale, true
	}
	if lookup(t.female, w) {
		return GenderFemale, true
	}
	return GenderUnknown, false
}

// LookupSurname сообщает, что слово является фамилией из словаря в начальной
// или в падежной форме.
func LookupSurname(w string) bool {
	return lookup(data().surnames, Normalize(w))
}

// KnownWord сообщает, что слово встречается хотя бы в одном из словарей.
// Нужен детектору, чтобы отличить осмысленный компонент имени от случайного
// слова с подходящим окончанием.
func KnownWord(w string) bool {
	if _, ok := LookupName(w); ok {
		return true
	}
	return LookupSurname(w)
}

// Sizes возвращает размеры словарей. Используется в проверках и в отладке,
// чтобы убедиться, что встроенные файлы действительно загрузились.
func Sizes() (male, female, surname int) {
	t := data()
	return len(t.male), len(t.female), len(t.surnames)
}

// translitPairs задаёт соответствия латинских сочетаний кириллице. Разбираются от
// длинных к коротким, поэтому shch превращается в щ раньше, чем sh в ш.
var translitPairs = map[string]string{
	"shch": "щ",
	"sch":  "щ",
	"zh":   "ж",
	"kh":   "х",
	"ts":   "ц",
	"ch":   "ч",
	"sh":   "ш",
	"yu":   "ю",
	"ya":   "я",
	"ye":   "е",
	"yo":   "е",
	"iu":   "ю",
	"ia":   "я",
	"ii":   "ий",
	"iy":   "ий",
	"iya":  "ия",
	"iyu":  "ию",
	"iye":  "ие",
	"ey":   "ей",
	"ay":   "ай",
	"oy":   "ой",
	"uy":   "уй",
	"a":    "а",
	"b":    "б",
	"c":    "к",
	"d":    "д",
	"e":    "е",
	"f":    "ф",
	"g":    "г",
	"h":    "х",
	"i":    "и",
	"j":    "й",
	"k":    "к",
	"l":    "л",
	"m":    "м",
	"n":    "н",
	"o":    "о",
	"p":    "п",
	"q":    "к",
	"r":    "р",
	"s":    "с",
	"t":    "т",
	"u":    "у",
	"v":    "в",
	"w":    "в",
	"x":    "кс",
	"z":    "з",
}

// cyrVowels перечисляет гласные, после которых латинская y читается как й.
const cyrVowels = "аеиоуыэюя"

// TranslitToCyr переводит латинскую запись имени в кириллицу, чтобы её можно
// было сравнить с обычным словарём. Перевод нужен только для сравнения и не
// претендует на обратимость.
func TranslitToCyr(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s) * 2)
	for i := 0; i < len(s); {
		rep, size := translitChunk(s, i)
		// Одиночная y читается по соседям, поэтому её чтение подбирается
		// отдельно. Сочетания ya, yu, ye разобраны выше и сюда не попадают.
		if size == 1 && s[i] == 'y' {
			rep = translitY(b.String(), s, i)
		}
		b.WriteString(rep)
		i += size
	}
	return b.String()
}

// translitChunk подбирает самое длинное известное сочетание, начинающееся с
// указанной позиции.
func translitChunk(s string, i int) (string, int) {
	for n := 4; n >= 1; n-- {
		if i+n > len(s) {
			continue
		}
		if rep, ok := translitPairs[s[i:i+n]]; ok {
			return rep, n
		}
	}
	// Незнакомый символ переносится без изменений: он всё равно не даст
	// совпадения со словарём и просто прервёт слово.
	return s[i : i+1], 1
}

// translitY выбирает чтение латинской y: после гласной и в конце слова это й,
// в конце слова после согласной это ий, в остальных случаях ы.
func translitY(done, s string, i int) string {
	prevVowel := false
	if r := lastRune(done); r != 0 && strings.ContainsRune(cyrVowels, r) {
		prevVowel = true
	}
	last := i == len(s)-1
	switch {
	case prevVowel:
		return "й"
	case last:
		return "ий"
	default:
		return "ы"
	}
}

// lastRune возвращает последнюю руну строки или ноль для пустой строки.
func lastRune(s string) rune {
	last := rune(0)
	for _, r := range s {
		last = r
	}
	return last
}
