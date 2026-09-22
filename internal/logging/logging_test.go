package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer — буфер вывода, пригодный для одновременной записи. Глушитель
// повторов пишет из своей горутины, поэтому обычный bytes.Buffer дал бы гонку.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newTestLogger собирает журнал с выводом в буфер.
func newTestLogger(t *testing.T, cfg Config) (*Logger, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	if cfg.Level == "" {
		cfg = DefaultConfig()
	}
	cfg.Output = buf
	l, err := New(cfg)
	if err != nil {
		t.Fatalf("журнал не создан: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, buf
}

// records разбирает вывод в набор записей.
func records(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("запись не разобрана: %v: %s", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// TestRequiredFields проверяет стандарт полей: обязательные поля есть в каждой
// записи, даже если место вызова их не передало.
func TestRequiredFields(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	ctx := WithRequestID(context.Background(), "abc123")
	SetSystem(ctx, "crm")

	l.Slog().InfoContext(ctx, "обработан запрос", Event(EventProcess), Took(1500*time.Microsecond))
	l.Slog().Info("сообщение без полей")

	recs := records(t, buf)
	if len(recs) != 2 {
		t.Fatalf("ожидалось две записи, получено %d", len(recs))
	}
	for i, rec := range recs {
		for _, field := range []string{slog.TimeKey, slog.LevelKey, FieldEvent, FieldDuration, FieldService} {
			if _, ok := rec[field]; !ok {
				t.Errorf("запись %d: нет обязательного поля %q", i, field)
			}
		}
	}
	if recs[0][FieldRequestID] != "abc123" {
		t.Errorf("идентификатор запроса не подставлен из контекста: %v", recs[0][FieldRequestID])
	}
	if recs[0][FieldSystem] != "crm" {
		t.Errorf("система не подставлена из контекста: %v", recs[0][FieldSystem])
	}
	if recs[0][FieldDuration] != 1.5 {
		t.Errorf("длительность записана неверно: %v", recs[0][FieldDuration])
	}
	if recs[1][FieldEvent] != EventUnspecified {
		t.Errorf("событие по умолчанию не подставлено: %v", recs[1][FieldEvent])
	}
}

// TestNoPIIInLog — главный тест пакета. Что бы место вызова ни передало,
// значения персональных данных в выводе не появляются.
func TestNoPIIInLog(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	log := l.Slog()

	secrets := []string{
		"Иванов Иван Иванович",
		"ivanov@mail.ru",
		"+7 (916) 123-45-67",
		"4111 1111 1111 1111",
		"500100732259",
		"12.03.1985",
	}
	log.Info("обработан запрос",
		slog.String("value", secrets[0]),
		slog.String("email", secrets[1]),
		slog.String("phone", secrets[2]),
		slog.Group("payload",
			slog.String("card", secrets[3]),
			slog.String("inn", secrets[4])),
		slog.Any("any", secrets[5]),
	)
	log.Info("клиент Иванов Иван Иванович не найден")
	log.Error("ошибка разбора", Err(&testError{text: "не разобрано значение 4111 1111 1111 1111"}))

	out := buf.String()
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Fatalf("значение %q попало в журнал:\n%s", s, out)
		}
	}
	if l.Stats().Redactions.Load() == 0 {
		t.Fatal("счётчик вычищенных значений не увеличился")
	}
}

type testError struct{ text string }

func (e *testError) Error() string { return e.text }

// TestSetLevel проверяет переключение уровня без перезапуска.
func TestSetLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Level = "info"
	l, buf := newTestLogger(t, cfg)

	l.Slog().Debug("на горячем пути", Event(EventProcess))
	if len(records(t, buf)) != 0 {
		t.Fatal("отладочная запись вышла на уровне info")
	}
	if err := l.SetLevel("debug"); err != nil {
		t.Fatalf("уровень не принят: %v", err)
	}
	l.Slog().Debug("на горячем пути", Event(EventProcess))
	recs := records(t, buf)
	if len(recs) != 2 {
		t.Fatalf("ожидались запись о смене уровня и отладочная запись, получено %d", len(recs))
	}
	if recs[0][FieldEvent] != EventLogLevel {
		t.Errorf("смена уровня не записана: %v", recs[0])
	}
	if err := l.SetLevel("нет такого"); err == nil {
		t.Fatal("неизвестный уровень принят")
	}
}

// TestTextFormat проверяет читаемый формат для разработки.
func TestTextFormat(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Format = FormatText
	l, buf := newTestLogger(t, cfg)
	l.Slog().Info("сервис запущен", Event(EventServiceStart))
	out := buf.String()
	if !strings.Contains(out, "event=service_start") {
		t.Fatalf("читаемый формат не применён: %s", out)
	}
}

// TestRedactOff проверяет, что проверку значений можно выключить. Выключение
// допустимо только в измерениях скорости, но возможность нужна.
func TestRedactOff(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Redact = false
	l, buf := newTestLogger(t, cfg)
	l.Slog().Info("проверка", slog.String("value", "Иванов Иван Иванович"))
	if !strings.Contains(buf.String(), "Иванов") {
		t.Fatal("проверка значений не выключилась")
	}
}

// TestFromEnv проверяет чтение настроек из окружения: журнал поднимается
// раньше, чем читается файл настроек, и обязан работать без него.
func TestFromEnv(t *testing.T) {
	t.Setenv("PII_LOG_LEVEL", "warn")
	t.Setenv("PII_LOG_FORMAT", FormatText)
	t.Setenv("PII_LOG_REPEAT_WINDOW", "42s")
	t.Setenv("PII_LOG_SAMPLE", "25")
	t.Setenv("PII_LOG_SLOW", "700ms")
	t.Setenv("PII_INSTANCE", "pii-guard-7")
	t.Setenv("PII_AUDIT", "0")

	cfg := FromEnv()
	if cfg.Level != "warn" || cfg.Format != FormatText {
		t.Errorf("уровень и формат прочитаны неверно: %q %q", cfg.Level, cfg.Format)
	}
	if cfg.RepeatWindow != 42*time.Second {
		t.Errorf("окно глушения повторов равно %v", cfg.RepeatWindow)
	}
	if cfg.SampleN != 25 || cfg.Slow != 700*time.Millisecond {
		t.Errorf("прореживание прочитано неверно: %d %v", cfg.SampleN, cfg.Slow)
	}
	if cfg.Instance != "pii-guard-7" {
		t.Errorf("имя копии равно %q", cfg.Instance)
	}
	if cfg.Audit.Enabled {
		t.Error("аудит не выключился")
	}

	cfg.Output = &syncBuffer{}
	l, err := New(cfg)
	if err != nil {
		t.Fatalf("журнал не создан: %v", err)
	}
	defer func() { _ = l.Close() }()
	opts := l.MiddlewareOptions()
	if opts.SampleN != 25 || opts.Slow != 700*time.Millisecond {
		t.Errorf("настройки прослойки не согласованы с журналом: %+v", opts)
	}
}

// TestBadLevelRejected проверяет, что непонятный уровень в окружении не
// остаётся незамеченным.
func TestBadLevelRejected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Level = "подробно"
	if _, err := New(cfg); err == nil {
		t.Fatal("неизвестный уровень принят при создании журнала")
	}
}
