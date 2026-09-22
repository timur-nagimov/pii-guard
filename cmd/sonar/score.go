package main

import (
	"strings"
	"unicode/utf8"
)

// alignLimit ограничивает размер таблицы сопоставления рун. Сопоставление
// квадратично по длине, а тексты проверки короткие: на длинном тексте дешевле
// грубая оценка, чем десятки миллионов ячеек.
const alignLimit = 1500

// FragmentScore содержит оценку одного эталонного фрагмента.
type FragmentScore struct {
	// Type повторяет тип персональных данных из разметки набора.
	Type string
	// Distance равна нормированному расстоянию Левенштейна между исходным
	// значением и тем, что встало на его место. Единица означает, что внутри
	// фрагмента изменено всё, ноль означает, что не изменено ничего.
	Distance float64
	// Changed сообщает, что фрагмент изменён хотя бы частично.
	Changed bool
}

// MaskScore содержит оценку маскирования одного элемента набора.
type MaskScore struct {
	// Fragments перечисляет оценки эталонных фрагментов в порядке разметки.
	Fragments []FragmentScore
	// OutsideChangedBytes и OutsideTotalBytes дают долю изменённых байтов вне
	// эталонных фрагментов. Это мера лишних срабатываний: проверяющая система
	// её не штрафует, но жюри смотрит на неё глазами.
	OutsideChangedBytes int
	OutsideTotalBytes   int
	// Exact сообщает, что длина ответа совпала с длиной запроса и оценка взята
	// прямым сравнением, без сопоставления рун.
	Exact bool
	// Approx сообщает, что смещения восстановить не удалось и оценка фрагментов
	// сведена к признаку «значение осталось в тексте целиком».
	Approx bool
}

// LevenshteinRunes считает расстояние редактирования между строками по рунам.
// По рунам, а не по байтам: кириллическая буква занимает два байта, эмодзи до
// четырёх, и байтовое расстояние завысило бы оценку на ровном месте.
func LevenshteinRunes(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

// NormalizedLevenshtein приводит расстояние к отрезку от нуля до единицы:
// делит на длину более длинной строки в рунах. Две пустые строки считаются
// неизменёнными, поэтому дают ноль.
func NormalizedLevenshtein(a, b string) float64 {
	longest := max(utf8.RuneCountInString(a), utf8.RuneCountInString(b))
	if longest == 0 {
		return 0
	}
	return float64(LevenshteinRunes(a, b)) / float64(longest)
}

// ScoreMasking сравнивает ответ сервиса с исходным текстом по разметке набора.
// Если длина ответа не изменилась, смещения эталонных фрагментов остаются
// верными и сравнение прямое; иначе позиции восстанавливаются сопоставлением
// рун, потому что маска могла сдвинуть текст.
func ScoreMasking(s Sample, masked string) MaskScore {
	if len(masked) == len(s.Text) {
		return scoreSameLength(s, masked)
	}
	return scoreShifted(s, masked)
}

// scoreSameLength оценивает ответ, сохранивший длину исходного текста.
func scoreSameLength(s Sample, masked string) MaskScore {
	sc := MaskScore{Exact: true, Fragments: make([]FragmentScore, 0, len(s.Fragments))}
	for _, f := range s.Fragments {
		sc.Fragments = append(sc.Fragments, fragmentScore(f, masked[f.Start:f.End]))
	}
	sc.OutsideChangedBytes, sc.OutsideTotalBytes = outsideBytes(s, masked)
	return sc
}

// scoreShifted оценивает ответ, изменивший длину текста.
func scoreShifted(s Sample, masked string) MaskScore {
	origRunes := []rune(s.Text)
	maskedRunes := []rune(masked)
	if len(origRunes) > alignLimit || len(maskedRunes) > alignLimit {
		return scoreHeuristic(s, masked)
	}
	align := alignRunes(origRunes, maskedRunes)
	byteToRune := runeIndexByByte(s.Text)
	sc := MaskScore{Fragments: make([]FragmentScore, 0, len(s.Fragments))}
	for _, f := range s.Fragments {
		got := alignedView(align, maskedRunes, byteToRune[f.Start], byteToRune[f.End])
		sc.Fragments = append(sc.Fragments, fragmentScore(f, got))
	}
	sc.OutsideChangedBytes, sc.OutsideTotalBytes = outsideRunes(s, origRunes, align)
	return sc
}

// scoreHeuristic оценивает слишком длинный ответ без сопоставления рун: важно
// лишь то, уцелело ли исходное значение в тексте целиком.
func scoreHeuristic(s Sample, masked string) MaskScore {
	sc := MaskScore{Approx: true, Fragments: make([]FragmentScore, 0, len(s.Fragments))}
	for _, f := range s.Fragments {
		score := FragmentScore{Type: f.Type, Distance: 1, Changed: true}
		if strings.Contains(masked, f.Value) {
			score = FragmentScore{Type: f.Type}
		}
		sc.Fragments = append(sc.Fragments, score)
	}
	return sc
}

// fragmentScore считает оценку одного фрагмента.
func fragmentScore(f Fragment, got string) FragmentScore {
	d := NormalizedLevenshtein(f.Value, got)
	return FragmentScore{Type: f.Type, Distance: d, Changed: d > 0}
}

// fragmentMask отмечает байты, попавшие внутрь эталонных фрагментов.
func fragmentMask(s Sample) []bool {
	m := make([]bool, len(s.Text))
	for _, f := range s.Fragments {
		for i := f.Start; i < f.End && i < len(m); i++ {
			m[i] = true
		}
	}
	return m
}

// outsideBytes считает изменённые байты вне эталонных фрагментов для ответа
// той же длины.
func outsideBytes(s Sample, masked string) (changed, total int) {
	inside := fragmentMask(s)
	for i := 0; i < len(s.Text); i++ {
		if inside[i] {
			continue
		}
		total++
		if masked[i] != s.Text[i] {
			changed++
		}
	}
	return changed, total
}

// outsideRunes считает изменённые байты вне эталонных фрагментов по итогам
// сопоставления рун: несопоставленная руна считается изменённой целиком.
func outsideRunes(s Sample, origRunes []rune, align []int) (changed, total int) {
	inside := fragmentMask(s)
	offset := 0
	for i, r := range origRunes {
		size := utf8.RuneLen(r)
		if !inside[offset] {
			total += size
			if align[i] < 0 {
				changed += size
			}
		}
		offset += size
	}
	return changed, total
}

// runeIndexByByte строит перевод байтового смещения в номер руны. Каждому
// байту руны присвоен номер самой руны, а длине текста соответствует их число.
func runeIndexByByte(text string) []int {
	idx := make([]int, len(text)+1)
	n := 0
	for i, r := range text {
		size := utf8.RuneLen(r)
		for j := i; j < i+size; j++ {
			idx[j] = n
		}
		n++
	}
	idx[len(text)] = n
	return idx
}

// alignRunes сопоставляет руны исходного текста рунам ответа по наибольшей
// общей подпоследовательности. Для каждой руны исходного текста возвращается
// номер совпавшей руны ответа либо минус единица, если руна не уцелела.
func alignRunes(orig, masked []rune) []int {
	n, m := len(orig), len(masked)
	width := m + 1
	table := make([]int32, (n+1)*width)
	for i := n - 1; i >= 0; i-- {
		row := i * width
		next := row + width
		for j := m - 1; j >= 0; j-- {
			if orig[i] == masked[j] {
				table[row+j] = table[next+j+1] + 1
				continue
			}
			table[row+j] = max(table[next+j], table[row+j+1])
		}
	}
	return backtrackAlign(orig, masked, table, width)
}

// backtrackAlign проходит таблицу сопоставления и выписывает пары совпавших
// рун. Вынесено отдельно, чтобы обе части остались простыми.
func backtrackAlign(orig, masked []rune, table []int32, width int) []int {
	align := make([]int, len(orig))
	for i := range align {
		align[i] = -1
	}
	i, j := 0, 0
	for i < len(orig) && j < len(masked) {
		switch {
		case orig[i] == masked[j]:
			align[i] = j
			i++
			j++
		case table[(i+1)*width+j] >= table[i*width+j+1]:
			i++
		default:
			j++
		}
	}
	return align
}

// alignedView вырезает из ответа то, что стоит на месте эталонного фрагмента.
// Границы берутся по ближайшим уцелевшим рунам слева и справа от фрагмента:
// сам фрагмент мог быть заменён целиком и опорных точек внутри не осталось.
func alignedView(align []int, masked []rune, startRune, endRune int) string {
	lo := 0
	for i := startRune - 1; i >= 0; i-- {
		if align[i] >= 0 {
			lo = align[i] + 1
			break
		}
	}
	hi := len(masked)
	for i := endRune; i < len(align); i++ {
		if align[i] >= 0 {
			hi = align[i]
			break
		}
	}
	if hi < lo {
		hi = lo
	}
	return string(masked[lo:hi])
}
