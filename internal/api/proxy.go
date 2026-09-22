package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/mask"
)

// chatRequest — часть запроса к языковой модели, которая нас интересует.
// Остальные поля сохраняются без изменений и уходят к модели как есть.
type chatRequest struct {
	Messages []chatMessage  `json:"messages"`
	Stream   bool           `json:"stream,omitempty"`
	rest     map[string]any `json:"-"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// handleChatCompletions работает как прокси перед языковой моделью: маскирует
// персональные данные в запросе, передаёт его модели и восстанавливает
// исходные значения в ответе.
//
// В этом режиме используется пресет с плейсхолдерами: инициалы и звёздочки
// модель в ответе не сохраняет, а типизированный плейсхолдер переживает
// пересказ и позволяет вернуть пользователю настоящее значение.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "поддерживается только POST")
		return
	}

	cfg := s.Config()
	sys, ok := s.resolveSystem(r, cfg)
	if !ok {
		s.writeError(w, r, http.StatusForbidden, "system_not_allowed", "система не опознана или отключена")
		return
	}
	if sys.Upstream.URL == "" {
		s.writeError(w, r, http.StatusNotImplemented, "upstream_not_configured", "для системы не задан адрес языковой модели")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cfg.Server.MaxBodyBytes))
	if err != nil {
		s.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "тело запроса превышает допустимый размер")
		return
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		s.writeValidation(w, r, "json_invalid", []string{"body"}, "не удалось разобрать JSON")
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeValidation(w, r, "json_invalid", []string{"body", "messages"}, "не удалось разобрать список сообщений")
		return
	}

	// Маскируем текст каждого сообщения и запоминаем подстановки.
	opts := sys.MaskOptions(cfg.Defaults)
	if opts.Default != mask.PresetToken {
		// Для обращения к модели годится только пресет с плейсхолдерами.
		opts = mask.Options{Default: mask.PresetToken, PerType: nil}
	}
	back := make(map[string]string)
	masked := make([]chatMessage, len(req.Messages))
	for i, msg := range req.Messages {
		text, isString := decodeContent(msg.Content)
		if !isString {
			masked[i] = msg
			continue
		}
		res := s.engine.Mask(text, sys, cfg.Defaults)
		applied := mask.Apply(text, res.Spans, opts)
		for _, ph := range applied.Placeholders {
			back[ph.Token] = ph.Value
		}
		encoded, err := json.Marshal(applied.Text)
		if err != nil {
			s.writeError(w, r, http.StatusServiceUnavailable, "internal_degraded", "не удалось подготовить запрос")
			return
		}
		masked[i] = chatMessage{Role: msg.Role, Content: encoded}
	}
	raw["messages"] = masked
	if req.Stream {
		// Потоковый режим у модели не запрашиваем: ответ нужно целиком, чтобы
		// вернуть в нём исходные значения.
		raw["stream"] = false
	}
	if sys.Upstream.Model != "" {
		raw["model"] = sys.Upstream.Model
	}

	outBody, err := json.Marshal(raw)
	if err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "internal_degraded", "не удалось подготовить запрос")
		return
	}

	started := time.Now()
	resp, err := s.callUpstream(r, sys, outBody)
	if err != nil {
		s.metrics.ObserveUpstream(sys.Name, "error", time.Since(started))
		s.log.Warn("языковая модель недоступна", "system", sys.Name, "error", err.Error())
		s.writeError(w, r, http.StatusBadGateway, "upstream_unavailable", "языковая модель недоступна")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, "upstream_unavailable", "не удалось прочитать ответ модели")
		return
	}
	s.metrics.ObserveUpstream(sys.Name, itoa(resp.StatusCode), time.Since(started))

	restored := restorePlaceholders(string(respBody), back)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-PII-Masked", itoa(len(back)))
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write([]byte(restored)); err != nil {
		s.log.Warn("не удалось записать ответ", "error", err.Error())
	}
}

// callUpstream отправляет подготовленный запрос языковой модели.
func (s *Server) callUpstream(r *http.Request, sys config.System, body []byte) (*http.Response, error) {
	timeout := sys.Upstream.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	url := strings.TrimRight(sys.Upstream.URL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if sys.Upstream.TokenEnv != "" {
		if token := os.Getenv(sys.Upstream.TokenEnv); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	client := &http.Client{Timeout: timeout}
	return client.Do(req)
}

// decodeContent достаёт текст сообщения, если он задан строкой.
func decodeContent(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, true
	}
	return "", false
}

// restorePlaceholders возвращает в текст ответа исходные значения. Разбор
// терпим к искажениям: модель может изменить регистр или оформление
// плейсхолдера. Нераспознанный плейсхолдер остаётся как есть — выдумывать
// персональные данные нельзя.
func restorePlaceholders(text string, back map[string]string) string {
	if len(back) == 0 {
		return text
	}
	out := text
	for token, value := range back {
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		quoted := string(encoded)
		quoted = strings.TrimPrefix(quoted, "\"")
		quoted = strings.TrimSuffix(quoted, "\"")

		inner := strings.TrimSuffix(strings.TrimPrefix(token, "["), "]")
		for _, variant := range []string{
			token,
			"[" + strings.ReplaceAll(inner, "_", " ") + "]",
			"<" + inner + ">",
			"{" + inner + "}",
			inner,
		} {
			out = strings.ReplaceAll(out, variant, quoted)
		}
	}
	return out
}
