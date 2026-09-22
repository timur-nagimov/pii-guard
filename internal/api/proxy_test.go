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
