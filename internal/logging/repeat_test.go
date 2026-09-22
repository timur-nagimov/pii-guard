package logging

import (
	"strings"
	"testing"
	"time"
)

// TestRepeatSuppress проверяет главное свойство глушителя: шторм одинаковых
// ошибок не затапливает журнал, но и не исчезает бесследно.
func TestRepeatSuppress(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RepeatWindow = time.Hour
	l, buf := newTestLogger(t, cfg)

	for i := 0; i < 1000; i++ {
		l.Slog().Warn("общее хранилище недоступно, работаем через память",
			Event(EventStoreDegraded), Component("store"))
	}
	recs := records(t, buf)
	if len(recs) != 1 {
		t.Fatalf("тысяча одинаковых предупреждений дала %d записей вместо одной", len(recs))
	}
	if got := l.Stats().Suppressed.Load(); got != 999 {
		t.Fatalf("заглушено %d повторов вместо 999", got)
	}
}

// TestRepeatReportsCount проверяет, что счётчик повторов выводится по
// истечении окна, даже если шторм прекратился.
func TestRepeatReportsCount(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RepeatWindow = 30 * time.Millisecond
	l, buf := newTestLogger(t, cfg)

	for i := 0; i < 50; i++ {
		l.Slog().Error("сбой детектора", Event(EventDetectorPanic))
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), EventLogSuppressed) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, EventLogSuppressed) {
		t.Fatalf("счётчик повторов не выведен:\n%s", out)
	}
	if !strings.Contains(out, `"suppressed":49`) {
		t.Fatalf("в счётчике не 49 повторов:\n%s", out)
	}
}

// TestRepeatKeepsDifferent проверяет, что разные сообщения не склеиваются.
func TestRepeatKeepsDifferent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RepeatWindow = time.Hour
	l, buf := newTestLogger(t, cfg)
	l.Slog().Warn("первое", Event(EventOverload))
	l.Slog().Warn("второе", Event(EventOverload))
	l.Slog().Error("первое", Event(EventOverload))
	if n := len(records(t, buf)); n != 3 {
		t.Fatalf("разные записи склеились: %d записей вместо трёх", n)
	}
}

// TestRepeatSkipsInfo проверяет, что обычные записи глушитель не трогает:
// поток успешных запросов прореживается в другом месте и другим правилом.
func TestRepeatSkipsInfo(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RepeatWindow = time.Hour
	l, buf := newTestLogger(t, cfg)
	for i := 0; i < 5; i++ {
		l.Slog().Info("обработан запрос", Event(EventProcess))
	}
	if n := len(records(t, buf)); n != 5 {
		t.Fatalf("обычные записи заглушены: %d вместо пяти", n)
	}
}
