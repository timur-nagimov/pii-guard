package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// carriers возвращает носители для проверок: с кириллицей, с числами, без
// пробелов и с уже размеченным фрагментом.
func carriers() []Record {
	return []Record{
		{ID: "a", Source: sourceWikipedia, Text: "Пётр Ильич родился в 1840 году в Воткинске. Учился в Петербурге.", Spans: []Span{}, PartialLabels: true},
		{ID: "b", Source: sourceReviews, Text: "Очень долго ждал ответа, 45 минут в очереди! Больше сюда не приду.", Spans: []Span{}, PartialLabels: true},
		{ID: "c", Source: sourceReviews, Text: "Однословно", Spans: []Span{}, PartialLabels: true},
		{ID: "d", Source: sourceHFRuPII, Text: "Клиент Иванов Иван обратился повторно и получил отказ банка.",
			Spans: []Span{{Start: 13, End: 34, Type: "FIO"}}, PartialLabels: true},
	}
}

// TestInjectSpansSliceToValues проверяет главное свойство вставки: границы
// записаны в байтах и срез по ним даёт ровно вставленное значение.
func TestInjectSpansSliceToValues(t *testing.T) {
	for seed := uint64(0); seed < 60; seed++ {
		m := newMaker(seed)
		for _, c := range carriers() {
			rec, err := injectInto(m, c, "wiki_injected", 3)
			if err != nil {
				t.Fatalf("seed %d, носитель %s: %v", seed, c.ID, err)
			}
			if reason := validate(rec); reason != "" {
				t.Fatalf("seed %d, носитель %s: элемент забракован: %s", seed, c.ID, reason)
			}
			if !rec.PartialLabels {
				t.Fatalf("seed %d: признак частичной разметки потерян", seed)
			}
			if len(rec.Spans) == 0 {
				t.Fatalf("seed %d, носитель %s: ничего не вставлено", seed, c.ID)
			}
			checkSpansSorted(t, rec)
		}
	}
}

// checkSpansSorted проверяет, что фрагменты идут по возрастанию, не
// пересекаются и вырезаются как правильный UTF-8.
func checkSpansSorted(t *testing.T, rec Record) {
	t.Helper()
	prevEnd := 0
	for _, s := range rec.Spans {
		if s.Start < prevEnd {
			t.Fatalf("%s: фрагменты пересекаются на %d", rec.ID, s.Start)
		}
		got := rec.Text[s.Start:s.End]
		if !utf8.ValidString(got) {
			t.Fatalf("%s: срез %q не декодируется", rec.ID, got)
		}
		if strings.TrimSpace(got) != got {
			t.Fatalf("%s: срез %q обрамлён пробелами", rec.ID, got)
		}
		prevEnd = s.End
	}
}

// TestInjectKeepsCarrierText проверяет, что исходный текст носителя уцелел:
// вставка только дописывает, но ничего не выбрасывает.
func TestInjectKeepsCarrierText(t *testing.T) {
	m := newMaker(11)
	c := carriers()[0]
	rec, err := injectInto(m, c, "wiki_injected", 2)
	if err != nil {
		t.Fatal(err)
	}
	rest := rec.Text
	for _, word := range strings.Fields(c.Text) {
		idx := strings.Index(rest, word)
		if idx < 0 {
			t.Fatalf("слово %q носителя пропало из текста %q", word, rec.Text)
		}
		rest = rest[idx+len(word):]
	}
}

// TestInjectShiftsCarrierSpans проверяет, что разметка носителя переезжает
// вместе с текстом и продолжает указывать на то же значение.
func TestInjectShiftsCarrierSpans(t *testing.T) {
	c := carriers()[3]
	want := c.Text[c.Spans[0].Start:c.Spans[0].End]
	for seed := uint64(0); seed < 40; seed++ {
		rec, err := injectInto(newMaker(seed), c, "hf_ru_pii_injected", 3)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, s := range rec.Spans {
			if s.Type == "FIO" && rec.Text[s.Start:s.End] == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("seed %d: исходный фрагмент %q потерян в %q", seed, want, rec.Text)
		}
	}
}

// TestInjectDeterministic проверяет, что один seed даёт один и тот же текст.
func TestInjectDeterministic(t *testing.T) {
	c := carriers()[1]
	first, err := injectInto(newMaker(5), c, "informal_injected", 3)
	if err != nil {
		t.Fatal(err)
	}
	second, err := injectInto(newMaker(5), c, "informal_injected", 3)
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != second.Text {
		t.Fatalf("один seed дал разные тексты:\n%s\n%s", first.Text, second.Text)
	}
}

// TestBoundariesAtWordStarts проверяет, что точки вставки стоят на границе
// слова и никогда не разрывают слово или число.
func TestBoundariesAtWordStarts(t *testing.T) {
	text := "Счёт 40817810099910004312 открыт 12.03.2019 в Москве"
	set := boundaries(text, nil)
	for _, at := range set.any {
		if at == len(text) {
			continue
		}
		if !isSpace(text[at-1]) {
			t.Fatalf("точка %d стоит внутри слова: %q", at, text[max(0, at-5):at+5])
		}
	}
	if len(set.strong) != 1 || set.strong[0] != len(text) {
		t.Fatalf("в тексте без знаков препинания предпочтительна только концовка, получено %v", set.strong)
	}
}

// TestBoundariesSkipMarkedFragments проверяет, что вставка не попадает внутрь
// уже размеченного фрагмента носителя.
func TestBoundariesSkipMarkedFragments(t *testing.T) {
	text := "Клиент Иванов Иван Петрович подал заявление"
	marked := []Span{{Start: 13, End: 50, Type: "FIO"}}
	for _, at := range boundaries(text, marked).any {
		if at > marked[0].Start && at < marked[0].End {
			t.Fatalf("точка %d попала внутрь размеченного фрагмента", at)
		}
	}
}

// TestGeneratedChecksums проверяет контрольные суммы порождённых значений:
// номер карты обязан сходиться по алгоритму Луна, ИНН по весам налоговой.
func TestGeneratedChecksums(t *testing.T) {
	m := newMaker(3)
	for i := 0; i < 500; i++ {
		card := strings.NewReplacer(" ", "", "-", "").Replace(m.card())
		if luhn(card[:len(card)-1]) != string(card[len(card)-1]) {
			t.Fatalf("номер карты %q не сходится по алгоритму Луна", card)
		}
		inn := m.inn()
		if !innValid(inn) {
			t.Fatalf("ИНН %q не сходится по весам", inn)
		}
	}
}

// innValid проверяет контрольные разряды ИНН для теста.
func innValid(inn string) bool {
	switch len(inn) {
	case 10:
		return innDigit(inn[:9], innW10) == inn[9:]
	case 12:
		return innDigit(inn[:10], innW11) == inn[10:11] &&
			innDigit(inn[:11], innW12) == inn[11:]
	default:
		return false
	}
}

// TestValidateRejectsBroken проверяет отбраковку испорченных элементов.
func TestValidateRejectsBroken(t *testing.T) {
	base := Record{ID: "x", Source: sourceSynthetic, Text: "Пётр Ильич", Category: "test"}
	cases := []struct {
		name string
		rec  Record
		want string
	}{
		{"без идентификатора", Record{Source: sourceSynthetic, Text: "текст"}, reasonNoID},
		{"без текста", Record{ID: "x", Source: sourceSynthetic}, reasonNoText},
		{"чужой источник", Record{ID: "x", Source: "твиттер", Text: "текст"}, reasonBadSource},
		{"тип вне перечня", withSpan(base, Span{0, 4, "NICKNAME"}), reasonBadType},
		{"границы за текстом", withSpan(base, Span{0, 500, "FIO"}), reasonBadBounds},
		{"разрез буквы", withSpan(base, Span{1, 4, "FIO"}), reasonBadSlice},
		{"годный", withSpan(base, Span{0, 8, "FIO"}), ""},
	}
	for _, c := range cases {
		if got := validate(c.rec); got != c.want {
			t.Errorf("%s: получено %q, ожидалось %q", c.name, got, c.want)
		}
	}
}

// withSpan возвращает копию элемента с одним фрагментом.
func withSpan(r Record, s Span) Record {
	r.Spans = []Span{s}
	return r
}

// TestMergeDropsAndCounts проверяет сборку: повтор идентификатора и битая
// строка отбрасываются, остальное попадает в набор со сводкой по источникам.
func TestMergeDropsAndCounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "in.jsonl")
	lines := []string{
		`{"id":"a","category":"c","source":"wikipedia","text":"Пётр","spans":[{"start":0,"end":8,"type":"FIO"}],"partial_labels":true}`,
		`{"id":"a","category":"c","source":"wikipedia","text":"Пётр","spans":[]}`,
		`{"id":"b","category":"c","source":"","text":"Телефон 89990001122","spans":[{"start":15,"end":26,"type":"PHONE"}]}`,
		`{"id":"c","category":"c","source":"reviews","text":"Плохо","spans":[{"start":0,"end":99,"type":"FIO"}]}`,
		`не json`,
	}
	writeLines(t, path, lines)

	mg := newMerger(sourceSynthetic)
	if err := mg.addFile(path); err != nil {
		t.Fatal(err)
	}
	if len(mg.recs) != 2 {
		t.Fatalf("взято %d элементов, ожидалось 2", len(mg.recs))
	}
	if mg.bySource[sourceSynthetic] != 1 || mg.bySource[sourceWikipedia] != 1 {
		t.Fatalf("сводка по источникам неверна: %v", mg.bySource)
	}
	for _, reason := range []string{reasonDupID, reasonBadBounds, reasonBadLine} {
		if mg.dropped[reason] != 1 {
			t.Fatalf("причина %q посчитана %d раз, ожидался один", reason, mg.dropped[reason])
		}
	}
	if mg.byType["PHONE"] != 1 || mg.byType["FIO"] != 1 {
		t.Fatalf("сводка по типам неверна: %v", mg.byType)
	}
}

// TestMergeShuffleDeterministic проверяет, что один seed даёт один порядок, а
// разные seed дают разный.
func TestMergeShuffleDeterministic(t *testing.T) {
	build := func(seed uint64) []string {
		mg := newMerger(sourceSynthetic)
		for i := 0; i < 40; i++ {
			mg.add(Record{ID: fmt.Sprintf("id-%02d", i), Source: sourceSynthetic, Text: "текст", Spans: []Span{}})
		}
		mg.shuffle(seed)
		ids := make([]string, 0, len(mg.recs))
		for _, r := range mg.recs {
			ids = append(ids, r.ID)
		}
		return ids
	}
	same := strings.Join(build(42), ",")
	if same != strings.Join(build(42), ",") {
		t.Fatal("один seed дал разный порядок элементов")
	}
	if same == strings.Join(build(43), ",") {
		t.Fatal("разные seed дали один и тот же порядок элементов")
	}
}

// TestReadWriteRoundTrip проверяет, что запись и чтение набора не портят
// текст и границы.
func TestReadWriteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	want := []Record{{
		ID: "a", Category: "wiki_injected", Source: sourceWikipedia,
		Text: "Тел. <b>89990001122</b> & адрес", Spans: []Span{{Start: 11, End: 22, Type: "PHONE"}},
		PartialLabels: true,
	}}
	if err := writeRecords(path, want); err != nil {
		t.Fatal(err)
	}
	got, broken, err := readRecords(path)
	if err != nil || broken != 0 {
		t.Fatalf("чтение не удалось: %v, битых строк %d", err, broken)
	}
	if len(got) != 1 || got[0].Text != want[0].Text || got[0].Spans[0] != want[0].Spans[0] {
		t.Fatalf("запись и чтение исказили элемент: %+v", got)
	}
	if got[0].Text[got[0].Spans[0].Start:got[0].Spans[0].End] != "89990001122" {
		t.Fatalf("границы указывают не на телефон: %q", got[0].Text)
	}
}

// writeLines кладёт строки в файл для проверок.
func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := writeFile(path, strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
}
