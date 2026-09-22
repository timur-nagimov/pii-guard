// Package metrics публикует показатели работы сервиса в формате Prometheus:
// задержку, число запросов в секунду и оценку числа токенов в секунду.
// Значения персональных данных в показатели не попадают — только типы и счётчики.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
	"time"
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
}

// New создаёт набор показателей со своим реестром.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
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
			Buckets: []float64{0.0001, 0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1},
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

	reg.MustRegister(
		m.duration, m.requests, m.detections, m.skipped, m.payload, m.tokensEst,
		m.inflight, m.queueWait, m.records, m.degraded, m.ambiguous, m.panics, m.upstream,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
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
func (m *Metrics) ObserveQueueWait(d time.Duration) { m.queueWait.Observe(d.Seconds()) }

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
