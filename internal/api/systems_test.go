package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSystemsListsEnabledSystems проверяет, что страница проверки получает
// список включённых систем без секретов: ни ключей, ни адресов модели.
func TestSystemsListsEnabledSystems(t *testing.T) {
	h := newInspectServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/systems", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("ожидался код 200, получен %d, тело %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Systems []systemInfo `json:"systems"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("не удалось разобрать ответ: %v", err)
	}
	if len(out.Systems) == 0 {
		t.Fatal("список систем пуст")
	}
	for _, sys := range out.Systems {
		if sys.Name == "" {
			t.Error("у системы пустое имя")
		}
		if sys.Preset == "" {
			t.Errorf("у системы %q пустой пресет", sys.Name)
		}
	}
}

// TestSystemsRejectsNonGet проверяет, что список систем отвечает только на GET.
func TestSystemsRejectsNonGet(t *testing.T) {
	h := newInspectServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/v1/systems", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("метод %s дал код %d, ожидался 405", method, rec.Code)
		}
	}
}

// TestInspectSystemOverride проверяет, что страница может выбрать профиль
// системы по имени, не зная ключа доступа.
func TestInspectSystemOverride(t *testing.T) {
	h := newInspectServer(t)
	text := "Клиент Иванов Иван Иванович, тел +7 916 123-45-67"

	// Имя системы задано — разбор выполняется от её имени.
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"text": text, "system": "alfasonar"})
	req := httptest.NewRequest(http.MethodPost, "/v1/inspect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ожидался код 200, получен %d, тело %s", rec.Code, rec.Body.String())
	}
	var out inspectResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("не удалось разобрать ответ: %v", err)
	}
	if out.System != "alfasonar" {
		t.Errorf("система в ответе %q, ожидалась alfasonar", out.System)
	}

	// Неизвестное имя — отказ, а не молчаливый возврат к системе по ключу.
	rec = httptest.NewRecorder()
	body, _ = json.Marshal(map[string]any{"text": text, "system": "неттакой"})
	req = httptest.NewRequest(http.MethodPost, "/v1/inspect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("неизвестная система дала код %d, ожидался 403", rec.Code)
	}
}
