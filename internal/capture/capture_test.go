package capture

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// buf — файл в памяти: пишет в буфер и считает закрытия.
type buf struct {
	mu     sync.Mutex
	b      bytes.Buffer
	closed bool
	fail   bool
}

func (x *buf) Write(p []byte) (int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.fail {
		return 0, errors.New("диск недоступен")
	}
	return x.b.Write(p)
}

func (x *buf) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.closed = true
	return nil
}

func (x *buf) String() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.b.String()
}

func opener(x *buf) func(string, int64, int) (io.WriteCloser, error) {
	return func(string, int64, int) (io.WriteCloser, error) { return x, nil }
}

func sample() Record {
	return Record{
		Time:       time.Unix(0, 0).UTC(),
		System:     "jury",
		PayloadID:  "id-1",
		Direction:  "mask",
		PayloadLen: 42,
		Counts:     map[string]int{"FIO": 1},
		TookMS:     0.5,
		Payload:    "Иванов Иван, паспорт 4509 123456",
		Result:     "***********, паспорт ***********",
	}
}

// TestDisabledWritesNothing: выключенный канал не пишет и не падает. Файл при
// этом вообще не открывается — иначе выключенный захват оставлял бы следы.
func TestDisabledWritesNothing(t *testing.T) {
	opened := false
	w, err := New(Config{Enabled: false, Path: "/нет/такого"}, func(string, int64, int) (io.WriteCloser, error) {
		opened = true
		return nil, errors.New("открывать не должны")
	})
	if err != nil {
		t.Fatalf("выключенный канал вернул ошибку: %v", err)
	}
	if opened {
		t.Fatal("выключенный канал открыл файл")
	}
	w.Write(sample())
	if w.Enabled() {
		t.Error("выключенный канал считает себя включённым")
	}
	if err := w.Close(); err != nil {
		t.Errorf("закрытие выключенного канала: %v", err)
	}
}

// TestWithoutPayloadStripsText: без явного разрешения текст в файл не идёт.
// Это главное свойство канала: включить захват и нечаянно начать писать
// персональные данные на диск нельзя.
func TestWithoutPayloadStripsText(t *testing.T) {
	x := &buf{}
	w, err := New(Config{Enabled: true, Path: "f", WithPayload: false}, opener(x))
	if err != nil {
		t.Fatalf("канал не создался: %v", err)
	}
	w.Write(sample())
	if err := w.Close(); err != nil {
		t.Fatalf("закрытие: %v", err)
	}
	line := x.String()
	if strings.Contains(line, "Иванов") || strings.Contains(line, "4509") {
		t.Fatalf("в файл попал исходный текст: %s", line)
	}
	var rec Record
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("запись не разобралась: %v", err)
	}
	if rec.PayloadLen != 42 || rec.Counts["FIO"] != 1 || rec.PayloadID != "id-1" {
		t.Errorf("служебные поля потерялись: %+v", rec)
	}
}

// TestWithPayloadKeepsText: с явным разрешением текст сохраняется целиком —
// ради этого канал и нужен.
func TestWithPayloadKeepsText(t *testing.T) {
	x := &buf{}
	w, _ := New(Config{Enabled: true, Path: "f", WithPayload: true}, opener(x))
	w.Write(sample())
	if err := w.Close(); err != nil {
		t.Fatalf("закрытие: %v", err)
	}
	var rec Record
	if err := json.Unmarshal([]byte(x.String()), &rec); err != nil {
		t.Fatalf("запись не разобралась: %v", err)
	}
	if rec.Payload != sample().Payload || rec.Result != sample().Result {
		t.Errorf("текст не сохранился: %+v", rec)
	}
}

// blocker — писатель, который держит первую запись, пока тест не отпустит.
// Нужен, чтобы очередь заведомо переполнилась: без этого горутина успевает
// разобрать очередь, потерь нет, и проверять становится нечего.
type blocker struct {
	release chan struct{}
	once    sync.Once
}

func (b *blocker) Write(p []byte) (int, error) {
	b.once.Do(func() { <-b.release })
	return len(p), nil
}

func (b *blocker) Close() error { return nil }

// TestFullQueueDropsAndCounts: при полной очереди запись отбрасывается, а не
// ждёт диска. Проверяются оба обещания сразу: вызов не блокирует обработчик и
// потери считаются — молчаливая потеря образцов хуже видимой.
func TestFullQueueDropsAndCounts(t *testing.T) {
	b := &blocker{release: make(chan struct{})}
	w, _ := New(Config{Enabled: true, Path: "f", Queue: 1},
		func(string, int64, int) (io.WriteCloser, error) { return b, nil })

	const n = 64
	done := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			w.Write(sample())
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		close(b.release)
		t.Fatal("запись заблокировала вызывающего: очередь полна, а Write ждёт диска")
	}

	_, dropped := w.Stats()
	if dropped == 0 {
		close(b.release)
		t.Fatal("очередь на одну запись приняла все 64: потери не считаются")
	}
	close(b.release)
	_ = w.Close()
}

// TestWriteErrorCountsAsDrop: ошибка записи не роняет сервис и считается
// потерей, а не успехом.
func TestWriteErrorCountsAsDrop(t *testing.T) {
	x := &buf{fail: true}
	w, _ := New(Config{Enabled: true, Path: "f"}, opener(x))
	w.Write(sample())
	_ = w.Close()
	written, dropped := w.Stats()
	if written != 0 || dropped != 1 {
		t.Fatalf("записано %d, отброшено %d, ожидалось 0 и 1", written, dropped)
	}
}

// TestCloseClosesFile: файл закрывается, иначе хвост очереди теряется.
func TestCloseClosesFile(t *testing.T) {
	x := &buf{}
	w, _ := New(Config{Enabled: true, Path: "f"}, opener(x))
	w.Write(sample())
	if err := w.Close(); err != nil {
		t.Fatalf("закрытие: %v", err)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.closed {
		t.Error("файл остался открытым")
	}
}
