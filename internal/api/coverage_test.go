package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// newCoverageServer поднимает сервер с настройками, в которых есть и
// выключенная система, и система без обратного преобразования, и тип,
// добавленный настройкой через custom_types.
func newCoverageServer(t *testing.T) *Server {
	t.Helper()

	cfg, err := config.Parse([]byte(coverageConfigYAML))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 16})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, st, engine.New(pii.NewRegistry()), metrics.New(), log)
}

// coverageConfigYAML — настройки для тестов матрицы: одна включённая система
// с обратным преобразованием, одна выключенная, одна без обратного, и тип,
// добавленный настройкой.
const coverageConfigYAML = `
defaults:
  preset: full
  min_confidence: 0.6
systems:
  alfasonar:
    enabled: true
    auth: none
    types: [FIO, PHONE, CUSTOM_ID]
    demask: true
    preset: partial
    per_type:
      PHONE: full
  crm:
    enabled: true
    auth:
      header: X-System-Key
      key_sha256: 1111111111111111111111111111111111111111111111111111111111111111
    types: [all]
    demask: false
    preset: full
  offline:
    enabled: false
    auth:
      header: X-System-Key
      key_sha256: 2222222222222222222222222222222222222222222222222222222222222222
    types: [all]
    demask: true
    preset: full
custom_types:
  - name: CUSTOM_ID
    pattern: "[A-Z]{2}[0-9]{4}"
`

// doCoverage выполняет запрос матрицы и разбирает ответ.
func doCoverage(t *testing.T, srv *Server, method string) (*http.Response, coverageResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.handleCoverage(rec, httptest.NewRequest(method, "/v1/coverage", nil))
	resp := rec.Result()
	var out coverageResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("не удалось разобрать ответ: %v", err)
		}
	}
	return resp, out
}

// TestCoverageMatrix проверяет главное: матрица отдаёт системы, типы и клетки,
// и по ней видно, что маскируется, что выключено, а чего нет вовсе.
func TestCoverageMatrix(t *testing.T) {
	srv := newCoverageServer(t)
	resp, out := doCoverage(t, srv, http.MethodGet)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код ответа %d, ожидался 200", resp.StatusCode)
	}

	if len(out.Systems) != 3 {
		t.Fatalf("систем в матрице %d, ожидалось 3", len(out.Systems))
	}
	if len(out.Types) == 0 {
		t.Fatal("в матрице нет ни одного типа")
	}

	// Тип из задания обязан быть помечен как task.
	foundTask := false
	for _, tt := range out.Types {
		if tt.Name == string(pii.TypeFIO) && tt.Source == coverageSourceTask {
			foundTask = true
		}
	}
	if !foundTask {
		t.Error("тип FIO из задания не помечен источником task")
	}

	// Тип, добавленный настройкой, обязан быть помечен как custom.
	foundCustom := false
	for _, tt := range out.Types {
		if tt.Name == "CUSTOM_ID" && tt.Source == coverageSourceCustom {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Error("тип CUSTOM_ID из custom_types не помечен источником custom")
	}
}

// TestCoverageCells проверяет раскладку клеток: пресет по типу, выключенная
// система и тип, который система не маскирует.
func TestCoverageCells(t *testing.T) {
	srv := newCoverageServer(t)
	_, out := doCoverage(t, srv, http.MethodGet)

	// alfasonar маскирует FIO пресетом partial (свой), PHONE — full (per_type).
	if got := out.Cells["FIO"]["alfasonar"]; got != "partial" {
		t.Errorf("FIO в alfasonar = %q, ожидался partial", got)
	}
	if got := out.Cells["PHONE"]["alfasonar"]; got != "full" {
		t.Errorf("PHONE в alfasonar = %q, ожидался full из per_type", got)
	}
	// crm маскирует всё пресетом full.
	if got := out.Cells["FIO"]["crm"]; got != "full" {
		t.Errorf("FIO в crm = %q, ожидался full", got)
	}
	// offline выключена целиком.
	if got := out.Cells["FIO"]["offline"]; got != coverageDisabled {
		t.Errorf("FIO в offline = %q, ожидался disabled", got)
	}
	// alfasonar не маскирует тип, которого нет в её списке.
	if got := out.Cells["EMAIL"]["alfasonar"]; got != coverageOff {
		t.Errorf("EMAIL в alfasonar = %q, ожидался off", got)
	}
}

// TestCoverageSystemFlags проверяет, что по матрице видно обратное
// преобразование и порог уверенности каждой системы.
func TestCoverageSystemFlags(t *testing.T) {
	srv := newCoverageServer(t)
	_, out := doCoverage(t, srv, http.MethodGet)

	byName := make(map[string]coverageSystem, len(out.Systems))
	for _, sys := range out.Systems {
		byName[sys.Name] = sys
	}

	if !byName["alfasonar"].Demask {
		t.Error("alfasonar: demask должен быть разрешён")
	}
	if byName["crm"].Demask {
		t.Error("crm: demask должен быть запрещён")
	}
	if byName["offline"].Enabled {
		t.Error("offline: система должна быть помечена выключенной")
	}
	if got := byName["alfasonar"].MinConfidence; got != 0.6 {
		t.Errorf("alfasonar: порог уверенности %f, ожидался 0.6", got)
	}
}

// TestCoverageReflectsConfigChange проверяет, что матрица живёт в такт с
// настройками: после подмены конфигурации без перезапуска она меняется.
func TestCoverageReflectsConfigChange(t *testing.T) {
	srv := newCoverageServer(t)

	_, before := doCoverage(t, srv, http.MethodGet)
	if got := before.Cells["FIO"]["alfasonar"]; got != "partial" {
		t.Fatalf("до смены FIO в alfasonar = %q, ожидался partial", got)
	}

	cfg, err := config.Parse([]byte(coverageConfigYAML))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	// Меняем пресет alfasonar на full и применяем без перезапуска.
	sys := cfg.Systems["alfasonar"]
	sys.Preset = "full"
	cfg.Systems["alfasonar"] = sys
	srv.SetConfig(cfg)

	_, after := doCoverage(t, srv, http.MethodGet)
	if got := after.Cells["FIO"]["alfasonar"]; got != "full" {
		t.Errorf("после смены FIO в alfasonar = %q, ожидался full", got)
	}
}

// TestCoverageRejectsNonGet проверяет, что матрица отвечает только на GET.
func TestCoverageRejectsNonGet(t *testing.T) {
	srv := newCoverageServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		resp, _ := doCoverage(t, srv, method)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("метод %s дал код %d, ожидался 405", method, resp.StatusCode)
		}
		if got := resp.Header.Get("Allow"); got != http.MethodGet {
			t.Errorf("метод %s: заголовок Allow = %q, ожидался GET", method, got)
		}
		if !json.Valid(body) {
			t.Errorf("метод %s: ответ не JSON", method)
		}
	}
}
