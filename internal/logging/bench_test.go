package logging

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// typicalValues — значения полей, которые сервис пишет на горячем пути.
var typicalValues = []string{
	"process", "crm", "mask", "payload-42", "/process",
	"POST", "обработан запрос", "10.129.0.22:8080",
	"application/json", "connection reset by peer",
}

// BenchmarkInspectString измеряет стоимость проверки одного значения.
func BenchmarkInspectString(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, v := range typicalValues {
			if _, bad := inspectString(v); bad {
				b.Fatalf("значение %q задержано", v)
			}
		}
	}
}

// BenchmarkRecordGuarded измеряет стоимость записи со всей защитой.
func BenchmarkRecordGuarded(b *testing.B) { benchRecord(b, true) }

// BenchmarkRecordPlain измеряет ту же запись без защиты: разница между двумя
// измерениями и есть цена второго рубежа.
func BenchmarkRecordPlain(b *testing.B) { benchRecord(b, false) }

func benchRecord(b *testing.B, redact bool) {
	cfg := DefaultConfig()
	cfg.Redact = redact
	cfg.Output = io.Discard
	cfg.Audit.Enabled = false
	l, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	ctx := WithRequestID(context.Background(), "0123456789abcdef0123456789abcdef")
	SetSystem(ctx, "crm")
	log := l.Slog()
	counts := map[string]int{"PHONE": 2, "DOB": 1}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.LogAttrs(ctx, slog.LevelInfo, "обработан запрос",
			slog.String(FieldEvent, EventProcess),
			slog.String(FieldOp, "mask"),
			slog.String(FieldPayloadID, "payload-42"),
			slog.Int(FieldBytes, 247),
			slog.Any("pii_types", counts),
			Took(360*time.Microsecond))
	}
}

// BenchmarkDisabledDebug измеряет стоимость отладочной записи при выключенном
// отладочном уровне: на горячем пути она обязана быть почти бесплатной.
func BenchmarkDisabledDebug(b *testing.B) {
	cfg := DefaultConfig()
	cfg.Output = io.Discard
	cfg.Audit.Enabled = false
	l, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	log := l.Slog()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.LogAttrs(context.Background(), slog.LevelDebug, "разбор текста",
			slog.String(FieldEvent, EventProcess), slog.Int(FieldBytes, 247))
	}
}

// TestRecordSize печатает размер типовых записей. Числа из него идут в расчёт
// объёма журнала в документе docs/LOGGING.md. Настройки взяты эксплуатационные:
// с именем копии и версией сборки, потому что на стенде они заданы.
func TestRecordSize(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Instance = "pii-guard-1"
	cfg.Version = "2026.09.22-1"
	l, buf := newTestLogger(t, cfg)
	ctx := WithRequestID(context.Background(), "0123456789abcdef0123456789abcdef")
	SetSystem(ctx, "crm")

	l.Slog().LogAttrs(ctx, slog.LevelInfo, "запрос обслужен",
		slog.String(FieldEvent, EventHTTPRequest),
		slog.String(FieldMethod, "POST"),
		slog.String(FieldPath, "/process"),
		slog.Int(FieldStatus, 200),
		slog.Int(FieldBytes, 512),
		Took(360*time.Microsecond))
	access := len(buf.String())

	l.Slog().LogAttrs(ctx, slog.LevelInfo, "обработан запрос",
		slog.String(FieldEvent, EventProcess),
		slog.String(FieldOp, "mask"),
		slog.String(FieldPayloadID, "payload-42"),
		slog.Int(FieldBytes, 247),
		slog.Any("pii_types", map[string]int{"PHONE": 2, "DOB": 1}),
		Took(360*time.Microsecond))
	process := len(buf.String()) - access

	l.Audit().Write(ctx, AuditEvent{
		Op: "mask", System: "crm", Actor: ActorFromKey("k"), PayloadID: "payload-42",
		Bytes: 247, Types: map[string]int{"PHONE": 2, "DOB": 1}, Result: "ok",
		Duration: 360 * time.Microsecond,
	})
	audit := len(buf.String()) - access - process

	t.Logf("размер записи о запросе: %d байт", access)
	t.Logf("размер записи об обработке: %d байт", process)
	t.Logf("размер записи аудита: %d байт", audit)
	if access > 400 || process > 450 || audit > 550 {
		t.Fatalf("записи разрослись: %d, %d и %d байт, расчёт объёма в docs/LOGGING.md устарел",
			access, process, audit)
	}
}
