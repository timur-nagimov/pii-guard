package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLevenshteinRunes проверяет расстояние на парах, где байтовый счёт дал бы
// другой ответ: кириллица, эмодзи, пустые и одинаковые строки.
func TestLevenshteinRunes(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want int
	}{
		{"обе пустые", "", "", 0},
		{"первая пустая", "", "абв", 3},
		{"вторая пустая", "абв", "", 3},
		{"одинаковые латиницей", "kitten", "kitten", 0},
		{"классический пример", "kitten", "sitting", 3},
		{"одинаковые кириллицей", "Иванов", "Иванов", 0},
		{"замена одной кириллической буквы", "Иванов", "Ивинов", 1},
		{"полная замена звёздочками", "Иванов", "******", 6},
		{"удаление буквы", "Иванов", "Иванв", 1},
		{"вставка буквы", "Иванов", "Ивановв", 1},
		{"регистр считается заменой", "иванов", "Иванов", 1},
		{"латиница против кириллицы", "Ivanov", "Иванов", 6},
		{"эмодзи одинаковые", "приветik", "приветik", 0},
		{"эмодзи против пустой", "", "ok", 2},
		{"эмодзи заменён", "ok", "no", 2},
		{"эмодзи добавлен", "да", "да!", 1},
		{"разная длина цифр", "1234", "123456", 2},
		{"маска сохраняет длину", "1234", "****", 4},
		{"частичная маска", "1234", "12**", 2},
		{"пробелы значимы", "а б", "аб", 1},
		{"перестановка", "аб", "ба", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LevenshteinRunes(c.a, c.b); got != c.want {
				t.Fatalf("LevenshteinRunes(%q, %q) = %d, ожидалось %d", c.a, c.b, got, c.want)
			}
			if got := LevenshteinRunes(c.b, c.a); got != c.want {
				t.Fatalf("расстояние несимметрично на паре %q и %q", c.a, c.b)
			}
		})
	}
}

// TestNormalizedLevenshtein проверяет нормировку: единица означает, что внутри
// фрагмента изменено всё, ноль означает, что не изменено ничего.
func TestNormalizedLevenshtein(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want float64
	}{
		{"обе пустые", "", "", 0},
		{"одинаковые", "Иванов", "Иванов", 0},
		{"всё заменено", "Иванов", "******", 1},
		{"значение стёрто", "Иванов", "", 1},
		{"половина заменена", "1234", "12**", 0.5},
		{"четверть заменена", "1234", "123*", 0.25},
		{"эмодзи стёрт", "ok", "*k", 0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizedLevenshtein(c.a, c.b)
			if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("NormalizedLevenshtein(%q, %q) = %f, ожидалось %f", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestStreakCounter проверяет правило остановки: пять невалидных ответов
// подряд останавливают прогон, успешный ответ сбрасывает серию.
func TestStreakCounter(t *testing.T) {
	cases := []struct {
		name     string
		sequence []outcomeKind
		wantStop bool
		wantMax  int
	}{
		{"без ответов", nil, false, 0},
		{"четыре невалидных", repeat(outcomeInvalid, 4), false, 4},
		{"пять невалидных", repeat(outcomeInvalid, 5), true, 5},
		{"шесть невалидных", repeat(outcomeInvalid, 6), true, 6},
		{
			name:     "успех сбрасывает серию",
			sequence: []outcomeKind{outcomeInvalid, outcomeInvalid, outcomeInvalid, outcomeInvalid, outcomeOK, outcomeInvalid, outcomeInvalid},
			wantStop: false,
			wantMax:  4,
		},
		{
			name:     "серия набирается после сброса",
			sequence: []outcomeKind{outcomeInvalid, outcomeOK, outcomeInvalid, outcomeInvalid, outcomeInvalid, outcomeInvalid, outcomeInvalid},
			wantStop: true,
			wantMax:  5,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			counter := newStreakCounter(invalidStreakLimit)
			stop := false
			for _, kind := range c.sequence {
				stop = counter.observe(kind)
			}
			if stop != c.wantStop || counter.stopped() != c.wantStop {
				t.Fatalf("остановка = %v, ожидалось %v", stop, c.wantStop)
			}
			if counter.longestStreak() != c.wantMax {
				t.Fatalf("максимальная серия = %d, ожидалось %d", counter.longestStreak(), c.wantMax)
			}
		})
	}
}

// TestStreakCounterThrottleNotInvalid проверяет, что код 429 невалидным не
// считается: он не наращивает серию и не сбрасывает её.
func TestStreakCounterThrottleNotInvalid(t *testing.T) {
	counter := newStreakCounter(invalidStreakLimit)
	for i := 0; i < 20; i++ {
		if counter.observe(outcomeThrottled) {
			t.Fatal("перегрузка не должна останавливать прогон")
		}
	}
	if counter.longestStreak() != 0 {
		t.Fatalf("перегрузка не должна попадать в серию, получено %d", counter.longestStreak())
	}

	sequence := []outcomeKind{
		outcomeInvalid, outcomeInvalid, outcomeThrottled, outcomeThrottled,
		outcomeInvalid, outcomeInvalid,
	}
	for _, kind := range sequence {
		if counter.observe(kind) {
			t.Fatal("четырёх невалидных ответов недостаточно для остановки")
		}
	}
	if counter.longestStreak() != 4 {
		t.Fatalf("серия через перегрузку должна продолжаться, получено %d", counter.longestStreak())
	}
	if !counter.observe(outcomeInvalid) {
		t.Fatal("пятый невалидный ответ обязан остановить прогон")
	}
}

// TestParseRetryAfter проверяет разбор заголовка в обоих разрешённых видах.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"пустой заголовок", "", 0, false},
		{"пробелы", "   ", 0, false},
		{"ноль секунд", "0", 0, true},
		{"одна секунда", "1", time.Second, true},
		{"секунды с пробелами", " 12 ", 12 * time.Second, true},
		{"отрицательные секунды", "-5", 0, false},
		{"мусор", "потом", 0, false},
		{"дробные секунды не разрешены", "1.5", 0, false},
		{"дата в будущем", "Tue, 22 Sep 2026 12:00:30 GMT", 30 * time.Second, true},
		{"дата в прошлом", "Tue, 22 Sep 2026 11:59:30 GMT", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseRetryAfter(c.value, now)
			if ok != c.ok {
				t.Fatalf("признак разбора = %v, ожидалось %v", ok, c.ok)
			}
			if got != c.want {
				t.Fatalf("пауза = %s, ожидалось %s", got, c.want)
			}
		})
	}
}

// TestRetryAfterDelay проверяет, что пауза не выходит за разумные пределы и
// что без заголовка берётся значение по умолчанию.
func TestRetryAfterDelay(t *testing.T) {
	now := time.Now()
	if got := retryAfterDelay("", now); got != defaultRetryAfter {
		t.Fatalf("без заголовка ожидалась пауза по умолчанию, получено %s", got)
	}
	if got := retryAfterDelay("600", now); got != maxRetryAfter {
		t.Fatalf("длинная пауза должна обрезаться, получено %s", got)
	}
	if got := retryAfterDelay("2", now); got != 2*time.Second {
		t.Fatalf("пауза по заголовку = %s, ожидалось 2s", got)
	}
}

// TestClassifyResponse проверяет отнесение ответа к успеху, отказу и перегрузке.
func TestClassifyResponse(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		status    int
		body      string
		throttled bool
		invalid   bool
		result    string
	}{
		{"успех", http.StatusOK, `{"result":"***"}`, false, false, "***"},
		{"успех с пустым результатом", http.StatusOK, `{"result":""}`, false, false, ""},
		{"нет поля result", http.StatusOK, `{"masked":"***"}`, false, true, ""},
		{"испорченный JSON", http.StatusOK, `{`, false, true, ""},
		{"перегрузка", http.StatusTooManyRequests, ``, true, false, ""},
		{"ошибка проверки", http.StatusUnprocessableEntity, `{"detail":[]}`, false, true, ""},
		{"недоступен", http.StatusServiceUnavailable, ``, false, true, ""},
		{"внутренняя ошибка", http.StatusInternalServerError, ``, false, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at := classifyResponse(c.status, []byte(c.body), "", now)
			if at.Throttled != c.throttled || at.Invalid != c.invalid {
				t.Fatalf("перегрузка = %v, невалидность = %v", at.Throttled, at.Invalid)
			}
			if at.Result != c.result {
				t.Fatalf("результат = %q, ожидалось %q", at.Result, c.result)
			}
		})
	}
}

// TestClientRetriesThenSucceeds проверяет, что клиент делает до двух повторов
// и что перегрузка не тратит попытки впустую.
func TestClientRetriesThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			writeResult(w, "маска")
		}
	}))
	defer srv.Close()

	client := NewClient(srv.URL, time.Second, 2)
	resp := client.Do(context.Background(), "текст", "id-1")
	if resp.Outcome != outcomeOK || resp.Result != "маска" {
		t.Fatalf("исход = %s, результат = %q", resp.Outcome, resp.Result)
	}
	if len(resp.Attempts) != 3 || resp.Throttle != 1 {
		t.Fatalf("попыток %d, перегрузок %d", len(resp.Attempts), resp.Throttle)
	}
}

// TestClientGivesUpAfterThreeAttempts проверяет, что попыток ровно три.
func TestClientGivesUpAfterThreeAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	resp := NewClient(srv.URL, time.Second, 1).Do(context.Background(), "текст", "id-2")
	if resp.Outcome != outcomeInvalid {
		t.Fatalf("исход = %s, ожидался невалидный", resp.Outcome)
	}
	if calls.Load() != 3 {
		t.Fatalf("сделано попыток %d, ожидалось 3", calls.Load())
	}
}

// TestScoreMaskingSameLength проверяет оценку ответа, сохранившего длину.
// Латиница и цифры маскируются звёздочкой байт в байт, поэтому смещения
// эталонных фрагментов остаются верными и сравнение идёт напрямую.
func TestScoreMaskingSameLength(t *testing.T) {
	text := "client Ivanov, card 4276 1234 5678 9012"
	sample := Sample{
		PayloadID: "s1",
		Category:  "анкета",
		Text:      text,
		Fragments: []Fragment{
			{Start: len("client "), End: len("client Ivanov"), Type: "FIO", Value: "Ivanov"},
			{Start: len("client Ivanov, card "), End: len(text), Type: "CARD", Value: "4276 1234 5678 9012"},
		},
	}
	masked := "client " + strings.Repeat("*", 6) + ", card " + strings.Repeat("*", 19)

	sc := ScoreMasking(sample, masked)
	if !sc.Exact {
		t.Fatal("длины совпадают, оценка должна быть точной")
	}
	if len(sc.Fragments) != 2 {
		t.Fatalf("оценено фрагментов %d", len(sc.Fragments))
	}
	for _, f := range sc.Fragments {
		if f.Distance != 1 || !f.Changed {
			t.Fatalf("фрагмент %s изменён не полностью: %f", f.Type, f.Distance)
		}
	}
	if sc.OutsideChangedBytes != 0 {
		t.Fatalf("вне фрагментов изменено %d байт, ожидалось 0", sc.OutsideChangedBytes)
	}
	if sc.OutsideTotalBytes == 0 {
		t.Fatal("вне фрагментов должен быть текст")
	}
}

// TestScoreMaskingUntouched проверяет, что пропуск фрагмента виден в оценке.
func TestScoreMaskingUntouched(t *testing.T) {
	sample := Sample{
		Text:      "телефон +79991234567 записан",
		Fragments: []Fragment{{Start: len("телефон "), End: len("телефон +79991234567"), Type: "PHONE", Value: "+79991234567"}},
	}
	sc := ScoreMasking(sample, sample.Text)
	if sc.Fragments[0].Distance != 0 || sc.Fragments[0].Changed {
		t.Fatalf("нетронутый фрагмент должен давать ноль, получено %f", sc.Fragments[0].Distance)
	}
}

// TestScoreMaskingOutsideNoise проверяет учёт лишних срабатываний вне
// эталонных фрагментов.
func TestScoreMaskingOutsideNoise(t *testing.T) {
	sample := Sample{
		Text:      "заказ 12345 сумма 1000",
		Fragments: []Fragment{{Start: 0, End: len("заказ"), Type: "ORDER", Value: "заказ"}},
	}
	sc := ScoreMasking(sample, "заказ ***** сумма 1000")
	if sc.OutsideChangedBytes != 5 {
		t.Fatalf("вне фрагментов изменено %d байт, ожидалось 5", sc.OutsideChangedBytes)
	}
}

// TestScoreMaskingShifted проверяет оценку ответа, изменившего длину текста.
func TestScoreMaskingShifted(t *testing.T) {
	sample := Sample{
		Text:      "клиент Иванов Иван Иванович, счёт открыт",
		Fragments: []Fragment{{Start: len("клиент "), End: len("клиент Иванов Иван Иванович"), Type: "FIO", Value: "Иванов Иван Иванович"}},
	}
	sc := ScoreMasking(sample, "клиент [FIO_1], счёт открыт")
	if sc.Exact {
		t.Fatal("длины разные, оценка не может быть точной")
	}
	if sc.Fragments[0].Distance < 0.9 {
		t.Fatalf("плейсхолдер должен считаться полной заменой, получено %f", sc.Fragments[0].Distance)
	}
	if sc.OutsideChangedBytes != 0 {
		t.Fatalf("вне фрагмента текст не менялся, получено %d", sc.OutsideChangedBytes)
	}
}

// TestLoadDataset проверяет чтение набора в обоих написаниях и восстановление
// границ фрагмента по значению.
func TestLoadDataset(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "dataset.jsonl")
	content := `{"payload_id":"a1","category":"анкета","text":"Иванов Иван","spans":[{"start":0,"end":21,"type":"FIO"}]}
{"id":"a2","payload":"почта ivan@example.com","fragments":[{"value":"ivan@example.com","label":"EMAIL"}]}
`
	if err := os.WriteFile(jsonl, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	samples, err := LoadDataset(jsonl)
	if err != nil {
		t.Fatalf("набор не прочитан: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("прочитано элементов %d", len(samples))
	}
	if samples[0].Fragments[0].Value != "Иванов Иван" {
		t.Fatalf("значение фрагмента = %q", samples[0].Fragments[0].Value)
	}
	if samples[1].PayloadID != "a2" || samples[1].Category != "без категории" {
		t.Fatalf("поля второго элемента не восстановлены: %+v", samples[1])
	}
	got := samples[1].Fragments[0]
	if got.Type != "EMAIL" || samples[1].Text[got.Start:got.End] != "ivan@example.com" {
		t.Fatalf("границы по значению не найдены: %+v", got)
	}

	arr := filepath.Join(dir, "dataset.json")
	if err := os.WriteFile(arr, []byte(`[{"payload_id":"b1","text":"телефон","spans":[]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDataset(arr); err != nil {
		t.Fatalf("массив JSON не прочитан: %v", err)
	}
}

// TestLoadDatasetRejectsBrokenMarkup проверяет, что разметка за пределами
// текста отбрасывается: такой элемент нельзя честно оценить.
func TestLoadDatasetRejectsBrokenMarkup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(path, []byte(`{"payload_id":"x","text":"кратко","spans":[{"start":0,"end":99,"type":"FIO"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDataset(path); err == nil {
		t.Fatal("ожидалась ошибка разбора разметки")
	}
}

// TestRunnerPairFlow прогоняет имитатор против простого сервиса и проверяет,
// что пара «маска и восстановление» считается целиком.
func TestRunnerPairFlow(t *testing.T) {
	srv := httptest.NewServer(maskingStub())
	defer srv.Close()

	samples := []Sample{{
		PayloadID: "p1",
		Category:  "анкета",
		Text:      "карта 4276123456789012 клиента",
		Fragments: []Fragment{{Start: 6, End: 22, Type: "CARD", Value: "4276123456789012"}},
	}}
	opts := Options{
		URL: srv.URL, RPS: 200, Duration: 300 * time.Millisecond,
		Workers: 4, Timeout: 2 * time.Second,
		DupAfterSuccess: true, ConcurrentDup: true, DemaskRetry: true, DemaskUnknownID: true,
	}
	stats := NewRunner(opts, samples).Run(context.Background())
	rep := stats.BuildReport(opts, datasetReport("тест", samples))

	if rep.Masking.Fragments == 0 {
		t.Fatal("ни одного фрагмента не оценено")
	}
	if rep.Masking.AvgDistance != 1 {
		t.Fatalf("карта должна быть замаскирована целиком, получено %f", rep.Masking.AvgDistance)
	}
	if rep.Demask.ExactShare != 1 {
		t.Fatalf("обратный шаг должен восстанавливать текст, доля %f", rep.Demask.ExactShare)
	}
	// Редкий обрыв соединения при переиспользовании keep-alive допустим:
	// важно, что повторы его закрывают и серия до пяти не доходит.
	if rep.StoppedByStreak || rep.Load.MaxInvalidStreak >= invalidStreakLimit {
		t.Fatalf("исправный сервис не должен давать серию невалидных ответов: %+v", rep.Load)
	}
	if rep.Robustness.DupAfterSuccess.Total != rep.Robustness.DupAfterSuccess.Stable {
		t.Fatal("повтор прямого запроса обязан давать ту же маску")
	}
	if rep.Robustness.ConcurrentDup.Total != rep.Robustness.ConcurrentDup.Stable {
		t.Fatal("одновременные одинаковые запросы обязаны давать одну маску")
	}
	if len(rep.Robustness.UnknownID) == 0 {
		t.Fatal("проверка неизвестного идентификатора не выполнена")
	}
}

// TestRunnerStopsAfterFiveInvalid проверяет, что прогон останавливается по
// правилу пяти невалидных ответов подряд.
func TestRunnerStopsAfterFiveInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	samples := []Sample{{PayloadID: "p1", Text: "текст", Category: "прочее"}}
	opts := Options{URL: srv.URL, RPS: 500, Duration: 5 * time.Second, Workers: 1, Timeout: time.Second}
	started := time.Now()
	stats := NewRunner(opts, samples).Run(context.Background())
	rep := stats.BuildReport(opts, datasetReport("тест", samples))

	if !rep.StoppedByStreak {
		t.Fatal("прогон обязан остановиться по серии невалидных ответов")
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("остановка должна наступить задолго до конца заданной длительности")
	}
	if rep.Masking.Failed == 0 {
		t.Fatal("элементы без маски должны быть учтены")
	}
}

// TestWriteReports проверяет, что оба файла отчёта создаются и читаются.
func TestWriteReports(t *testing.T) {
	dir := t.TempDir()
	rep := Report{
		URL:     "http://127.0.0.1:8080/process",
		Dataset: DatasetReport{Path: "testdata/dataset.jsonl", Samples: 1, Fragments: 2},
		Masking: MaskingReport{
			Fragments:   2,
			AvgDistance: 0.75,
			ByType:      map[string]SliceReport{"FIO": {Fragments: 1, AvgDistance: 1}, "PHONE": {Fragments: 1, AvgDistance: 0.5}},
			ByCategory:  map[string]SliceReport{"анкета": {Fragments: 2, AvgDistance: 0.75}},
		},
		Demask: DemaskReport{Total: 1, Exact: 1, ExactShare: 1},
	}
	if err := WriteReports(dir, rep); err != nil {
		t.Fatalf("отчёты не записаны: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed Report
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("машинный отчёт не разобран: %v", err)
	}
	if parsed.Masking.ByType["FIO"].AvgDistance != 1 {
		t.Fatal("разбиение по типам потерялось")
	}
	md, err := os.ReadFile(filepath.Join(dir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(md)
	for _, want := range []string{"Качество по типам", "Качество по категориям набора", "PHONE", "анкета"} {
		if !strings.Contains(text, want) {
			t.Fatalf("в отчёте нет раздела %q", want)
		}
	}
	// Худший тип обязан стоять выше: у PHONE изменение меньше, чем у FIO.
	if strings.Index(text, "| PHONE ") > strings.Index(text, "| FIO ") {
		t.Fatal("срезы должны быть отсортированы худшим вперёд")
	}
}

// TestPacing проверяет подбор периода и пачки под заданную частоту.
func TestPacing(t *testing.T) {
	cases := []struct {
		rps      float64
		interval time.Duration
		batch    int
	}{
		{0, time.Millisecond, 1},
		{1, time.Second, 1},
		{10, 100 * time.Millisecond, 1},
		{1000, time.Millisecond, 1},
		{5000, time.Millisecond, 5},
	}
	for _, c := range cases {
		interval, batch := pacing(c.rps)
		if interval != c.interval || batch != c.batch {
			t.Fatalf("pacing(%.0f) = %s и %d, ожидалось %s и %d", c.rps, interval, batch, c.interval, c.batch)
		}
	}
}

// TestInflate проверяет, что тяжёлое тело набирается до нужного размера и не
// обрывается посреди символа.
func TestInflate(t *testing.T) {
	for _, size := range []int{1, 7, 64, 1024} {
		got := inflate("Иванов Иван", size)
		if len(got) < size {
			t.Fatalf("тело короче заданного: %d против %d", len(got), size)
		}
		if len(got) > size+8 {
			t.Fatalf("тело длиннее разумного: %d против %d", len(got), size)
		}
		if !isRuneStart(got[len(got)-1]) && strings.ContainsRune(got, '�') {
			t.Fatal("тело оборвано посреди символа")
		}
	}
}

// TestPayloadID проверяет, что каждый круг по набору получает свой
// идентификатор: иначе обратный шаг вернёт маску прошлого круга.
func TestPayloadID(t *testing.T) {
	if got := payloadID("p1", 0); got != "p1" {
		t.Fatalf("на первом круге идентификатор = %q", got)
	}
	if got := payloadID("p1", 3); got != "p1#3" {
		t.Fatalf("на четвёртом круге идентификатор = %q", got)
	}
}

// repeat собирает последовательность одинаковых исходов для табличных тестов.
func repeat(kind outcomeKind, n int) []outcomeKind {
	out := make([]outcomeKind, n)
	for i := range out {
		out[i] = kind
	}
	return out
}

// writeResult отвечает телом в формате сервиса.
func writeResult(w http.ResponseWriter, result string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"result": result})
}

// maskingStub изображает сервис: цифры заменяются звёздочками, обратный запрос
// с той же маской возвращает исходный текст.
func maskingStub() http.HandlerFunc {
	type entry struct{ orig, masked string }
	var store atomic.Pointer[map[string]entry]
	initial := map[string]entry{}
	store.Store(&initial)
	mu := make(chanMutex, 1)

	return func(w http.ResponseWriter, r *http.Request) {
		var req processRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		mu.lock()
		defer mu.unlock()
		current := *store.Load()
		next := make(map[string]entry, len(current)+1)
		for k, v := range current {
			next[k] = v
		}
		if e, ok := next[req.PayloadID]; ok {
			switch req.Payload {
			case e.orig:
				writeResult(w, e.masked)
				return
			case e.masked:
				writeResult(w, e.orig)
				return
			}
		}
		masked := maskDigits(req.Payload)
		next[req.PayloadID] = entry{orig: req.Payload, masked: masked}
		store.Store(&next)
		writeResult(w, masked)
	}
}

// maskDigits заменяет цифры звёздочками, сохраняя длину строки.
func maskDigits(s string) string {
	out := []byte(s)
	for i, b := range out {
		if b >= '0' && b <= '9' {
			out[i] = '*'
		}
	}
	return string(out)
}

// chanMutex даёт простую блокировку на канале: изображаемый сервис должен
// отвечать одинаково на одновременные одинаковые запросы.
type chanMutex chan struct{}

func (m chanMutex) lock()   { m <- struct{}{} }
func (m chanMutex) unlock() { <-m }

// TestStatsIgnoresAborted проверяет, что запрос, оборванный концом прогона,
// не попадает ни в невалидные ответы, ни в задержки: сервис тут ни при чём.
func TestStatsIgnoresAborted(t *testing.T) {
	stats := NewStats()
	aborted := Response{
		Outcome:  outcomeAborted,
		Attempts: []Attempt{{Aborted: true, Latency: 5 * time.Second}},
	}
	for i := 0; i < 10; i++ {
		if !stats.Observe(aborted) {
			t.Fatal("оборванный запрос не должен останавливать прогон")
		}
	}
	rep := stats.BuildReport(Options{RPS: 10}, DatasetReport{})
	if rep.Load.Requests != 0 || rep.Load.Invalid != 0 {
		t.Fatalf("оборванные запросы попали в показатели: %+v", rep.Load)
	}
	if rep.Load.MaxInvalidStreak != 0 || rep.StoppedByStreak {
		t.Fatal("оборванные запросы не должны копить серию")
	}
}

// TestRunnerAbortedOnDeadline проверяет тот же случай целиком: медленный
// сервис и короткий прогон дают обрывы, но не серию невалидных ответов.
func TestRunnerAbortedOnDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			writeResult(w, "поздно")
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	samples := []Sample{{PayloadID: "p1", Text: "текст", Category: "прочее"}}
	opts := Options{URL: srv.URL, RPS: 100, Duration: 200 * time.Millisecond, Workers: 4, Timeout: 5 * time.Second}
	stats := NewRunner(opts, samples).Run(context.Background())
	rep := stats.BuildReport(opts, datasetReport("тест", samples))
	if rep.StoppedByStreak || rep.Load.Invalid != 0 {
		t.Fatalf("обрыв по концу прогона не должен считаться отказом сервиса: %+v", rep.Load)
	}
}
