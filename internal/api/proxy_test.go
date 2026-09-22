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
