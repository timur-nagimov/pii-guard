package api

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/logging"
	"pii-guard/internal/mask"
)

// chatRequest — часть запроса к языковой модели, которая нас интересует.
// Остальные поля сохраняются без изменений и уходят к модели как есть.
type chatRequest struct {
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`
	// System выбирает профиль системы-потребителя по имени. Поле нужно странице
	// проверки, которая переключает профили, не зная ключей; модели оно не
	// передаётся.
	System string         `json:"system,omitempty"`
	rest   map[string]any `json:"-"`
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
	begin := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "поддерживается только POST")
		return
	}

	cfg := s.Config()

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
	// Поле system — выбор профиля для страницы проверки, модели оно не нужно.
	delete(raw, "system")

	// Страница проверки выбирает профиль системы по имени; ключ доступа ей не
	// нужен. Имя задано — подменяем систему, имени нет — остаёмся на системе,
	// опознанной по ключу.
	sys, ok := s.resolveRequestSystem(r, cfg, req.System)
	if !ok {
		s.writeError(w, r, http.StatusForbidden, "system_not_allowed", "система не опознана или отключена")
		return
	}
	if sys.Upstream.URL == "" {
		s.writeError(w, r, http.StatusNotImplemented, "upstream_not_configured", "для системы не задан адрес языковой модели")
		return
	}
	s.extendWriteDeadline(w, r, sys)

	// Маскируем текст каждого сообщения и запоминаем подстановки.
	opts := sys.MaskOptions(cfg.Defaults)
	if opts.Default != mask.PresetToken {
		// Для обращения к модели годится только пресет с плейсхолдерами.
		opts = mask.Options{Default: mask.PresetToken, PerType: nil}
	}
	back := make(map[string]string)
	counts := make(map[string]int)
	masked := make([]chatMessage, len(req.Messages))
	// Счётчик плейсхолдеров общий на весь запрос: иначе в каждом сообщении
	// первый телефон получал бы [PHONE_1], и разные значения столкнулись бы в
	// одной подстановке, а обратное преобразование подставило бы одно последнее.
	shared := &mask.TokenState{}
	for i, msg := range req.Messages {
		text, isString := decodeContent(msg.Content)
		if !isString {
			masked[i] = msg
			continue
		}
		res := s.engine.Mask(text, sys, cfg.Defaults)
		for t, n := range res.Counts {
			counts[string(t)] += n
		}
		opts.Shared = shared
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
	// Потоковый режим у модели не запрашиваем никогда: ответ нужен целиком,
	// чтобы вернуть в нём исходные значения. Поле задаётся явно и всегда,
	// потому что платформа отвергает запрос без него четырёхсотым кодом
	// с пустым телом [проверено на живом обращении].
	raw["stream"] = false
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
		s.log.WarnContext(r.Context(), "языковая модель недоступна",
			logging.Event(logging.EventProxy), logging.Component("proxy"), logging.Err(err))
		s.writeError(w, r, http.StatusBadGateway, "upstream_unavailable", "языковая модель недоступна")
		// Текст уже ушёл модели, поэтому обращение к персональным данным
		// состоялось независимо от того, дождались мы ответа или нет.
		s.auditProcess(r, sys, "", "proxy", len(body), counts, time.Since(begin), "error")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, "upstream_unavailable", "не удалось прочитать ответ модели")
		return
	}
	s.metrics.ObserveUpstream(sys.Name, itoa(resp.StatusCode), time.Since(started))

	// Модель может отказать с пустым телом: так, например, выглядит ответ на
	// неверный адрес ручки. Пересылать пустоту нельзя, по ней причину понять
	// невозможно, а вызывающий видит только «модель не ответила». Подставляем
	// объяснимый ответ с кодом, который она вернула.
	if resp.StatusCode >= 400 && len(bytes.TrimSpace(respBody)) == 0 {
		s.log.WarnContext(r.Context(), "модель отказала без объяснения",
			logging.Event(logging.EventProxy), logging.Component("proxy"),
			slog.Int("upstream_status", resp.StatusCode))
		s.writeError(w, r, resp.StatusCode, "upstream_rejected",
			"языковая модель отказала кодом "+itoa(resp.StatusCode)+" без объяснения; "+
				"проверьте адрес и ключ доступа в разделе upstream настроек системы")
		s.auditProcess(r, sys, "", "proxy", len(body), counts, time.Since(begin), "error")
		return
	}

	restored := restorePlaceholders(string(respBody), back)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-PII-Masked", itoa(len(back)))
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write([]byte(restored)); err != nil {
		s.log.WarnContext(r.Context(), "не удалось записать ответ",
			logging.Event(logging.EventProxy), logging.Component("proxy"), logging.Err(err))
	}
	s.auditProcess(r, sys, "", "proxy", len(body), counts, time.Since(begin), "ok")
}

// defaultUpstreamTimeout — срок ожидания ответа модели, когда он не задан
// настройками.
const defaultUpstreamTimeout = 60 * time.Second

// proxyWriteMargin — запас поверх срока ожидания модели: за это время нужно
// вернуть в ответ исходные значения и записать его клиенту.
const proxyWriteMargin = 5 * time.Second

// extendWriteDeadline продлевает срок записи ответа на этом соединении.
//
// Общий срок записи у сервера равен девяти секундам, и он выбран под контракт
// /process: девять короче десятисекундного таймаута проверяющей системы,
// поэтому обрыв остаётся решением сервиса, а не клиента. На пути прокси тот же
// срок переворачивает порядок: модель по настройкам отвечает до шестидесяти
// секунд, и ответ, пришедший на десятой, записать клиенту было бы уже нечем —
// соединение сервер закрыл бы сам.
//
// http.ResponseController меняет срок на одном соединении и только на текущий
// запрос: следующий запрос по тому же соединению снова получит общий срок
// сервера. Так обе цепочки живут со своими сроками, и общий срок сервера
// менять не приходится.
func (s *Server) extendWriteDeadline(w http.ResponseWriter, r *http.Request, sys config.System) {
	timeout := sys.Upstream.Timeout
	if timeout <= 0 {
		timeout = defaultUpstreamTimeout
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + proxyWriteMargin)); err != nil {
		// Не смертельно: сервис останется с общим сроком записи. Но знать об
		// этом надо, потому что длинные ответы модели будут обрываться.
		s.log.WarnContext(r.Context(), "не удалось продлить срок записи ответа",
			logging.Event(logging.EventProxy), logging.Component("proxy"), logging.Err(err))
	}
}

// upstreamChatURL приводит адрес модели к полному адресу ручки чата.
//
// В настройках адрес задают по-разному, и все формы встречаются в живых
// инструкциях: базовый адрес площадки, он же с версией, и сразу полный путь
// ручки. Прежний код просто дописывал суффикс, поэтому полный путь удваивался
// и превращался в «.../chat/completions/chat/completions». Модель отвечала на
// это кодом 401 с пустым телом, и понять причину по такому ответу нельзя.
//
// Разбираем все три формы:
//
//	https://host/continue-dev            → https://host/continue-dev/v1/chat/completions
//	https://host/continue-dev/v1         → https://host/continue-dev/v1/chat/completions
//	https://host/continue-dev/v1/chat/completions → без изменений
func upstreamChatURL(raw string) string {
	base := strings.TrimRight(raw, "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/chat/completions"
}

// callUpstream отправляет подготовленный запрос языковой модели.
func (s *Server) callUpstream(r *http.Request, sys config.System, body []byte) (*http.Response, error) {
	timeout := sys.Upstream.Timeout
	if timeout <= 0 {
		timeout = defaultUpstreamTimeout
	}
	url := upstreamChatURL(sys.Upstream.URL)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Обращение к модели должно быть видно в её журнале под тем же номером,
	// под которым запрос клиента виден у нас.
	logging.Propagate(r.Context(), req)
	if sys.Upstream.TokenEnv != "" {
		if token := os.Getenv(sys.Upstream.TokenEnv); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return s.upstreamClient(sys, timeout).Do(req)
}

// upstreamClient возвращает клиента для обращения к языковой модели, при
// необходимости с дополнительным корневым сертификатом. Клиенты кешируются по
// имени системы: создавать транспорт на каждый запрос дорого и мешает
// переиспользованию соединений.
func (s *Server) upstreamClient(sys config.System, timeout time.Duration) *http.Client {
	if v, ok := s.upstreams.Load(sys.Name); ok {
		if c, valid := v.(*http.Client); valid {
			return c
		}
	}
	client := &http.Client{Timeout: timeout}
	if sys.Upstream.PinSHA256 != "" {
		client.Transport = &http.Transport{
			TLSClientConfig:     pinnedTLSConfig(sys.Upstream.PinSHA256),
			MaxIdleConnsPerHost: 64,
		}
		s.upstreams.Store(sys.Name, client)
		return client
	}
	if sys.Upstream.CAFile != "" {
		pool, err := caPool(sys.Upstream.CAFile)
		if err != nil {
			s.log.Warn("не удалось прочитать корневой сертификат, используется системное хранилище",
				"system", sys.Name, "error", err.Error())
		} else {
			client.Transport = &http.Transport{
				TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
				MaxIdleConnsPerHost: 64,
			}
		}
	}
	s.upstreams.Store(sys.Name, client)
	return client
}

// pinnedTLSConfig строит настройки соединения, где подлинность сервера
// проверяется закреплённым отпечатком открытого ключа, а не цепочкой доверия.
//
// Так сделано потому, что внутренние ресурсы банка подписаны удостоверяющим
// центром, которого нет ни в системном хранилище, ни в открытом доступе с
// машины разработчика. Отпечаток снимается один раз и кладётся в настройки:
// это строже проверки по цепочке, поскольку принимается ровно один ключ.
func pinnedTLSConfig(pin string) *tls.Config {
	return &tls.Config{
		// Штатная проверка отключена намеренно: вместо неё ниже сравнивается
		// отпечаток ключа, и соединение с чужим сервером будет отвергнуто.
		InsecureSkipVerify: true, //nolint:gosec // подлинность проверяется закреплённым отпечатком
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			for _, raw := range rawCerts {
				cert, err := x509.ParseCertificate(raw)
				if err != nil {
					continue
				}
				spki, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
				if err != nil {
					continue
				}
				sum := sha256.Sum256(spki)
				if base64.StdEncoding.EncodeToString(sum[:]) == pin {
					return nil
				}
			}
			return errors.New("отпечаток ключа сервера не совпал с закреплённым")
		},
	}
}

// caPool читает файл с сертификатами и собирает из них хранилище доверия,
// дополняя системное.
func caPool(path string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // путь задаёт оператор сервиса
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil, errors.New("в файле нет ни одного сертификата")
	}
	return pool, nil
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

	// Замена идёт ОДНИМ проходом, а не по очереди для каждого плейсхолдера.
	// Прежний способ переписывал уже изменённый текст, из-за чего значение,
	// подставленное вместо одного плейсхолдера, попадало под замену
	// следующего: данные одного человека искажались подстановкой другого.
	// Вдобавок порядок обхода карты в Go случаен, поэтому ответ модели был
	// невоспроизводим между одинаковыми запросами.
	//
	// strings.Replacer проходит текст один раз и подставленное не перечитывает.
	// Порядок пар задаётся явно: сначала длинные образцы, потом короткие, а при
	// равной длине по алфавиту. Это нужно, чтобы короткий образец не съедал
	// начало длинного и чтобы результат не зависел от карты.
	type pair struct{ from, to string }
	var pairs []pair

	for token, value := range back {
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		quoted := strings.TrimSuffix(strings.TrimPrefix(string(encoded), "\""), "\"")

		inner := strings.TrimSuffix(strings.TrimPrefix(token, "["), "]")
		for _, variant := range []string{
			token,
			"[" + strings.ReplaceAll(inner, "_", " ") + "]",
			"<" + inner + ">",
			"{" + inner + "}",
			inner,
		} {
			if variant == "" {
				continue
			}
			pairs = append(pairs, pair{variant, quoted})
		}
	}
	if len(pairs) == 0 {
		return text
	}

	sort.Slice(pairs, func(i, j int) bool {
		if len(pairs[i].from) != len(pairs[j].from) {
			return len(pairs[i].from) > len(pairs[j].from)
		}
		return pairs[i].from < pairs[j].from
	})

	flat := make([]string, 0, len(pairs)*2)
	seen := make(map[string]bool, len(pairs))
	for _, pr := range pairs {
		if seen[pr.from] {
			continue // один образец не должен встречаться дважды
		}
		seen[pr.from] = true
		flat = append(flat, pr.from, pr.to)
	}
	return strings.NewReplacer(flat...).Replace(text)
}
