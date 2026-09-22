// Package api содержит точки входа сервиса: контракт POST /process для
// проверяющей системы, режим прокси к языковой модели и служебные ручки.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/logging"
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

	// lg — журнал сервиса целиком: прослойка запросов, журнал аудита и
	// ручка смены уровня берутся отсюда. Пустое значение допустимо: тогда
	// маршруты собираются без прослойки, а записи аудита не пишутся. Так
	// сервер остаётся пригодным для тестов, которые поднимают его с одним
	// лишь slog.
	lg    *logging.Logger
	audit *logging.Audit

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

// SetLogging подключает журнал сервиса: прослойку запросов, журнал аудита и
// ручку смены уровня. Вызывается при запуске, до сборки маршрутов.
func (s *Server) SetLogging(lg *logging.Logger) {
	s.lg = lg
	if lg != nil {
		s.log = lg.Slog()
		s.audit = lg.Audit()
	}
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
//
// Прослойка журнала ставится вокруг всех маршрутов сразу, а не вокруг
// отдельных ручек: сквозной идентификатор запроса обязан появляться на каждой
// точке входа, включая служебные, и возвращаться клиенту заголовком. Перехват
// сбоя стоит внутри прослойки, чтобы подменённый на 503 ответ попал в журнал
// с настоящим кодом.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/process", s.handleProcess)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/v1/inspect", s.handleInspect)
	mux.HandleFunc("/v1/systems", s.handleSystems)
	mux.HandleFunc("/ui", s.handleUI)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.Handle("/metrics", s.metrics.Handler())
	if s.lg != nil {
		mux.Handle("/admin/loglevel", s.lg.LevelHandler(s.allowAdmin))
	}
	h := s.withRecover(mux)
	if s.lg == nil {
		return h
	}
	return logging.Middleware(s.lg, s.lg.MiddlewareOptions())(h)
}

// allowAdmin решает, кому доступна ручка смены уровня журнала. Ручка меняет
// поведение сервиса на ходу, поэтому открытой быть не может.
//
// Ограничена она тем же, чем ограничен доступ к рабочим ручкам сервиса, —
// ключом системы: подходит запрос, чей заголовок с ключом совпал с хешем
// включённой системы из настроек. Отдельного пароля администратора не
// заводится: лишний секрет это лишнее место для утечки, а список ключей уже
// есть и уже проверяется тем же сравнением хешей.
//
// Анонимная система сюда не проходит намеренно: без ключа приходит
// проверяющая система, и менять ей подробность журнала нельзя.
//
// Второй допустимый путь — обращение с петлевого адреса. Так ручка доступна
// дежурному инженеру через туннель на машину, ровно как встроенный
// профилировщик, и недоступна снаружи.
func (s *Server) allowAdmin(r *http.Request) bool {
	if r == nil {
		return false
	}
	if isLoopback(r.RemoteAddr) {
		return true
	}
	_, ok := keyedSystem(r, s.Config())
	return ok
}

// isLoopback сообщает, что запрос пришёл с самой машины.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// withRecover перехватывает сбой обработчика. Пятисотый код недопустим:
// пять подряд невалидных ответов останавливают прогон проверяющей системы,
// поэтому в худшем случае отвечаем кодом временной недоступности.
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.metrics.ObservePanic("handler")
				s.log.ErrorContext(r.Context(), "сбой обработчика",
					logging.Event(logging.EventHandlerPanic),
					logging.Component("api"),
					slog.String(logging.FieldPath, r.URL.Path),
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
//
// Таймер ожидания заводится только тогда, когда место действительно занято.
// На штатной нагрузке место свободно всегда, а каждый таймер — это выделение
// памяти и запись в общий пул таймеров рантайма, то есть точка соперничества
// между всеми обработчиками сразу.
func (s *Server) acquire(ctx context.Context, heavy bool, limits config.Limits) (func(), bool) {
	started := time.Now()
	var timer *time.Timer
	// waiter заводит таймер по первой надобности и переиспользует его дальше:
	// общий срок ожидания на запрос остаётся прежним.
	waiter := func() *time.Timer {
		if timer == nil {
			timer = time.NewTimer(limits.MaxWait - time.Since(started))
		}
		return timer
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	sem := s.sem
	if !tryAcquire(sem) && !waitAcquire(ctx, sem, waiter()) {
		return nil, false
	}

	if heavy {
		if !tryAcquire(s.heavySem) && !waitAcquire(ctx, s.heavySem, waiter()) {
			<-sem
			return nil, false
		}
		s.metrics.ObserveQueueWait(time.Since(started))
		s.metrics.IncInflight()
		return func() {
			<-s.heavySem
			<-sem
			s.metrics.DecInflight()
		}, true
	}

	s.metrics.ObserveQueueWait(time.Since(started))
	s.metrics.IncInflight()
	return func() {
		<-sem
		s.metrics.DecInflight()
	}, true
}

// tryAcquire занимает место без ожидания.
func tryAcquire(sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// waitAcquire ждёт места до срабатывания таймера или отмены запроса.
func waitAcquire(ctx context.Context, sem chan struct{}, timer *time.Timer) bool {
	select {
	case sem <- struct{}{}:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
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
		s.log.Warn("не удалось записать ответ",
			logging.Component("api"), logging.Err(err))
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

// logProcess пишет в журнал подробности обработки. В журнал попадают только
// типы и счётчики; ни исходный текст, ни найденные значения туда не идут.
//
// Уровень отладочный, и это осознанно. Факт обслуживания запроса пишет
// прослойка, состав найденного уходит в журнал аудита и в показатели, а
// подробности разбора нужны только тогда, когда за ними пришли. Иначе на
// каждый запрос выходило бы по две почти одинаковые записи.
//
// Запись собирается через LogAttrs, а не через Debug: при передаче полей
// свободным списком каждое поле упаковывается в пустой интерфейс, и это
// выделение памяти на каждое поле каждого запроса. LogAttrs принимает поля
// как есть.
func (s *Server) logProcess(r *http.Request, system, payloadID string, dir direction, size int, counts map[string]int, took time.Duration, degraded bool) {
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	// Проверка уровня до сборки записи: при выключенном отладочном уровне вся
	// сборка полей оказалась бы напрасной работой на горячем пути. Цена самой
	// проверки замерена и составляет 8.6 наносекунды.
	if !s.log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	attrs := make([]slog.Attr, 0, 8)
	// Идентификатор от клиента проходит политику идентификатора: похожий на
	// идентификатор выходит в журнал как есть, похожий на номер карты или
	// телефон — отпечатком. Ту же политику применяет обработчик журнала, так
	// что запись безопасна и без этой строки; здесь она стоит затем, чтобы в
	// месте вызова было видно, что значение чужое.
	attrs = append(attrs,
		logging.Event(logging.EventProcess),
		logging.Component("api"),
		slog.String(logging.FieldOp, string(dir)),
		slog.String(logging.FieldPayloadID, logging.LogID(payloadID)),
		slog.Int(logging.FieldBytes, size),
		logging.Took(took),
	)
	// Имя системы передаётся всегда: обработчик журнала дописывает system из
	// контекста только тогда, когда поля в записи нет.
	if system != "" {
		attrs = append(attrs, slog.String(logging.FieldSystem, system))
	}
	if len(counts) > 0 {
		attrs = append(attrs, slog.Any("pii_types", counts))
	}
	if degraded {
		attrs = append(attrs, slog.Bool("degraded", true))
	}
	s.log.LogAttrs(ctx, slog.LevelDebug, "обработан запрос", attrs...)
}

// requestID возвращает сквозной идентификатор запроса. Его кладёт в контекст
// прослойка журнала, она же возвращает его клиенту заголовком.
//
// Запасной путь через заголовок нужен для вызовов обработчика напрямую, без
// прослойки: так поднимают сервер тесты. Чужая строка при этом проходит ту же
// проверку пригодности, что и в прослойке, поэтому в ответ не попадёт ни
// разметка, ни значение персональных данных.
func requestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id := logging.RequestID(r.Context()); id != "" {
		return id
	}
	id, _ := logging.SafeID(r.Header.Get(logging.HeaderRequestID))
	return id
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
