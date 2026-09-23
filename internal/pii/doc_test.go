package pii

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// checkDocTokens сверяет классы разобранных токенов. Класс решает, где
// детекторы видят границу слова, поэтому сверяется каждый токен, а не только
// их число: одна подменённая граница тихо меняет находки всего движка.
func checkDocTokens(t *testing.T, d *Doc, wantKinds []Kind) {
	t.Helper()
	if len(d.Tokens) != len(wantKinds) {
		t.Fatalf("токенов %d, ожидалось %d: %+v", len(d.Tokens), len(wantKinds), d.Tokens)
	}
	for i, tok := range d.Tokens {
		if tok.Kind != wantKinds[i] {
			t.Errorf("токен %d: класс %d, ожидался %d", i, tok.Kind, wantKinds[i])
		}
	}
}

// checkDocReparsed сверяет переиспользованный документ с эталонным разбором.
// Сверяются все поля сразу: недосброшенным может остаться любое из них, а
// цикл переразборов отвечает только за повторы.
func checkDocReparsed(t *testing.T, d, want *Doc) {
	t.Helper()
	if d.Text != want.Text {
		t.Fatalf("после переиспользования Text = %q, ожидалось %q", d.Text, want.Text)
	}
	if d.Lower != want.Lower {
		t.Fatalf("после переиспользования Lower = %q, ожидалось %q", d.Lower, want.Lower)
	}
	if len(d.Tokens) != len(want.Tokens) {
		t.Fatalf("после переиспользования токенов %d, ожидалось %d", len(d.Tokens), len(want.Tokens))
	}
	for j := range want.Tokens {
		if d.Tokens[j] != want.Tokens[j] {
			t.Fatalf("токен %d после переиспользования = %+v, ожидался %+v", j, d.Tokens[j], want.Tokens[j])
		}
	}
	if d.RuneLen() != want.RuneLen() {
		t.Fatalf("после переиспользования RuneLen = %d, ожидалось %d", d.RuneLen(), want.RuneLen())
	}
	if got, wantRuns := d.NumRuns(), want.NumRuns(); len(got) != len(wantRuns) {
		t.Fatalf("после переиспользования числовых последовательностей %d, ожидалось %d", len(got), len(wantRuns))
	}
}

// TestNewDocBasics проверяет разбор текста на разных алфавитах. Doc строится
// один раз на запрос и дальше только читается, поэтому ошибка разбора
// отражается сразу на всех детекторах.
func TestNewDocBasics(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantLower string
		wantRunes int
		wantKinds []Kind
	}{
		{
			name:      "пустая строка",
			text:      "",
			wantLower: "",
			wantRunes: 0,
			wantKinds: nil,
		},
		{
			name:      "только кириллица",
			text:      "Привет",
			wantLower: "привет",
			wantRunes: 6,
			wantKinds: []Kind{KindCyr},
		},
		{
			name:      "кириллица с буквой ё",
			text:      "ЁЖИК",
			wantLower: "ёжик",
			wantRunes: 4,
			wantKinds: []Kind{KindCyr},
		},
		{
			name:      "смесь кириллицы, латиницы и цифр",
			text:      "Привет World 42",
			wantLower: "привет world 42",
			wantRunes: 15,
			wantKinds: []Kind{KindCyr, KindSpace, KindLat, KindSpace, KindDigit},
		},
		{
			name:      "эмодзи относится к знакам",
			text:      "привет 😀 мир",
			wantLower: "привет 😀 мир",
			wantRunes: 12,
			wantKinds: []Kind{KindCyr, KindSpace, KindPunct, KindSpace, KindCyr},
		},
		{
			name:      "знаки препинания отдельным токеном",
			text:      "инн: 7707083893",
			wantLower: "инн: 7707083893",
			wantRunes: 15,
			wantKinds: []Kind{KindCyr, KindPunct, KindSpace, KindDigit},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := NewDoc(c.text)
			if d.Text != c.text {
				t.Fatalf("Text изменился: %q", d.Text)
			}
			if d.Lower != c.wantLower {
				t.Fatalf("Lower = %q, ожидалось %q", d.Lower, c.wantLower)
			}
			if got := d.RuneLen(); got != c.wantRunes {
				t.Fatalf("RuneLen = %d, ожидалось %d", got, c.wantRunes)
			}
			checkDocTokens(t, d, c.wantKinds)
		})
	}
}

// TestDocParseReuse проверяет, что повторный разбор через Parse даёт тот же
// результат, что и свежий NewDoc. Пул переиспользует документ между запросами,
// и ошибка сброса привела бы к тихому искажению данных соседнего запроса —
// худшему виду сбоя, который не падает, а отдаёт чужое.
func TestDocParseReuse(t *testing.T) {
	texts := []string{
		"",
		"Иванов Иван Иванович, паспорт 4509 123456",
		"тел. +7 916 123-45-67, ИНН 500100732259",
		"эмодзи 😀 и знаки «»№, Türkçe ÄÖÜ ß",
	}
	for _, text := range texts {
		want := NewDoc(text)
		d := &Doc{}
		for i := 0; i < 3; i++ {
			d.Parse(text)
			checkDocReparsed(t, d, want)
		}
	}
}

// TestDocLowerSameByteLength проверяет главное условие движка: Lower побайтово
// совпадает по длине с Text. Иначе смещения, найденные по нижнему регистру,
// указывали бы в исходном тексте не туда.
func TestDocLowerSameByteLength(t *testing.T) {
	texts := []string{
		"",
		"Иванов Иван Иванович",
		"ЁЛКА ёлка Ёлка",
		"MiXeD Case Латиница",
		"Паспорт 4509 123456, ИНН 7707083893",
		"эмодзи 😀 и знаки «»№",
		"Türkçe ÄÖÜ ß İstanbul",
		"ΑΒΓΔ греческие ΣΤ",
	}
	for _, text := range texts {
		t.Run(text, func(t *testing.T) {
			d := NewDoc(text)
			if len(d.Text) != len(d.Lower) {
				t.Fatalf("длины расходятся: Text %d байт, Lower %d байт", len(d.Text), len(d.Lower))
			}
		})
	}
}

// TestDocTokensCoverText проверяет, что токены идут подряд и покрывают текст
// целиком: детекторы ходят по токенам и не должны терять куски текста.
func TestDocTokensCoverText(t *testing.T) {
	text := "Паспорт 45 09 123456, выдан 12.03.2015 ОВД «Хамовники»"
	d := NewDoc(text)
	prev := 0
	for i, tok := range d.Tokens {
		if tok.Start != prev {
			t.Fatalf("токен %d начинается на %d, ожидалось %d", i, tok.Start, prev)
		}
		if tok.End <= tok.Start {
			t.Fatalf("токен %d пуст: %d:%d", i, tok.Start, tok.End)
		}
		prev = tok.End
	}
	if prev != len(text) {
		t.Fatalf("токены покрыли %d байт из %d", prev, len(text))
	}
}

// TestWindowRunesCountsRunes проверяет, что окно считается в рунах. На
// кириллице байтовое окно вдвое короче задуманного, и якорь не достаёт до
// значения, поэтому подмена рун байтами ломает все детекторы разом.
func TestWindowRunesCountsRunes(t *testing.T) {
	text := "абвгде жзиклм нопрст"
	d := NewDoc(text)
	start := strings.Index(text, "жзиклм")
	end := start + len("жзиклм")

	lo, hi := d.WindowRunes(start, end, 4, 2)
	got := text[lo:hi]
	if want := "где жзиклм н"; got != want {
		t.Fatalf("окно = %q, ожидалось %q", got, want)
	}
	if n := utf8.RuneCountInString(text[lo:start]); n != 4 {
		t.Fatalf("слева захвачено %d рун, ожидалось 4", n)
	}
	if n := utf8.RuneCountInString(text[end:hi]); n != 2 {
		t.Fatalf("справа захвачено %d рун, ожидалось 2", n)
	}
}

// TestWindowRunesEdges проверяет, что окно не выходит за границы текста.
func TestWindowRunesEdges(t *testing.T) {
	text := "абвгд"
	d := NewDoc(text)
	cases := []struct {
		name           string
		start, end     int
		before, after  int
		wantLo, wantHi int
	}{
		{"окно шире текста", 4, 6, 100, 100, 0, len(text)},
		{"нулевое окно", 4, 6, 0, 0, 4, 6},
		{"начало текста", 0, 2, 5, 0, 0, 2},
		{"конец текста", len(text) - 2, len(text), 0, 5, len(text) - 2, len(text)},
		{"отрицательное начало", -5, 2, 0, 0, 0, 2},
		{"конец за пределами текста", 0, len(text) + 10, 0, 0, 0, len(text)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lo, hi := d.WindowRunes(c.start, c.end, c.before, c.after)
			if lo != c.wantLo || hi != c.wantHi {
				t.Fatalf("WindowRunes = %d:%d, ожидалось %d:%d", lo, hi, c.wantLo, c.wantHi)
			}
			if !utf8.ValidString(text[lo:hi]) {
				t.Fatalf("окно %d:%d разрезало руну", lo, hi)
			}
		})
	}
}

// TestLowerWindowAndFindAnchor проверяет, что окно отдаётся в нижнем регистре и
// что якорь ищется в обе стороны от значения.
func TestLowerWindowAndFindAnchor(t *testing.T) {
	text := "ПАСПОРТ 4509 123456 ВЫДАН ОВД"
	d := NewDoc(text)
	start := strings.Index(text, "4509")
	end := start + len("4509 123456")

	if got := d.LowerWindow(start, end, 8, 6); !strings.Contains(got, "паспорт") {
		t.Fatalf("окно %q не содержит якоря в нижнем регистре", got)
	}
	if a, ok := d.FindAnchor(start, end, []string{"паспорт"}, 10, 0); !ok || a != "паспорт" {
		t.Fatalf("якорь слева не найден: %q %v", a, ok)
	}
	if a, ok := d.FindAnchor(start, end, []string{"выдан"}, 0, 10); !ok || a != "выдан" {
		t.Fatalf("якорь справа не найден: %q %v", a, ok)
	}
	if _, ok := d.FindAnchor(start, end, []string{"снилс"}, 40, 40); ok {
		t.Fatal("найден якорь, которого в тексте нет")
	}
	if a, ok := d.FindAnchor(start, end, []string{"порт", "паспорт"}, 10, 0); !ok || a != "паспорт" {
		t.Fatalf("выбран короткий якорь %q вместо длинного", a)
	}
	if _, ok := d.HasAnchorBefore(start, []string{"выдан"}, 40); ok {
		t.Fatal("якорь справа засчитан как якорь слева")
	}
}

// TestLineBounds проверяет границы строки. Фрагмент персональных данных не
// должен пересекать перевод строки, иначе маска затирает чужой текст.
func TestLineBounds(t *testing.T) {
	text := "первая\nвторая\nтретья"
	d := NewDoc(text)
	cases := []struct {
		name string
		off  int
		want string
	}{
		{"начало текста", 0, "первая"},
		{"середина первой строки", 5, "первая"},
		{"сам перевод строки после первой", 12, "первая"},
		{"начало второй строки", 13, "вторая"},
		{"середина второй строки", 14, "вторая"},
		{"конец второй строки", 25, "вторая"},
		{"последняя строка", 27, "третья"},
		{"конец текста", len(text), "третья"},
		{"отрицательное смещение", -5, "первая"},
		{"смещение за пределами текста", len(text) + 100, "третья"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lo, hi := d.LineBounds(c.off)
			if got := text[lo:hi]; got != c.want {
				t.Fatalf("LineBounds(%d) = %q, ожидалось %q", c.off, got, c.want)
			}
		})
	}
}

// TestLineBoundsSingleLine проверяет текст без переводов строки.
func TestLineBoundsSingleLine(t *testing.T) {
	text := "одна строка без переводов"
	d := NewDoc(text)
	lo, hi := d.LineBounds(7)
	if lo != 0 || hi != len(text) {
		t.Fatalf("LineBounds = %d:%d, ожидалось 0:%d", lo, hi, len(text))
	}
}

// TestTokenIndexAt проверяет поиск токена по смещению, включая края текста.
func TestTokenIndexAt(t *testing.T) {
	text := "инн 7707083893"
	d := NewDoc(text)
	numStart := strings.Index(text, "7707083893")
	cases := []struct {
		name string
		off  int
		want int
	}{
		{"первый байт текста", 0, 0},
		{"последний байт первого токена", 5, 0},
		{"пробел между словами", 6, 1},
		{"первый байт числа", numStart, 2},
		{"последний байт числа", len(text) - 1, 2},
		{"конец текста", len(text), -1},
		{"смещение за пределами текста", len(text) + 10, -1},
		{"отрицательное смещение", -1, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := d.TokenIndexAt(c.off); got != c.want {
				t.Fatalf("TokenIndexAt(%d) = %d, ожидалось %d", c.off, got, c.want)
			}
		})
	}
}

// TestTokenIndexAtEmpty проверяет, что пустой документ не приводит к панике.
func TestTokenIndexAtEmpty(t *testing.T) {
	d := NewDoc("")
	if got := d.TokenIndexAt(0); got != -1 {
		t.Fatalf("TokenIndexAt на пустом документе = %d, ожидалось -1", got)
	}
	if got := d.RuneLen(); got != 0 {
		t.Fatalf("RuneLen на пустом документе = %d, ожидалось 0", got)
	}
}

// TestIsTokenBoundary проверяет, что фрагмент начинается и заканчивается на
// границе токена. Это защищает от якоря, попавшего внутрь чужого слова.
func TestIsTokenBoundary(t *testing.T) {
	text := "инн 7707083893 ок"
	d := NewDoc(text)
	numStart := strings.Index(text, "7707083893")
	numEnd := numStart + len("7707083893")
	cases := []struct {
		name       string
		start, end int
		want       bool
	}{
		{"ровно по числу", numStart, numEnd, true},
		{"начало внутри числа", numStart + 1, numEnd, false},
		{"конец внутри числа", numStart, numEnd - 1, false},
		{"весь текст", 0, len(text), true},
		{"первое слово", 0, 6, true},
		{"перевёрнутые границы", numEnd, numStart, false},
		{"конец за пределами текста", numStart, len(text) + 5, false},
		{"отрицательное начало", -1, numEnd, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := d.IsTokenBoundary(c.start, c.end); got != c.want {
				t.Fatalf("IsTokenBoundary(%d, %d) = %v, ожидалось %v", c.start, c.end, got, c.want)
			}
		})
	}
}
