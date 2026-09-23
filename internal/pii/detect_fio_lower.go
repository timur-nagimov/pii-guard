package pii

import "sort"

// Фамилия со строчной буквы — последний шаг разбора имён. Шаг вынесен в
// отдельный файл: он самостоятельный, опирается на уже найденные фрагменты и
// правила у него свои, строже общих. Разбор слов с заглавной буквы лежит в
// detect_fio.go, словари — в detect_fio_words.go.
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
// из наборов детектора. Наборы разнесены по смыслу — обычное слово, омоним
// имени, слово-маркер, — потому что за восемью проверками подряд уже не видно,
// что именно отсеивается.
func fioLowerTrapWord(w string) bool {
	return fioOrdinaryWord(w) || fioHomonyms[w] || fioMarkerWord(w)
}

// fioOrdinaryWord сообщает, что слово перечислено как обычное: ловушка с
// фамильным окончанием, стоп-слово или частое слово рядом с именем. Эти же
// наборы проверяются и по основе слова, поэтому они собраны вместе.
func fioOrdinaryWord(w string) bool {
	return fioLowerTrapWords[w] || fioLowerTrapStems[w] ||
		fioStopWords[w] || fioCommonWords[w]
}

// fioMarkerWord сообщает, что слово это маркер, а не имя: якорь, после
// которого имя только ожидается, служебное слово между якорем и именем или
// указание на улицу.
func fioMarkerWord(w string) bool {
	return fioAnchors[w] || fioFillerWords[w] || fioAddressMarkers[w]
}

// fioLowerTrapStem сообщает, что основа принадлежит обычному слову. Наборы с
// именами и обращениями здесь не проверяются: у фамилии «Романов» основа
// совпадает с именем «Роман», и такая проверка отбросила бы саму фамилию.
func fioLowerTrapStem(stem string) bool {
	return fioOrdinaryWord(stem)
}
