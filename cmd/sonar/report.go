package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SliceReport описывает качество маскирования по одному срезу: по типу данных
// либо по категории набора.
type SliceReport struct {
	Fragments           int     `json:"fragments"`
	AvgDistance         float64 `json:"avg_distance"`
	ChangedShare        float64 `json:"changed_share"`
	OutsideChangedShare float64 `json:"outside_changed_share"`
}

// LoadReport описывает показатели скорости и устойчивости под нагрузкой.
type LoadReport struct {
	TargetRPS        float64        `json:"target_rps"`
	AchievedRPS      float64        `json:"achieved_rps"`
	AchievedTotalRPS float64        `json:"achieved_total_rps"`
	Requests         int            `json:"requests"`
	ProbeRequests    int            `json:"probe_requests"`
	Retries          int            `json:"retries"`
	RetryShare       float64        `json:"retry_share"`
	Throttled        int            `json:"throttled"`
	ThrottledShare   float64        `json:"throttled_share"`
	Invalid          int            `json:"invalid"`
	MaxInvalidStreak int            `json:"max_invalid_streak"`
	LatencyP50Ms     float64        `json:"latency_p50_ms"`
	LatencyP95Ms     float64        `json:"latency_p95_ms"`
	LatencyP99Ms     float64        `json:"latency_p99_ms"`
	Statuses         map[string]int `json:"statuses"`
}

// MaskingReport описывает качество прямого шага.
type MaskingReport struct {
	Samples             int                    `json:"samples"`
	Failed              int                    `json:"failed"`
	Fragments           int                    `json:"fragments"`
	AvgDistance         float64                `json:"avg_distance"`
	ChangedShare        float64                `json:"changed_share"`
	OutsideChangedShare float64                `json:"outside_changed_share"`
	OutsideTotalBytes   int64                  `json:"outside_total_bytes"`
	ApproxSamples       int                    `json:"approx_samples"`
	ByType              map[string]SliceReport `json:"by_type"`
	ByCategory          map[string]SliceReport `json:"by_category"`
}

// DemaskReport описывает качество обратного шага.
type DemaskReport struct {
	Total      int     `json:"total"`
	Exact      int     `json:"exact"`
	ExactShare float64 `json:"exact_share"`
	Throttled  int     `json:"throttled"`
}

// RobustnessReport описывает итоги проверок устойчивости.
type RobustnessReport struct {
	DupAfterSuccess pairCounter    `json:"dup_after_success"`
	ConcurrentDup   pairCounter    `json:"concurrent_dup"`
	DemaskRetry     pairCounter    `json:"demask_retry"`
	UnknownID       map[string]int `json:"demask_unknown_id"`
	BigPayload      *bigResult     `json:"big_payload,omitempty"`
}

// DatasetReport описывает исходный набор.
type DatasetReport struct {
	Path      string `json:"path"`
	Samples   int    `json:"samples"`
	Fragments int    `json:"fragments"`
}

// Report содержит итоги прогона в машиночитаемом виде.
type Report struct {
	StartedAt       string           `json:"started_at"`
	FinishedAt      string           `json:"finished_at"`
	DurationSeconds float64          `json:"duration_seconds"`
	URL             string           `json:"url"`
	Dataset         DatasetReport    `json:"dataset"`
	Load            LoadReport       `json:"load"`
	Masking         MaskingReport    `json:"masking"`
	Demask          DemaskReport     `json:"demask"`
	Robustness      RobustnessReport `json:"robustness"`
	StoppedByStreak bool             `json:"stopped_by_invalid_streak"`
}

// BuildReport собирает отчёт по накопленным показателям.
func (s *Stats) BuildReport(opts Options, dataset DatasetReport) Report {
	s.mu.Lock()
	defer s.mu.Unlock()

	finished := s.finished
	if finished.IsZero() {
		finished = time.Now()
	}
	elapsed := finished.Sub(s.started).Seconds()
	lat := s.sortedLatencies()

	return Report{
		StartedAt:       s.started.Format(time.RFC3339),
		FinishedAt:      finished.Format(time.RFC3339),
		DurationSeconds: round(elapsed, 3),
		URL:             opts.URL,
		Dataset:         dataset,
		Load:            s.loadReport(opts, lat, elapsed),
		Masking:         s.maskingReport(),
		Demask:          s.demaskReport(),
		Robustness:      s.robustnessReport(),
		StoppedByStreak: s.streak.stopped(),
	}
}

// loadReport считает показатели скорости. Достигнутая частота считается по
// запросам протокола вместе с повторами: повтор тоже нагрузка на сервис.
// Запросы проверок устойчивости вынесены отдельно, чтобы не путать картину.
func (s *Stats) loadReport(opts Options, lat []time.Duration, elapsed float64) LoadReport {
	rps, totalRPS := 0.0, 0.0
	if elapsed > 0 {
		rps = float64(s.protocol) / elapsed
		totalRPS = float64(s.attempts) / elapsed
	}
	statuses := make(map[string]int, len(s.statuses))
	for code, n := range s.statuses {
		statuses[fmt.Sprintf("%d", code)] = n
	}
	return LoadReport{
		TargetRPS:        opts.RPS,
		AchievedRPS:      round(rps, 2),
		AchievedTotalRPS: round(totalRPS, 2),
		Requests:         s.protocol,
		ProbeRequests:    s.attempts - s.protocol,
		Retries:          s.retries,
		RetryShare:       round(share(s.retries, s.protocol), 4),
		Throttled:        s.throttled,
		ThrottledShare:   round(share(s.throttled, s.protocol), 4),
		Invalid:          s.invalid,
		MaxInvalidStreak: s.streak.longestStreak(),
		LatencyP50Ms:     millis(quantile(lat, 0.50)),
		LatencyP95Ms:     millis(quantile(lat, 0.95)),
		LatencyP99Ms:     millis(quantile(lat, 0.99)),
		Statuses:         statuses,
	}
}

// maskingReport считает качество маскирования в целом и по срезам.
func (s *Stats) maskingReport() MaskingReport {
	return MaskingReport{
		Samples:             s.maskOK,
		Failed:              s.maskFailed,
		Fragments:           s.overall.Count,
		AvgDistance:         round(avgDistance(&s.overall), 4),
		ChangedShare:        round(share(s.overall.Changed, s.overall.Count), 4),
		OutsideChangedShare: round(shareInt64(s.outsideChanged, s.outsideTotal), 6),
		OutsideTotalBytes:   s.outsideTotal,
		ApproxSamples:       s.approxSamples,
		ByType:              slices(s.byType),
		ByCategory:          slices(s.byCategory),
	}
}

// demaskReport считает качество обратного шага.
func (s *Stats) demaskReport() DemaskReport {
	return DemaskReport{
		Total:      s.demaskTotal,
		Exact:      s.demaskExact,
		ExactShare: round(share(s.demaskExact, s.demaskTotal), 4),
		Throttled:  s.demaskThrottled,
	}
}

// robustnessReport собирает итоги проверок устойчивости.
func (s *Stats) robustnessReport() RobustnessReport {
	return RobustnessReport{
		DupAfterSuccess: s.dupAfterSuccess,
		ConcurrentDup:   s.concurrentDup,
		DemaskRetry:     s.demaskRetry,
		UnknownID:       s.unknownID,
		BigPayload:      s.big,
	}
}

// slices переводит накопители срезов в готовые к выводу доли.
func slices(src map[string]*aggregate) map[string]SliceReport {
	out := make(map[string]SliceReport, len(src))
	for key, a := range src {
		out[key] = SliceReport{
			Fragments:           a.Count,
			AvgDistance:         round(avgDistance(a), 4),
			ChangedShare:        round(share(a.Changed, a.Count), 4),
			OutsideChangedShare: round(shareInt64(a.OutsideChanged, a.OutsideTotal), 6),
		}
	}
	return out
}

// avgDistance считает среднее расстояние по срезу.
func avgDistance(a *aggregate) float64 {
	if a.Count == 0 {
		return 0
	}
	return a.SumDistance / float64(a.Count)
}

// shareInt64 считает долю для больших счётчиков.
func shareInt64(part, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// millis переводит длительность в миллисекунды с двумя знаками.
func millis(d time.Duration) float64 {
	return round(float64(d)/float64(time.Millisecond), 2)
}

// round округляет до заданного числа знаков, чтобы отчёт читался глазами.
func round(v float64, digits int) float64 {
	p := 1.0
	for i := 0; i < digits; i++ {
		p *= 10
	}
	shifted := v * p
	if shifted >= 0 {
		return float64(int64(shifted+0.5)) / p
	}
	return float64(int64(shifted-0.5)) / p
}

// Права на каталог и файлы отчёта. Отчёт снимается с боевого стенда и несёт
// куски настоящих текстов, поэтому доступ даётся только владельцу прогона:
// на общей машине чужой учётной записи в отчёте делать нечего.
const (
	reportDirPerm  = 0o750
	reportFilePerm = 0o600
)

// WriteReports кладёт отчёт в два файла: удобный для чтения и машинный.
func WriteReports(dir string, rep *Report) error {
	if err := os.MkdirAll(dir, reportDirPerm); err != nil {
		return fmt.Errorf("не удалось создать каталог отчёта: %w", err)
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("не удалось собрать машинный отчёт: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), reportFilePerm); err != nil {
		return fmt.Errorf("не удалось записать report.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(RenderMarkdown(rep)), reportFilePerm); err != nil {
		return fmt.Errorf("не удалось записать report.md: %w", err)
	}
	return nil
}

// RenderMarkdown собирает отчёт для чтения глазами. Срезы отсортированы от
// худшего к лучшему: сверху видно, какой тип проседает.
func RenderMarkdown(rep *Report) string {
	var b strings.Builder
	b.WriteString("# Отчёт имитатора проверяющей системы\n\n")
	renderSummary(&b, rep)
	renderLoad(&b, &rep.Load)
	renderMasking(&b, &rep.Masking)
	renderSliceTable(&b, "## Качество по типам", "Тип", rep.Masking.ByType, false)
	renderSliceTable(&b, "## Качество по категориям набора", "Категория", rep.Masking.ByCategory, true)
	renderDemask(&b, rep.Demask)
	renderRobustness(&b, rep.Robustness)
	return b.String()
}

// renderSummary выводит шапку отчёта.
func renderSummary(b *strings.Builder, rep *Report) {
	fmt.Fprintf(b, "Сервис: `%s`\n\n", rep.URL)
	fmt.Fprintf(b, "Набор: `%s`, элементов %d, эталонных фрагментов %d\n\n",
		rep.Dataset.Path, rep.Dataset.Samples, rep.Dataset.Fragments)
	fmt.Fprintf(b, "Прогон: с %s по %s, %.1f с\n\n", rep.StartedAt, rep.FinishedAt, rep.DurationSeconds)
	if rep.StoppedByStreak {
		b.WriteString("**Прогон остановлен по правилу пяти невалидных ответов подряд.**\n\n")
	}
}

// renderLoad выводит показатели скорости.
func renderLoad(b *strings.Builder, l *LoadReport) {
	b.WriteString("## Нагрузка\n\n")
	b.WriteString("| Показатель | Значение |\n|---|---|\n")
	fmt.Fprintf(b, "| Целевая частота, запросов в секунду | %.2f |\n", l.TargetRPS)
	fmt.Fprintf(b, "| Достигнутая частота, запросов в секунду | %.2f |\n", l.AchievedRPS)
	fmt.Fprintf(b, "| Частота с учётом проверок устойчивости | %.2f |\n", l.AchievedTotalRPS)
	fmt.Fprintf(b, "| Запросов протокола, с повторами | %d |\n", l.Requests)
	fmt.Fprintf(b, "| Запросов проверок устойчивости | %d |\n", l.ProbeRequests)
	fmt.Fprintf(b, "| Доля повторов | %.2f%% |\n", l.RetryShare*100)
	fmt.Fprintf(b, "| Доля ответов 429 | %.2f%% |\n", l.ThrottledShare*100)
	fmt.Fprintf(b, "| Невалидных ответов | %d |\n", l.Invalid)
	fmt.Fprintf(b, "| Максимальная серия невалидных | %d |\n", l.MaxInvalidStreak)
	fmt.Fprintf(b, "| Задержка, доля 50 | %.2f мс |\n", l.LatencyP50Ms)
	fmt.Fprintf(b, "| Задержка, доля 95 | %.2f мс |\n", l.LatencyP95Ms)
	fmt.Fprintf(b, "| Задержка, доля 99 | %.2f мс |\n\n", l.LatencyP99Ms)
	renderStatuses(b, l.Statuses)
}

// renderStatuses выводит распределение кодов ответа.
func renderStatuses(b *strings.Builder, statuses map[string]int) {
	if len(statuses) == 0 {
		return
	}
	keys := sortedKeys(statuses)
	b.WriteString("Коды ответа: ")
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, statuses[k]))
	}
	b.WriteString(strings.Join(parts, ", "))
	b.WriteString("\n\n")
}

// renderMasking выводит качество прямого шага.
func renderMasking(b *strings.Builder, m *MaskingReport) {
	b.WriteString("## Маскирование\n\n")
	b.WriteString("| Показатель | Значение |\n|---|---|\n")
	fmt.Fprintf(b, "| Элементов оценено | %d |\n", m.Samples)
	fmt.Fprintf(b, "| Элементов без маски | %d |\n", m.Failed)
	fmt.Fprintf(b, "| Эталонных фрагментов | %d |\n", m.Fragments)
	fmt.Fprintf(b, "| Среднее изменение внутри фрагмента | %.4f |\n", m.AvgDistance)
	fmt.Fprintf(b, "| Доля затронутых фрагментов | %.2f%% |\n", m.ChangedShare*100)
	fmt.Fprintf(b, "| Доля изменённых байтов вне фрагментов | %.4f%% |\n", m.OutsideChangedShare*100)
	fmt.Fprintf(b, "| Элементов с приблизительной оценкой | %d |\n\n", m.ApproxSamples)
}

// renderSliceTable выводит таблицу по срезу, худшее сверху. Для категорий
// добавляется колонка с лишними срабатываниями: отрицательные сценарии только
// ею и оцениваются.
func renderSliceTable(b *strings.Builder, title, column string, data map[string]SliceReport, withNoise bool) {
	if len(data) == 0 {
		return
	}
	fmt.Fprintf(b, "%s\n\n", title)
	if withNoise {
		fmt.Fprintf(b, "| %s | Фрагментов | Среднее изменение | Доля затронутых | Лишних байтов |\n|---|---|---|---|---|\n", column)
	} else {
		fmt.Fprintf(b, "| %s | Фрагментов | Среднее изменение | Доля затронутых |\n|---|---|---|---|\n", column)
	}
	for _, key := range sortedByQuality(data) {
		v := data[key]
		if withNoise {
			fmt.Fprintf(b, "| %s | %d | %.4f | %.2f%% | %.4f%% |\n",
				key, v.Fragments, v.AvgDistance, v.ChangedShare*100, v.OutsideChangedShare*100)
			continue
		}
		fmt.Fprintf(b, "| %s | %d | %.4f | %.2f%% |\n", key, v.Fragments, v.AvgDistance, v.ChangedShare*100)
	}
	b.WriteString("\n")
}

// renderDemask выводит качество обратного шага.
func renderDemask(b *strings.Builder, d DemaskReport) {
	b.WriteString("## Обратный шаг\n\n")
	b.WriteString("| Показатель | Значение |\n|---|---|\n")
	fmt.Fprintf(b, "| Элементов | %d |\n", d.Total)
	fmt.Fprintf(b, "| Совпало побайтово | %d |\n", d.Exact)
	fmt.Fprintf(b, "| Доля точного восстановления | %.2f%% |\n", d.ExactShare*100)
	fmt.Fprintf(b, "| Упёрлось в 429 | %d |\n\n", d.Throttled)
}

// renderRobustness выводит итоги проверок устойчивости.
func renderRobustness(b *strings.Builder, r RobustnessReport) {
	b.WriteString("## Устойчивость\n\n")
	b.WriteString("| Проверка | Выполнено | Ответ совпал |\n|---|---|---|\n")
	fmt.Fprintf(b, "| Повтор прямого запроса после успеха | %d | %d |\n", r.DupAfterSuccess.Total, r.DupAfterSuccess.Stable)
	fmt.Fprintf(b, "| Два одинаковых запроса одновременно | %d | %d |\n", r.ConcurrentDup.Total, r.ConcurrentDup.Stable)
	fmt.Fprintf(b, "| Повтор обратного запроса | %d | %d |\n\n", r.DemaskRetry.Total, r.DemaskRetry.Stable)
	if len(r.UnknownID) > 0 {
		b.WriteString("Обратный запрос с неизвестным идентификатором: ")
		parts := make([]string, 0, len(r.UnknownID))
		for _, k := range sortedKeys(r.UnknownID) {
			parts = append(parts, fmt.Sprintf("%s: %d", k, r.UnknownID[k]))
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString("\n\n")
	}
	if r.BigPayload != nil {
		fmt.Fprintf(b, "Тяжёлый запрос: %d байт, исход %s, код %d, ответ за %.0f мс, маска применена: %v\n\n",
			r.BigPayload.Bytes, r.BigPayload.Outcome, r.BigPayload.Status,
			float64(r.BigPayload.Latency)/float64(time.Millisecond), r.BigPayload.Masked)
	}
}

// sortedKeys возвращает ключи карты по возрастанию.
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedByQuality ставит вперёд срезы с наименьшим изменением: именно они
// проседают, их и надо чинить в первую очередь. Срезы без эталонных
// фрагментов уходят вниз: нулевое изменение там ничего не значит.
func sortedByQuality(data map[string]SliceReport) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := data[keys[i]], data[keys[j]]
		if (a.Fragments == 0) != (b.Fragments == 0) {
			return b.Fragments == 0
		}
		if a.AvgDistance != b.AvgDistance {
			return a.AvgDistance < b.AvgDistance
		}
		return keys[i] < keys[j]
	})
	return keys
}
