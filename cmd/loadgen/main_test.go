package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadTextsBySize проверяет порождение текста заданного размера: длина
// совпадает с запрошенной, а текст содержит персональные данные.
func TestLoadTextsBySize(t *testing.T) {
	for _, size := range []int{1, 100, 2048} {
		texts, err := loadTexts("", size)
		if err != nil {
			t.Fatalf("размер %d: %v", size, err)
		}
		if len(texts) != 1 {
			t.Fatalf("размер %d: текстов %d, ожидался один", size, len(texts))
		}
		if len(texts[0]) != size {
			t.Fatalf("размер %d: длина текста %d", size, len(texts[0]))
		}
	}
}

// TestLoadTextsFromFile проверяет чтение текстов из набора: битые строки и
// пустые тексты пропускаются.
func TestLoadTextsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "set.jsonl")
	lines := []string{
		`{"text":"Иванов Иван"}`,
		`не json`,
		`{"text":""}`,
		`{"text":"тел. +79991234567"}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	texts, err := loadTexts(path, 0)
	if err != nil {
		t.Fatalf("чтение не удалось: %v", err)
	}
	if len(texts) != 2 {
		t.Fatalf("прочитано текстов %d, ожидалось 2", len(texts))
	}
}

// TestLoadTextsErrors проверяет отказы: без набора и без размера, отсутствующий
// файл, пустой набор.
func TestLoadTextsErrors(t *testing.T) {
	if _, err := loadTexts("", 0); err == nil {
		t.Fatal("без набора и размера ожидалась ошибка")
	}
	if _, err := loadTexts(filepath.Join(t.TempDir(), "нет.jsonl"), 0); err == nil {
		t.Fatal("отсутствующий файл должен давать ошибку")
	}
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, []byte("не json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTexts(empty, 0); err == nil {
		t.Fatal("набор без текстов должен давать ошибку")
	}
}

// TestSend проверяет отправку запроса и разбор ответа через httptest.
func TestSend(t *testing.T) {
	var gotPayload, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotPayload = body["payload"]
		gotID = body["payload_id"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"маска"}`))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second}
	lat, status, err := send(context.Background(), client, srv.URL, "Иванов Иван", "load-1-1")
	if err != nil {
		t.Fatalf("отправка не удалась: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("код ответа %d", status)
	}
	if lat <= 0 {
		t.Fatalf("задержка не измерена: %v", lat)
	}
	if gotPayload != "Иванов Иван" || gotID != "load-1-1" {
		t.Fatalf("тело запроса неверно: payload=%q id=%q", gotPayload, gotID)
	}
}

// TestSendError проверяет, что сбой сети возвращает ошибку.
func TestSendError(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	_, _, err := send(context.Background(), client, "http://127.0.0.1:1", "текст", "id")
	if err == nil {
		t.Fatal("ожидалась ошибка сети")
	}
}

// TestSendBadURL проверяет, что неразбираемый адрес даёт ошибку при сборке
// запроса, а не при отправке.
func TestSendBadURL(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	_, _, err := send(context.Background(), client, "://не адрес", "текст", "id")
	if err == nil {
		t.Fatal("ожидалась ошибка разбора адреса")
	}
}

// TestRunWorkerCounts проверяет, что отправитель различает успех, перегрузку и
// ошибку и что задержки успешных ответов собираются.
func TestRunWorkerCounts(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls % 3 {
		case 0:
			w.WriteHeader(http.StatusTooManyRequests)
		case 1:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":"маска"}`))
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var c counters
	latencies := runWorker(ctx, srv.Client(), srv.URL, []string{"текст"}, 0, 1, 0, &c)
	if c.ok.Load() == 0 {
		t.Fatal("успешных ответов нет")
	}
	if c.throttled.Load() == 0 {
		t.Fatal("перегрузок нет")
	}
	if c.failed.Load() == 0 {
		t.Fatal("ошибок нет")
	}
	if len(latencies) == 0 {
		t.Fatal("задержки успешных ответов не собраны")
	}
}

// TestNoteError проверяет запоминание причин сбоев с обрезкой длинных сообщений.
func TestNoteError(t *testing.T) {
	var c counters
	c.noteError("короткая причина")
	c.noteError("короткая причина")
	long := strings.Repeat("а", 300)
	c.noteError(long)
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.errSeen["короткая причина"] != 2 {
		t.Fatalf("повторная причина посчитана %d раз", c.errSeen["короткая причина"])
	}
	for msg := range c.errSeen {
		if len(msg) > 120 {
			t.Fatalf("длинное сообщение не обрезано: %d символов", len(msg))
		}
	}
}

// TestWaitInterval проверяет паузу между запросами: нулевой интервал не ждёт,
// положительный ждёт и учитывает отмену контекста.
func TestWaitInterval(t *testing.T) {
	next := time.Now()
	if got := waitInterval(context.Background(), 0, next); !got.Equal(next) {
		t.Fatal("нулевой интервал не должен двигать время")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := waitInterval(ctx, time.Hour, next)
	if !got.After(next) {
		t.Fatal("интервал должен двигать время вперёд")
	}
}

// TestPct проверяет выбор перцентиля из отсортированных задержек.
func TestPct(t *testing.T) {
	if got := pct(nil, 0.5); got != 0 {
		t.Fatalf("пустой список: %v", got)
	}
	sorted := []time.Duration{1, 2, 3, 4}
	if got := pct(sorted, 0.5); got != 2 {
		t.Fatalf("доля 50: %v", got)
	}
	if got := pct(sorted, 1.0); got != 4 {
		t.Fatalf("доля 100: %v", got)
	}
}

// TestMs проверяет перевод длительности в миллисекунды.
func TestMs(t *testing.T) {
	if got := ms(1500 * time.Microsecond); got != 1.5 {
		t.Fatalf("миллисекунды: %v", got)
	}
}

// TestReportResults проверяет печать итогов прогона.
func TestReportResults(t *testing.T) {
	var c counters
	c.ok.Add(10)
	c.throttled.Add(2)
	c.failed.Add(1)
	c.noteError("причина сбоя")
	latencies := [][]time.Duration{{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}}
	out := captureStdout(t, func() {
		reportResults(&c, latencies, 100, time.Second)
	})
	for _, want := range []string{"частота", "успешно 10", "причина сбоя", "задержка"} {
		if !strings.Contains(out, want) {
			t.Fatalf("в выводе нет %q:\n%s", want, out)
		}
	}
}

// TestRunLoad проверяет прогон отправителей целиком: запросы уходят на сервис,
// итоги печатаются.
func TestRunLoad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"маска"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	client := &http.Client{Timeout: time.Second}
	out := captureStdout(t, func() {
		runLoad(ctx, client, srv.URL+"/process", []string{"текст"}, 100, 4, 1)
	})
	if !strings.Contains(out, "успешно") {
		t.Fatalf("итоги не напечатаны:\n%s", out)
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
