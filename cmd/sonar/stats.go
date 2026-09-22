package main

import (
	"sort"
	"sync"
	"time"
)

// invalidStreakLimit повторяет правило проверяющей системы: после пяти подряд
// невалидных ответов прогон останавливается.
const invalidStreakLimit = 5

// streakCounter считает подряд идущие невалидные ответы. Код 429 невалидным
// не считается и счётчик не сбрасывает, успешный ответ сбрасывает его в ноль.
type streakCounter struct {
	mu      sync.Mutex
	limit   int
	current int
	longest int
	tripped bool
}

// newStreakCounter создаёт счётчик с заданным порогом остановки.
func newStreakCounter(limit int) *streakCounter {
	return &streakCounter{limit: limit}
}

// observe учитывает исход запроса и сообщает, надо ли останавливать прогон.
func (c *streakCounter) observe(kind outcomeKind) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch kind {
	case outcomeOK:
		c.current = 0
	case outcomeInvalid:
		c.current++
		if c.current > c.longest {
			c.longest = c.current
		}
		if c.current >= c.limit {
			c.tripped = true
		}
	case outcomeThrottled, outcomeAborted:
		// Перегрузка не портит серию: проверяющая система ждёт и повторяет.
		// Оборванный концом прогона запрос тем более ни на что не влияет.
	}
	return c.tripped
}

// longestStreak возвращает самую длинную серию невалидных ответов.
func (c *streakCounter) longestStreak() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.longest
}

// stopped сообщает, что прогон был остановлен по серии невалидных ответов.
func (c *streakCounter) stopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tripped
}

// aggregate копит оценку по срезу: по типу данных либо по категории набора.
// Байты вне фрагментов считаются только по категориям: у отрицательных
// сценариев эталонных фрагментов нет, и лишние срабатывания там единственный
// показатель, который что-то говорит.
type aggregate struct {
	Count          int
	SumDistance    float64
	Changed        int
	OutsideChanged int64
	OutsideTotal   int64
}

// add учитывает оценку одного фрагмента.
func (a *aggregate) add(f FragmentScore) {
	a.Count++
	a.SumDistance += f.Distance
	if f.Changed {
		a.Changed++
	}
}

// pairCounter считает проверки вида «ответ должен совпасть с предыдущим».
type pairCounter struct {
	Total  int `json:"total"`
	Stable int `json:"stable"`
}

// add учитывает одну такую проверку.
func (p *pairCounter) add(ok bool) {
	p.Total++
	if ok {
		p.Stable++
	}
}

// bigResult хранит итог единственного запроса с большим телом.
type bigResult struct {
	Bytes   int           `json:"bytes"`
	Status  int           `json:"status"`
	Outcome string        `json:"outcome"`
	Latency time.Duration `json:"latency_ns"`
	Masked  bool          `json:"masked"`
}

// Stats копит показатели прогона. Все методы безопасны для одновременного
// вызова из воркеров.
type Stats struct {
	mu     sync.Mutex
	streak *streakCounter

	latencies []time.Duration
	attempts  int
	protocol  int
	retries   int
	throttled int
	invalid   int
	statuses  map[int]int

	maskOK     int
	maskFailed int

	byType     map[string]*aggregate
	byCategory map[string]*aggregate
	overall    aggregate

	outsideChanged int64
	outsideTotal   int64
	approxSamples  int

	demaskTotal     int
	demaskExact     int
	demaskThrottled int

	dupAfterSuccess pairCounter
	concurrentDup   pairCounter
	demaskRetry     pairCounter
	unknownID       map[string]int

	big *bigResult

	started  time.Time
	finished time.Time
}

// NewStats создаёт накопитель показателей.
func NewStats() *Stats {
	return &Stats{
		streak:     newStreakCounter(invalidStreakLimit),
		statuses:   make(map[int]int),
		byType:     make(map[string]*aggregate),
		byCategory: make(map[string]*aggregate),
		unknownID:  make(map[string]int),
		started:    time.Now(),
	}
}

// Finish фиксирует момент окончания прогона.
func (s *Stats) Finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = time.Now()
}

// Observe учитывает выполненный запрос и сообщает, можно ли продолжать прогон.
// Возвращает false, когда набралась серия из пяти невалидных ответов подряд.
func (s *Stats) Observe(resp Response) bool {
	s.mu.Lock()
	for _, at := range resp.Attempts {
		if at.Aborted {
			continue
		}
		s.attempts++
		s.protocol++
		s.latencies = append(s.latencies, at.Latency)
		if at.Status > 0 {
			s.statuses[at.Status]++
		}
		if at.Throttled {
			s.throttled++
		}
		if at.Invalid {
			s.invalid++
		}
	}
	s.retries += resp.Retries
	s.mu.Unlock()
	return !s.streak.observe(resp.Outcome)
}

// ObserveProbe учитывает проверку устойчивости, не влияя на счётчик серии:
// такие запросы проверяющая система не шлёт, и её правила к ним не применимы.
func (s *Stats) ObserveProbe(resp Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, at := range resp.Attempts {
		if at.Aborted {
			continue
		}
		s.attempts++
		s.latencies = append(s.latencies, at.Latency)
		if at.Status > 0 {
			s.statuses[at.Status]++
		}
	}
}

// AddMaskScore учитывает оценку маскирования одного элемента набора.
func (s *Stats) AddMaskScore(category string, sc MaskScore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maskOK++
	if sc.Approx {
		s.approxSamples++
	}
	s.outsideChanged += int64(sc.OutsideChangedBytes)
	s.outsideTotal += int64(sc.OutsideTotalBytes)
	cat := bucket(s.byCategory, category)
	cat.OutsideChanged += int64(sc.OutsideChangedBytes)
	cat.OutsideTotal += int64(sc.OutsideTotalBytes)
	for _, f := range sc.Fragments {
		s.overall.add(f)
		cat.add(f)
		bucket(s.byType, f.Type).add(f)
	}
}

// AddMaskFailure учитывает элемент, по которому маска не получена.
func (s *Stats) AddMaskFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maskFailed++
}

// AddDemask учитывает обратный шаг: восстановленная строка обязана побайтово
// совпасть с исходной.
func (s *Stats) AddDemask(exact bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demaskTotal++
	if exact {
		s.demaskExact++
	}
}

// AddDemaskThrottled учитывает элемент, обратный шаг которого упёрся в 429.
// Элемент засчитан, проверка продолжается.
func (s *Stats) AddDemaskThrottled() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demaskTotal++
	s.demaskThrottled++
}

// AddDupAfterSuccess учитывает повтор прямого запроса после успеха.
func (s *Stats) AddDupAfterSuccess(same bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dupAfterSuccess.add(same)
}

// AddConcurrentDup учитывает пару одновременных одинаковых запросов.
func (s *Stats) AddConcurrentDup(same bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.concurrentDup.add(same)
}

// AddDemaskRetry учитывает повтор обратного запроса.
func (s *Stats) AddDemaskRetry(same bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.demaskRetry.add(same)
}

// AddUnknownID учитывает обратный запрос с неизвестным идентификатором.
// Правильного ответа тут нет: важно, что сервис отвечает одинаково и не падает.
func (s *Stats) AddUnknownID(outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unknownID[outcome]++
}

// SetBig сохраняет итог запроса с большим телом.
func (s *Stats) SetBig(r bigResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.big = &r
}

// bucket достаёт срез из карты, создавая его при первом обращении.
func bucket(m map[string]*aggregate, key string) *aggregate {
	if key == "" {
		key = "UNKNOWN"
	}
	a, ok := m[key]
	if !ok {
		a = &aggregate{}
		m[key] = a
	}
	return a
}

// quantile возвращает значение выборочной доли по отсортированному срезу.
func quantile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(q * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// sortedLatencies отдаёт копию задержек по возрастанию.
func (s *Stats) sortedLatencies() []time.Duration {
	out := make([]time.Duration, len(s.latencies))
	copy(out, s.latencies)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// share считает долю, не деля на ноль.
func share(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}
