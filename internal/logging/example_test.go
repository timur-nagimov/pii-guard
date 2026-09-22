package logging_test

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"pii-guard/internal/logging"
)

// Example показывает подключение журнала при запуске сервиса. Это тот самый
// набор строк, который заменяет прямое создание slog.New в cmd/pii-guard.
func Example() {
	lg, err := logging.New(logging.FromEnv())
	if err != nil {
		os.Exit(1)
	}
	defer func() { _ = lg.Close() }()

	log := lg.Slog() // обычный *slog.Logger для пакетов, не знающих про этот
	log.Info("сервис запущен",
		logging.Event(logging.EventServiceStart),
		slog.Int("detectors", 11))
}

// ExampleMiddleware показывает прослойку на всех маршрутах: идентификатор
// запроса появляется на каждой точке входа и возвращается клиенту.
func ExampleMiddleware() {
	lg := logging.Discard()
	defer func() { _ = lg.Close() }()

	mux := http.NewServeMux()
	mux.HandleFunc("/process", func(w http.ResponseWriter, r *http.Request) {
		// Имя системы известно после проверки ключа доступа.
		logging.SetSystem(r.Context(), "crm")
		w.WriteHeader(http.StatusOK)
	})

	opts := logging.DefaultMiddlewareOptions()
	opts.SampleN = 20 // на большой нагрузке пишем каждый двадцатый запрос
	handler := logging.Middleware(lg, opts)(mux)
	_ = handler
}

// ExampleAudit_Write показывает запись аудита: кто, когда, какой системой,
// сколько фрагментов какого типа. Значений в записи нет.
func ExampleAudit_Write() {
	lg := logging.Discard()
	defer func() { _ = lg.Close() }()

	ctx := logging.WithRequestID(context.Background(), "req-1")
	lg.Audit().Write(ctx, logging.AuditEvent{
		Op:        "mask",
		System:    "crm",
		Actor:     logging.ActorFromKey(os.Getenv("PII_SYSTEM_KEY")),
		PayloadID: "payload-42",
		Bytes:     247,
		Types:     map[string]int{"PHONE": 2, "DOB": 1},
		Result:    "ok",
		Duration:  360 * time.Microsecond,
	})
}

// ExampleLogger_LevelHandler показывает ручку управления уровнем. Её вешают
// на служебный слушатель или закрывают проверкой ключа.
func ExampleLogger_LevelHandler() {
	lg := logging.Discard()
	defer func() { _ = lg.Close() }()

	admin := http.NewServeMux()
	admin.Handle("/admin/loglevel", lg.LevelHandler(func(r *http.Request) bool {
		return r.Header.Get("X-Admin-Key") == os.Getenv("PII_ADMIN_KEY")
	}))
}
