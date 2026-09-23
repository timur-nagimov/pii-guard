package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// TestConfigReloadUnderLoad проверяет, что применение новых настроек под
// нагрузкой не создаёт гонки данных. Сто параллельных запросов /process и
// одновременно десять перезагрузок настроек: сброс фильтров и кеша клиентов
// обязан быть атомарным, иначе детектор гонок поймал бы присваивание
// sync.Map{} целиком при живых чтениях.
func TestConfigReloadUnderLoad(t *testing.T) {
	cfg, err := config.Parse([]byte(testConfigYAML()))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 10000})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector(), pii.NewFIODetector())
	eng := engine.New(reg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, eng, metrics.New(), log)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	const payload = "Иванов Иван, телефон +79161234567, почта ivan@example.com"
	body := processBody(t, payload, "reload-under-load")

	var wg sync.WaitGroup
	// Сто параллельных запросов /process: каждый читает фильтры систем и
	// кеш клиентов, которые перезагрузка сбрасывает.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/process", strings.NewReader(body))
			if err != nil {
				t.Errorf("запрос не собрался: %v", err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Errorf("запрос не выполнился: %v", err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()
	}
	// Десять перезагрузок настроек одновременно с запросами.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.SetConfig(cfg)
			eng.ResetFilters()
		}()
	}
	wg.Wait()
}
