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

// TestProxyStripsSystemField проверяет, что выбор профиля для страницы не
// уходит к модели: поле system вырезается из запроса, а замаскированный текст
// проходит насквозь, потому что персональных данных в нём уже нет.
func TestProxyStripsSystemField(t *testing.T) {
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[FIO_1] остался"}}]}`))
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "Клиент [FIO_1], тел [PHONE_1]"},
		},
		"system": "kilo",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ожидался код 200, получен %d, тело %s", rec.Code, rec.Body.String())
	}
	if _, ok := gotBody["system"]; ok {
		t.Error("поле system ушло к модели, а должно быть вырезано")
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("сообщения не дошли до модели: %v", gotBody)
	}
	first := msgs[0].(map[string]any)
	content, _ := first["content"].(string)
	if !strings.Contains(content, "[FIO_1]") {
		t.Errorf("замаскированный текст не прошёл насквозь: %q", content)
	}
}

// TestProxyUnknownSystemRejected проверяет, что неизвестное имя системы в
// запросе прокси — отказ, а не обращение к модели.
func TestProxyUnknownSystemRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "привет"}},
		"system":   "неттакой",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("ожидался код 403, получен %d, тело %s", rec.Code, rec.Body.String())
	}
}

// newProxyServer поднимает сервер с системой, у которой задан адрес модели.
func newProxyServer(t *testing.T, upstreamURL string) http.Handler {
	t.Helper()

	cfg, err := config.Parse([]byte(proxyConfigYAML(upstreamURL)))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 1000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	reg := pii.NewRegistry()
	reg.Register(pii.NewFIODetector(), pii.NewNumericDetector())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, st, engine.New(reg), metrics.New(), log).Routes()
}

func proxyConfigYAML(upstreamURL string) string {
	return `
defaults:
  preset: token
  min_confidence: 0.5
  inspect_enabled: true
systems:
  kilo:
    enabled: true
    auth:
      header: X-System-Key
      key_sha256: "62af8704764faf8ea82fc61ce9c4c3908b6cb97d463a634e9e587d7c885db0ef"
    types: [all]
    demask: true
    preset: token
    upstream:
      url: "` + upstreamURL + `"
`
}

// TestProxyPlaceholderCollision проверяет, что разные значения одного типа в
// разных сообщениях одного запроса получают разные плейсхолдеры. Раньше
// счётчик плейсхолдеров начинался с единицы в каждом сообщении, оба телефона
// получали [PHONE_1], и обратное преобразование подставляло вместо обоих одно
// последнее значение.
func TestProxyPlaceholderCollision(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		msgs := req["messages"].([]any)
		var sb strings.Builder
		for _, m := range msgs {
			mm := m.(map[string]any)
			sb.WriteString(mm["content"].(string))
			sb.WriteString(" ")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": sb.String()}}},
		})
	}))
	defer upstream.Close()

	cfg, err := config.Parse([]byte(`
server:
  max_body_bytes: 65536
limits:
  inflight: 16
  heavy_inflight: 4
  heavy_threshold_bytes: 65536
  max_wait: 2s
store:
  ttl: 60m
defaults:
  preset: token
  min_confidence: 0.5
systems:
  proxy:
    enabled: true
    auth:
      header: X-Key
      key_sha256: "` + hashKey("proxy-key") + `"
    types: [all]
    preset: token
    upstream:
      url: ` + upstream.URL + `
`))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 1000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)
	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, engine.New(reg), metrics.New(), log)
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	body := `{"messages":[{"role":"user","content":"телефон +79161234567"},{"role":"user","content":"телефон +79990001122"}]}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Key", "proxy-key")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос не выполнился: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код %d: %s", resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("ответ не разобран: %v", err)
	}
	content := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "+79161234567") || !strings.Contains(content, "+79990001122") {
		t.Fatalf("значения не восстановлены: %q", content)
	}
}
