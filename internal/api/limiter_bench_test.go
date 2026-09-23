package api

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// benchServer собирает сервер с большим ограничителем: замер меряет стоимость
// самого ограничителя, а не ожидания в нём.
func benchServer(tb testing.TB, inflight int) *Server {
	tb.Helper()
	cfg, err := config.Parse([]byte(`
limits:
  inflight: 4096
  heavy_inflight: 64
  heavy_threshold_bytes: 65536
  max_wait: 500ms
store:
  ttl: 60m
defaults:
  preset: full
  min_confidence: 0.5
systems:
  bench:
    enabled: true
    auth: none
    types: [all]
    preset: full
`))
	if err != nil {
		tb.Fatalf("настройки не разобрались: %v", err)
	}
	cfg.Limits.Inflight = inflight
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 1000})
	if err != nil {
		tb.Fatalf("хранилище не создалось: %v", err)
	}
	tb.Cleanup(st.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, st, engine.New(pii.NewRegistry()), metrics.New(), log)
}

// BenchmarkAcquireFree меряет ограничитель, когда место свободно: именно так
// он работает на штатной нагрузке.
func BenchmarkAcquireFree(b *testing.B) {
	s := benchServer(b, 4096)
	limits := s.Config().Limits
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			release, ok := s.acquire(ctx, false, limits)
			if !ok {
				b.Fatal("место в ограничителе не нашлось")
			}
			release()
		}
	})
}

// TestAcquireRefusesWhenFull проверяет, что при занятом ограничителе запрос
// получает отказ не позже отведённого срока ожидания, а не висит до таймаута
// клиента.
func TestAcquireRefusesWhenFull(t *testing.T) {
	s := benchServer(t, 1)
	limits := s.Config().Limits
	limits.MaxWait = 50 * time.Millisecond
	ctx := context.Background()

	release, ok := s.acquire(ctx, false, limits)
	if !ok {
		t.Fatal("первое место в ограничителе не выдалось")
	}

	started := time.Now()
	if _, ok := s.acquire(ctx, false, limits); ok {
		t.Fatal("ограничитель выдал место сверх своего размера")
	}
	waited := time.Since(started)
	if waited < limits.MaxWait {
		t.Fatalf("отказ пришёл через %v, раньше отведённого срока %v", waited, limits.MaxWait)
	}
	if waited > 10*limits.MaxWait {
		t.Fatalf("отказ пришёл через %v, намного позже отведённого срока %v", waited, limits.MaxWait)
	}

	release()
	if _, ok := s.acquire(ctx, false, limits); !ok {
		t.Fatal("освобождённое место не выдалось заново")
	}
}

// TestAcquireReleasesCommonSlotOnHeavyRefusal проверяет, что отказ в отдельном
// ограничителе для больших текстов возвращает занятое место в общем
// ограничителе. Иначе общий ограничитель протекал бы на каждом отказе.
func TestAcquireReleasesCommonSlotOnHeavyRefusal(t *testing.T) {
	s := benchServer(t, 4)
	limits := s.Config().Limits
	limits.MaxWait = 50 * time.Millisecond
	ctx := context.Background()

	// Занимаем единственное место для больших текстов.
	s.heavySem = make(chan struct{}, 1)
	s.heavySem <- struct{}{}

	if _, ok := s.acquire(ctx, true, limits); ok {
		t.Fatal("ограничитель больших текстов выдал место сверх своего размера")
	}
	if len(s.sem) != 0 {
		t.Fatalf("после отказа в общем ограничителе осталось занято %d мест", len(s.sem))
	}
}

// TestAcquireStopsOnCanceledContext проверяет, что отменённый запрос перестаёт
// ждать места сразу, не занимая ограничитель на весь отведённый срок.
func TestAcquireStopsOnCanceledContext(t *testing.T) {
	s := benchServer(t, 1)
	limits := s.Config().Limits
	limits.MaxWait = 5 * time.Second

	release, ok := s.acquire(context.Background(), false, limits)
	if !ok {
		t.Fatal("первое место в ограничителе не выдалось")
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	if _, ok := s.acquire(ctx, false, limits); ok {
		t.Fatal("отменённый запрос получил место в ограничителе")
	}
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("отменённый запрос ждал %v вместо немедленного выхода", waited)
	}
}

// limiterServer поднимает сервер с заданными размерами обоих ограничителей.
func limiterServer(tb testing.TB, inflight, heavyInflight int) *Server {
	tb.Helper()
	s := benchServer(tb, inflight)
	cfg := s.Config()
	cfg.Limits.Inflight = inflight
	cfg.Limits.HeavyInflight = heavyInflight
	s.sem = make(chan struct{}, inflight)
	s.heavySem = make(chan struct{}, heavyInflight)
	s.SetConfig(cfg)
	return s
}

// TestHeavyDoesNotStarveLight закрепляет разделение ограничителей.
//
// Находка ночного ревью: тяжёлый запрос занимает место в ОБЩЕМ ограничителе и
// только потом встаёт в очередь за местом в тяжёлом. Пока он ждёт, общее место
// занято впустую, и при потоке тяжёлых лёгкие запросы голодают до истечения
// срока ожидания, хотя работы для них нет.
//
// Числа в самой находке были неверны: два тяжёлых запроса при девяноста шести
// общих местах ничего не блокируют. Условие наступает, когда тяжёлых запросов
// столько же, сколько общих мест. Тест воспроизводит именно это, на малых
// числах.
func TestHeavyDoesNotStarveLight(t *testing.T) {
	const common, heavy = 4, 1
	s := limiterServer(t, common, heavy)
	limits := s.Config().Limits
	limits.MaxWait = 150 * time.Millisecond
	ctx := context.Background()

	// Занимаем все общие места тяжёлыми запросами. Первый получит место в
	// тяжёлом ограничителе, остальные встанут в очередь за ним.
	var releases []func()
	t.Cleanup(func() {
		for _, r := range releases {
			r()
		}
	})
	var mu sync.Mutex
	// Каждый тяжёлый запрос отдельной горутиной: в одной они выполнялись бы
	// по очереди, и второй заблокировал бы цикл, не дав остальным занять
	// места. Именно на этом первая версия теста проходила без починки.
	for i := 0; i < common; i++ {
		go func() {
			if release, ok := s.acquire(ctx, true, limits); ok {
				mu.Lock()
				releases = append(releases, release)
				mu.Unlock()
			}
		}()
	}
	// Ждём, пока тяжёлые разойдутся по очередям. Проверять промежуточное
	// состояние нельзя: после починки оно как раз и не наступает, потому что
	// ожидающие тяжёлые общих мест не держат. Проверяем исход.
	for i := 0; i < 100 && len(s.heavySem) < heavy; i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if len(s.heavySem) < heavy {
		t.Fatalf("условие не воспроизведено: тяжёлых мест занято %d из %d", len(s.heavySem), heavy)
	}

	// Лёгкому запросу работы хватает: тяжёлые, стоящие в очереди, ничего не
	// считают. Он обязан получить место, а не ждать до отказа.
	started := time.Now()
	release, ok := s.acquire(ctx, false, limits)
	waited := time.Since(started)
	if !ok {
		t.Fatalf("лёгкий запрос получил отказ через %v: тяжёлые заняли общие места, пока ждали своей очереди", waited)
	}
	release()
	if waited > limits.MaxWait/2 {
		t.Errorf("лёгкий запрос ждал %v при сроке ожидания %v: он стоял за тяжёлыми", waited, limits.MaxWait)
	}
}
