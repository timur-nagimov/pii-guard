// Package metrics публикует показатели работы сервиса в формате Prometheus:
// задержку, число запросов в секунду и оценку числа токенов в секунду.
// Значения персональных данных в показатели не попадают — только типы и счётчики.
package metrics

import (
	"net/http"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics — набор показателей сервиса.
type Metrics struct {
	registry *prometheus.Registry

	duration   *prometheus.HistogramVec
	requests   *prometheus.CounterVec
	detections *prometheus.CounterVec
	skipped    *prometheus.CounterVec
	payload    prometheus.Histogram
	tokensEst  *prometheus.CounterVec
	inflight   prometheus.Gauge
	queueWait  prometheus.Histogram
	records    prometheus.Gauge
	degraded   *prometheus.CounterVec
	ambiguous  *prometheus.CounterVec
	panics     *prometheus.CounterVec
	upstream   *prometheus.HistogramVec

	// Показатели для автоматического масштабирования. Оба считаются по уже
	// собираемым наблюдениям и не требуют отдельных вызовов из обработчиков.
	capacity    prometheus.GaugeFunc
	waitP95     prometheus.GaugeFunc
	loadRing    *sumRing
	waitRing    *quantileRing
	coresUsable float64
}

// New создаёт набор показателей со своим реестром.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	// GOMAXPROCS, а не число ядер машины: начиная с Go 1.25 среда выполнения
	// сама учитывает ограничение процессора в контейнере, и планировать надо
	// по тому числу ядер, которым процесс реально располагает.
	cores := float64(runtime.GOMAXPROCS(0)) * usableCoreFraction
	m := &Metrics{
		registry:    reg,
		loadRing:    newSumRing(loadWindowSeconds),
		waitRing:    newQuantileRing(waitWindowSeconds, queueWaitBuckets),
		coresUsable: cores,
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pii_request_duration_seconds",
			Help:    "Длительность обработки запроса.",
			Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"op", "system"}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_requests_total",
			Help: "Число обработанных запросов.",
		}, []string{"op", "code", "system"}),
		detections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_detections_total",
			Help: "Число найденных фрагментов персональных данных по типам.",
		}, []string{"type", "system"}),
		skipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_skipped_total",
			Help: "Число фрагментов, снятых с маскирования, по причинам.",
		}, []string{"type", "reason"}),
		payload: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "pii_payload_bytes",
			Help:    "Размер входного текста в байтах.",
			Buckets: prometheus.ExponentialBuckets(128, 4, 10),
		}),
		tokensEst: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_tokens_est_total",
			Help: "Оценка числа обработанных токенов; точный токенайзер не используется.",
		}, []string{"direction"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pii_inflight",
			Help: "Число запросов в обработке.",
		}),
		queueWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "pii_queue_wait_seconds",
			Help:    "Ожидание свободного места в ограничителе.",
			Buckets: queueWaitBuckets,
		}),
		records: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pii_store_records",
			Help: "Число записей соответствия в хранилище.",
		}),
		degraded: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_degraded_total",
			Help: "Число ответов, отданных в режиме деградации.",
		}, []string{"system"}),
		ambiguous: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_direction_ambiguous_total",
			Help: "Число запросов, где направление обработки не определилось однозначно.",
		}, []string{"system"}),
		panics: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pii_detector_panics_total",
			Help: "Число сбоев внутри детекторов.",
		}, []string{"type"}),
		upstream: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "pii_upstream_duration_seconds",
			Help:    "Длительность обращения к языковой модели в режиме прокси.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"system", "code"}),
	}

	// pii_capacity_used_ratio — доля модельной ёмкости копии, занятая за
	// последние loadWindowSeconds секунд. Это основной сигнал автоматического
	// масштабирования: в отличие от загрузки процессора он считает только свою
	// работу, выражен в тех же единицах, что и модель ёмкости из docs/SCALING.md,
	// и прямо переводится в число копий.
	m.capacity = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pii_capacity_used_ratio",
		Help: "Доля модельной ёмкости копии, занятая разбором текста за последние 15 секунд.",
	}, m.capacityUsedRatio)

	// pii_queue_wait_p95_seconds — доля 95 ожидания места в ограничителе за
	// последнюю минуту. Аварийный сигнал: он растёт и тогда, когда занятость
	// ёмкости уже упёрлась в единицу и перестала отличать двукратную перегрузку
	// от десятикратной.
	m.waitP95 = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "pii_queue_wait_p95_seconds",
		Help: "Доля 95 ожидания места в ограничителе за последние 60 секунд.",
	}, m.queueWaitP95)

	reg.MustRegister(
		m.capacity, m.waitP95,
		m.duration, m.requests, m.detections, m.skipped, m.payload, m.tokensEst,
		m.inflight, m.queueWait, m.records, m.degraded, m.ambiguous, m.panics, m.upstream,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Register добавляет сторонний сборщик в реестр показателей сервиса. Реестр
// свой, а не общий по умолчанию, поэтому без этой точки чужие показатели в
// ответ /metrics не попадут.
func (m *Metrics) Register(c prometheus.Collector) error {
	return m.registry.Register(c)
}

// Handler возвращает обработчик для выдачи показателей.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveProcess записывает показатели успешной обработки запроса.
func (m *Metrics) ObserveProcess(system, op string, d time.Duration, payloadBytes int, counts map[string]int) {
	m.duration.WithLabelValues(op, system).Observe(d.Seconds())
	m.requests.WithLabelValues(op, "200", system).Inc()
	m.payload.Observe(float64(payloadBytes))
	m.loadRing.add(time.Now().Unix(), modelCoreMillis(payloadBytes))
	m.tokensEst.WithLabelValues(op).Add(EstimateTokens(payloadBytes))
	for t, n := range counts {
		m.detections.WithLabelValues(t, system).Add(float64(n))
	}
}

// ObserveStatus записывает ответ с кодом, отличным от успешного.
func (m *Metrics) ObserveStatus(system, op string, code string) {
	m.requests.WithLabelValues(op, code, system).Inc()
}

// ObserveSkipped записывает снятый с маскирования фрагмент.
func (m *Metrics) ObserveSkipped(pType, reason string) {
	m.skipped.WithLabelValues(pType, reason).Inc()
}

// ObserveDegraded записывает ответ в режиме деградации.
func (m *Metrics) ObserveDegraded(system string) { m.degraded.WithLabelValues(system).Inc() }

// ObserveAmbiguous записывает запрос с неоднозначным направлением.
func (m *Metrics) ObserveAmbiguous(system string) { m.ambiguous.WithLabelValues(system).Inc() }

// ObservePanic записывает сбой детектора.
func (m *Metrics) ObservePanic(pType string) { m.panics.WithLabelValues(pType).Inc() }

// ObserveUpstream записывает обращение к языковой модели.
func (m *Metrics) ObserveUpstream(system, code string, d time.Duration) {
	m.upstream.WithLabelValues(system, code).Observe(d.Seconds())
}

// ObserveQueueWait записывает ожидание в ограничителе.
func (m *Metrics) ObserveQueueWait(d time.Duration) {
	m.queueWait.Observe(d.Seconds())
	m.waitRing.observe(time.Now().Unix(), d.Seconds())
}

// IncInflight и DecInflight отмечают вход и выход из обработки.
func (m *Metrics) IncInflight() { m.inflight.Inc() }

// DecInflight отмечает выход из обработки.
func (m *Metrics) DecInflight() { m.inflight.Dec() }

// SetRecords обновляет число записей в хранилище.
func (m *Metrics) SetRecords(n int) { m.records.Set(float64(n)) }

// Коэффициенты оценки числа токенов по числу байтов. Точный токенайзер модели
// на горячем пути не используется: он дороже самой обработки. Поэтому в имени
// показателя стоит пометка est.
const bytesPerToken = 2.8

// EstimateTokens оценивает число токенов по размеру текста в байтах.
func EstimateTokens(payloadBytes int) float64 {
	if payloadBytes <= 0 {
		return 0
	}
	return float64(payloadBytes) / bytesPerToken
}

// --- Сигналы автоматического масштабирования ---
//
// Оба показателя ниже считаются по тем же наблюдениям, которые обработчик уже
// отдаёт в ObserveProcess и ObserveQueueWait, поэтому новых вызовов на горячем
// пути не появляется: добавляется одно сложение под коротким замком на запрос.
//
// Зачем они нужны, если есть загрузка процессора. Загрузка процессора считает
// всё, что делает машина, включая сборку, выкладку и наблюдение, упирается в
// сто процентов и после этого перестаёт различать перегрузку в два раза и в
// десять. Занятость модельной ёмкости считает только разбор текста, выражена в
// тех же единицах, что и модель ёмкости в docs/SCALING.md, и прямо переводится
// в число копий: нужное число копий равно текущему, умноженному на отношение
// занятости к цели.

// Коэффициенты модели стоимости запроса. Подобраны по двум замеренным точкам
// потолка на стенде: 23 200 запросов в секунду на текстах по 250 байт и 5 980
// на текстах по 2 килобайта, обе при 22 занятых ядрах из 24. Проверка на двух
// других точках замера: 500 байт расхождение 8 процентов, 8 килобайт
// расхождение 1 процент. Постоянная часть это разбор JSON, HTTP и запись в
// хранилище, часть на килобайт это проход детекторов с маскированием.
const (
	reqFixedMillis     = 0.57
	reqPerKiBMillis    = 1.56
	longTextBytes      = 2048
	longTextFactor     = 1.8
	usableCoreFraction = 0.92

	loadWindowSeconds = 15
	waitWindowSeconds = 60
)

// queueWaitBuckets — границы наблюдений ожидания в ограничителе. Общие у
// гистограммы и у скользящего окна, иначе доля 95 в них считалась бы по разным
// сеткам и расходилась бы на панели.
var queueWaitBuckets = []float64{0.0001, 0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1}

// modelCoreMillis оценивает, сколько процессорного времени одного ядра стоит
// запрос с текстом заданного размера. Тексты длиннее двух килобайт стоят
// дороже линейной оценки: замер потолка на восьми килобайтах дал 950 запросов
// в секунду против 1686 по линейной модели, отсюда множитель 1.8.
func modelCoreMillis(payloadBytes int) float64 {
	if payloadBytes <= 0 {
		return reqFixedMillis
	}
	cost := reqFixedMillis + reqPerKiBMillis*float64(payloadBytes)/1024
	if payloadBytes > longTextBytes {
		cost *= longTextFactor
	}
	return cost
}

// capacityUsedRatio возвращает долю модельной ёмкости копии, занятую за окно
// наблюдения. Единица означает, что разбор занял все доступные процессу ядра.
func (m *Metrics) capacityUsedRatio() float64 {
	if m.coresUsable <= 0 {
		return 0
	}
	// Доступно за окно: ядра, умноженные на длину окна в миллисекундах.
	available := m.coresUsable * loadWindowSeconds * 1000
	return m.loadRing.sum(time.Now().Unix()) / available
}

// queueWaitP95 возвращает долю 95 ожидания места в ограничителе за окно
// наблюдения. Значение ограничено сверху последней границей наблюдений.
func (m *Metrics) queueWaitP95() float64 {
	return m.waitRing.quantile(time.Now().Unix(), 0.95)
}

// sumRing — сумма значений за скользящее окно. Окно разложено на слоты по
// секунде: слот протухает целиком, поэтому памяти нужно столько же, сколько
// секунд в окне, а не столько, сколько было запросов.
type sumRing struct {
	mu    sync.Mutex
	slots []float64
	idx   int
	last  int64
}

func newSumRing(seconds int) *sumRing {
	return &sumRing{slots: make([]float64, seconds)}
}

// advance сдвигает окно к текущей секунде, обнуляя пройденные слоты.
// Вызывается под замком.
func (r *sumRing) advance(now int64) {
	if r.last == 0 {
		r.last = now
		return
	}
	steps := now - r.last
	if steps <= 0 {
		// Время не шло вперёд: либо та же секунда, либо часы перевели назад.
		return
	}
	if steps >= int64(len(r.slots)) {
		for i := range r.slots {
			r.slots[i] = 0
		}
		r.idx = 0
		r.last = now
		return
	}
	for i := int64(0); i < steps; i++ {
		r.idx = (r.idx + 1) % len(r.slots)
		r.slots[r.idx] = 0
	}
	r.last = now
}

func (r *sumRing) add(now int64, v float64) {
	r.mu.Lock()
	r.advance(now)
	r.slots[r.idx] += v
	r.mu.Unlock()
}

func (r *sumRing) sum(now int64) float64 {
	r.mu.Lock()
	r.advance(now)
	total := 0.0
	for _, v := range r.slots {
		total += v
	}
	r.mu.Unlock()
	return total
}

// quantileRing — доля наблюдений за скользящее окно. Хранит не сами значения,
// а счётчики по тем же границам, что и гистограмма: на минуту окна уходит
// шестьдесят строк по десять чисел независимо от частоты запросов.
type quantileRing struct {
	mu     sync.Mutex
	bounds []float64
	slots  [][]uint64
	idx    int
	last   int64
}

func newQuantileRing(seconds int, bounds []float64) *quantileRing {
	slots := make([][]uint64, seconds)
	for i := range slots {
		slots[i] = make([]uint64, len(bounds)+1)
	}
	return &quantileRing{bounds: bounds, slots: slots}
}

// advance сдвигает окно к текущей секунде. Вызывается под замком.
func (r *quantileRing) advance(now int64) {
	if r.last == 0 {
		r.last = now
		return
	}
	steps := now - r.last
	if steps <= 0 {
		return
	}
	if steps >= int64(len(r.slots)) {
		for i := range r.slots {
			clear(r.slots[i])
		}
		r.idx = 0
		r.last = now
		return
	}
	for i := int64(0); i < steps; i++ {
		r.idx = (r.idx + 1) % len(r.slots)
		clear(r.slots[r.idx])
	}
	r.last = now
}

func (r *quantileRing) observe(now int64, v float64) {
	b := sort.SearchFloat64s(r.bounds, v)
	r.mu.Lock()
	r.advance(now)
	r.slots[r.idx][b]++
	r.mu.Unlock()
}

// quantile считает долю q по счётчикам окна с линейным приближением внутри
// наблюдения. Наблюдения выше последней границы отдаются как эта граница:
// точнее по счётчикам сказать нельзя, и это честнее, чем придумывать число.
func (r *quantileRing) quantile(now int64, q float64) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advance(now)

	counts := make([]uint64, len(r.bounds)+1)
	var total uint64
	for _, slot := range r.slots {
		for i, c := range slot {
			counts[i] += c
			total += c
		}
	}
	if total == 0 {
		return 0
	}

	target := q * float64(total)
	var cum float64
	for i, c := range counts {
		if c == 0 {
			continue
		}
		if cum+float64(c) < target {
			cum += float64(c)
			continue
		}
		if i == len(r.bounds) {
			return r.bounds[len(r.bounds)-1]
		}
		lo := 0.0
		if i > 0 {
			lo = r.bounds[i-1]
		}
		hi := r.bounds[i]
		return lo + (hi-lo)*(target-cum)/float64(c)
	}
	return r.bounds[len(r.bounds)-1]
}
