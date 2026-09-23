// Команда loadgen создаёт нагрузку и измеряет только задержку. В отличие от
// имитатора проверяющей системы она не считает качество маскирования, поэтому
// не тратит процессор отправителя на разбор ответов.
//
// Нужна для поиска потолка пропускной способности: имитатор при высокой частоте
// упирается в собственный подсчёт и показывает не предел сервиса, а предел
// машины, с которой идёт нагрузка.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type sample struct {
	Text string `json:"text"`
}

type counters struct {
	sent      atomic.Int64
	ok        atomic.Int64
	throttled atomic.Int64
	failed    atomic.Int64

	errMu   sync.Mutex
	errSeen map[string]int
}

// noteError запоминает причины сбоев, чтобы прогон сообщал не только их число,
// но и суть: без этого потолок легко спутать с отказом отправителя.
func (c *counters) noteError(msg string) {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.errSeen == nil {
		c.errSeen = make(map[string]int)
	}
	if len(msg) > 120 {
		msg = msg[:120]
	}
	c.errSeen[msg]++
}

// flags собирает ключи запуска.
type flags struct {
	url      string
	dataset  string
	rps      int
	duration time.Duration
	workers  int
	payload  int
	seed     uint64
}

func main() {
	f := parseFlags()
	if err := run(f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// parseFlags читает ключи запуска.
func parseFlags() flags {
	var f flags
	flag.StringVar(&f.url, "url", "http://127.0.0.1:8080", "адрес сервиса")
	flag.StringVar(&f.dataset, "dataset", "", "файл с текстами, по строке JSON на элемент")
	flag.IntVar(&f.rps, "rps", 1000, "целевая частота запросов в секунду, ноль означает без ограничения")
	flag.DurationVar(&f.duration, "duration", 30*time.Second, "длительность прогона")
	flag.IntVar(&f.workers, "workers", 256, "число одновременных отправителей")
	flag.IntVar(&f.payload, "payload", 0, "размер текста в байтах; если задан, набор не используется")
	flag.Uint64Var(&f.seed, "seed", 42, "начальное значение для выбора текстов")
	flag.Parse()
	return f
}

// run выполняет прогон целиком: берёт тексты, подаёт нагрузку, печатает итог.
func run(f flags) error {
	target, err := processURL(f.url)
	if err != nil {
		return err
	}
	texts, err := loadTexts(f.dataset, f.payload)
	if err != nil {
		return err
	}

	ctx, cancel := runContext(f.duration)
	defer cancel()

	runLoad(ctx, newClient(f.workers), target, texts, f.rps, f.workers, f.seed)
	return nil
}

// processURL дописывает путь ручки и проверяет схему адреса. Схему проверяем
// не из недоверия к оператору, а потому что опечатка в ключе запуска
// выглядела бы как отказ стенда: запрос не ушёл бы никуда, а в итоге прогона
// это читается как «сервис не отвечает».
func processURL(raw string) (string, error) {
	trimmed := strings.TrimRight(raw, "/")
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("не разобрать адрес сервиса %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("адрес сервиса должен начинаться с http:// или https://, задано %q", raw)
	}
	return trimmed + "/process", nil
}

// runContext обрывает прогон по времени и по сигналу остановки: нагрузку
// нередко прекращают руками, и оборванные на полпути запросы не должны
// попадать в счётчик ошибок.
func runContext(d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		cancel()
	}()
	return ctx, cancel
}

// newClient держит пул соединений под число отправителей. Пул меньше их числа
// превратил бы прогон в измерение очереди за соединением, а не предела сервиса.
func newClient(workers int) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        workers * 2,
			MaxIdleConnsPerHost: workers * 2,
			MaxConnsPerHost:     workers * 2,
			IdleConnTimeout:     60 * time.Second,
		},
	}
}

// sendInterval считает паузу между запросами одного отправителя.
//
// Интервал между запросами одного отправителя подобран так, чтобы вместе
// они давали заданную частоту. При нулевой частоте отправители работают
// без пауз и показывают предел связки.
func sendInterval(rps, workers int) time.Duration {
	if rps <= 0 {
		return 0
	}
	return time.Duration(float64(workers) / float64(rps) * float64(time.Second))
}

// loadRun — общее для всех отправителей: куда слать, что слать и куда
// складывать итоги. Собирается один раз, чтобы каждый отправитель не таскал
// один и тот же набор параметров по отдельности.
type loadRun struct {
	client   *http.Client
	target   string
	texts    []string
	interval time.Duration
	seed     uint64
	c        *counters
}

// runLoad гоняет отправителей до конца контекста и печатает итоги. Вынесена из
// main, чтобы прогон можно было проверить тестом без запуска команды.
func runLoad(ctx context.Context, client *http.Client, target string, texts []string, rps, workers int, seed uint64) {
	var c counters
	l := &loadRun{
		client:   client,
		target:   target,
		texts:    texts,
		interval: sendInterval(rps, workers),
		seed:     seed,
		c:        &c,
	}
	latencies, elapsed := l.run(ctx, workers)
	reportResults(&c, latencies, rps, elapsed)
}

// run поднимает отправителей и ждёт конца прогона. Задержки собираются в
// отдельный ряд на отправителя: общий ряд под замком стоил бы дороже самой
// отправки и занизил бы измеряемый предел.
func (l *loadRun) run(ctx context.Context, workers int) (perWorker [][]time.Duration, elapsed time.Duration) {
	perWorker = make([][]time.Duration, workers)
	var wg sync.WaitGroup
	started := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			perWorker[id] = l.worker(ctx, id)
		}(w)
	}
	wg.Wait()
	return perWorker, time.Since(started)
}

// worker шлёт запросы до конца прогона и возвращает задержки удачных ответов.
func (l *loadRun) worker(ctx context.Context, id int) []time.Duration {
	// Отправитель выбирает тексты по своему зерну: прогон с тем же ключом
	// должен повторяться дословно, иначе замеры не сравнить между собой.
	rnd := rand.New(rand.NewPCG(l.seed, uint64(id))) //nolint:gosec // воспроизводимость важнее криптостойкости
	local := make([]time.Duration, 0, 4096)
	next := time.Now()
	for ctx.Err() == nil {
		next = waitInterval(ctx, l.interval, next)
		text := l.texts[rnd.IntN(len(l.texts))]
		reqID := fmt.Sprintf("load-%d-%d", id, l.c.sent.Add(1))
		lat, status, sendErr := send(ctx, l.client, l.target, text, reqID)
		if ctx.Err() != nil {
			// Прогон закончился: запрос оборвал не сервис, а мы сами,
			// и считать это сбоем нельзя.
			break
		}
		if sendErr != nil {
			l.c.noteError(sendErr.Error())
		}
		switch status {
		case http.StatusOK:
			l.c.ok.Add(1)
			local = append(local, lat)
		case http.StatusTooManyRequests:
			l.c.throttled.Add(1)
		default:
			l.c.failed.Add(1)
		}
	}
	return local
}

// waitInterval выдерживает паузу до следующего запроса отправителя и возвращает
// время следующего запроса. При нулевом интервале отправители работают без пауз
// и показывают предел связки.
func waitInterval(ctx context.Context, interval time.Duration, next time.Time) time.Time {
	if interval <= 0 {
		return next
	}
	next = next.Add(interval)
	waitTurn(ctx, time.Until(next))
	return next
}

// waitTurn выдерживает паузу до следующего запроса. Конец прогона паузу
// прерывает, но сам по себе цикл не останавливает: решение о выходе принимает
// отправитель по итогу очередного запроса.
func waitTurn(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// sortedLatencies сводит задержки всех отправителей в один упорядоченный ряд:
// доли считаются по прогону целиком, а не по отдельному отправителю.
func sortedLatencies(perWorker [][]time.Duration) []time.Duration {
	var all []time.Duration
	for _, l := range perWorker {
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return all
}

// reportResults печатает итоги прогона: частоту, счётчики, причины сбоев и
// задержки. Задержки приходят по отправителям и сводятся в общий ряд.
func reportResults(c *counters, latencies [][]time.Duration, rps int, elapsed time.Duration) {
	printReport(c, sortedLatencies(latencies), rps, elapsed)
}

// printReport печатает итог прогона. Причины сбоев идут отдельной строкой:
// без них потолок пропускной способности легко спутать с отказом отправителя.
func printReport(c *counters, all []time.Duration, rps int, elapsed time.Duration) {
	fmt.Printf("частота: цель %d, достигнуто %.0f запросов в секунду\n", rps, float64(c.ok.Load()+c.throttled.Load()+c.failed.Load())/elapsed.Seconds())
	fmt.Printf("успешно %d, отказов с просьбой повторить %d, ошибок %d\n", c.ok.Load(), c.throttled.Load(), c.failed.Load())
	c.errMu.Lock()
	for msg, n := range c.errSeen {
		fmt.Printf("причина сбоя (%d раз): %s\n", n, msg)
	}
	c.errMu.Unlock()
	if len(all) > 0 {
		fmt.Printf("задержка: доля 50 %.2f мс, доля 95 %.2f мс, доля 99 %.2f мс, максимум %.2f мс\n",
			ms(pct(all, 0.50)), ms(pct(all, 0.95)), ms(pct(all, 0.99)), ms(all[len(all)-1]))
	}
}

func send(ctx context.Context, client *http.Client, target, text, id string) (time.Duration, int, error) {
	body, err := json.Marshal(map[string]string{"payload": text, "payload_id": id})
	if err != nil {
		return 0, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	// Адрес стенда задаёт оператор ключом запуска, а схема проверена при
	// разборе ключей: чужому адресу взяться здесь неоткуда.
	resp, err := client.Do(req) //nolint:gosec // адрес стенда задаёт оператор ключом запуска
	if err != nil {
		return 0, 0, err
	}
	// Тело нужно дочитать и закрыть, иначе соединение не переиспользуется
	// и отправитель упрётся в исчерпание портов.
	_, _ = bufio.NewReader(resp.Body).WriteTo(discard{})
	_ = resp.Body.Close()
	return time.Since(start), resp.StatusCode, nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return sorted[i]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// loadTexts читает тексты из набора либо порождает один текст заданного размера.
func loadTexts(path string, size int) ([]string, error) {
	if size > 0 {
		base := "Клиент Иванов Иван Иванович, паспорт 4509 123456, тел. +7 916 123-45-67, ИНН 500100732259. "
		var b strings.Builder
		for b.Len() < size {
			b.WriteString(base)
		}
		return []string{b.String()[:size]}, nil
	}
	if path == "" {
		return nil, fmt.Errorf("укажите набор текстов либо размер текста")
	}
	f, err := os.Open(path) //nolint:gosec // путь задаёт разработчик
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var s sample
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil || s.Text == "" {
			continue
		}
		out = append(out, s.Text)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("в наборе нет текстов")
	}
	return out, sc.Err()
}
