package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Options задаёт параметры прогона.
type Options struct {
	// URL указывает на ручку сервиса, принимающую пары запросов.
	URL string
	// RPS задаёт целевое число запросов в секунду. Считаются запросы обоих
	// шагов, поэтому пар в секунду ровно вдвое меньше.
	RPS float64
	// Duration ограничивает длительность прогона.
	Duration time.Duration
	// Workers задаёт число одновременно работающих отправителей.
	Workers int
	// Timeout задаёт таймаут одного запроса.
	Timeout time.Duration

	// Режимы проверки устойчивости. Их запросы идут сверх нагрузки и в правило
	// пяти невалидных ответов не попадают: проверяющая система их не шлёт.
	DupAfterSuccess bool
	ConcurrentDup   bool
	DemaskRetry     bool
	DemaskUnknownID bool
	// BigPayload задаёт размер тела единственного тяжёлого запроса в байтах.
	// Ноль выключает проверку.
	BigPayload int
}

// Runner прогоняет набор через сервис парами запросов, как это делает
// проверяющая система организаторов.
type Runner struct {
	opts    Options
	client  *Client
	samples []Sample
	stats   *Stats
}

// NewRunner собирает прогон по набору.
func NewRunner(opts Options, samples []Sample) *Runner {
	return &Runner{
		opts:    opts,
		client:  NewClient(opts.URL, opts.Timeout, opts.Workers),
		samples: samples,
		stats:   NewStats(),
	}
}

// Run подаёт нагрузку заданной частоты и длительности и возвращает показатели.
// Элементы набора переиспользуются по кругу, каждому кругу достаётся свой
// идентификатор, чтобы пара «маска и восстановление» была честной.
func (r *Runner) Run(ctx context.Context) *Stats {
	runCtx, cancel := context.WithTimeout(ctx, r.opts.Duration)
	defer cancel()

	jobs := make(chan int)
	// Каждый элемент набора стоит двух запросов, прямого и обратного, поэтому
	// пары выдаются вдвое реже заданной частоты запросов.
	go pace(runCtx, jobs, r.opts.RPS/2)

	var wg sync.WaitGroup
	for i := 0; i < r.opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(runCtx, jobs, cancel)
		}()
	}
	wg.Wait()

	if r.opts.BigPayload > 0 && ctx.Err() == nil {
		r.bigPayloadCheck(ctx)
	}
	r.stats.Finish()
	return r.stats
}

// worker разбирает задания до конца прогона. Остановку по серии невалидных
// ответов воркер объявляет всем сразу, отменяя общий контекст.
func (r *Runner) worker(ctx context.Context, jobs <-chan int, stop context.CancelFunc) {
	for n := range jobs {
		if ctx.Err() != nil {
			return
		}
		sample := &r.samples[n%len(r.samples)]
		id := payloadID(sample.PayloadID, n/len(r.samples))
		if !r.pair(ctx, sample, id) {
			stop()
			return
		}
	}
}

// payloadID даёт идентификатор для круга: на первом круге он совпадает с
// заданным в наборе, дальше получает номер круга.
func payloadID(base string, cycle int) string {
	if cycle == 0 {
		return base
	}
	return fmt.Sprintf("%s#%d", base, cycle)
}

// pair исполняет проверку одного элемента: прямой шаг с ожиданием маски и
// обратный шаг с ожиданием исходной строки. Возвращает false, когда прогон
// пора останавливать.
func (r *Runner) pair(ctx context.Context, s *Sample, id string) bool {
	resp := r.client.Do(ctx, s.Text, id)
	if !r.stats.Observe(&resp) {
		return false
	}
	if resp.Outcome == outcomeAborted {
		return false
	}
	if resp.Outcome != outcomeOK {
		// Маски нет, значит обратный шаг по этому элементу не запрашиваем.
		r.stats.AddMaskFailure()
		return ctx.Err() == nil
	}

	masked := resp.Result
	r.stats.AddMaskScore(s.Category, ScoreMasking(s, masked))
	r.maskRobustness(ctx, s, id, masked)
	r.demaskStep(ctx, s, id, masked)
	return ctx.Err() == nil
}

// maskRobustness проверяет идемпотентность прямого шага: повтор после успеха и
// два одинаковых запроса одновременно обязаны дать ту же маску.
func (r *Runner) maskRobustness(ctx context.Context, s *Sample, id, masked string) {
	if r.opts.DupAfterSuccess {
		dup := r.client.Do(ctx, s.Text, id)
		r.stats.ObserveProbe(&dup)
		r.stats.AddDupAfterSuccess(dup.Outcome == outcomeOK && dup.Result == masked)
	}
	if r.opts.ConcurrentDup {
		r.stats.AddConcurrentDup(r.concurrentSame(ctx, s.Text, id))
	}
}

// concurrentSame шлёт два одинаковых запроса одновременно и сообщает, совпали
// ли ответы. Расхождение означает гонку в хранилище соответствий.
func (r *Runner) concurrentSame(ctx context.Context, payload, id string) bool {
	var wg sync.WaitGroup
	results := make([]Response, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = r.client.Do(ctx, payload, id)
		}(i)
	}
	wg.Wait()
	for _, res := range results {
		r.stats.ObserveProbe(&res)
	}
	return results[0].Outcome == outcomeOK &&
		results[1].Outcome == outcomeOK &&
		results[0].Result == results[1].Result
}

// demaskStep выполняет обратный шаг и проверки устойчивости к нему.
func (r *Runner) demaskStep(ctx context.Context, s *Sample, id, masked string) {
	resp := r.client.Do(ctx, masked, id)
	if !r.stats.Observe(&resp) {
		return
	}
	if resp.Outcome == outcomeAborted {
		return
	}
	if resp.Outcome == outcomeThrottled {
		// Перегрузка на обратном шаге не отменяет элемент: он засчитан,
		// проверка продолжается.
		r.stats.AddDemaskThrottled()
		return
	}
	restored := resp.Result
	r.stats.AddDemask(resp.Outcome == outcomeOK && restored == s.Text)

	if r.opts.DemaskRetry {
		again := r.client.Do(ctx, masked, id)
		r.stats.ObserveProbe(&again)
		r.stats.AddDemaskRetry(again.Outcome == outcomeOK && again.Result == restored)
	}
	if r.opts.DemaskUnknownID {
		r.unknownIDProbe(ctx, masked, id)
	}
}

// unknownIDProbe шлёт обратный запрос с идентификатором, которого сервис не
// видел. Единственно верного ответа тут нет, поэтому просто копим, чем сервис
// отвечает: важно, что ответ один и тот же и сервис не разваливается.
func (r *Runner) unknownIDProbe(ctx context.Context, masked, id string) {
	resp := r.client.Probe(ctx, masked, id+"~unknown")
	r.stats.ObserveProbe(&resp)
	r.stats.AddUnknownID(fmt.Sprintf("%s/%d", resp.Outcome, resp.Status))
}

// bigPayloadCheck шлёт один тяжёлый запрос после основного прогона: так видно
// запас по времени ответа, не мешая замеру задержек под нагрузкой.
func (r *Runner) bigPayloadCheck(ctx context.Context) {
	text := inflate(r.samples[0].Text, r.opts.BigPayload)
	started := time.Now()
	resp := r.client.Do(ctx, text, fmt.Sprintf("big-%d", started.UnixNano()))
	r.stats.SetBig(bigResult{
		Bytes:   len(text),
		Status:  resp.Status,
		Outcome: resp.Outcome.String(),
		Latency: time.Since(started),
		Masked:  resp.Outcome == outcomeOK && resp.Result != text,
	})
}

// inflate повторяет текст, пока не наберётся нужный размер в байтах. Граница
// режется по рунам, иначе в теле окажется ломаный символ.
func inflate(text string, size int) string {
	if text == "" {
		text = "проверка размера тела\n"
	}
	var b strings.Builder
	b.Grow(size + len(text))
	for b.Len() < size {
		b.WriteString(text)
		b.WriteString("\n")
	}
	out := b.String()
	for size < len(out) && !isRuneStart(out[size]) {
		size++
	}
	if size > len(out) {
		size = len(out)
	}
	return out[:size]
}

// isRuneStart сообщает, что байт начинает руну.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// pace выдаёт номера заданий с заданной частотой и закрывает канал, когда
// прогон окончен.
func pace(ctx context.Context, jobs chan<- int, rps float64) {
	defer close(jobs)
	interval, batch := pacing(rps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < batch; i++ {
				select {
				case jobs <- n:
					n++
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// pacing подбирает период тикера и размер пачки. Таймеры не дают надёжных
// интервалов короче миллисекунды, поэтому высокая частота набирается пачками.
func pacing(rps float64) (interval time.Duration, batch int) {
	if rps <= 0 {
		return time.Millisecond, 1
	}
	interval = time.Duration(float64(time.Second) / rps)
	if interval >= time.Millisecond {
		return interval, 1
	}
	batch = int(rps/1000 + 0.5)
	if batch < 1 {
		batch = 1
	}
	return time.Millisecond, batch
}
