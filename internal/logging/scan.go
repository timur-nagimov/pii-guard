package logging

import "unicode"

// inspectString разбирает значение поля и сообщает, похоже ли оно на
// персональные данные. Разбор идёт за один проход по рунам, без регулярных
// выражений и без выделения памяти: проверка стоит на горячем пути.
//
// Разбор намеренно грубый. Его задача не распознать тип данных, а поймать
// форму: длинную цепочку цифр, группу цифр с разделителями, адрес почты, два
// слова с прописной буквы подряд. Ложное срабатывание стоит потери одного
// поля в журнале, пропуск стоит утечки, поэтому выбор сделан в пользу строгости.
func inspectString(s string) (string, bool) {
	if len(s) < minSuspectLen {
		return "", false
	}
	if len(s) > maxValueBytes {
		return reasonTooLong, true
	}
	var sc scanner
	sc.groupOnlyDots = true
	for _, r := range s {
		if reason := sc.feed(r); reason != "" {
			return reason, true
		}
	}
	return sc.finish(len(s))
}

// scanner — состояние разбора значения.
type scanner struct {
	digitRun int

	groupDigits   int
	groupRuns     int
	groupMaxRun   int
	groupOnlyDots bool

	wordKind byte
	wordLen  int
	capWords int
	upperRun int

	hasLower   bool
	lastLetter bool
	atSeen     bool
	dotAfterAt bool
}

// Виды слов: C это слово с прописной кириллической буквы и дальше строчные,
// U это слово целиком из прописных латинских, x это любое другое.
const (
	wordCap   byte = 'C'
	wordUpper byte = 'U'
	wordOther byte = 'x'
)

func (sc *scanner) feed(r rune) string {
	switch {
	case r >= '0' && r <= '9':
		return sc.feedDigit(r)
	case isSep(r):
		return sc.feedSep(r)
	case isLetter(r):
		return sc.feedLetter(r)
	default:
		return sc.feedOther(r)
	}
}

func (sc *scanner) feedDigit(r rune) string {
	if reason := sc.endWord(r); reason != "" {
		return reason
	}
	sc.lastLetter = false
	sc.digitRun++
	if sc.digitRun == 1 {
		sc.groupRuns++
	}
	if sc.digitRun > sc.groupMaxRun {
		sc.groupMaxRun = sc.digitRun
	}
	sc.groupDigits++
	if sc.digitRun >= digitRunLimit {
		return reasonDigitRun
	}
	return ""
}

// feedSep обрабатывает знак, который не разрывает группу цифр: пробел, дефис,
// точку, скобки, косую черту, плюс. Именно так записывают телефоны, номера
// карт, даты и коды подразделений.
func (sc *scanner) feedSep(r rune) string {
	sc.digitRun = 0
	if sc.groupDigits > 0 {
		if r == '.' {
			if sc.atSeen {
				sc.dotAfterAt = true
			}
		} else {
			sc.groupOnlyDots = false
		}
	} else if r == '.' && sc.atSeen {
		sc.dotAfterAt = true
	}
	return sc.endWord(r)
}

func (sc *scanner) feedLetter(r rune) string {
	if sc.dotAfterAt {
		// Буква после точки после собачки: форма адреса электронной почты.
		return reasonEmail
	}
	sc.digitRun = 0
	if reason := sc.closeGroup(); reason != "" {
		return reason
	}
	sc.lastLetter = true
	sc.addLetter(r)
	return ""
}

func (sc *scanner) feedOther(r rune) string {
	sc.digitRun = 0
	if reason := sc.closeGroup(); reason != "" {
		return reason
	}
	if r == '@' && sc.lastLetter {
		sc.atSeen = true
	}
	if reason := sc.endWord(r); reason != "" {
		return reason
	}
	sc.lastLetter = false
	return ""
}

// addLetter продолжает текущее слово и уточняет его вид.
func (sc *scanner) addLetter(r rune) {
	lower := isLowerLetter(r)
	if lower {
		sc.hasLower = true
	}
	if sc.wordLen == 0 {
		switch {
		case isUpperCyr(r):
			sc.wordKind = wordCap
		case r >= 'A' && r <= 'Z':
			sc.wordKind = wordUpper
		default:
			sc.wordKind = wordOther
		}
		sc.wordLen = 1
		return
	}
	switch sc.wordKind {
	case wordCap:
		if !isLowerCyr(r) {
			sc.wordKind = wordOther
		}
	case wordUpper:
		if r < 'A' || r > 'Z' {
			sc.wordKind = wordOther
		}
	}
	sc.wordLen++
}

// endWord закрывает слово и считает слова, похожие на части имени.
//
// Кириллические слова с прописной буквы считаются по всей строке, а не подряд:
// адрес «г. Москва, ул. Ленина, д. 5» разрывает цепочку запятыми, но два
// собственных имени в одном значении поля это уже персональные данные.
//
// Латинские слова из одних прописных считаются только подряд: имя держателя
// карты пишется двумя словами рядом, а сокращения вроде HTTP стоят по одному.
func (sc *scanner) endWord(term rune) string {
	kind, n := sc.wordKind, sc.wordLen
	sc.wordKind, sc.wordLen = 0, 0
	keep := term == ' ' || term == 0

	if n == 0 {
		if !keep {
			sc.upperRun = 0
		}
		return ""
	}
	switch {
	case kind == wordCap && n >= nameWordLen:
		sc.capWords++
		sc.upperRun = 0
	case kind == wordUpper && n >= upperWordLen:
		sc.upperRun++
	default:
		sc.upperRun = 0
	}
	if sc.capWords >= nameWordsLimit {
		return reasonName
	}
	if !keep {
		sc.upperRun = 0
	}
	return ""
}

// closeGroup закрывает группу цифр и проверяет её на длину. Из проверки
// выведены сетевые адреса вида 10.129.0.22: четыре части не длиннее трёх цифр,
// разделённые только точками. Без этого исключения любое упоминание адреса
// копии сервиса в тексте ошибки пропадало бы из журнала.
func (sc *scanner) closeGroup() string {
	bad := sc.groupDigits >= digitGroupLimit && !sc.ipLike()
	sc.groupDigits, sc.groupRuns, sc.groupMaxRun = 0, 0, 0
	sc.groupOnlyDots = true
	if bad {
		return reasonDigits
	}
	return ""
}

func (sc *scanner) ipLike() bool {
	return sc.groupOnlyDots && sc.groupRuns == 4 && sc.groupMaxRun <= 3
}

func (sc *scanner) finish(n int) (string, bool) {
	if reason := sc.endWord(0); reason != "" {
		return reason, true
	}
	if reason := sc.closeGroup(); reason != "" {
		return reason, true
	}
	// Имя держателя карты пишется прописными латинскими буквами: IVAN IVANOV.
	// Требование «во всей строке нет строчных букв» отсекает наши собственные
	// сообщения, где сокращения вроде HTTP соседствуют с обычным текстом.
	if sc.upperRun >= nameWordsLimit && !sc.hasLower && n >= upperNameLen {
		return reasonUpper, true
	}
	return "", false
}

// isSep сообщает, что знак разделяет части одного значения и не разрывает
// группу цифр. Двоеточие и запятая сюда не входят намеренно: иначе адрес с
// портом и перечисление чисел выглядели бы как номер документа.
func isSep(r rune) bool {
	switch r {
	case ' ', '-', '.', '(', ')', '/', '+', '\u00a0':
		return true
	default:
		return false
	}
}

func isLetter(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return true
	case r >= 0x0400 && r <= 0x04ff:
		return true
	case r < 0x80:
		return false
	default:
		return unicode.IsLetter(r)
	}
}

func isUpperCyr(r rune) bool {
	return (r >= 'А' && r <= 'Я') || r == 'Ё'
}

func isLowerCyr(r rune) bool {
	return (r >= 'а' && r <= 'я') || r == 'ё'
}

func isLowerLetter(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case isLowerCyr(r):
		return true
	case r < 0x80:
		return false
	default:
		return unicode.IsLower(r)
	}
}
