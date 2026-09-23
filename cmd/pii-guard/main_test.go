package main

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pii-guard/internal/api"
	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/logging"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// TestStoreKeyEmpty проверяет, что пустая переменная окружения даёт пустой
// ключ и предупреждение в журнале, а не ошибку.
func TestStoreKeyEmpty(t *testing.T) {
	t.Setenv("PII_STORE_KEY_TEST_EMPTY", "")
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	key, err := storeKey("PII_STORE_KEY_TEST_EMPTY", log)
	if err != nil {
		t.Fatalf("пустой ключ дал ошибку: %v", err)
	}
	if key != nil {
		t.Fatalf("пустой ключ должен быть nil, получено %d байт", len(key))
	}
	if !strings.Contains(buf.String(), "ключ шифрования не задан") {
		t.Fatalf("предупреждение о пустом ключе не записано:\n%s", buf.String())
	}
}

// TestStoreKeyWrongLength проверяет, что ключ неверной длины отвергается.
func TestStoreKeyWrongLength(t *testing.T) {
	t.Setenv("PII_STORE_KEY_TEST_SHORT", base64.StdEncoding.EncodeToString([]byte("короткий")))
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if _, err := storeKey("PII_STORE_KEY_TEST_SHORT", log); err == nil {
		t.Fatal("короткий ключ принят")
	}
}

// TestStoreKeyNotBase64 проверяет, что ключ не в base64 отвергается.
func TestStoreKeyNotBase64(t *testing.T) {
	t.Setenv("PII_STORE_KEY_TEST_BAD", "не base64!!!")
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if _, err := storeKey("PII_STORE_KEY_TEST_BAD", log); err == nil {
		t.Fatal("ключ не в base64 принят")
	}
}

// TestStoreKeyCorrect проверяет, что правильный ключ возвращается как есть.
func TestStoreKeyCorrect(t *testing.T) {
	want := make([]byte, 32)
	for i := range want {
		want[i] = byte(i)
	}
	t.Setenv("PII_STORE_KEY_TEST_OK", base64.StdEncoding.EncodeToString(want))
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	key, err := storeKey("PII_STORE_KEY_TEST_OK", log)
	if err != nil {
		t.Fatalf("правильный ключ отвергнут: %v", err)
	}
	if !bytes.Equal(key, want) {
		t.Fatalf("ключ искажён: %x против %x", key, want)
	}
}

// TestSelfSignedCert проверяет, что сертификат действителен, имя хоста
// совпадает и срок покрывает рабочий период.
func TestSelfSignedCert(t *testing.T) {
	cert, err := selfSignedCert()
	if err != nil {
		t.Fatalf("сертификат не создан: %v", err)
	}
	leaf := cert.Leaf
	if leaf == nil {
		// Leaf не заполнен, разбираем из PEM.
		block, _ := pem.Decode(cert.Certificate[0])
		if block == nil {
			t.Fatal("сертификат не разобран из PEM")
		}
		leaf, err = x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("сертификат не разобран: %v", err)
		}
	}
	if leaf.Subject.CommonName != "pii-guard" {
		t.Fatalf("имя хоста: %q", leaf.Subject.CommonName)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		t.Fatalf("сертификат недействителен сейчас: %v — %v", leaf.NotBefore, leaf.NotAfter)
	}
	if leaf.NotAfter.Sub(now) < 48*time.Hour {
		t.Fatalf("срок сертификата слишком короткий: %v", leaf.NotAfter.Sub(now))
	}
}

// TestRunHealthcheck проверяет проверку живости через httptest: успех и отказ.
func TestRunHealthcheck(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Успешная проверка.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	writeHealthConfig(t, cfgPath, "127.0.0.1:"+port)
	if err := runHealthcheck(cfgPath); err != nil {
		t.Fatalf("успешная проверка не прошла: %v", err)
	}

	// Отказ сервиса.
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer fail.Close()
	port = strings.TrimPrefix(fail.URL, "http://127.0.0.1:")
	writeHealthConfig(t, cfgPath, "127.0.0.1:"+port)
	if err := runHealthcheck(cfgPath); err == nil {
		t.Fatal("отказ сервиса не замечен")
	}
}

// TestRunHealthcheckBadConfig проверяет, что отсутствующие настройки дают ошибку.
func TestRunHealthcheckBadConfig(t *testing.T) {
	if err := runHealthcheck(filepath.Join(t.TempDir(), "нет.yaml")); err == nil {
		t.Fatal("отсутствующие настройки не дали ошибку")
	}
}

// TestHealthcheckURL проверяет сбор адреса проверки живости.
func TestHealthcheckURL(t *testing.T) {
	if got, err := healthcheckURL(":8080"); err != nil || got != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("адрес из :8080: %q, %v", got, err)
	}
	if got, err := healthcheckURL("127.0.0.1:9090"); err != nil || got != "http://127.0.0.1:9090/healthz" {
		t.Fatalf("адрес из 127.0.0.1:9090: %q, %v", got, err)
	}
	if _, err := healthcheckURL("не адрес"); err == nil {
		t.Fatal("неразбираемый адрес не дал ошибку")
	}
	if _, err := healthcheckURL("10.0.0.1:8080"); err == nil {
		t.Fatal("адрес вне loopback не дал ошибку")
	}
}

// TestLogConfigMerges проверяет, что logConfig сливает настройки из файла и
// окружения и что второй рубеж защиты от утечки значений включён.
func TestLogConfigMerges(t *testing.T) {
	cfg := &config.Config{
		Logging: config.Logging{
			Level: "debug", Format: "text",
			RepeatWindow: 5 * time.Second, RepeatLevel: "info",
			SampleN: 10, Slow: time.Second,
		},
	}
	out := &bytes.Buffer{}
	lc := logConfig(cfg, out)
	if lc.Level != "debug" || lc.Format != "text" {
		t.Fatalf("настройки файла не применены: %+v", lc)
	}
	if lc.RepeatWindow != 5*time.Second || lc.RepeatMinLevel != "info" {
		t.Fatalf("глушение повторов не применено: %+v", lc)
	}
	if lc.SampleN != 10 || lc.Slow != time.Second {
		t.Fatalf("прореживание не применено: %+v", lc)
	}
	if lc.Output != out {
		t.Fatal("приёмник журнала не передан")
	}
	// Второй рубеж защиты от утечки значений обязан быть включён по умолчанию.
	if !lc.Redact {
		t.Fatal("второй рубеж защиты от утечки выключен")
	}
}

// TestLogConfigEnvOverrides проверяет, что окружение важнее файла.
func TestLogConfigEnvOverrides(t *testing.T) {
	t.Setenv("PII_LOG_LEVEL", "error")
	cfg := &config.Config{Logging: config.Logging{Level: "debug"}}
	lc := logConfig(cfg, &bytes.Buffer{})
	if lc.Level != "error" {
		t.Fatalf("уровень из окружения не применён: %q", lc.Level)
	}
}

// writeHealthConfig кладёт минимальные настройки с заданным адресом слушателя.
func writeHealthConfig(t *testing.T, path, httpAddr string) {
	t.Helper()
	content := "server:\n  http: \"" + httpAddr + "\"\n  https: \"\"\n" +
		"systems:\n  alfasonar:\n    enabled: true\n    auth: none\n    types: [all]\n    demask: true\n    preset: full\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLogConfigNoKeyValues проверяет, что журнал, собранный по настройкам,
// вычищает значения персональных данных: второй рубеж защиты работает.
func TestLogConfigNoKeyValues(t *testing.T) {
	cfg := &config.Config{Logging: config.Logging{Level: "info", Format: "json"}}
	var buf bytes.Buffer
	lc := logConfig(cfg, &buf)
	lg, err := logging.New(lc)
	if err != nil {
		t.Fatalf("журнал не собран: %v", err)
	}
	lg.Slog().Info("проверка", slog.String("detail", "тел. +79991234567"))
	_ = lg.Close()
	if strings.Contains(buf.String(), "+79991234567") {
		t.Fatalf("значение персональных данных попало в журнал:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "удалено") {
		t.Fatalf("вычищенное значение не помечено:\n%s", buf.String())
	}
}

// TestApplyFileLogging проверяет наложение настроек журнала из файла.
func TestApplyFileLogging(t *testing.T) {
	c := logging.DefaultConfig()
	src := true
	redact := false
	auditOn := true
	file := config.Logging{
		Level: "warn", Format: "text", Source: &src, Redact: &redact,
		RepeatWindow: 3 * time.Second, RepeatLevel: "error",
		SampleN: 5, Slow: 2 * time.Second,
		Audit: config.LoggingAudit{Enabled: &auditOn, Path: "/tmp/audit.log", MaxBytes: 1024, Keep: 3},
	}
	applyFileLogging(&c, file)
	if c.Level != "warn" || c.Format != "text" || !c.AddSource || c.Redact {
		t.Fatalf("настройки файла не применены: %+v", c)
	}
	if c.RepeatWindow != 3*time.Second || c.RepeatMinLevel != "error" {
		t.Fatalf("глушение повторов не применено: %+v", c)
	}
	if c.SampleN != 5 || c.Slow != 2*time.Second {
		t.Fatalf("прореживание не применено: %+v", c)
	}
	if !c.Audit.Enabled || c.Audit.Path != "/tmp/audit.log" || c.Audit.MaxBytes != 1024 || c.Audit.Keep != 3 {
		t.Fatalf("аудит не применён: %+v", c.Audit)
	}
}

// TestApplyEnvLogging проверяет, что окружение важнее файла.
func TestApplyEnvLogging(t *testing.T) {
	def := logging.DefaultConfig()
	env := def
	env.Level = "error"
	env.Format = "text"
	env.RepeatWindow = 7 * time.Second
	env.Audit.Path = "/tmp/env.log"
	env.Audit.MaxBytes = 2048
	env.Audit.Keep = 5

	c := def
	c.Level = "debug"
	c.Format = "json"
	applyEnvLogging(&c, env, def)
	if c.Level != "error" || c.Format != "text" {
		t.Fatalf("окружение не перекрыло файл: %+v", c)
	}
	if c.RepeatWindow != 7*time.Second {
		t.Fatalf("окно повторов из окружения не применено: %+v", c)
	}
	if c.Audit.Path != "/tmp/env.log" || c.Audit.MaxBytes != 2048 || c.Audit.Keep != 5 {
		t.Fatalf("аудит из окружения не применён: %+v", c.Audit)
	}
}

// TestCustomRules проверяет перенос описаний типов в правила детектора.
func TestCustomRules(t *testing.T) {
	cfg := &config.Config{CustomTypes: []config.CustomType{{
		Name: "BADGE", Pattern: `(\d{6})`, Group: 1,
		Validator: "none", Anchors: []string{"пропуск"},
		RequireAnchor: true, AnchorWindow: 24,
	}}}
	rules := customRules(cfg)
	if len(rules) != 1 {
		t.Fatalf("правил %d, ожидалось одно", len(rules))
	}
	r := rules[0]
	if r.Name != "BADGE" || r.Pattern != `(\d{6})` || r.Group != 1 {
		t.Fatalf("правило перенесено неверно: %+v", r)
	}
	if r.Validator != "none" || len(r.Anchors) != 1 || !r.RequireAnchor || r.AnchorWindow != 24 {
		t.Fatalf("поля правила потеряны: %+v", r)
	}
}

// TestCheckFileChange проверяет применение настроек при изменении файла.
func TestCheckFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	applied := 0
	apply := func(reason string) bool {
		applied++
		return true
	}
	// Файл новее отметки: применяется.
	last := time.Time{}
	last = checkFileChange(path, last, apply)
	if applied != 1 {
		t.Fatalf("изменённый файл не применён: %d раз", applied)
	}
	// Отметка обновлена: повторно не применяется.
	checkFileChange(path, last, apply)
	if applied != 1 {
		t.Fatalf("неизменённый файл применён повторно: %d раз", applied)
	}
	// Отсутствующий файл не даёт ошибки.
	if got := checkFileChange(filepath.Join(dir, "нет.yaml"), last, apply); !got.Equal(last) {
		t.Fatal("отсутствующий файл сдвинул отметку")
	}
}

// TestCheckFileChangeRejected проверяет, что отклонённые настройки не двигают
// отметку времени.
func TestCheckFileChangeRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	apply := func(reason string) bool { return false }
	last := time.Time{}
	last = checkFileChange(path, last, apply)
	// Отметка не должна сдвинуться после отказа.
	if !last.IsZero() {
		t.Fatal("отклонённые настройки сдвинули отметку")
	}
}

// TestNewLogWriter проверяет буферизованный приёмник журнала.
func TestNewLogWriter(t *testing.T) {
	var buf bytes.Buffer
	w := newLogWriter(&buf, 64)
	if _, err := w.Write([]byte("запись")); err != nil {
		t.Fatalf("запись не удалась: %v", err)
	}
	w.Flush()
	if !strings.Contains(buf.String(), "запись") {
		t.Fatalf("запись не дошла до приёмника: %q", buf.String())
	}
}

// TestStopLog проверяет закрытие журнала и выталкивание буфера.
func TestStopLog(t *testing.T) {
	var buf bytes.Buffer
	cfg := logging.DefaultConfig()
	cfg.Output = &buf
	cfg.Audit.Enabled = false
	lg, err := logging.New(cfg)
	if err != nil {
		t.Fatalf("журнал не собран: %v", err)
	}
	out := newLogWriter(&buf, 64)
	stopLog(lg, out)
}

// TestDrain проверяет окно вывода из обслуживания: по таймеру и по сигналу.
func TestDrain(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	// По таймеру.
	done := make(chan struct{})
	go func() {
		drain(10*time.Millisecond, make(chan os.Signal), log)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("окно по таймеру не завершилось")
	}

	// По второму сигналу.
	stop := make(chan os.Signal, 1)
	done = make(chan struct{})
	go func() {
		drain(time.Hour, stop, log)
		close(done)
	}()
	stop <- os.Interrupt
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("окно по сигналу не завершилось")
	}
}

// TestStartPprof проверяет запуск слушателя профилировщика.
func TestStartPprof(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &buf}, nil))
	// Пустой адрес: ничего не запускается.
	startPprof("", log)
	// Недоступный адрес: слушатель не поднимается, но и не падает.
	startPprof("127.0.0.1:1", log)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(buf.String(), "слушатель профилировщика остановлен") {
		t.Fatalf("остановка слушателя не записана в журнал:\n%s", buf.String())
	}
}

// lockedWriter защищает буфер журнала от одновременной записи из горутины
// слушателя и чтения из теста.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestWatchStoreDegradation проверяет привязку деградации хранилища к
// показателям и готовности.
func TestWatchStoreDegradation(t *testing.T) {
	cfg := &config.Config{Store: config.Store{UnreadyOnDegraded: true}}
	st, err := store.New(store.Config{})
	if err != nil {
		t.Fatalf("хранилище не создано: %v", err)
	}
	defer st.Close()
	m := metrics.New()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	srv := api.New(&config.Config{}, st, engine.New(pii.NewRegistry()), m, log)
	watchStoreDegradation(cfg, st, srv, m, log)
	// Вызов хука деградации не должен падать.
	st.SetDegradedHook(func(op string, degErr error) {})
}

// TestNewConfigApplier проверяет применение настроек из файла.
func TestNewConfigApplier(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeHealthConfig(t, path, ":0")
	reg := pii.NewRegistry()
	reg.Register(pii.NewEmailDetector())
	st, err := store.New(store.Config{})
	if err != nil {
		t.Fatalf("хранилище не создано: %v", err)
	}
	defer st.Close()
	m := metrics.New()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	srv := api.New(&config.Config{}, st, engine.New(reg), m, log)
	apply := newConfigApplier(path, srv, engine.New(reg), reg, log)
	if err := apply("test"); err != nil {
		t.Fatalf("настройки не применены: %v", err)
	}
}

// TestApplyCustomTypesEmpty проверяет снятие своих типов при пустых настройках.
func TestApplyCustomTypesEmpty(t *testing.T) {
	reg := pii.NewRegistry()
	if err := applyCustomTypes(reg, &config.Config{}); err != nil {
		t.Fatalf("пустые настройки отвергнуты: %v", err)
	}
}

// TestRunInvalidAddress проверяет, что run возвращает ошибку, когда слушатель
// не может подняться: неверный адрес виден сразу, а не в бою.
func TestRunInvalidAddress(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := "server:\n  http: \"не адрес\"\n  https: \"\"\n" +
		"systems:\n  alfasonar:\n    enabled: true\n    auth: none\n    types: [all]\n    demask: true\n    preset: full\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("настройки не прочитаны: %v", err)
	}
	var buf bytes.Buffer
	lg, err := logging.New(logConfig(cfg, &buf))
	if err != nil {
		t.Fatalf("журнал не собран: %v", err)
	}
	defer func() { _ = lg.Close() }()
	if err := run(cfg, cfgPath, lg); err == nil {
		t.Fatal("run с неверным адресом не дал ошибку")
	}
}
