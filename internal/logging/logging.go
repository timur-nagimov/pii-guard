// Package logging собирает журнал сервиса в одной точке. Пакет отвечает за
// четыре вещи сразу:
//
//   - создание журнала по настройкам: уровень и формат, JSON для эксплуатации
//     и читаемый текст для разработки;
//   - единый стандарт полей: время, уровень, событие, идентификатор запроса,
//     система-потребитель, длительность есть в каждой записи;
//   - второй рубеж защиты от утечки: перед записью значения полей проверяются
//     и то, что похоже на персональные данные, наружу не уходит;
//   - глушение повторов: шторм одинаковых ошибок не затапливает хранилище.
//
// Первый рубеж защиты это дисциплина вызова: значения персональных данных в
// журнал не передаются вовсе. Проверка здесь страхует от ошибки человека.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Форматы вывода журнала.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// Имена обязательных и часто встречающихся полей. Имя поля задаётся здесь и
// нигде больше: разнобой в именах делает журнал непригодным для поиска.
const (
	FieldEvent     = "event"
	FieldRequestID = "request_id"
	FieldSystem    = "system"
	FieldDuration  = "duration_ms"
	FieldComponent = "component"
	FieldError     = "error"
	FieldOp        = "op"
	FieldStatus    = "status"
	FieldMethod    = "method"
	FieldPath      = "path"
	FieldPayloadID = "payload_id"
	FieldBytes     = "bytes"
	FieldSuppress  = "suppressed"
	FieldRedacted  = "redacted"
	FieldStream    = "stream"
	FieldService   = "service"
	FieldInstance  = "instance"
	FieldVersion   = "version"
)

// Значения поля event. Событие это машинный код записи; по нему строятся
// выборки и правила оповещения, поэтому набор закрыт и расширяется осознанно.
const (
	EventUnspecified    = "unspecified"
	EventServiceStart   = "service_start"
	EventServiceStop    = "service_stop"
	EventListen         = "listen"
	EventConfigApplied  = "config_applied"
	EventConfigRejected = "config_rejected"
	EventHTTPRequest    = "http_request"
	EventProcess        = "process"
	EventProxy          = "proxy"
	EventInspect        = "inspect"
	EventStoreDegraded  = "store_degraded"
	EventDetectorPanic  = "detector_panic"
	EventHandlerPanic   = "handler_panic"
	EventOverload       = "overload"
	EventLogSuppressed  = "log_suppressed"
	EventLogLevel       = "log_level_changed"
	EventAudit          = "audit"
)

// Config — настройки журнала. Нулевое значение пригодно к работе: JSON,
// уровень info, вывод в стандартный поток, защита от утечки включена.
type Config struct {
	// Level — минимальный уровень записи: debug, info, warn, error.
	Level string
	// Format — json для эксплуатации, text для разработки.
	Format string
	// Output — куда писать. Пустое значение означает стандартный вывод.
	Output io.Writer
	// AddSource добавляет файл и строку места вызова. На горячем пути стоит
	// дорого, поэтому по умолчанию выключено.
	AddSource bool
	// Redact включает проверку значений перед записью. Выключать её можно
	// только в измерениях скорости.
	Redact bool
	// RepeatWindow — окно глушения повторов. Одинаковая запись выводится не
	// чаще одного раза в окно, остальные считаются.
	RepeatWindow time.Duration
	// RepeatMinLevel — с какого уровня включается глушение повторов. По
	// умолчанию warn: поток обычных записей прореживается другим способом.
	RepeatMinLevel string
	// SampleN — коэффициент прореживания записей об успешных запросах.
	// Единица означает, что пишутся все. Как выбрать значение, смотри расчёт
	// объёма в docs/LOGGING.md.
	SampleN int
	// Slow — порог, после которого запрос пишется вопреки прореживанию.
	Slow time.Duration
	// Service, Instance, Version попадают в каждую запись и позволяют
	// отличить копии сервиса друг от друга в общем хранилище журнала.
	Service  string
	Instance string
	Version  string
	// Audit — настройки отдельного журнала аудита.
	Audit AuditConfig
}

// DefaultConfig возвращает настройки по умолчанию.
func DefaultConfig() Config {
	return Config{
		Level:          "info",
		Format:         FormatJSON,
		Redact:         true,
		RepeatWindow:   10 * time.Second,
		RepeatMinLevel: "warn",
		SampleN:        1,
		Slow:           250 * time.Millisecond,
		Service:        "pii-guard",
		Audit:          AuditConfig{Enabled: true},
	}
}

// FromEnv читает настройки журнала из окружения поверх значений по умолчанию.
// Окружение выбрано сознательно: журнал поднимается раньше, чем читается файл
// настроек, и обязан работать даже если файл не прочитан.
//
//	PII_LOG_LEVEL          debug|info|warn|error
//	PII_LOG_FORMAT         json|text
//	PII_LOG_SOURCE         1 добавляет файл и строку
//	PII_LOG_REDACT         0 выключает проверку значений
//	PII_LOG_REPEAT_WINDOW  окно глушения повторов, например 10s
//	PII_LOG_REPEAT_LEVEL   с какого уровня глушить повторы
//	PII_LOG_SAMPLE         коэффициент прореживания записей о запросах
//	PII_LOG_SLOW           порог, после которого запрос пишется всегда
//	PII_INSTANCE           имя копии сервиса
//	PII_VERSION            версия сборки
//	PII_AUDIT              0 выключает журнал аудита
//	PII_AUDIT_PATH         файл журнала аудита, пусто означает общий вывод
//	PII_AUDIT_MAX_BYTES    размер файла, после которого делается ротация
//	PII_AUDIT_KEEP         сколько файлов хранить после ротации
func FromEnv() Config {
	cfg := DefaultConfig()
	if v := os.Getenv("PII_LOG_LEVEL"); v != "" {
		cfg.Level = v
	}
	if v := os.Getenv("PII_LOG_FORMAT"); v != "" {
		cfg.Format = v
	}
	cfg.AddSource = envBool("PII_LOG_SOURCE", cfg.AddSource)
	cfg.Redact = envBool("PII_LOG_REDACT", cfg.Redact)
	if v, ok := envDuration("PII_LOG_REPEAT_WINDOW"); ok {
		cfg.RepeatWindow = v
	}
	if v := os.Getenv("PII_LOG_REPEAT_LEVEL"); v != "" {
		cfg.RepeatMinLevel = v
	}
	if v, err := strconv.Atoi(os.Getenv("PII_LOG_SAMPLE")); err == nil && v > 0 {
		cfg.SampleN = v
	}
	if v, ok := envDuration("PII_LOG_SLOW"); ok {
		cfg.Slow = v
	}
	if v := os.Getenv("PII_INSTANCE"); v != "" {
		cfg.Instance = v
	}
	if v := os.Getenv("PII_VERSION"); v != "" {
		cfg.Version = v
	}
	cfg.Audit.Enabled = envBool("PII_AUDIT", cfg.Audit.Enabled)
	cfg.Audit.Path = os.Getenv("PII_AUDIT_PATH")
	if v, err := strconv.ParseInt(os.Getenv("PII_AUDIT_MAX_BYTES"), 10, 64); err == nil && v > 0 {
		cfg.Audit.MaxBytes = v
	}
	if v, err := strconv.Atoi(os.Getenv("PII_AUDIT_KEEP")); err == nil && v > 0 {
		cfg.Audit.Keep = v
	}
	return cfg
}

func envBool(name string, def bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

func envDuration(name string) (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, false
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// Имена уровней журнала. Разбор имени и обратная печать обязаны знать одно и
// то же написание: ручка смены уровня на ходу возвращает имя в ответе, и
// разойдись они — сервис перестал бы понимать то, что сам же отдал.
const (
	levelDebug = "debug"
	levelInfo  = "info"
	levelWarn  = "warn"
	levelError = "error"
)

// ParseLevel переводит имя уровня в значение slog.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case levelDebug:
		return slog.LevelDebug, nil
	case "", levelInfo:
		return slog.LevelInfo, nil
	case levelWarn, "warning":
		return slog.LevelWarn, nil
	case levelError:
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("неизвестный уровень журнала %q", s)
	}
}

// LevelName возвращает имя уровня строчными буквами.
func LevelName(l slog.Level) string {
	switch {
	case l <= slog.LevelDebug:
		return levelDebug
	case l < slog.LevelWarn:
		return levelInfo
	case l < slog.LevelError:
		return levelWarn
	default:
		return levelError
	}
}

// Logger — журнал сервиса вместе с управлением уровнем, счётчиками и
// отдельным потоком аудита.
type Logger struct {
	log   *slog.Logger
	audit *Audit
	level *slog.LevelVar
	stats *Stats

	repeat  *repeatHandler
	closers []io.Closer
	once    sync.Once

	sampleN int
	slow    time.Duration
}

// New собирает журнал по настройкам. Возвращённый журнал нужно закрыть при
// остановке сервиса, иначе последние записи аудита могут не дойти до файла.
func New(cfg Config) (*Logger, error) {
	if cfg.Level == "" {
		cfg.Level = "info"
	}
	if cfg.Format == "" {
		cfg.Format = FormatJSON
	}
	if cfg.Service == "" {
		cfg.Service = "pii-guard"
	}
	lvl, err := ParseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	repeatMin, err := ParseLevel(cfg.RepeatMinLevel)
	if err != nil {
		return nil, err
	}
	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}

	lv := new(slog.LevelVar)
	lv.Set(lvl)
	stats := &Stats{}

	h := sink(out, cfg.Format, lv, cfg.AddSource)
	h = newGuard(h, cfg.Redact, stats)
	rep := newRepeatHandler(h, cfg.RepeatWindow, repeatMin, stats)

	base := slog.New(rep).With(commonAttrs(&cfg)...)

	l := &Logger{
		log: base, level: lv, stats: stats, repeat: rep,
		sampleN: cfg.SampleN, slow: cfg.Slow,
	}
	rep.start()

	audit, closer, err := newAudit(cfg, out, stats)
	if err != nil {
		_ = rep.Close()
		return nil, err
	}
	l.audit = audit
	if closer != nil {
		l.closers = append(l.closers, closer)
	}
	return l, nil
}

func commonAttrs(cfg *Config) []any {
	attrs := []any{slog.String(FieldService, cfg.Service)}
	if cfg.Instance != "" {
		attrs = append(attrs, slog.String(FieldInstance, cfg.Instance))
	}
	if cfg.Version != "" {
		attrs = append(attrs, slog.String(FieldVersion, cfg.Version))
	}
	return attrs
}

// sink создаёт обработчик вывода нужного формата.
func sink(out io.Writer, format string, lv *slog.LevelVar, addSource bool) slog.Handler {
	opts := &slog.HandlerOptions{Level: lv, AddSource: addSource}
	if strings.EqualFold(format, FormatText) {
		opts.ReplaceAttr = shortTime
		return slog.NewTextHandler(out, opts)
	}
	return slog.NewJSONHandler(out, opts)
}

// shortTime укорачивает время в читаемом формате: при разработке нужны часы,
// минуты и миллисекунды, а не полная дата с часовым поясом.
func shortTime(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		if t, ok := a.Value.Any().(time.Time); ok {
			return slog.String(slog.TimeKey, t.Format("15:04:05.000"))
		}
	}
	return a
}

// Slog возвращает журнал в виде стандартного slog.Logger. Такой журнал можно
// передавать в пакеты, которые ничего не знают об этом пакете.
func (l *Logger) Slog() *slog.Logger { return l.log }

// Audit возвращает журнал аудита.
func (l *Logger) Audit() *Audit { return l.audit }

// MiddlewareOptions возвращает настройки прослойки, согласованные с
// настройками журнала. Отдельного места для коэффициента прореживания заводить
// не нужно: он приходит оттуда же, откуда уровень и формат.
func (l *Logger) MiddlewareOptions() MiddlewareOptions {
	opts := DefaultMiddlewareOptions()
	if l.sampleN > 0 {
		opts.SampleN = l.sampleN
	}
	if l.slow > 0 {
		opts.Slow = l.slow
	}
	return opts
}

// Stats возвращает счётчики работы журнала: сколько записей вышло, сколько
// значений вычищено, сколько повторов заглушено.
func (l *Logger) Stats() *Stats { return l.stats }

// Level возвращает действующий уровень.
func (l *Logger) Level() slog.Level { return l.level.Level() }

// SetLevel меняет уровень на ходу, без перезапуска сервиса. Это то, ради чего
// отладочный уровень на горячем пути вообще допустим: его включают на минуту
// и выключают обратно.
func (l *Logger) SetLevel(name string) error {
	lvl, err := ParseLevel(name)
	if err != nil {
		return err
	}
	prev := l.level.Level()
	l.level.Set(lvl)
	if prev != lvl {
		l.log.Warn("уровень журнала изменён",
			slog.String(FieldEvent, EventLogLevel),
			slog.String("from", LevelName(prev)),
			slog.String("to", LevelName(lvl)))
	}
	return nil
}

// Enabled сообщает, будет ли записан уровень. Нужен, чтобы не собирать
// дорогие поля отладочной записи впустую.
func (l *Logger) Enabled(lvl slog.Level) bool { return lvl >= l.level.Level() }

// Close останавливает фоновые задачи журнала и закрывает файлы.
func (l *Logger) Close() error {
	var err error
	l.once.Do(func() {
		if l.repeat != nil {
			err = l.repeat.Close()
		}
		for _, c := range l.closers {
			if cerr := c.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}
	})
	return err
}

// Discard возвращает журнал, который ничего не пишет. Нужен в тестах.
func Discard() *Logger {
	l, err := New(Config{Level: "error", Output: io.Discard, Audit: AuditConfig{}})
	if err != nil {
		panic(err)
	}
	return l
}

// Event возвращает поле события.
func Event(name string) slog.Attr { return slog.String(FieldEvent, name) }

// Took возвращает поле длительности в миллисекундах с точностью до микросекунды.
// Миллисекунды выбраны потому, что доли задержки сервиса лежат в диапазоне от
// десятых долей миллисекунды до секунды.
func Took(d time.Duration) slog.Attr {
	return slog.Float64(FieldDuration, float64(d.Microseconds())/1000)
}

// Err возвращает поле ошибки. Текст ошибки проходит ту же проверку, что и
// остальные значения: ошибка разбора может утащить за собой кусок входа.
func Err(err error) slog.Attr {
	if err == nil {
		return slog.String(FieldError, "")
	}
	return slog.String(FieldError, err.Error())
}

// Component возвращает поле подсистемы: api, store, engine, proxy, config.
func Component(name string) slog.Attr { return slog.String(FieldComponent, name) }
