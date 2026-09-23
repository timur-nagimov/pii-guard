// Команда sonar повторяет поведение проверяющей системы организаторов: подаёт
// нагрузку парами запросов, следит за правилами повторов и остановки и считает
// качество маскирования и обратного преобразования. Нужна, чтобы измерять себя
// до сдачи, а не узнавать оценку от жюри.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// flags собирает ключи запуска.
type flags struct {
	url      string
	dataset  string
	rps      float64
	duration time.Duration
	workers  int
	timeout  time.Duration
	out      string

	dupAfterSuccess bool
	concurrentDup   bool
	demaskRetry     bool
	demaskUnknownID bool
	bigPayload      int
}

func main() {
	f := parseFlags()
	if err := run(&f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// parseFlags читает ключи запуска. Значения по умолчанию повторяют профиль
// проверяющей системы: таймаут десять секунд, умеренная нагрузка.
func parseFlags() flags {
	var f flags
	flag.StringVar(&f.url, "url", "http://127.0.0.1:8080/process", "адрес ручки сервиса")
	flag.StringVar(&f.dataset, "dataset", "testdata/dataset.jsonl", "файл набора: массив JSON либо построчный JSON")
	flag.Float64Var(&f.rps, "rps", 10, "целевое число пар запросов в секунду")
	flag.DurationVar(&f.duration, "duration", 30*time.Second, "длительность прогона")
	flag.IntVar(&f.workers, "workers", 16, "число одновременных отправителей")
	flag.DurationVar(&f.timeout, "timeout", 10*time.Second, "таймаут одного запроса")
	flag.StringVar(&f.out, "out", "report", "каталог для report.md и report.json")
	flag.BoolVar(&f.dupAfterSuccess, "dup-after-success", false, "повторять прямой запрос после успеха и сверять маску")
	flag.BoolVar(&f.concurrentDup, "concurrent-dup", false, "слать два одинаковых запроса одновременно")
	flag.BoolVar(&f.demaskRetry, "demask-retry", false, "повторять обратный запрос и сверять ответ")
	flag.BoolVar(&f.demaskUnknownID, "demask-unknown-id", false, "слать обратный запрос с неизвестным идентификатором")
	flag.IntVar(&f.bigPayload, "big-payload", 0, "размер тела тяжёлого запроса в байтах, ноль выключает проверку")
	flag.Parse()
	f.url = normalizeURL(f.url)
	return f
}

// run выполняет прогон целиком: читает набор, подаёт нагрузку, пишет отчёты.
func run(f *flags) error {
	if err := validate(f); err != nil {
		return err
	}
	samples, err := LoadDataset(f.dataset)
	if err != nil {
		return err
	}

	opts := Options{
		URL:             f.url,
		RPS:             f.rps,
		Duration:        f.duration,
		Workers:         f.workers,
		Timeout:         f.timeout,
		DupAfterSuccess: f.dupAfterSuccess,
		ConcurrentDup:   f.concurrentDup,
		DemaskRetry:     f.demaskRetry,
		DemaskUnknownID: f.demaskUnknownID,
		BigPayload:      f.bigPayload,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("прогон: %s, набор %s, элементов %d, частота %.1f, длительность %s\n",
		f.url, f.dataset, len(samples), f.rps, f.duration)

	stats := NewRunner(opts, samples).Run(ctx)
	rep := stats.BuildReport(opts, datasetReport(f.dataset, samples))
	if err := WriteReports(f.out, &rep); err != nil {
		return err
	}
	printSummary(&rep, f.out)
	if rep.StoppedByStreak {
		return fmt.Errorf("прогон остановлен: пять невалидных ответов подряд")
	}
	return nil
}

// validate отсекает заведомо бессмысленные ключи запуска.
func validate(f *flags) error {
	switch {
	case f.url == "":
		return fmt.Errorf("не задан адрес сервиса")
	// Схему проверяем не из недоверия к оператору: пропущенное http:// не
	// уходит никуда, а в итоге прогона это читалось бы как отказ стенда.
	case !strings.HasPrefix(f.url, "http://") && !strings.HasPrefix(f.url, "https://"):
		return fmt.Errorf("адрес сервиса должен начинаться с http:// или https://, задано %q", f.url)
	case f.rps <= 0:
		return fmt.Errorf("частота запросов должна быть больше нуля")
	case f.duration <= 0:
		return fmt.Errorf("длительность прогона должна быть больше нуля")
	case f.workers <= 0:
		return fmt.Errorf("число отправителей должно быть больше нуля")
	case f.timeout <= 0:
		return fmt.Errorf("таймаут запроса должен быть больше нуля")
	case f.bigPayload < 0:
		return fmt.Errorf("размер тяжёлого запроса не может быть отрицательным")
	default:
		return nil
	}
}

// datasetReport описывает набор для отчёта.
func datasetReport(path string, samples []Sample) DatasetReport {
	total := 0
	for _, s := range samples {
		total += len(s.Fragments)
	}
	return DatasetReport{Path: path, Samples: len(samples), Fragments: total}
}

// printSummary печатает короткий итог в поток вывода: по нему видно результат,
// не открывая отчёт.
func printSummary(rep *Report, dir string) {
	fmt.Printf("запросов %d, достигнуто %.1f в секунду, доля 95 задержки %.0f мс\n",
		rep.Load.Requests, rep.Load.AchievedRPS, rep.Load.LatencyP95Ms)
	fmt.Printf("маскирование: фрагментов %d, среднее изменение %.4f, затронуто %.1f%%, лишних байтов вне фрагментов %.4f%%\n",
		rep.Masking.Fragments, rep.Masking.AvgDistance,
		rep.Masking.ChangedShare*100, rep.Masking.OutsideChangedShare*100)
	fmt.Printf("обратный шаг: точное восстановление %.1f%% на %d элементах\n",
		rep.Demask.ExactShare*100, rep.Demask.Total)
	fmt.Printf("отчёты: %s/report.md и %s/report.json\n", dir, dir)
}

// normalizeURL дописывает путь ручки, если передали только адрес сервиса.
// На стенде удобнее указывать адрес целиком, и ошибка в пути выглядела бы как
// «решение не держит нагрузку», хотя дело в опечатке запуска.
func normalizeURL(raw string) string {
	trimmed := strings.TrimRight(raw, "/")
	if strings.HasSuffix(trimmed, "/process") {
		return trimmed
	}
	return trimmed + "/process"
}
