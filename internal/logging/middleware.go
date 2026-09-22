package logging

import (
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// MiddlewareOptions — настройки прослойки журналирования запросов.
type MiddlewareOptions struct {
	// SampleN включает прореживание: в журнал попадает каждый N-й успешный
	// запрос. Ноль и единица означают, что пишутся все. Ошибки, отказы и
	// медленные запросы пишутся всегда, независимо от прореживания: терять
	// нужно скучное, а не интересное.
	SampleN int
	// Slow — порог, после которого запрос пишется всегда.
	Slow time.Duration
	// Skip — пути, которые не пишутся вовсе. По умолчанию служебные ручки:
	// сбор показателей идёт раз в пять секунд, проверки живости чаще, и
	// вместе они дают больше записей, чем полезная работа на малой нагрузке.
	Skip map[string]bool
}

// DefaultMiddlewareOptions возвращает настройки по умолчанию.
func DefaultMiddlewareOptions() MiddlewareOptions {
	return MiddlewareOptions{
		SampleN: 1,
		Slow:    250 * time.Millisecond,
		Skip: map[string]bool{
			"/metrics": true,
			"/healthz": true,
			"/readyz":  true,
		},
	}
}

// Middleware выдаёт прослойку, которая делает четыре вещи:
//
//  1. берёт идентификатор запроса из заголовка или порождает свой;
//  2. кладёт его в контекст, откуда журнал достаёт его сам;
//  3. возвращает его клиенту в заголовке ответа, чтобы тот мог сослаться;
//  4. пишет одну запись на запрос с кодом ответа, размером и длительностью.
//
// Прослойка ставится вокруг всех маршрутов сразу, поэтому идентификатор
// появляется на всех точках входа, включая прокси к языковой модели.
func Middleware(l *Logger, opts MiddlewareOptions) func(http.Handler) http.Handler {
	if opts.SampleN < 1 {
		opts.SampleN = 1
	}
	log := l.Slog()
	var counter atomic.Uint64

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, generated := EnsureID(r)
			ctx := WithRequestID(r.Context(), id)
			w.Header().Set(HeaderRequestID, id)

			started := time.Now()
			rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			r = r.WithContext(ctx)

			if l.Enabled(slog.LevelDebug) && !opts.Skip[r.URL.Path] {
				log.DebugContext(ctx, "запрос принят",
					slog.String(FieldEvent, EventHTTPRequest),
					slog.String(FieldMethod, r.Method),
					slog.String(FieldPath, r.URL.Path),
					slog.Bool("id_generated", generated),
					slog.Int64("content_length", r.ContentLength))
			}

			next.ServeHTTP(rw, r)

			took := time.Since(started)
			if opts.Skip[r.URL.Path] {
				return
			}
			if !l.Enabled(slog.LevelDebug) && !shouldLog(&counter, opts, rw.status, took) {
				l.stats.Sampled.Add(1)
				return
			}
			log.LogAttrs(ctx, levelForStatus(rw.status), "запрос обслужен",
				slog.String(FieldEvent, EventHTTPRequest),
				slog.String(FieldMethod, r.Method),
				slog.String(FieldPath, r.URL.Path),
				slog.Int(FieldStatus, rw.status),
				slog.Int(FieldBytes, rw.written),
				Took(took))
		})
	}
}

// shouldLog решает, писать ли запись об успешном запросе.
func shouldLog(counter *atomic.Uint64, opts MiddlewareOptions, status int, took time.Duration) bool {
	if status >= http.StatusBadRequest {
		return true
	}
	if opts.Slow > 0 && took >= opts.Slow {
		return true
	}
	if opts.SampleN <= 1 {
		return true
	}
	return counter.Add(1)%uint64(opts.SampleN) == 0
}

func levelForStatus(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// statusWriter запоминает код ответа и размер тела. Он умышленно прозрачен
// для потоковой отдачи: режим прокси к языковой модели отдаёт ответ по частям,
// и прослойка не имеет права эту потоковость ломать.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int
	done    bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.done {
		return
	}
	w.done = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.done = true
	n, err := w.ResponseWriter.Write(p)
	w.written += n
	return n, err
}

// Unwrap открывает исходный ответ для http.ResponseController: через него
// обработчики меняют сроки записи и сбрасывают буфер.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush передаёт сброс буфера дальше, если он поддерживается.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
