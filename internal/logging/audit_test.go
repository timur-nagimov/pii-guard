package logging

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAuditRecord проверяет состав записи аудита: кто, когда, какой системой,
// сколько фрагментов какого типа. Значений в записи нет.
func TestAuditRecord(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	ctx := WithRequestID(context.Background(), "req-1")

	l.Audit().Write(ctx, AuditEvent{
		Op:        "mask",
		System:    "crm",
		Actor:     ActorFromKey("секретный ключ"),
		PayloadID: "payload-42",
		Bytes:     247,
		Types:     map[string]int{"PHONE": 2, "DOB": 1},
		Result:    "ok",
		Duration:  360 * time.Microsecond,
	})

	recs := records(t, buf)
	if len(recs) != 1 {
		t.Fatalf("ожидалась одна запись аудита, получено %d", len(recs))
	}
	rec := recs[0]
	want := map[string]any{
		FieldStream:    "audit",
		FieldEvent:     EventAudit,
		FieldOp:        "mask",
		FieldSystem:    "crm",
		FieldPayloadID: "payload-42",
		"result":       "ok",
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("поле %q равно %v, ожидалось %v", k, rec[k], v)
		}
	}
	if rec["pii_total"] != float64(3) {
		t.Errorf("общее число фрагментов равно %v", rec["pii_total"])
	}
	if rec[FieldRequestID] != "req-1" {
		t.Errorf("идентификатор запроса не подставлен: %v", rec[FieldRequestID])
	}
	if strings.Contains(buf.String(), "секретный ключ") {
		t.Fatal("ключ доступа попал в журнал аудита")
	}
	if !strings.HasPrefix(rec["actor"].(string), "key:") {
		t.Errorf("отпечаток ключа записан неверно: %v", rec["actor"])
	}
}

// TestAuditIgnoresLevel проверяет, что аудит не выключается поднятием уровня
// обычного журнала. Пропущенная запись аудита это пробел в отчётности.
func TestAuditIgnoresLevel(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	if err := l.SetLevel("error"); err != nil {
		t.Fatal(err)
	}
	l.Audit().Write(context.Background(), AuditEvent{Op: "demask", System: "crm", Result: "ok"})
	found := false
	for _, rec := range records(t, buf) {
		if rec[FieldStream] == "audit" {
			found = true
		}
	}
	if !found {
		t.Fatal("запись аудита пропала при поднятом уровне журнала")
	}
}

// TestAuditRejectsBadPayloadID проверяет, что чужой идентификатор со значением
// внутри в журнал аудита не попадает.
func TestAuditRejectsBadPayloadID(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	l.Audit().Write(context.Background(), AuditEvent{
		Op: "mask", System: "crm", Result: "ok",
		PayloadID: "Иванов Иван Иванович",
	})
	if strings.Contains(buf.String(), "Иванов") {
		t.Fatalf("значение попало в журнал аудита: %s", buf.String())
	}
}

// TestAuditFile проверяет запись аудита в отдельный файл и ротацию по размеру.
func TestAuditFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	cfg := DefaultConfig()
	cfg.Audit = AuditConfig{Enabled: true, Path: path, MaxBytes: 2048, Keep: 2}
	cfg.Output = os.Stdout
	l, err := New(cfg)
	if err != nil {
		t.Fatalf("журнал не создан: %v", err)
	}
	for i := 0; i < 200; i++ {
		l.Audit().Write(context.Background(), AuditEvent{
			Op: "mask", System: "crm", Result: "ok", Bytes: 247,
			Types: map[string]int{"PHONE": 1},
		})
	}
	if err := l.Close(); err != nil {
		t.Fatalf("журнал не закрыт: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл аудита не прочитан: %v", err)
	}
	var rec map[string]any
	line := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("запись аудита не разобрана: %v", err)
	}
	if rec[FieldStream] != "audit" {
		t.Errorf("поток записи равен %v", rec[FieldStream])
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("ротация не сработала: %v", err)
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Fatal("хранится больше файлов, чем задано")
	}
}

// TestAuditDisabled проверяет, что выключенный аудит ничего не пишет и не
// роняет вызов.
func TestAuditDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Audit.Enabled = false
	l, buf := newTestLogger(t, cfg)
	l.Audit().Write(context.Background(), AuditEvent{Op: "mask"})
	if n := len(records(t, buf)); n != 0 {
		t.Fatalf("выключенный аудит записал %d строк", n)
	}
}

// TestAuditCompression измеряет, во сколько раз сжимается журнал аудита.
// Число нужно для расчёта хранения: журнал аудита хранится годами, и хранить
// его в несжатом виде негде.
func TestAuditCompression(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Instance = "pii-guard-1"
	cfg.Version = "2026.09.22-1"
	l, buf := newTestLogger(t, cfg)

	types := []map[string]int{
		{"PHONE": 1},
		{"PHONE": 2, "DOB": 1},
		{"FIO": 1, "ADDRESS": 1, "PASSPORT": 1},
		{},
	}
	ops := []string{"mask", "demask", "proxy"}
	for i := 0; i < 10000; i++ {
		ctx := WithRequestID(context.Background(), NewID())
		l.Audit().Write(ctx, AuditEvent{
			Op:        ops[i%len(ops)],
			System:    "crm",
			Actor:     ActorFromKey("ключ"),
			PayloadID: "payload-" + strconv.Itoa(i),
			Bytes:     200 + i%300,
			Types:     types[i%len(types)],
			Result:    "ok",
			Duration:  time.Duration(300+i%500) * time.Microsecond,
		})
	}
	raw := []byte(buf.String())

	var packed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&packed, gzip.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	ratio := float64(len(raw)) / float64(packed.Len())
	t.Logf("десять тысяч записей аудита: %d байт, после сжатия %d байт, отношение %.1f",
		len(raw), packed.Len(), ratio)
	if ratio < 3 {
		t.Fatalf("журнал сжимается всего в %.1f раза, расчёт хранения в docs/LOGGING.md надо пересмотреть", ratio)
	}
}
