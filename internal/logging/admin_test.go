package logging

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLevelHandler проверяет управление уровнем на ходу и возврат уровня по
// истечении срока: забытый отладочный уровень на боевой машине недопустим.
func TestLevelHandler(t *testing.T) {
	l, _ := newTestLogger(t, DefaultConfig())
	h := l.LevelHandler(nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/loglevel", nil))
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("ответ не разобран: %v", err)
	}
	if body["level"] != "info" {
		t.Fatalf("уровень по умолчанию равен %q", body["level"])
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/loglevel?level=debug&ttl=50ms", nil))
	if l.LevelName() != "debug" {
		t.Fatalf("уровень не поднят: %q", l.LevelName())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && l.LevelName() != "info" {
		time.Sleep(5 * time.Millisecond)
	}
	if l.LevelName() != "info" {
		t.Fatal("уровень не вернулся сам по истечении срока")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/loglevel?level=нет", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("неизвестный уровень принят с кодом %d", rec.Code)
	}
}

// TestLevelHandlerForbidden проверяет запрет доступа.
func TestLevelHandlerForbidden(t *testing.T) {
	l, _ := newTestLogger(t, DefaultConfig())
	h := l.LevelHandler(func(*http.Request) bool { return false })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/loglevel", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ручка открыта без проверки: код %d", rec.Code)
	}
}
