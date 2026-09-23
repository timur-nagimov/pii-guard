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

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "адрес сервиса")
	dataset := flag.String("dataset", "", "файл с текстами, по строке JSON на элемент")
	rps := flag.Int("rps", 1000, "целевая частота запросов в секунду, ноль означает без ограничения")
	duration := flag.Duration("duration", 30*time.Second, "длительность прогона")
	workers := flag.Int("workers", 256, "число одновременных отправителей")
	payload := flag.Int("payload", 0, "размер текста в байтах; если задан, набор не используется")
	seed := flag.Uint64("seed", 42, "начальное значение для выбора текстов")
	flag.Parse()

	target := strings.TrimRight(*url, "/") + "/process"
	texts, err := loadTexts(*dataset, *payload)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		cancel()
	}()

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        *workers * 2,
			MaxIdleConnsPerHost: *workers * 2,
			MaxConnsPerHost:     *workers * 2,
			IdleConnTimeout:     60 * time.Second,
		},
	}

	var c counters
	latencies := make([][]time.Duration, *workers)
	var wg sync.WaitGroup
	started := time.Now()

	// Интервал между запросами одного отправителя подобран так, чтобы вместе
	// они давали заданную частоту. При нулевой частоте отправители работают
	// без пауз и показывают предел связки.
	var interval time.Duration
	if *rps > 0 {
		interval = time.Duration(float64(*workers) / float64(*rps) * float64(time.Second))
	}

	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			latencies[id] = runWorker(ctx, client, target, texts, interval, *seed, id, &c)
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(started)

	reportResults(&c, latencies, *rps, elapsed)
}

// runWorker гоняет запросы одного отправителя, пока жив контекст, и собирает
// задержки успешных ответов. Возвращает список задержек для этого отправителя.
func runWorker(ctx context.Context, client *http.Client, target string, texts []string, interval time.Duration, seed uint64, id int, c *counters) []time.Duration {
	rnd := rand.New(rand.NewPCG(seed, uint64(id))) //nolint:gosec // нагрузка, не секрет
	local := make([]time.Duration, 0, 4096)
	next := time.Now()
	for ctx.Err() == nil {
		next = waitInterval(ctx, interval, next)
		text := texts[rnd.IntN(len(texts))]
		reqID := fmt.Sprintf("load-%d-%d", id, c.sent.Add(1))
		lat, status, sendErr := send(ctx, client, target, text, reqID)
		if ctx.Err() != nil {
			// Прогон закончился: запрос оборвал не сервис, а мы сами,
			// и считать это сбоем нельзя.
			break
		}
		if sendErr != nil {
			c.noteError(sendErr.Error())
		}
		switch status {
		case http.StatusOK:
			c.ok.Add(1)
			local = append(local, lat)
		case http.StatusTooManyRequests:
			c.throttled.Add(1)
		default:
			c.failed.Add(1)
		}
	}
	return local
}

// waitInterval выдерживает паузу до следующего запроса отправителя. При
// нулевом интервале отправители работают без пауз и показывают предел связки.
func waitInterval(ctx context.Context, interval time.Duration, next time.Time) time.Time {
	if interval <= 0 {
		return next
	}
	next = next.Add(interval)
	if d := time.Until(next); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}
	return next
}

// reportResults печатает итоги прогона: частоту, счётчики, причины сбоев и
// задержки.
func reportResults(c *counters, latencies [][]time.Duration, rps int, elapsed time.Duration) {
	var all []time.Duration
	for _, l := range latencies {
		all = append(all, l...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

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

func send(ctx context.Context, client *http.Client, url, text, id string) (time.Duration, int, error) {
	body, err := json.Marshal(map[string]string{"payload": text, "payload_id": id})
	if err != nil {
		return 0, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := client.Do(req)
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
