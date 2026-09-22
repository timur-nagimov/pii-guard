package logging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"sort"
	"time"
)

// AuditConfig — настройки журнала аудита.
type AuditConfig struct {
	// Enabled включает журнал аудита.
	Enabled bool
	// Path — файл журнала. Пустое значение означает общий поток вывода: в
	// этом случае записи аудита отличаются полем stream.
	Path string
	// MaxBytes — размер, после которого файл переименовывается, а запись
	// продолжается в новый. Ноль означает размер по умолчанию.
	MaxBytes int64
	// Keep — сколько переименованных файлов хранить.
	Keep int
}

// Размеры ротации по умолчанию.
const (
	defaultAuditMaxBytes = 256 << 20
	defaultAuditKeep     = 8
)

// Audit — журнал аудита. Он отвечает на вопрос «кто, когда и какой системой
// обращался к персональным данным», а не на вопрос «что происходило внутри
// сервиса», и поэтому живёт отдельно от обычного журнала.
//
// Отличия от обычного журнала:
//
//   - состав записи фиксирован и не меняется от места вызова;
//   - уровень не влияет: запись аудита выходит всегда, даже когда обычный
//     журнал поднят до уровня ошибок;
//   - повторы не глушатся: пропущенная запись аудита это пробел в отчётности;
//   - срок хранения свой, обычно годы против недель у обычного журнала;
//   - права на чтение свои: журнал аудита читает служба контроля, а не
//     дежурный инженер.
//
// Значений персональных данных в записи нет: только типы, их число и размер
// текста. По записи видно, что систему CRM обслужили, нашли два телефона и
// одну дату рождения, и сколько это заняло, но не видно ни одного значения.
type Audit struct {
	log   *slog.Logger
	stats *Stats
	on    bool
}

// AuditEvent — одно обращение к персональным данным.
type AuditEvent struct {
	// Op — операция: mask, demask, proxy, inspect.
	Op string
	// System — система-потребитель, от имени которой пришёл запрос.
	System string
	// Actor — отпечаток ключа доступа, которым опознан вызывающий. Сам ключ
	// в журнал не попадает.
	Actor string
	// PayloadID — идентификатор текста в контракте обмена. Приходит от
	// клиента, поэтому перед записью проходит политику идентификатора LogID:
	// непохожее на идентификатор значение выходит в журнал отпечатком.
	PayloadID string
	// Bytes — размер обработанного текста.
	Bytes int
	// Types — сколько фрагментов какого типа найдено.
	Types map[string]int
	// Result — ok, error, degraded.
	Result string
	// Duration — сколько заняла обработка.
	Duration time.Duration
}

// newAudit создаёт журнал аудита. Уровень фиксирован: аудит не выключается
// поднятием уровня обычного журнала.
func newAudit(cfg Config, out io.Writer, stats *Stats) (*Audit, io.Closer, error) {
	if !cfg.Audit.Enabled {
		return &Audit{}, nil, nil
	}
	dst := out
	var closer io.Closer
	if cfg.Audit.Path != "" {
		maxBytes := cfg.Audit.MaxBytes
		if maxBytes <= 0 {
			maxBytes = defaultAuditMaxBytes
		}
		keep := cfg.Audit.Keep
		if keep <= 0 {
			keep = defaultAuditKeep
		}
		w, err := newRotatingWriter(cfg.Audit.Path, maxBytes, keep)
		if err != nil {
			return nil, nil, err
		}
		dst, closer = w, w
	}

	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	h := sink(dst, FormatJSON, lv, false)
	h = newGuard(h, cfg.Redact, stats)
	log := slog.New(h).With(
		slog.String(FieldStream, "audit"),
		slog.String(FieldService, cfg.Service),
	)
	if cfg.Instance != "" {
		log = log.With(slog.String(FieldInstance, cfg.Instance))
	}
	return &Audit{log: log, stats: stats, on: true}, closer, nil
}

// Enabled сообщает, ведётся ли аудит.
func (a *Audit) Enabled() bool { return a != nil && a.on }

// Write записывает обращение к персональным данным.
func (a *Audit) Write(ctx context.Context, ev AuditEvent) {
	if !a.Enabled() {
		return
	}
	attrs := []any{
		slog.String(FieldEvent, EventAudit),
		slog.String(FieldOp, ev.Op),
		slog.String("result", ev.Result),
		slog.Int(FieldBytes, ev.Bytes),
		Took(ev.Duration),
	}
	if ev.System != "" {
		attrs = append(attrs, slog.String(FieldSystem, ev.System))
	}
	if ev.Actor != "" {
		attrs = append(attrs, slog.String("actor", ev.Actor))
	}
	// Политика идентификатора применяется здесь, а не только в обработчике
	// журнала: обработчик разбирает значения лишь при включённой защите, а
	// запись аудита обязана быть безопасной при любых настройках. Обработчик
	// применит её повторно и ничего не изменит: отпечаток на идентификатор
	// похож.
	if id := LogID(ev.PayloadID); id != "" {
		if id != ev.PayloadID {
			a.stats.IDReplaced.Add(1)
		}
		attrs = append(attrs, slog.String(FieldPayloadID, id))
	}
	if len(ev.Types) > 0 {
		attrs = append(attrs, slog.Any("pii_types", ev.Types), slog.Int("pii_total", total(ev.Types)))
	}
	a.log.LogAttrs(ctx, slog.LevelInfo, "обращение к персональным данным", toAttrs(attrs)...)
}

func total(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func toAttrs(vals []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(vals))
	for _, v := range vals {
		if a, ok := v.(slog.Attr); ok {
			out = append(out, a)
		}
	}
	return out
}

// ActorFromKey возвращает отпечаток ключа доступа: первые восемь байтов
// хеша в шестнадцатеричной записи. По отпечатку видно, что ключ один и тот
// же, но сам ключ по нему не восстановить.
func ActorFromKey(key string) string {
	if key == "" {
		return "anonymous"
	}
	sum := sha256.Sum256([]byte(key))
	return "key:" + hex.EncodeToString(sum[:8])
}

// TypeNames возвращает отсортированный список типов из счётчика. Нужен, когда
// в записи важен состав, а не числа.
func TypeNames(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
