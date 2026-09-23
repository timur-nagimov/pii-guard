package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pii-guard/internal/mask"
)

// TestLoadReadsSamples проверяет разбор набора: строки читаются, битые строки
// пропускаются, фильтр по категории и предел работают.
func TestLoadReadsSamples(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "set.jsonl")
	lines := []string{
		`{"id":"a","category":"анкета","text":"Иванов Иван","spans":[{"start":0,"end":21,"type":"FIO"}]}`,
		`не json`,
		`{"id":"b","category":"договор","text":"тел. +79991234567","spans":[{"start":5,"end":18,"type":"PHONE"}]}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	all, err := load(path, "", 0)
	if err != nil {
		t.Fatalf("чтение не удалось: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("прочитано элементов %d, ожидалось 2", len(all))
	}

	only, err := load(path, "анкета", 0)
	if err != nil {
		t.Fatalf("чтение с фильтром не удалось: %v", err)
	}
	if len(only) != 1 || only[0].ID != "a" {
		t.Fatalf("фильтр по категории неверен: %+v", only)
	}

	limited, err := load(path, "", 1)
	if err != nil {
		t.Fatalf("чтение с пределом не удалось: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("предел не сработал: %d элементов", len(limited))
	}
}

// TestLoadMissingFile проверяет, что отсутствующий набор даёт ошибку.
func TestLoadMissingFile(t *testing.T) {
	if _, err := load(filepath.Join(t.TempDir(), "нет.jsonl"), "", 0); err == nil {
		t.Fatal("ожидалась ошибка чтения отсутствующего набора")
	}
}

// TestChangedRatio проверяет долю изменённых рун внутри фрагмента.
func TestChangedRatio(t *testing.T) {
	orig := []rune("Иванов Иван")
	mask := []rune("****** Иван")
	if got := changedRatio(orig, mask, 0, 6); got != 1 {
		t.Fatalf("полностью изменённый фрагмент: %v", got)
	}
	if got := changedRatio(orig, mask, 7, 11); got != 0 {
		t.Fatalf("нетронутый фрагмент: %v", got)
	}
	if got := changedRatio(orig, mask, 0, 0); got != 0 {
		t.Fatalf("пустой фрагмент: %v", got)
	}
	if got := changedRatio(orig, mask, -1, 3); got != 0 {
		t.Fatalf("отрицательное начало: %v", got)
	}
	if got := changedRatio(orig, mask, 0, 99); got != 0 {
		t.Fatalf("конец за текстом: %v", got)
	}
	// Разная длина рун означает полную замену.
	if got := changedRatio([]rune("абв"), []rune("аб"), 0, 3); got != 1 {
		t.Fatalf("разная длина: %v", got)
	}
}

// TestOutsideChanges проверяет подсчёт изменённых рун вне эталона.
func TestOutsideChanges(t *testing.T) {
	orig := []rune("Иванов Иван")
	mask := []rune("****** Иван")
	inside := []bool{true, true, true, true, true, true, false, false, false, false, false}
	changed, total := outsideChanges(orig, mask, inside)
	if changed != 0 || total != 5 {
		t.Fatalf("вне эталона изменено %d из %d, ожидалось 0 из 5", changed, total)
	}
	// Разная длина: всё считается изменённым.
	changed, total = outsideChanges([]rune("абв"), []rune("аб"), []bool{false, false, false})
	if changed != 0 || total != 3 {
		t.Fatalf("разная длина: изменено %d из %d", changed, total)
	}
}

// TestRuneIndex проверяет перевод байтовых смещений в номера рун.
func TestRuneIndex(t *testing.T) {
	text := "Иванов Иван"
	idx := runeIndex(text)
	// «И» занимает два байта, поэтому байт 2 — начало второй руны.
	if idx[2] != 1 {
		t.Fatalf("смещение 2 должно быть руной 1, получено %d", idx[2])
	}
	if idx[len(text)] != 11 {
		t.Fatalf("конец текста должен быть руной 11, получено %d", idx[len(text)])
	}
}

// TestScoreSampleCounts проверяет, что обработка элемента копит показатели по
// типу, категории и источнику и что ложные срабатывания вне эталона считаются.
func TestScoreSampleCounts(t *testing.T) {
	eng, sys, defs := buildEngine(0.5, mask.PresetFull)
	s := sample{
		ID: "a", Category: "анкета", Source: "gen",
		Text: "Иванов Иван, тел. +79991234567",
		Spans: []goldSpan{
			{Start: 0, End: 21, Type: "FIO"},
			{Start: 31, End: 43, Type: "PHONE"},
		},
	}
	byType := map[string]*stat{}
	byCategory := map[string]*stat{}
	bySource := map[string]*stat{}
	shown := scoreSample(s, eng, sys, defs, false, 0, "", 0, byType, byCategory, bySource)

	if shown != 0 {
		t.Fatalf("примеров напечатано %d, ожидалось 0", shown)
	}
	if byType["FIO"].fragments != 1 || byType["PHONE"].fragments != 1 {
		t.Fatalf("фрагменты по типам не посчитаны: %+v", byType)
	}
	if byCategory["анкета"].fragments != 2 {
		t.Fatalf("фрагменты по категории не посчитаны: %+v", byCategory)
	}
	if bySource["gen"].fragments != 2 {
		t.Fatalf("фрагменты по источнику не посчитаны: %+v", bySource)
	}
	// Вне эталона изменений быть не должно: маска сохраняет длину.
	if byCategory["анкета"].extra != 0 {
		t.Fatalf("ложных срабатываний %d, ожидалось 0", byCategory["анкета"].extra)
	}
}

// TestScoreSampleLower проверяет, что ключ -lower приводит текст к нижнему
// регистру перед замером.
func TestScoreSampleLower(t *testing.T) {
	eng, sys, defs := buildEngine(0.5, mask.PresetFull)
	s := sample{
		ID: "a", Category: "анкета", Source: "gen",
		Text:  "ИВАНОВ ИВАН",
		Spans: []goldSpan{{Start: 0, End: 21, Type: "FIO"}},
	}
	byType := map[string]*stat{}
	byCategory := map[string]*stat{}
	bySource := map[string]*stat{}
	scoreSample(s, eng, sys, defs, true, 0, "", 0, byType, byCategory, bySource)
	if byType["FIO"].fragments != 1 {
		t.Fatalf("фрагмент не посчитан: %+v", byType)
	}
}

// TestReportPrintsSlices проверяет, что отчёт печатает срезы с колонкой ложных
// срабатываний и что неотрицательный срез с ложными срабатываниями виден.
func TestReportPrintsSlices(t *testing.T) {
	m := map[string]*stat{
		"FIO": {fragments: 2, changed: 2, touched: 2, full: 2, extra: 3, outside: 100},
	}
	out := captureStdout(t, func() { report("Типы", m, "", false) })
	for _, want := range []string{"Типы", "срез", "изменено", "ложных", "FIO"} {
		if !strings.Contains(out, want) {
			t.Fatalf("в отчёте нет %q:\n%s", want, out)
		}
	}
	// Доля ложных срабатываний 3 из 100 обязана быть в выводе.
	if !strings.Contains(out, "3.00%") {
		t.Fatalf("доля ложных срабатываний не напечатана:\n%s", out)
	}
}

// captureStdout перехватывает вывод в стандартный поток на время вызова.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	os.Stdout = old
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			break
		}
	}
	_ = r.Close()
	return string(buf)
}

// TestSetLabel проверяет превращение пути в короткое имя набора.
func TestSetLabel(t *testing.T) {
	if got := setLabel("corpus/dataset.jsonl"); got != "dataset" {
		t.Fatalf("метка набора: %q", got)
	}
	if got := setLabel("a/b/c.jsonl"); got != "c" {
		t.Fatalf("метка набора: %q", got)
	}
}

// TestLowerLabel проверяет подпись нижнего регистра.
func TestLowerLabel(t *testing.T) {
	if got := lowerLabel(true); got == "" {
		t.Fatal("для нижнего регистра подпись не должна быть пустой")
	}
	if got := lowerLabel(false); got != "" {
		t.Fatalf("без нижнего регистра подпись должна быть пустой, получено %q", got)
	}
}

// TestAvg проверяет среднюю долю изменённого.
func TestAvg(t *testing.T) {
	if got := avg(&stat{}); got != 0 {
		t.Fatalf("пустая статистика: %v", got)
	}
	if got := avg(&stat{fragments: 2, changed: 1}); got != 0.5 {
		t.Fatalf("средняя доля: %v", got)
	}
}

// TestCut проверяет обрезку длинного текста для примера.
func TestCut(t *testing.T) {
	long := strings.Repeat("а", 200)
	if got := cut(long); len([]rune(got)) != 151 {
		t.Fatalf("длинный текст не обрезан: %d рун", len([]rune(got)))
	}
	if got := cut("коротко"); got != "коротко" {
		t.Fatalf("короткий текст изменён: %q", got)
	}
	if got := cut("а\nб"); got != "а б" {
		t.Fatalf("перевод строки не заменён: %q", got)
	}
}

// TestScoreSpanExample проверяет печать примера пропуска при слабой маске.
func TestScoreSpanExample(t *testing.T) {
	eng, sys, defs := buildEngine(0.5, mask.PresetFull)
	s := sample{
		ID: "a", Category: "анкета", Source: "gen",
		Text: "обычный текст без данных",
		// Тип ORDER детектор не знает, поэтому фрагмент не маскируется и
		// попадает в пример пропуска.
		Spans: []goldSpan{{Start: 0, End: 45, Type: "ORDER"}},
	}
	byType := map[string]*stat{}
	byCategory := map[string]*stat{}
	bySource := map[string]*stat{}
	// examples=1 и onlyType=ORDER: пример должен напечататься.
	out := captureStdout(t, func() {
		scoreSample(s, eng, sys, defs, false, 1, "ORDER", 0, byType, byCategory, bySource)
	})
	if !strings.Contains(out, "ПРОПУСК") {
		t.Fatalf("пример пропуска не напечатан:\n%s", out)
	}
}

// TestScoreSpanBadBounds проверяет, что фрагмент за пределами текста не
// считается и не роняет обработку.
func TestScoreSpanBadBounds(t *testing.T) {
	eng, sys, defs := buildEngine(0.5, mask.PresetFull)
	s := sample{
		ID: "a", Category: "анкета", Source: "gen",
		Text:  "Иванов Иван",
		Spans: []goldSpan{{Start: 0, End: 500, Type: "FIO"}},
	}
	byType := map[string]*stat{}
	byCategory := map[string]*stat{}
	bySource := map[string]*stat{}
	shown := scoreSample(s, eng, sys, defs, false, 0, "", 0, byType, byCategory, bySource)
	if shown != 0 {
		t.Fatalf("примеров напечатано %d", shown)
	}
	if byType["FIO"] != nil {
		t.Fatalf("фрагмент за пределами текста посчитан: %+v", byType["FIO"])
	}
}

// TestRunDatasets проверяет прогон по нескольким наборам: общая таблица
// тип×набор и итог по наборам печатаются, битый набор не роняет прогон.
func TestRunDatasets(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "a.jsonl")
	if err := os.WriteFile(good, []byte(`{"id":"a","category":"анкета","source":"gen","text":"Иванов Иван","spans":[{"start":0,"end":21,"type":"FIO"}]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "нет.jsonl")

	out := captureStdout(t, func() {
		runDatasets(good+","+missing, 0.5, mask.PresetFull, false, false)
	})
	for _, want := range []string{"Качество по типам и наборам", "Итог по наборам", "a", "FIO"} {
		if !strings.Contains(out, want) {
			t.Fatalf("в выводе нет %q:\n%s", want, out)
		}
	}
}

// TestRunDatasetsLowerSplit проверяет ключи -lower и -split в режиме наборов.
func TestRunDatasetsLowerSplit(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "a.jsonl")
	if err := os.WriteFile(good, []byte(`{"id":"a","category":"анкета","source":"gen","text":"Иванов Иван","spans":[{"start":0,"end":21,"type":"FIO"}]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		runDatasets(good, 0.5, mask.PresetFull, true, true)
	})
	if !strings.Contains(out, "нижний регистр") {
		t.Fatalf("подпись нижнего регистра не напечатана:\n%s", out)
	}
	if !strings.Contains(out, "[задание]") {
		t.Fatalf("пометка типа задания не напечатана:\n%s", out)
	}
}

// TestScoreDatasetSample проверяет подсчёт показателей по паре тип×набор.
func TestScoreDatasetSample(t *testing.T) {
	eng, sys, defs := buildEngine(0.5, mask.PresetFull)
	s := sample{
		ID: "a", Category: "анкета", Source: "gen",
		Text:  "Иванов Иван",
		Spans: []goldSpan{{Start: 0, End: 21, Type: "FIO"}},
	}
	byType := map[string]map[string]*stat{}
	bySet := map[string]*stat{}
	scoreDatasetSample(s, eng, sys, defs, false, "набор", byType, bySet)
	if byType["FIO"]["набор"].fragments != 1 {
		t.Fatalf("фрагмент по паре тип×набор не посчитан: %+v", byType)
	}
	if bySet["набор"].fragments != 1 {
		t.Fatalf("итог по набору не посчитан: %+v", bySet)
	}
}

// TestEnsureType проверяет создание промежуточных карт по мере надобности.
func TestEnsureType(t *testing.T) {
	m := map[string]map[string]*stat{}
	st := ensureType(m, "FIO", "набор")
	if st == nil {
		t.Fatal("статистика не создана")
	}
	if m["FIO"] == nil || m["FIO"]["набор"] != st {
		t.Fatal("промежуточные карты не заполнены")
	}
	// Повторный вызов возвращает ту же статистику.
	if ensureType(m, "FIO", "набор") != st {
		t.Fatal("повторный вызов дал другую статистику")
	}
}

// TestSortedKeys проверяет сортировку ключей статистики.
func TestSortedKeys(t *testing.T) {
	m := map[string]*stat{"b": {}, "a": {}, "c": {}}
	got := sortedKeys(m)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("сортировка неверна: %v", got)
	}
}

// TestSortedTypeKeys проверяет сортировку ключей карты тип×набор.
func TestSortedTypeKeys(t *testing.T) {
	m := map[string]map[string]*stat{"B": {}, "A": {}}
	got := sortedTypeKeys(m)
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("сортировка неверна: %v", got)
	}
}

// TestEnsureEmptyCategory проверяет, что пустая категория получает имя.
func TestEnsureEmptyCategory(t *testing.T) {
	m := map[string]*stat{}
	st := ensure(m, "")
	if m["(без категории)"] != st {
		t.Fatal("пустая категория не получила имя")
	}
}
