package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// TestInspectShowsDecisions проверяет главное назначение ручки разбора:
// по ответу должно быть видно, какое правило сработало и на каком фрагменте.
func TestInspectShowsDecisions(t *testing.T) {
	h := newInspectServer(t)
	text := "Клиент Иванов Иван Иванович, паспорт 4509 123456, тел +7 916 123-45-67"

	resp := doInspect(t, h, map[string]any{"text": text})
	if resp.Masked == text {
		t.Fatal("текст не изменился, маска не наложена")
	}
	if len(resp.Spans) == 0 {
		t.Fatal("не найдено ни одного фрагмента")
	}

	seen := make(map[string]inspectSpan, len(resp.Spans))
	for _, sp := range resp.Spans {
		seen[sp.Type] = sp
		if sp.Reason == "" {
			t.Errorf("у фрагмента типа %s нет причины решения", sp.Type)
		}
		if sp.Value == "" {
			t.Errorf("у фрагмента типа %s пустое значение", sp.Type)
		}
		if sp.Start < 0 || sp.End > len(text) || sp.Start >= sp.End {
			t.Errorf("границы фрагмента %s вне текста: %d..%d", sp.Type, sp.Start, sp.End)
		}
		if got := text[sp.Start:sp.End]; got != sp.Value {
			t.Errorf("значение не совпало со срезом по границам: %q против %q", sp.Value, got)
		}
	}
	for _, want := range []string{"FIO", "PASSPORT", "PHONE"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("тип %s не найден, а должен быть", want)
		}
	}
}

// TestInspectRuneOffsets проверяет, что рунные границы пригодны для подсветки:
// кириллическая буква занимает два байта, и путаница между байтами и рунами
// сдвинула бы выделение в интерфейсе.
func TestInspectRuneOffsets(t *testing.T) {
	h := newInspectServer(t)
	text := "Здравствуйте, тел +7 916 123-45-67"

	resp := doInspect(t, h, map[string]any{"text": text})
	runes := []rune(text)
	for _, sp := range resp.Spans {
		if sp.StartRune < 0 || sp.EndRune > len(runes) || sp.StartRune >= sp.EndRune {
			t.Fatalf("рунные границы вне текста: %d..%d при длине %d", sp.StartRune, sp.EndRune, len(runes))
		}
		if got := string(runes[sp.StartRune:sp.EndRune]); got != sp.Value {
			t.Errorf("срез по рунам не совпал со значением: %q против %q", got, sp.Value)
		}
	}
}

// TestInspectPresetOverride проверяет разовую подмену вида маскирования:
// она нужна, чтобы сравнивать виды на одном тексте, не трогая настройки.
func TestInspectPresetOverride(t *testing.T) {
	h := newInspectServer(t)
	text := "Клиент Иванов Иван Иванович, паспорт 4509 123456"

	full := doInspect(t, h, map[string]any{"text": text, "preset": "full"})
	partial := doInspect(t, h, map[string]any{"text": text, "preset": "partial"})
	if full.Masked == partial.Masked {
		t.Fatal("разные виды маскирования дали одинаковый результат")
	}
	if !strings.Contains(partial.Masked, "45") {
		t.Errorf("частичная маска должна оставлять первые знаки видимыми, получено %q", partial.Masked)
	}
	if strings.ContainsAny(strings.ReplaceAll(full.Masked, "*", ""), "0123456789") {
		t.Errorf("полная маска не должна оставлять цифр, получено %q", full.Masked)
	}
}

// TestInspectTokenPresetReturnsPlaceholders проверяет, что для вида с
// плейсхолдерами ответ содержит соответствия: без них нельзя восстановить
// ответ языковой модели.
func TestInspectTokenPresetReturnsPlaceholders(t *testing.T) {
	h := newInspectServer(t)
	resp := doInspect(t, h, map[string]any{
		"text":   "Клиент Иванов Иван Иванович, тел +7 916 123-45-67",
		"preset": "token",
	})
	if len(resp.Placeholders) == 0 {
		t.Fatal("для вида с плейсхолдерами соответствия не возвращены")
	}
	for token, value := range resp.Placeholders {
		if !strings.HasPrefix(token, "[") || !strings.HasSuffix(token, "]") {
			t.Errorf("плейсхолдер записан не в квадратных скобках: %q", token)
		}
		if value == "" {
			t.Errorf("у плейсхолдера %q пустое значение", token)
		}
		if !strings.Contains(resp.Masked, token) {
			t.Errorf("плейсхолдер %q отсутствует в тексте маски", token)
		}
	}
}

// TestInspectValidation проверяет понятные отказы на неверные запросы.
func TestInspectValidation(t *testing.T) {
	h := newInspectServer(t)
	cases := []struct {
		name string
		body string
		code int
	}{
		{"пустой текст", `{"text":""}`, http.StatusUnprocessableEntity},
		{"нет поля текста", `{}`, http.StatusUnprocessableEntity},
		{"битый JSON", `{"text":`, http.StatusUnprocessableEntity},
		{"неизвестный вид маски", `{"text":"тест","preset":"нетакого"}`, http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/inspect", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			h.ServeHTTP(rec, req)
			if rec.Code != c.code {
				t.Fatalf("ожидался код %d, получен %d, тело %s", c.code, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestInspectMethodNotAllowed проверяет, что разбор доступен только методом POST.
func TestInspectMethodNotAllowed(t *testing.T) {
	h := newInspectServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/inspect", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("ожидался код 405, получен %d", rec.Code)
	}
}

// newInspectServer поднимает сервер с полным набором детекторов и включённым
// разбором: тесты разбора проверяют решения правил, поэтому неполный набор
// детекторов сделал бы их бессмысленными.
func newInspectServer(t *testing.T) http.Handler {
	t.Helper()

	cfg, err := config.Parse([]byte(inspectConfigYAML))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 1000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	reg := pii.NewRegistry()
	reg.Register(
		pii.NewNumericDetector(),
		pii.NewEmailDetector(),
		pii.NewFIODetector(),
		pii.NewDateDetector(config.DatePIIContext),
		pii.NewAddressDetector(),
		pii.NewBirthPlaceDetector(),
		pii.NewIssuerDetector(),
		pii.NewCitizenshipDetector(),
		pii.NewDriverLicenseLettersDetector(),
		pii.NewCardHolderDetector(),
		pii.NewExtraDocumentsDetector(),
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, st, engine.New(reg), metrics.New(), log).Routes()
}

// inspectConfigYAML — настройки для тестов разбора: одна анонимная система,
// разбор включён.
const inspectConfigYAML = `
defaults:
  preset: full
  min_confidence: 0.5
  inspect_enabled: true
systems:
  alfasonar:
    enabled: true
    auth: none
    types: [all]
    demask: true
    preset: full
    exclusions:
      public_figures: true
      org_addresses: true
`

// doInspect выполняет запрос разбора и разбирает ответ.
func doInspect(t *testing.T, h http.Handler, body map[string]any) inspectResponse {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("не удалось собрать запрос: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/inspect", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ожидался код 200, получен %d, тело %s", rec.Code, rec.Body.String())
	}
	var out inspectResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("не удалось разобрать ответ: %v", err)
	}
	return out
}
