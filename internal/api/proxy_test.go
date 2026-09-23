package api

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// TestUpstreamChatURL закрепляет разбор всех форм адреса модели, которые
// встречаются в живых инструкциях. Дефект был настоящим: полный путь
// удваивался, модель отвечала кодом 401 с пустым телом, и причину по такому
// ответу понять нельзя.
func TestUpstreamChatURL(t *testing.T) {
	const want = "https://alfagen.alfabank.ru/continue-dev/v1/chat/completions"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"базовый адрес площадки", "https://alfagen.alfabank.ru/continue-dev", want},
		{"базовый с косой чертой", "https://alfagen.alfabank.ru/continue-dev/", want},
		{"адрес с версией", "https://alfagen.alfabank.ru/continue-dev/v1", want},
		{"адрес с версией и чертой", "https://alfagen.alfabank.ru/continue-dev/v1/", want},
		{"сразу полный путь ручки", want, want},
		{"полный путь с чертой", want + "/", want},
		{"пустой адрес", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := upstreamChatURL(c.in); got != c.want {
				t.Errorf("из %q получено %q, ожидалось %q", c.in, got, c.want)
			}
		})
	}
}

// TestProxyExplainsEmptyUpstreamError проверяет, что отказ модели с пустым
// телом не пересылается как есть. Такой ответ приходит, например, на неверный
// адрес ручки, и по нему причину понять невозможно: вызывающий видит пустоту
// и код, а на странице это выглядит как «модель не ответила».
func TestProxyExplainsEmptyUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // тело пустое, как у живой модели
	}))
	defer upstream.Close()

	ts := httptest.NewServer(newProxyServer(t, upstream.URL))
	defer ts.Close()

	body := `{"messages":[{"role":"user","content":"тел +7 916 123-45-67"}]}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-System-Key", "test-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("запрос не прошёл: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)
	if len(bytes.TrimSpace(raw)) == 0 {
		t.Fatal("пустой отказ модели переслан как есть, причину понять нельзя")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("код ответа %d, ожидался тот же, что у модели: 401", resp.StatusCode)
	}
	if !strings.Contains(string(raw), "upstream_rejected") {
		t.Errorf("в ответе нет опознаваемой причины: %s", raw)
	}
}

// TestProxyMasksArrayContent проверяет, что сообщение, у которого content задан
// массивом частей (формат OpenAI для текста и картинок), маскируется по
// текстовым частям: до модели не доходит ни одно исходное значение, а в ответе
// плейсхолдер восстанавливается.
func TestProxyMasksArrayContent(t *testing.T) {
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
			{"role": "user", "content": []map[string]any{
				{"type": "text", "text": "Клиент Иванов Иван Иванович"},
				{"type": "image_url", "image_url": map[string]any{"url": "https://example.ru/photo.jpg"}},
			}},
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
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("сообщения не дошли до модели: %v", gotBody)
	}
	first := msgs[0].(map[string]any)
	parts, ok := first["content"].([]any)
	if !ok {
		t.Fatalf("content не остался массивом частей: %v", first["content"])
	}
	textPart := parts[0].(map[string]any)
	text, _ := textPart["text"].(string)
	if strings.Contains(text, "Иванов") {
		t.Errorf("исходное значение ушло модели в текстовой части: %q", text)
	}
	if !strings.Contains(text, "[FIO_1]") {
		t.Errorf("текстовая часть не замаскирована: %q", text)
	}
	if !strings.Contains(rec.Body.String(), "Иванов Иван Иванович") {
		t.Errorf("значение не восстановлено в ответе: %s", rec.Body.String())
	}
}

// TestProxyMasksToolsAndResponseFormat проверяет, что текст внутри
// tools[].function.description и в response_format маскируется: до модели не
// доходит ни одно исходное значение.
func TestProxyMasksToolsAndResponseFormat(t *testing.T) {
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"готово"}}]}`))
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "привет"},
		},
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{
				"name":        "find_client",
				"description": "Найти клиента Иванов Иван Иванович по телефону +79161234567",
			}},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":        "client",
				"description": "Данные клиента Иванов Иван Иванович",
			},
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
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools не дошли до модели: %v", gotBody)
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	desc, _ := fn["description"].(string)
	if strings.Contains(desc, "Иванов") || strings.Contains(desc, "+79161234567") {
		t.Errorf("исходное значение ушло модели в описании инструмента: %q", desc)
	}
	rf, ok := gotBody["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format не дошёл до модели: %v", gotBody)
	}
	js := rf["json_schema"].(map[string]any)
	rfDesc, _ := js["description"].(string)
	if strings.Contains(rfDesc, "Иванов") {
		t.Errorf("исходное значение ушло модели в response_format: %q", rfDesc)
	}
}

// TestProxyRestoreIgnoresServiceFields проверяет, что восстановление плейсхолдеров
// идёт только по текстовым полям ответа и не трогает служебные поля id и model,
// даже если в них специально положен текст, похожий на плейсхолдер.
func TestProxyRestoreIgnoresServiceFields(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"[FIO_1]","model":"[FIO_1]","choices":[{"message":{"role":"assistant","content":"[FIO_1] остался"}}]}`))
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "Клиент Иванов Иван Иванович"},
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
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("ответ не разобран: %v", err)
	}
	if id, _ := out["id"].(string); id != "[FIO_1]" {
		t.Errorf("служебное поле id изменено восстановлением: %q", id)
	}
	if model, _ := out["model"].(string); model != "[FIO_1]" {
		t.Errorf("служебное поле model изменено восстановлением: %q", model)
	}
	content := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if !strings.Contains(content, "Иванов Иван Иванович") {
		t.Errorf("значение не восстановлено в тексте ответа: %q", content)
	}
}

// TestProxyStreamNotSupported проверяет, что потоковый режим не превращается
// молча в одиночный ответ, а возвращает понятный отказ с кодом
// stream_not_supported.
func TestProxyStreamNotSupported(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "привет"}},
		"stream":   true,
		"system":   "kilo",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ожидался код 400, получен %d, тело %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "stream_not_supported") {
		t.Errorf("в ответе нет кода stream_not_supported: %s", rec.Body.String())
	}
}

// TestProxyConversationRestore проверяет многоходовой диалог: при наличии
// заголовка X-Conversation-Id соответствия плейсхолдеров сохраняются в
// хранилище, и плейсхолдер из первого ответа восстанавливается во втором.
func TestProxyConversationRestore(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[FIO_1] остался"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"снова [FIO_1]"}}]}`))
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)

	first, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "Клиент Иванов Иван Иванович"},
		},
		"system": "kilo",
	})
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(first))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-Conversation-Id", "dialog-1")
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("первый запрос: ожидался код 200, получен %d, тело %s", rec1.Code, rec1.Body.String())
	}
	if !strings.Contains(rec1.Body.String(), "Иванов Иван Иванович") {
		t.Fatalf("первый ответ не восстановлен: %s", rec1.Body.String())
	}

	second, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{
			{"role": "user", "content": "что вы сказали про клиента?"},
			{"role": "assistant", "content": "[FIO_1] остался"},
		},
		"system": "kilo",
	})
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(second))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Conversation-Id", "dialog-1")
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("второй запрос: ожидался код 200, получен %d, тело %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "Иванов Иван Иванович") {
		t.Errorf("плейсхолдер из первого ответа не восстановлен во втором: %s", rec2.Body.String())
	}
}

// TestRestorePlaceholdersDeterministic закрепляет два свойства восстановления.
//
// Первое: результат не зависит от порядка обхода карты. Карта в Go обходится
// в случайном порядке, а замены шли по уже изменённому тексту, поэтому при
// пересечении токенов ответ модели был невоспроизводим между запросами.
//
// Второе, важнее: значение, уже подставленное вместо одного плейсхолдера, не
// должно попадать под замену следующего. Иначе данные одного человека
// оказываются искажены подстановкой другого.
func TestRestorePlaceholdersDeterministic(t *testing.T) {
	// Значение ФИО само содержит токен телефона: так выглядит текст, где
	// модель перепутала границы или где имя действительно совпало с токеном.
	back := map[string]string{
		"[FIO_1]":   "Иванов [PHONE_1] Иванович",
		"[PHONE_1]": "+7 916 123-45-67",
		"[CARD_1]":  "4111 1111 1111 1111",
	}
	const text = `{"content":"Клиент [FIO_1], телефон [PHONE_1], карта [CARD_1]"}`

	first := restorePlaceholders(text, back)
	for i := 0; i < 50; i++ {
		if got := restorePlaceholders(text, back); got != first {
			t.Fatalf("результат зависит от порядка обхода карты:\n  %s\n  %s", first, got)
		}
	}

	// Подставленное значение ФИО обязано сохранить токен телефона внутри себя:
	// это чужой текст, а не место для подстановки.
	if !strings.Contains(first, "Иванов [PHONE_1] Иванович") {
		t.Errorf("значение одного плейсхолдера искажено подстановкой другого: %s", first)
	}
	if !strings.Contains(first, "4111 1111 1111 1111") {
		t.Errorf("карта не восстановлена: %s", first)
	}
}

// TestProxyMethodNotAllowed проверяет, что ручка прокси принимает только POST.
func TestProxyMethodNotAllowed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("ожидался код 405, получен %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "method_not_allowed") {
		t.Errorf("в ответе нет кода method_not_allowed: %s", rec.Body.String())
	}
}

// TestProxyPayloadTooLarge проверяет, что тело больше допустимого размера
// отклоняется кодом 413.
func TestProxyPayloadTooLarge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)
	big := strings.Repeat("а", 5<<20)
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": big}},
		"system":   "kilo",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("ожидался код 413, получен %d, тело %s", rec.Code, rec.Body.String())
	}
}

// TestProxyInvalidJSON проверяет, что неразбираемое тело отклоняется кодом 422.
func TestProxyInvalidJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("не json"))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ожидался код 422, получен %d, тело %s", rec.Code, rec.Body.String())
	}
}

// TestProxyUpstreamNotConfigured проверяет, что система без адреса модели
// отклоняется кодом 501.
func TestProxyUpstreamNotConfigured(t *testing.T) {
	cfg, err := config.Parse([]byte(`
defaults:
  preset: token
  min_confidence: 0.5
systems:
  kilo:
    enabled: true
    auth:
      header: X-System-Key
      key_sha256: "62af8704764faf8ea82fc61ce9c4c3908b6cb97d463a634e9e587d7c885db0ef"
    types: [all]
    preset: token
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
	reg.Register(pii.NewFIODetector(), pii.NewNumericDetector())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(cfg, st, engine.New(reg), metrics.New(), log).Routes()

	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "привет"}},
		"system":   "kilo",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("ожидался код 501, получен %d, тело %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_not_configured") {
		t.Errorf("в ответе нет кода upstream_not_configured: %s", rec.Body.String())
	}
}

// TestProxyUpstreamError проверяет, что отказ модели пересылается клиенту
// кодом 502, а не падает.
func TestProxyUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"сбой модели"}`))
	}))
	defer upstream.Close()

	h := newProxyServer(t, upstream.URL)
	body, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "привет"}},
		"system":   "kilo",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("ожидался код 500, получен %d, тело %s", rec.Code, rec.Body.String())
	}
}

// TestPinnedTLSConfig проверяет, что проверка отпечатка ключа принимает
// сертификат с совпадающим отпечатком и отвергает чужой.
func TestPinnedTLSConfig(t *testing.T) {
	cert, key := testCert(t)
	spki, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		t.Fatalf("не удалось снять отпечаток ключа: %v", err)
	}
	sum := sha256.Sum256(spki)
	pin := base64.StdEncoding.EncodeToString(sum[:])

	cfg := pinnedTLSConfig(pin)
	if cfg.InsecureSkipVerify != true {
		t.Error("проверка по цепочке должна быть отключена при закреплённом отпечатке")
	}
	if err := cfg.VerifyPeerCertificate([][]byte{cert.Raw}, nil); err != nil {
		t.Errorf("совпадающий отпечаток отвергнут: %v", err)
	}
	wrong := pinnedTLSConfig(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := wrong.VerifyPeerCertificate([][]byte{cert.Raw}, nil); err == nil {
		t.Error("чужой отпечаток принят, а должен быть отвергнут")
	}
}

// TestCAPool проверяет, что caPool собирает хранилище доверия из файла с
// сертификатом и отклоняет файл без сертификатов.
func TestCAPool(t *testing.T) {
	cert, _ := testCert(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatalf("не удалось записать сертификат: %v", err)
	}
	pool, err := caPool(path)
	if err != nil {
		t.Fatalf("caPool не собрал хранилище: %v", err)
	}
	if pool == nil {
		t.Fatal("хранилище доверия пустое")
	}

	if _, err := caPool(filepath.Join(dir, "нет-такого-файла")); err == nil {
		t.Error("чтение несуществующего файла не вернуло ошибку")
	}
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("не сертификат"), 0o600); err != nil {
		t.Fatalf("не удалось записать файл: %v", err)
	}
	if _, err := caPool(bad); err == nil {
		t.Error("файл без сертификатов не вернул ошибку")
	}
}

// TestUpstreamClientCAFile проверяет, что клиент с корневым сертификатом
// собирается и кешируется по имени системы.
func TestUpstreamClientCAFile(t *testing.T) {
	cert, _ := testCert(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatalf("не удалось записать сертификат: %v", err)
	}

	cfg, err := config.Parse([]byte(proxyConfigYAML("https://example.ru")))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 1000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)
	reg := pii.NewRegistry()
	reg.Register(pii.NewFIODetector())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, engine.New(reg), metrics.New(), log)

	sys := cfg.Systems["kilo"]
	sys.Upstream.CAFile = path
	first := srv.upstreamClient(sys, time.Second)
	second := srv.upstreamClient(sys, time.Second)
	if first != second {
		t.Error("клиент не закеширован по имени системы")
	}
	if first.Transport == nil {
		t.Error("транспорт с корневым сертификатом не собран")
	}
}

// testCert создаёт самоподписанный сертификат и его ключ для тестов TLS.
func testCert(t *testing.T) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("не удалось создать ключ: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "тест"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("не удалось создать сертификат: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("не удалось разобрать сертификат: %v", err)
	}
	return cert, key
}
