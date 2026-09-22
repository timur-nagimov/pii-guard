// Package api содержит точки входа сервиса: контракт POST /process для
// проверяющей системы, режим прокси к языковой модели и служебные ручки.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/store"
)

// Server обслуживает запросы сервиса.
type Server struct {
	cfg     atomic.Pointer[config.Config]
	store   *store.Store
	engine  *engine.Engine
	metrics *metrics.Metrics
	log     *slog.Logger
	flight  singleflight.Group

	sem      chan struct{}
	heavySem chan struct{}

	// upstreams кеширует клиентов к языковой модели по имени системы.
	upstreams sync.Map

	ready atomic.Bool
}

// New создаёт сервер.
func New(cfg *config.Config, st *store.Store, eng *engine.Engine, m *metrics.Metrics, log *slog.Logger) *Server {
	s := &Server{store: st, engine: eng, metrics: m, log: log}
	s.cfg.Store(cfg)
	s.sem = make(chan struct{}, cfg.Limits.Inflight)
	s.heavySem = make(chan struct{}, cfg.Limits.HeavyInflight)
	s.ready.Store(true)
	return s
}

// Config возвращает действующие настройки.
func (s *Server) Config() *config.Config { return s.cfg.Load() }

// SetConfig подменяет настройки без перезапуска сервиса. Размеры ограничителей
// остаются прежними: менять их на ходу небезопасно для идущих запросов.
func (s *Server) SetConfig(cfg *config.Config) {
	s.cfg.Store(cfg)
	// Настройки обращения к модели могли измениться, поэтому кешированных
	// клиентов нужно собрать заново.
	s.upstreams = sync.Map{}
}

// SetReady переключает готовность принимать запросы. При завершении работы
// готовность снимается заранее, чтобы балансировщик перестал слать запросы.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Routes собирает маршруты сервиса.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/process", s.handleProcess)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/v1/inspect", s.handleInspect)
	mux.HandleFunc("/ui", s.handleUI)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.Handle("/metrics", s.metrics.Handler())
	return s.withRecover(mux)
}

// withRecover перехватывает сбой обработчика. Пятисотый код недопустим:
// пять подряд невалидных ответов останавливают прогон проверяющей системы,
// поэтому в худшем случае отвечаем кодом временной недоступности.
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.metrics.ObservePanic("handler")
				s.log.Error("сбой обработчика",
					slog.String("path", r.URL.Path),
					slog.Any("panic", rec))
				w.Header().Set("Retry-After", "1")
				s.writeError(w, r, http.StatusServiceUnavailable, "internal_degraded", "временная ошибка обработки")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.metrics.SetRecords(s.store.Len())
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		s.writeError(w, r, http.StatusServiceUnavailable, "shutting_down", "сервис завершает работу")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// acquire занимает место в ограничителе одновременной обработки. Ограничитель
// служит предохранителем: отказ с просьбой повторить лучше, чем таймаут,
// но на штатной нагрузке до него доходить не должно.
func (s *Server) acquire(ctx context.Context, heavy bool, limits config.Limits) (func(), bool) {
	started := time.Now()
	timer := time.NewTimer(limits.MaxWait)
	defer timer.Stop()

	sem := s.sem
	select {
	case sem <- struct{}{}:
	case <-timer.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}

	if !heavy {
		s.metrics.ObserveQueueWait(time.Since(started))
		s.metrics.IncInflight()
		return func() {
			<-sem
			s.metrics.DecInflight()
		}, true
	}

	select {
	case s.heavySem <- struct{}{}:
		s.metrics.ObserveQueueWait(time.Since(started))
		s.metrics.IncInflight()
		return func() {
			<-s.heavySem
			<-sem
			s.metrics.DecInflight()
		}, true
	case <-timer.C:
		<-sem
		return nil, false
	case <-ctx.Done():
		<-sem
		return nil, false
	}
}

// errorBody — единый формат ошибки сервиса.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// validationBody повторяет формат ошибок разбора тела, принятый в эталонном
// образце контракта: так клиенту проверяющей системы привычнее.
type validationBody struct {
	Detail []validationItem `json:"detail"`
}

type validationItem struct {
	Type string   `json:"type"`
	Loc  []string `json:"loc"`
	Msg  string   `json:"msg"`
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		s.log.Warn("не удалось записать ответ", slog.String("error", err.Error()))
	}
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, code int, errCode, msg string) {
	s.metrics.ObserveStatus(systemLabel(r), "error", itoa(code))
	s.writeJSON(w, code, errorBody{Error: errorDetail{
		Code:      errCode,
		Message:   msg,
		RequestID: requestID(r),
	}})
}

func (s *Server) writeValidation(w http.ResponseWriter, r *http.Request, kind string, loc []string, msg string) {
	s.metrics.ObserveStatus(systemLabel(r), "error", "422")
	s.writeJSON(w, http.StatusUnprocessableEntity, validationBody{
		Detail: []validationItem{{Type: kind, Loc: loc, Msg: msg}},
	})
}

// logProcess пишет в журнал этапы обработки. В журнал попадают только типы и
// счётчики; ни исходный текст, ни найденные значения туда не идут.
func (s *Server) logProcess(r *http.Request, system, payloadID string, dir direction, size int, counts map[string]int, took time.Duration, degraded bool) {
	attrs := []any{
		slog.String("event", "process"),
		slog.String("system", system),
		slog.String("payload_id", payloadID),
		slog.String("direction", string(dir)),
		slog.Int("payload_bytes", size),
		slog.Duration("took", took),
		slog.String("request_id", requestID(r)),
	}
	if len(counts) > 0 {
		attrs = append(attrs, slog.Any("pii_types", counts))
	}
	if degraded {
		attrs = append(attrs, slog.Bool("degraded", true))
	}
	s.log.Info("обработан запрос", attrs...)
}

func requestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id := r.Header.Get("X-Request-Id"); id != "" {
		return id
	}
	return ""
}

func systemLabel(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	return "all"
}

// countsToStrings переводит счётчики типов в строковые ключи для журнала.
func countsToStrings(res engine.Result) map[string]int {
	out := make(map[string]int, len(res.Counts))
	for t, n := range res.Counts {
		out[string(t)] = n
	}
	return out
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
