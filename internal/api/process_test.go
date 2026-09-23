package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Ключи систем-потребителей для тестов. В настройки попадают только их хеши,
// сами ключи живут рядом с тестом и никуда не уходят.
const (
	testKeyDemask    = "ключ-системы-с-демаскированием"
	testKeyNoDemask  = "ключ-системы-без-демаскирования"
	testKeySynthetic = "ключ-системы-с-подстановкой"
	maxBodyBytes     = 4096
)

// hashKey повторяет способ, которым сервис сверяет ключ доступа.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// testConfigYAML собирает настройки с тремя системами: анонимной, которой
// отвечает проверяющая система, и двумя ключевыми с разными правами.
func testConfigYAML() string {
	return `
server:
  max_body_bytes: 4096
limits:
  inflight: 16
  heavy_inflight: 4
  heavy_threshold_bytes: 65536
  max_wait: 2s
store:
  ttl: 60m
defaults:
  preset: full
  min_confidence: 0.5
systems:
  alfasonar:
    enabled: true
    auth: none
    types: [all]
    demask: true
    preset: full
    on_error: open
    on_unknown_id: passthrough
  partner:
    enabled: true
    auth:
      header: X-System-Key
      key_sha256: "` + hashKey(testKeyDemask) + `"
    types: [all]
    demask: true
    preset: full
  nodemask:
    enabled: true
    auth:
      header: X-Partner-Key
      key_sha256: "` + hashKey(testKeyNoDemask) + `"
    types: [all]
    demask: false
    preset: full
`
}

// newTestServer поднимает сервис целиком на случайном порту: обработчик
// проверяется вместе с маршрутизацией, перехватом сбоев и ограничителями.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	cfg, err := config.Parse([]byte(testConfigYAML()))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}

	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 10000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector())

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, engine.New(reg), metrics.New(), log)

	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

// newTestServerWithTTL поднимает сервис целиком с заданным сроком жизни записи.
// Нужен для проверки поведения после истечения срока хранения.
func newTestServerWithTTL(t *testing.T, ttl time.Duration) *httptest.Server {
	t.Helper()

	cfg, err := config.Parse([]byte(testConfigYAML()))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}

	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: ttl, MaxRecords: 10000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector())

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, engine.New(reg), metrics.New(), log)

	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

// processCall описывает один запрос к сервису.
type processCall struct {
	method  string
	body    string
	headers map[string]string
}

// do отправляет запрос и возвращает код ответа вместе с телом. Тело читается
// целиком, чтобы соединение переиспользовалось и тест не ловил лишних ошибок.
func do(t *testing.T, ts *httptest.Server, call processCall) (int, string) {
	t.Helper()
	method := call.method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(t.Context(), method, ts.URL+"/process", strings.NewReader(call.body))
	if err != nil {
		t.Fatalf("не удалось собрать запрос: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range call.headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос не выполнился: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("не удалось прочитать ответ: %v", err)
	}
	if resp.StatusCode >= 500 && resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("сервис ответил кодом %d, пятисотые коды недопустимы: %s", resp.StatusCode, raw)
	}
	if resp.StatusCode == http.StatusInternalServerError {
		t.Fatalf("сервис ответил кодом 500: %s", raw)
	}
	return resp.StatusCode, string(raw)
}

// processBody собирает тело запроса контракта.
func processBody(t *testing.T, payload, id string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"payload": payload, "payload_id": id})
	if err != nil {
		t.Fatalf("не удалось собрать тело запроса: %v", err)
	}
	return string(raw)
}

// resultOf разбирает успешный ответ и требует наличия поля result.
func resultOf(t *testing.T, body string) string {
	t.Helper()
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("ответ %q не разобрался: %v", body, err)
	}
	raw, ok := parsed["result"]
	if !ok {
		t.Fatalf("в ответе %q нет поля result", body)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("поле result не строка: %v", err)
	}
	return value
}

// TestProcessLifecycle проверяет полный жизненный цикл: маскирование, повтор
// того же запроса и обратное преобразование по ранее выданной маске.
func TestProcessLifecycle(t *testing.T) {
	ts := newTestServer(t)
	const payload = "Иванов Иван, телефон +79161234567, почта ivan@example.com"
	const id = "payload-1"

	code, body := do(t, ts, processCall{body: processBody(t, payload, id)})
	if code != http.StatusOK {
		t.Fatalf("маскирование ответило кодом %d: %s", code, body)
	}
	masked := resultOf(t, body)
	if masked == payload {
		t.Fatal("текст вернулся без изменений, персональные данные не найдены")
	}
	if strings.Contains(masked, "+79161234567") || strings.Contains(masked, "ivan@example.com") {
		t.Fatalf("персональные данные остались в ответе: %q", masked)
	}

	// Повтор того же запроса обязан дать ту же маску: проверяющая система
	// повторяет запросы при таймаутах, и маска не должна меняться.
	code, body = do(t, ts, processCall{body: processBody(t, payload, id)})
	if code != http.StatusOK {
		t.Fatalf("повтор маскирования ответил кодом %d: %s", code, body)
	}
	if got := resultOf(t, body); got != masked {
		t.Fatalf("повтор дал другую маску %q вместо %q", got, masked)
	}

	// Обратное преобразование: присылаем ранее выданную маску с тем же
	// идентификатором.
	code, body = do(t, ts, processCall{body: processBody(t, masked, id)})
	if code != http.StatusOK {
		t.Fatalf("обратное преобразование ответило кодом %d: %s", code, body)
	}
	if got := resultOf(t, body); got != payload {
		t.Fatalf("восстановлен текст %q вместо %q", got, payload)
	}
}

// TestProcessSyntheticLifecycle проверяет жизненный цикл с видом маскирования
// synthetic: маскирование правдоподобной подстановкой и обратное
// преобразование по ранее выданной подстановке.
func TestProcessSyntheticLifecycle(t *testing.T) {
	cfg, err := config.Parse([]byte(testConfigYAML() + `
  synthetic:
    enabled: true
    auth:
      header: X-Synthetic-Key
      key_sha256: "` + hashKey(testKeySynthetic) + `"
    types: [all]
    demask: true
    preset: synthetic
`))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 10000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)
	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector(), pii.NewFIODetector())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, engine.New(reg), metrics.New(), log)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	const payload = "Иванов Иван, телефон +79161234567"
	const id = "payload-synthetic"

	code, body := do(t, ts, processCall{
		body:    processBody(t, payload, id),
		headers: map[string]string{"X-Synthetic-Key": testKeySynthetic},
	})
	if code != http.StatusOK {
		t.Fatalf("маскирование ответило кодом %d: %s", code, body)
	}
	masked := resultOf(t, body)
	if masked == payload {
		t.Fatal("текст вернулся без изменений, персональные данные не найдены")
	}
	if strings.Contains(masked, "Иванов") || strings.Contains(masked, "+79161234567") {
		t.Fatalf("персональные данные остались в ответе: %q", masked)
	}

	// Обратное преобразование: присылаем ранее выданную подстановку с тем же
	// идентификатором.
	code, body = do(t, ts, processCall{
		body:    processBody(t, masked, id),
		headers: map[string]string{"X-Synthetic-Key": testKeySynthetic},
	})
	if code != http.StatusOK {
		t.Fatalf("обратное преобразование ответило кодом %d: %s", code, body)
	}
	if got := resultOf(t, body); got != payload {
		t.Fatalf("восстановлен текст %q вместо %q", got, payload)
	}
}

// TestProcessDemaskByForeignSystem проверяет, что обратное преобразование
// доступно только той системе, которая маску выдала.
func TestProcessDemaskByForeignSystem(t *testing.T) {
	ts := newTestServer(t)
	const payload = "Телефон +79161234567"
	const id = "payload-foreign"

	_, body := do(t, ts, processCall{body: processBody(t, payload, id)})
	masked := resultOf(t, body)

	code, body := do(t, ts, processCall{
		body:    processBody(t, masked, id),
		headers: map[string]string{"X-System-Key": testKeyDemask},
	})
	if code != http.StatusForbidden {
		t.Fatalf("чужая система получила код %d вместо 403: %s", code, body)
	}
	if strings.Contains(body, "+79161234567") {
		t.Fatalf("исходный текст утёк в ответ об отказе: %s", body)
	}
}

// TestProcessDemaskForbiddenBySettings проверяет систему, которой обратное
// преобразование запрещено настройками.
func TestProcessDemaskForbiddenBySettings(t *testing.T) {
	ts := newTestServer(t)
	const payload = "Телефон +79161234567"
	const id = "payload-nodemask"

	_, body := do(t, ts, processCall{
		body:    processBody(t, payload, id),
		headers: map[string]string{"X-Partner-Key": testKeyNoDemask},
	})
	masked := resultOf(t, body)

	code, body := do(t, ts, processCall{
		body:    processBody(t, masked, id),
		headers: map[string]string{"X-Partner-Key": testKeyNoDemask},
	})
	if code != http.StatusForbidden {
		t.Fatalf("система без права обратного преобразования получила код %d вместо 403: %s", code, body)
	}
}

// TestProcessUnknownID проверяет неизвестный идентификатор: он означает новый
// текст, поэтому сервис маскирует его и отвечает успехом, а не отказом.
func TestProcessUnknownID(t *testing.T) {
	ts := newTestServer(t)
	code, body := do(t, ts, processCall{body: processBody(t, "Телефон +79161234567", "никогда-не-виденный")})
	if code != http.StatusOK {
		t.Fatalf("неизвестный идентификатор дал код %d: %s", code, body)
	}
	if got := resultOf(t, body); strings.Contains(got, "+79161234567") {
		t.Fatalf("текст не замаскирован: %q", got)
	}
}

// TestProcessSameIDDifferentText проверяет, что тот же идентификатор с другим
// текстом не портит сохранённую запись: присланный текст маскируется отдельно.
func TestProcessSameIDDifferentText(t *testing.T) {
	ts := newTestServer(t)
	const id = "payload-ambiguous"
	const first = "Телефон +79161234567"
	const second = "Другой телефон +79990001122"

	_, body := do(t, ts, processCall{body: processBody(t, first, id)})
	maskedFirst := resultOf(t, body)

	code, body := do(t, ts, processCall{body: processBody(t, second, id)})
	if code != http.StatusOK {
		t.Fatalf("получен код %d: %s", code, body)
	}
	if got := resultOf(t, body); strings.Contains(got, "+79990001122") {
		t.Fatalf("второй текст не замаскирован: %q", got)
	}

	// Запись по идентификатору осталась прежней, обратное преобразование
	// первой маски всё ещё работает.
	code, body = do(t, ts, processCall{body: processBody(t, maskedFirst, id)})
	if code != http.StatusOK {
		t.Fatalf("обратное преобразование дало код %d: %s", code, body)
	}
	if got := resultOf(t, body); got != first {
		t.Fatalf("восстановлен текст %q вместо %q", got, first)
	}
}

// TestProcessUnknownMaskPassthrough проверяет поведение после истечения срока
// хранения в режиме passthrough: клиент прислал уже замаскированный текст с
// тем же идентификатором, запись истекла, и сервис возвращает текст как есть с
// заголовком X-PII-Unknown-Id, а не маскирует звёздочки повторно.
func TestProcessUnknownMaskPassthrough(t *testing.T) {
	ts := newTestServerWithTTL(t, 100*time.Millisecond)
	const payload = "Телефон +79161234567"
	const id = "payload-ttl-passthrough"

	_, body := do(t, ts, processCall{body: processBody(t, payload, id)})
	masked := resultOf(t, body)

	// Ждём истечения срока хранения: запись исчезает, а клиент присылает
	// маску на восстановление.
	time.Sleep(150 * time.Millisecond)

	resp, err := ts.Client().Post(ts.URL+"/process", "application/json",
		strings.NewReader(processBody(t, masked, id)))
	if err != nil {
		t.Fatalf("запрос не выполнился: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("не удалось прочитать ответ: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("после истечения срока получен код %d вместо 200: %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("X-PII-Unknown-Id"); got != "1" {
		t.Fatalf("заголовок X-PII-Unknown-Id равен %q, ожидался 1", got)
	}
	if got := resultOf(t, string(raw)); got != masked {
		t.Fatalf("текст вернулся как %q, ожидалась присланная маска %q", got, masked)
	}
}

// TestProcessUnknownMask404 проверяет поведение после истечения срока хранения
// в режиме 404: клиент прислал маску на восстановление, запись истекла, и
// сервис отвечает 404 с кодом payload_not_found, не маскируя звёздочки повторно.
func TestProcessUnknownMask404(t *testing.T) {
	ts := newTestServerWithTTL(t, 100*time.Millisecond)
	const payload = "Телефон +79161234567"
	const id = "payload-ttl-404"

	// Система partner не задаёт on_unknown_id, поэтому по умолчанию работает
	// режим 404.
	_, body := do(t, ts, processCall{
		body:    processBody(t, payload, id),
		headers: map[string]string{"X-System-Key": testKeyDemask},
	})
	masked := resultOf(t, body)

	time.Sleep(150 * time.Millisecond)

	code, body := do(t, ts, processCall{
		body:    processBody(t, masked, id),
		headers: map[string]string{"X-System-Key": testKeyDemask},
	})
	if code != http.StatusNotFound {
		t.Fatalf("после истечения срока получен код %d вместо 404: %s", code, body)
	}
	if !strings.Contains(body, "payload_not_found") {
		t.Fatalf("в ответе нет кода ошибки payload_not_found: %s", body)
	}
	if strings.Contains(body, masked) {
		t.Fatalf("маска утекла в ответ об ошибке: %s", body)
	}
}

// TestProcessAmbiguousHeader проверяет, что неоднозначный повтор — тот же
// идентификатор с третьим текстом — маскируется и несёт заголовок
// X-PII-Ambiguous, чтобы клиент видел неоднозначность.
func TestProcessAmbiguousHeader(t *testing.T) {
	ts := newTestServer(t)
	const id = "payload-ambiguous-header"
	const first = "Телефон +79161234567"
	const second = "Другой телефон +79990001122"
	const third = "Третий телефон +78880001122"

	_, body := do(t, ts, processCall{body: processBody(t, first, id)})
	maskedFirst := resultOf(t, body)

	_, body = do(t, ts, processCall{body: processBody(t, second, id)})
	maskedSecond := resultOf(t, body)
	if maskedSecond == maskedFirst {
		t.Fatal("второй текст дал ту же маску, что и первый")
	}

	// Третий текст с тем же идентификатором — неоднозначный повтор.
	resp, err := ts.Client().Post(ts.URL+"/process", "application/json",
		strings.NewReader(processBody(t, third, id)))
	if err != nil {
		t.Fatalf("запрос не выполнился: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("не удалось прочитать ответ: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("неоднозначный повтор дал код %d: %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("X-PII-Ambiguous"); got != "1" {
		t.Fatalf("заголовок X-PII-Ambiguous равен %q, ожидался 1", got)
	}
	if got := resultOf(t, string(raw)); strings.Contains(got, "+78880001122") {
		t.Fatalf("третий текст не замаскирован: %q", got)
	}
}

// TestProcessNormalPathUnchanged проверяет, что обычный путь «маска →
// восстановление» не изменился побайтово: маска и восстановленный текст
// совпадают с исходными.
func TestProcessNormalPathUnchanged(t *testing.T) {
	ts := newTestServer(t)
	const payload = "Иванов Иван, телефон +79161234567, почта ivan@example.com"
	const id = "payload-normal-unchanged"

	_, body := do(t, ts, processCall{body: processBody(t, payload, id)})
	masked := resultOf(t, body)

	// Повтор маскирования даёт ту же маску побайтово.
	_, body = do(t, ts, processCall{body: processBody(t, payload, id)})
	if got := resultOf(t, body); got != masked {
		t.Fatalf("повтор дал другую маску %q вместо %q", got, masked)
	}

	// Обратное преобразование возвращает исходный текст побайтово.
	_, body = do(t, ts, processCall{body: processBody(t, masked, id)})
	if got := resultOf(t, body); got != payload {
		t.Fatalf("восстановлен текст %q вместо %q", got, payload)
	}
}

// TestProcessValidation проверяет разбор тела запроса. Ошибки разбора отдаются
// кодом 422 в формате, привычном клиенту проверяющей системы.
func TestProcessValidation(t *testing.T) {
	ts := newTestServer(t)
	cases := []struct {
		name string
		body string
		loc  string
	}{
		{"негодный JSON", "{не json", "body"},
		{"обрезанный JSON", `{"payload": "текст"`, "body"},
		{"не объект", `"просто строка"`, "body"},
		{"нет поля payload", `{"payload_id": "id-1"}`, "payload"},
		{"нет поля payload_id", `{"payload": "текст"}`, "payload_id"},
		{"пустое тело", "", "body"},
		{"поле payload не строка", `{"payload": 42, "payload_id": "id-1"}`, "body"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := do(t, ts, processCall{body: c.body})
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("получен код %d вместо 422: %s", code, body)
			}
			var parsed validationBody
			if err := json.Unmarshal([]byte(body), &parsed); err != nil {
				t.Fatalf("ответ %q не разобрался: %v", body, err)
			}
			if len(parsed.Detail) == 0 {
				t.Fatalf("в ответе нет описания ошибки: %s", body)
			}
			if !strings.Contains(strings.Join(parsed.Detail[0].Loc, "."), c.loc) {
				t.Fatalf("место ошибки %v не содержит %q", parsed.Detail[0].Loc, c.loc)
			}
		})
	}
}

// TestProcessPayloadTooLarge проверяет отказ на слишком большом теле запроса.
func TestProcessPayloadTooLarge(t *testing.T) {
	ts := newTestServer(t)
	huge := strings.Repeat("a", maxBodyBytes*4)
	code, body := do(t, ts, processCall{body: processBody(t, huge, "payload-huge")})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("получен код %d вместо 413: %s", code, body)
	}
}

// TestProcessMethodNotAllowed проверяет, что поддерживается только POST.
func TestProcessMethodNotAllowed(t *testing.T) {
	ts := newTestServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			code, body := do(t, ts, processCall{method: method})
			if code != http.StatusMethodNotAllowed {
				t.Fatalf("метод %s дал код %d вместо 405: %s", method, code, body)
			}
		})
	}
}

// TestProcessWrongKey проверяет, что негодный ключ доступа не пускается и не
// достаётся анонимной системе.
func TestProcessWrongKey(t *testing.T) {
	ts := newTestServer(t)
	code, body := do(t, ts, processCall{
		body:    processBody(t, "Телефон +79161234567", "payload-wrong-key"),
		headers: map[string]string{"X-System-Key": "не тот ключ"},
	})
	if code != http.StatusForbidden {
		t.Fatalf("негодный ключ дал код %d вместо 403: %s", code, body)
	}
}

// TestProcessNeverReturnsServerError прогоняет весь набор случаев и проверяет
// два общих требования: пятисотый код не возвращается никогда, а у успешного
// ответа всегда есть поле result. Пять подряд невалидных ответов останавливают
// прогон проверяющей системы, поэтому это требование важнее любого другого.
func TestProcessNeverReturnsServerError(t *testing.T) {
	ts := newTestServer(t)
	calls := []processCall{
		{body: processBody(t, "Телефон +79161234567", "общий-1")},
		{body: processBody(t, "Телефон +79161234567", "общий-1")},
		{body: processBody(t, "", "общий-пусто")},
		{body: processBody(t, "текст без персональных данных", "общий-2")},
		{body: processBody(t, strings.Repeat("Иванов Иван, ", 100), "общий-3")},
		{body: "{не json"},
		{body: `{"payload_id": "общий-4"}`},
		{body: `{"payload": "текст"}`},
		{body: ""},
		{body: processBody(t, strings.Repeat("a", maxBodyBytes*4), "общий-5")},
		{method: http.MethodGet},
		{method: http.MethodPut, body: processBody(t, "текст", "общий-6")},
		{body: processBody(t, "текст", "общий-7"), headers: map[string]string{"X-System-Key": "мимо"}},
		{body: processBody(t, "Телефон +79161234567", "общий-8"), headers: map[string]string{"X-System-Key": testKeyDemask}},
		{body: processBody(t, "\u0000 управляющие \n символы", "общий-9")},
	}
	for i, call := range calls {
		code, body := do(t, ts, call)
		if code == http.StatusInternalServerError {
			t.Fatalf("случай %d ответил кодом 500: %s", i, body)
		}
		if code == http.StatusOK {
			resultOf(t, body)
		}
	}
}

// TestProcessEmptyPayload проверяет пустой текст: он допустим и возвращается
// как есть, а не считается ошибкой.
func TestProcessEmptyPayload(t *testing.T) {
	ts := newTestServer(t)
	code, body := do(t, ts, processCall{body: processBody(t, "", "payload-empty")})
	if code != http.StatusOK {
		t.Fatalf("получен код %d: %s", code, body)
	}
	if got := resultOf(t, body); got != "" {
		t.Fatalf("получено %q, ожидалась пустая строка", got)
	}
}

// TestProcessResponseContentType проверяет заголовок ответа: проверяющая
// система разбирает тело как JSON в кодировке UTF-8.
func TestProcessResponseContentType(t *testing.T) {
	ts := newTestServer(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/process",
		bytes.NewReader([]byte(processBody(t, "Телефон +79161234567", "payload-ct"))))
	if err != nil {
		t.Fatalf("не удалось собрать запрос: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос не выполнился: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("заголовок Content-Type равен %q", got)
	}
}

// TestProcessConcurrentSameID проверяет одновременные одинаковые запросы:
// проверяющая система повторяет их при таймаутах, и все обязаны получить одну
// и ту же маску.
func TestProcessConcurrentSameID(t *testing.T) {
	ts := newTestServer(t)
	const payload = "Иванов Иван, телефон +79161234567"
	const id = "payload-concurrent"

	const n = 8
	// Тело собирается заранее: обращаться к testing.T из чужой горутины нельзя.
	body := processBody(t, payload, id)
	results := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/process", strings.NewReader(body))
			if err != nil {
				results <- "ошибка сборки запроса: " + err.Error()
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := ts.Client().Do(req)
			if err != nil {
				results <- "ошибка запроса: " + err.Error()
				return
			}
			defer func() { _ = resp.Body.Close() }()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				results <- "ошибка чтения: " + err.Error()
				return
			}
			if resp.StatusCode != http.StatusOK {
				results <- "код ответа: " + string(raw)
				return
			}
			var parsed processResponse
			if err := json.Unmarshal(raw, &parsed); err != nil {
				results <- "ошибка разбора: " + err.Error()
				return
			}
			results <- parsed.Result
		}()
	}
	first := <-results
	for i := 1; i < n; i++ {
		if got := <-results; got != first {
			t.Fatalf("одновременные одинаковые запросы дали разные ответы: %q и %q", got, first)
		}
	}
	if strings.Contains(first, "+79161234567") {
		t.Fatalf("текст не замаскирован: %q", first)
	}
}

// TestServiceEndpoints проверяет служебные ручки: они нужны балансировщику и
// сбору показателей и не должны падать.
func TestServiceEndpoints(t *testing.T) {
	ts := newTestServer(t)
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
			if err != nil {
				t.Fatalf("не удалось собрать запрос: %v", err)
			}
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("запрос не выполнился: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("ручка %s ответила кодом %d", path, resp.StatusCode)
			}
		})
	}
}

// apiPanicDetector — детектор, который падает при разборе. Нужен, чтобы
// проверить поведение сервиса при сбое детектора на уровне контракта.
type apiPanicDetector struct{}

func (apiPanicDetector) Types() []pii.Type { return []pii.Type{pii.TypePhone} }

func (apiPanicDetector) Detect(*pii.Doc) []pii.Span { panic("сбой детектора") }

// newDegradedTestServer поднимает сервис с паникующим детектором телефонов.
// Остальные детекторы исправны, поэтому текст обрабатывается частично.
func newDegradedTestServer(t *testing.T, onError string) *httptest.Server {
	t.Helper()

	cfg, err := config.Parse([]byte(testConfigYAML()))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	// Анонимной системе, которой отвечает проверяющая система, задаём нужный
	// режим поведения при ошибке.
	anon, ok := cfg.AnonymousSystem()
	if !ok {
		t.Fatal("анонимная система не найдена в настройках")
	}
	anon.OnError = onError
	cfg.Systems[anon.Name] = anon

	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 10000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector(), apiPanicDetector{})

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, engine.New(reg), metrics.New(), log)

	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

// TestProcessDegradedOpen проверяет щадящий режим: при сбое детектора ответ
// несёт заголовок деградации, текст обработан остальными детекторами, а
// исходные значения наружу не выходят.
func TestProcessDegradedOpen(t *testing.T) {
	ts := newDegradedTestServer(t, config.OnErrorOpen)
	const payload = "Телефон +79161234567, почта ivan@example.com"
	const id = "payload-degraded-open"

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/process",
		strings.NewReader(processBody(t, payload, id)))
	if err != nil {
		t.Fatalf("не удалось собрать запрос: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос не выполнился: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("не удалось прочитать ответ: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("щадящий режим ответил кодом %d: %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("X-PII-Degraded"); got != "1" {
		t.Fatalf("заголовок X-PII-Degraded равен %q, ожидался 1", got)
	}
	body := string(raw)
	if strings.Contains(body, "+79161234567") {
		t.Fatalf("исходное значение утекло в ответ: %s", body)
	}
	// Адрес почты обработан исправным детектором и замаскирован.
	if strings.Contains(body, "ivan@example.com") {
		t.Fatalf("исправный детектор не отработал: %s", body)
	}
}

// TestProcessDegradedClosed проверяет строгий режим: при сбое детектора ответ
// 503 с заголовком Retry-After, исходные значения наружу не выходят.
func TestProcessDegradedClosed(t *testing.T) {
	ts := newDegradedTestServer(t, config.OnErrorClosed)
	const payload = "Телефон +79161234567, почта ivan@example.com"
	const id = "payload-degraded-closed"

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/process",
		strings.NewReader(processBody(t, payload, id)))
	if err != nil {
		t.Fatalf("не удалось собрать запрос: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос не выполнился: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("не удалось прочитать ответ: %v", err)
	}

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("строгий режим ответил кодом %d вместо 503: %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatal("в ответе 503 нет заголовка Retry-After")
	}
	if strings.Contains(string(raw), "+79161234567") {
		t.Fatalf("исходное значение утекло в ответ: %s", raw)
	}
}

// TestProcessDegradedMetric проверяет, что сбой детектора увеличивает счётчик
// деградации в показателях.
func TestProcessDegradedMetric(t *testing.T) {
	ts := newDegradedTestServer(t, config.OnErrorOpen)
	const payload = "Телефон +79161234567"
	const id = "payload-degraded-metric"

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/process",
		strings.NewReader(processBody(t, payload, id)))
	if err != nil {
		t.Fatalf("не удалось собрать запрос: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("запрос не выполнился: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// Показатели читаются отдельным запросом: счётчик деградации обязан
	// появиться после ответа с признаком деградации.
	mreq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("не удалось собрать запрос показателей: %v", err)
	}
	mresp, err := ts.Client().Do(mreq)
	if err != nil {
		t.Fatalf("запрос показателей не выполнился: %v", err)
	}
	defer func() { _ = mresp.Body.Close() }()
	raw, err := io.ReadAll(mresp.Body)
	if err != nil {
		t.Fatalf("не удалось прочитать показатели: %v", err)
	}
	if !strings.Contains(string(raw), `pii_degraded_total{system="alfasonar"} 1`) {
		t.Fatalf("счётчик деградации не увеличился:\n%s", raw)
	}
}

// TestStatusWriterTracksHeader проверяет, что обёртка ответа запоминает факт
// отправки заголовка: по этому признаку перехват сбоя решает, дописывать ли
// ответ, и не пишет второй заголовок поверх уже отправленного.
func TestStatusWriterTracksHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}

	if sw.wroteHeader {
		t.Fatal("заголовок помечен отправленным до первой записи")
	}
	sw.WriteHeader(http.StatusOK)
	if !sw.wroteHeader {
		t.Fatal("заголовок не помечен отправленным после WriteHeader")
	}
	// Повторный WriteHeader не меняет код и не сбрасывает признак.
	sw.WriteHeader(http.StatusServiceUnavailable)
	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа %d, ожидался первый 200", rec.Code)
	}
	if !sw.wroteHeader {
		t.Fatal("признак отправки заголовка сброшен")
	}
}

// TestWithRecoverAfterWrite проверяет, что перехват сбоя не дописывает ответ,
// если заголовок и тело уже ушли клиенту: сбой после отправки не портит
// отправленное и не добавляет второй заголовок.
func TestWithRecoverAfterWrite(t *testing.T) {
	cfg, err := config.Parse([]byte(testConfigYAML()))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 10000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)
	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, engine.New(reg), metrics.New(), log)

	// Обработчик пишет ответ, а затем падает: перехват обязан не дописать
	// второй ответ поверх уже отправленного.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("уже отправлено"))
		panic("сбой после отправки")
	})
	handler := srv.withRecover(inner)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/process", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код ответа %d, ожидался первый 200", rec.Code)
	}
	if got := rec.Body.String(); got != "уже отправлено" {
		t.Fatalf("тело ответа %q, ожидался только первый ответ", got)
	}
}
