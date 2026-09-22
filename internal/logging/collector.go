package logging

import "github.com/prometheus/client_golang/prometheus"

// Показатели работы самого журнала. Они отвечают на вопросы, которые иначе
// пришлось бы выяснять чтением файлов: срабатывает ли защита от утечки,
// сколько записей проглочено глушителем повторов и прореживанием.
var (
	descRecords = prometheus.NewDesc(
		"pii_log_records_total",
		"Число записей, дошедших до вывода журнала.", nil, nil)
	descRedactions = prometheus.NewDesc(
		"pii_log_redactions_total",
		"Число значений, вычищенных защитой от утечки. Ненулевое значение означает ошибку в месте вызова.", nil, nil)
	descIDReplaced = prometheus.NewDesc(
		"pii_log_id_replaced_total",
		"Число идентификаторов, заменённых отпечатком: присланное клиентом значение на идентификатор не похоже.", nil, nil)
	descSuppressed = prometheus.NewDesc(
		"pii_log_suppressed_total",
		"Число заглушенных повторов записей.", nil, nil)
	descSampled = prometheus.NewDesc(
		"pii_log_sampled_total",
		"Число записей об успешных запросах, отброшенных прореживанием.", nil, nil)
)

// Collector отдаёт счётчики журнала в формате Prometheus.
type Collector struct{ stats *Stats }

// NewCollector создаёт сборщик показателей журнала.
func NewCollector(l *Logger) *Collector { return &Collector{stats: l.stats} }

// Describe перечисляет показатели.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descRecords
	ch <- descRedactions
	ch <- descIDReplaced
	ch <- descSuppressed
	ch <- descSampled
}

// Collect снимает значения счётчиков.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ch <- counter(descRecords, c.stats.Records.Load())
	ch <- counter(descRedactions, c.stats.Redactions.Load())
	ch <- counter(descIDReplaced, c.stats.IDReplaced.Load())
	ch <- counter(descSuppressed, c.stats.Suppressed.Load())
	ch <- counter(descSampled, c.stats.Sampled.Load())
}

func counter(d *prometheus.Desc, v uint64) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.CounterValue, float64(v))
}
