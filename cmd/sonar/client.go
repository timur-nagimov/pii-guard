package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxResponseBytes ограничивает читаемый ответ. Имитатор не должен падать по
// памяти, если сервис вернёт мусор вместо маски.
const maxResponseBytes = 8 << 20

// maxRetryAfter ограничивает паузу по заголовку Retry-After: сервис может
// попросить ждать минуту, но прогон нагрузки такой паузы не переживёт.
const maxRetryAfter = 10 * time.Second

// defaultRetryAfter применяется, когда код 429 пришёл без заголовка.
const defaultRetryAfter = 500 * time.Millisecond

// outcomeKind описывает, как проверяющая система расценивает ответ сервиса.
type outcomeKind int

// Исходы одного логического запроса со всеми его попытками.
const (
	// outcomeOK означает ответ с кодом 200 и полем result.
	outcomeOK outcomeKind = iota
	// outcomeInvalid означает неответ: таймаут, обрыв, чужой код, испорченное
	// тело. Пять таких подряд останавливают прогон.
	outcomeInvalid
	// outcomeThrottled означает, что все попытки закончились кодом 429.
	// Невалидным такой исход не считается.
	outcomeThrottled
	// outcomeAborted означает, что запрос оборвал конец прогона, а не сервис.
	// Такой исход не попадает ни в показатели, ни в счётчик серии.
	outcomeAborted
)

// String даёт имя исхода для отчёта.
func (k outcomeKind) String() string {
	switch k {
	case outcomeOK:
		return "ok"
	case outcomeThrottled:
		return "throttled"
	case outcomeAborted:
		return "aborted"
	default:
		return "invalid"
	}
}

// Attempt описывает одну попытку запроса.
type Attempt struct {
	Status     int
	Latency    time.Duration
	Throttled  bool
	Invalid    bool
	Aborted    bool
	RetryAfter time.Duration
	Result     string
	Err        error
}

// Response описывает итог логического запроса: до трёх попыток, из которых
// учитывается последняя удачная.
type Response struct {
	Outcome  outcomeKind
	Result   string
	Status   int
	Attempts []Attempt
	Retries  int
	Throttle int
	Err      error
}

// Latency возвращает длительность последней попытки: именно её видит
// проверяющая система как время ответа на запрос.
func (r Response) Latency() time.Duration {
	if len(r.Attempts) == 0 {
		return 0
	}
	return r.Attempts[len(r.Attempts)-1].Latency
}

// processRequest повторяет тело запроса проверяющей системы.
type processRequest struct {
	Payload   string `json:"payload"`
	PayloadID string `json:"payload_id"`
}

// processResponse повторяет тело успешного ответа. Поле указателем, чтобы
// отличить ответ без result от ответа с пустым result.
type processResponse struct {
	Result *string `json:"result"`
}

// Client шлёт запросы так же, как проверяющая система организаторов: таймаут
// одного запроса десять секунд, до двух повторов, при коде 429 пауза с учётом
// заголовка Retry-After.
type Client struct {
	url         string
	timeout     time.Duration
	maxAttempts int
	http        *http.Client
}

// NewClient собирает клиента под заданную нагрузку. Проверка сертификата
// отключена, потому что сервис поднимает самоподписанный сертификат, как и
// стенд организаторов.
func NewClient(url string, timeout time.Duration, conns int) *Client {
	transport := &http.Transport{
		MaxIdleConns:        conns * 2,
		MaxIdleConnsPerHost: conns * 2,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // стенд поднимает самоподписанный сертификат
	}
	return &Client{
		url:         url,
		timeout:     timeout,
		maxAttempts: 3,
		http:        &http.Client{Transport: transport, Timeout: timeout},
	}
}

// Do выполняет логический запрос с повторами. Код 429 даёт паузу и повтор,
// любой другой неудачный ответ даёт немедленный повтор.
func (c *Client) Do(ctx context.Context, payload, id string) Response {
	var resp Response
	for i := 0; i < c.maxAttempts; i++ {
		if i > 0 {
			resp.Retries++
		}
		at := c.attempt(ctx, payload, id)
		resp.Attempts = append(resp.Attempts, at)
		if at.Aborted {
			break
		}
		if at.Throttled {
			resp.Throttle++
			if !sleepCtx(ctx, at.RetryAfter) {
				break
			}
			continue
		}
		if !at.Invalid {
			resp.Outcome = outcomeOK
			resp.Result = at.Result
			resp.Status = at.Status
			return resp
		}
	}
	return finishResponse(resp)
}

// Probe выполняет ровно одну попытку без повторов. Нужен для проверок
// устойчивости, исход которых не должен попадать в счётчик проверяющей
// системы: неизвестный идентификатор вправе получить отказ.
func (c *Client) Probe(ctx context.Context, payload, id string) Response {
	at := c.attempt(ctx, payload, id)
	resp := Response{Attempts: []Attempt{at}, Status: at.Status}
	switch {
	case at.Aborted:
		resp.Outcome = outcomeAborted
	case at.Throttled:
		resp.Outcome = outcomeThrottled
		resp.Throttle = 1
	case at.Invalid:
		resp.Outcome = outcomeInvalid
		resp.Err = at.Err
	default:
		resp.Outcome = outcomeOK
		resp.Result = at.Result
	}
	return resp
}

// finishResponse проставляет исход, когда попытки закончились неудачей.
func finishResponse(resp Response) Response {
	if len(resp.Attempts) == 0 {
		resp.Outcome = outcomeInvalid
		return resp
	}
	last := resp.Attempts[len(resp.Attempts)-1]
	resp.Status = last.Status
	resp.Err = last.Err
	switch {
	case last.Aborted:
		resp.Outcome = outcomeAborted
	case last.Throttled:
		resp.Outcome = outcomeThrottled
	default:
		resp.Outcome = outcomeInvalid
	}
	return resp
}

// attempt делает один запрос и относит ответ к успеху, отказу или перегрузке.
func (c *Client) attempt(ctx context.Context, payload, id string) Attempt {
	body, err := json.Marshal(processRequest{Payload: payload, PayloadID: id})
	if err != nil {
		return Attempt{Invalid: true, Err: err}
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return Attempt{Invalid: true, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		// Конец прогона обрывает запрос на полпути. Это не отказ сервиса,
		// поэтому такой запрос не идёт ни в задержки, ни в серию невалидных.
		if ctx.Err() != nil {
			return Attempt{Aborted: true, Latency: time.Since(started), Err: err}
		}
		return Attempt{Invalid: true, Latency: time.Since(started), Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	latency := time.Since(started)
	if readErr != nil {
		return Attempt{Status: resp.StatusCode, Latency: latency, Invalid: true, Err: readErr}
	}
	at := classifyResponse(resp.StatusCode, data, resp.Header.Get("Retry-After"), time.Now())
	at.Latency = latency
	return at
}

// classifyResponse разбирает ответ сервиса. Успехом считается только код 200
// с полем result: всё остальное проверяющая система считает неответом.
func classifyResponse(status int, body []byte, retryAfter string, now time.Time) Attempt {
	at := Attempt{Status: status}
	if status == http.StatusTooManyRequests {
		at.Throttled = true
		at.RetryAfter = retryAfterDelay(retryAfter, now)
		return at
	}
	if status != http.StatusOK {
		at.Invalid = true
		at.Err = errors.New("код ответа " + strconv.Itoa(status))
		return at
	}
	var parsed processResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		at.Invalid = true
		at.Err = err
		return at
	}
	if parsed.Result == nil {
		at.Invalid = true
		at.Err = errors.New("в ответе нет поля result")
		return at
	}
	at.Result = *parsed.Result
	return at
}

// retryAfterDelay выбирает паузу перед повтором с оглядкой на заголовок.
func retryAfterDelay(header string, now time.Time) time.Duration {
	d, ok := parseRetryAfter(header, now)
	if !ok {
		return defaultRetryAfter
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// parseRetryAfter разбирает заголовок Retry-After в двух видах, разрешённых
// стандартом: целое число секунд и дата. Нераспознанное и прошедшее время
// дают признак неудачи, чтобы вызывающий взял паузу по умолчанию.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(value)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	when, err := http.ParseTime(v)
	if err != nil {
		return 0, false
	}
	d := when.Sub(now)
	if d < 0 {
		return 0, true
	}
	return d, true
}

// sleepCtx ждёт заданное время и сообщает, дождался ли. Прогон могут оборвать
// по истечении длительности прямо во время паузы.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
