package logging

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// repeatShards — число независимых частей карты повторов. Записи уровня
// предупреждения и выше редки, но при шторме идут сразу со всех ядер, поэтому
// одна общая блокировка стала бы точкой соперничества.
const repeatShards = 16

// repeatHandler глушит повторы. Одинаковая запись выходит не чаще одного раза
// в окно, остальные считаются и выводятся одной строкой со счётчиком.
//
// Ключ повтора это уровень и текст сообщения. Из этого следует правило: текст
// сообщения обязан быть константой, а всё переменное должно лежать в полях.
// Иначе глушение не сработает, потому что каждая запись будет новой.
//
// Зачем это нужно: при отказе общего хранилища сервис пишет предупреждение на
// каждый запрос. На потолке это двадцать три тысячи строк в секунду, то есть
// около семи мегабайт в секунду. Диск кончится раньше, чем человек успеет
// открыть журнал, а причина отказа утонет в повторах.
type repeatHandler struct {
	next   slog.Handler
	window time.Duration
	min    slog.Level
	stats  *Stats

	shards [repeatShards]repeatShard

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

type repeatShard struct {
	mu sync.Mutex
	m  map[uint64]*repeatEntry
}

type repeatEntry struct {
	level    slog.Level
	msg      string
	since    time.Time
	count    int
	reported time.Time
}

func newRepeatHandler(next slog.Handler, window time.Duration, min slog.Level, stats *Stats) *repeatHandler {
	h := &repeatHandler{
		next:   next,
		window: window,
		min:    min,
		stats:  stats,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	for i := range h.shards {
		h.shards[i].m = make(map[uint64]*repeatEntry)
	}
	return h
}

// start запускает вывод накопленных счётчиков раз в окно.
func (h *repeatHandler) start() {
	if h.window <= 0 {
		close(h.done)
		return
	}
	go h.loop()
}

func (h *repeatHandler) loop() {
	defer close(h.done)
	ticker := time.NewTicker(h.window)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			h.flush(time.Now())
			return
		case now := <-ticker.C:
			h.flush(now)
		}
	}
}

// Close останавливает вывод счётчиков и выводит остаток.
func (h *repeatHandler) Close() error {
	h.stopOnce.Do(func() {
		close(h.stop)
		<-h.done
	})
	return nil
}

func (h *repeatHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *repeatHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &repeatProxy{parent: h, next: h.next.WithAttrs(attrs)}
}

func (h *repeatHandler) WithGroup(name string) slog.Handler {
	return &repeatProxy{parent: h, next: h.next.WithGroup(name)}
}

func (h *repeatHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.handle(ctx, r, h.next)
}

// handle решает судьбу записи: пропустить, пропустить с пометкой о
// заглушенных повторах или проглотить.
func (h *repeatHandler) handle(ctx context.Context, r slog.Record, next slog.Handler) error {
	if h.window <= 0 || r.Level < h.min {
		return next.Handle(ctx, r)
	}
	now := r.Time
	if now.IsZero() {
		now = time.Now()
	}
	key := repeatKey(r.Level, r.Message)
	sh := &h.shards[key%repeatShards]

	sh.mu.Lock()
	e := sh.m[key]
	if e == nil {
		e = &repeatEntry{level: r.Level, msg: r.Message, since: now, reported: now}
		sh.m[key] = e
		sh.mu.Unlock()
		return next.Handle(ctx, r)
	}
	if now.Sub(e.reported) < h.window {
		e.count++
		sh.mu.Unlock()
		h.stats.Suppressed.Add(1)
		return nil
	}
	suppressed := e.count
	e.count = 0
	e.reported = now
	sh.mu.Unlock()

	if suppressed > 0 {
		r = r.Clone()
		r.AddAttrs(slog.Int(FieldSuppress, suppressed))
	}
	return next.Handle(ctx, r)
}

// flush выводит накопленные счётчики. Без него последние повторы шторма
// остались бы невидимыми до следующего такого же сообщения.
func (h *repeatHandler) flush(now time.Time) {
	for i := range h.shards {
		sh := &h.shards[i]
		sh.mu.Lock()
		for key, e := range sh.m {
			switch {
			case e.count > 0:
				h.reportEntry(sh, e, now)
			case now.Sub(e.reported) > 2*h.window:
				// Карта повторов не должна расти вечно: ключей столько же,
				// сколько мест вызова, но сервис живёт долго.
				delete(sh.m, key)
			default:
				// Ключ без накопленных повторов и ещё свежий: он пригодится
				// следующему такому же сообщению, трогать его нечем.
			}
		}
		sh.mu.Unlock()
	}
}

// reportEntry выводит накопленные повторы одного ключа и обнуляет счётчик.
// Блокировка шарда на время вывода снимается: запись уходит в нижний
// обработчик и упирается в диск, а под захваченной блокировкой на это время
// встали бы все места вызова, попавшие в тот же шард.
func (h *repeatHandler) reportEntry(sh *repeatShard, e *repeatEntry, now time.Time) {
	count := e.count
	level, msg := e.level, e.msg
	e.count = 0
	e.reported = now
	sh.mu.Unlock()
	h.report(level, msg, count, now)
	sh.mu.Lock()
}

func (h *repeatHandler) report(level slog.Level, msg string, count int, now time.Time) {
	rec := slog.NewRecord(now, level, msg, 0)
	rec.AddAttrs(
		slog.String(FieldEvent, EventLogSuppressed),
		slog.Int(FieldSuppress, count),
		slog.Float64("window_s", h.window.Seconds()),
	)
	_ = h.next.Handle(context.Background(), rec)
}

// repeatProxy сохраняет общее состояние глушителя при вызовах With.
type repeatProxy struct {
	parent *repeatHandler
	next   slog.Handler
}

func (p *repeatProxy) Enabled(ctx context.Context, l slog.Level) bool {
	return p.next.Enabled(ctx, l)
}

func (p *repeatProxy) Handle(ctx context.Context, r slog.Record) error {
	return p.parent.handle(ctx, r, p.next)
}

func (p *repeatProxy) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &repeatProxy{parent: p.parent, next: p.next.WithAttrs(attrs)}
}

func (p *repeatProxy) WithGroup(name string) slog.Handler {
	return &repeatProxy{parent: p.parent, next: p.next.WithGroup(name)}
}

// repeatKey считает ключ повтора: хеш FNV-1a по уровню и сообщению.
func repeatKey(level slog.Level, msg string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	h ^= uint64(level & 0xff)
	h *= prime
	for i := 0; i < len(msg); i++ {
		h ^= uint64(msg[i])
		h *= prime
	}
	return h
}
