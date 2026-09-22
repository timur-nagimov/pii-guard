// Команда gen порождает синтетический размеченный набор данных для проверки
// качества маскирования. У организаторов нет ни эталонного набора, ни
// примеров, поэтому собственный набор — единственная опора для измерения
// качества и для отчёта на защите.
//
// Запуск: go run ./cmd/gen -out corpus/dataset.jsonl -n 30000 -seed 42
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func main() {
	out := flag.String("out", "corpus/dataset.jsonl", "куда записать набор в формате JSON Lines")
	n := flag.Int("n", 30000, "число строк набора")
	seed := flag.Uint64("seed", 42, "зерно источника случайности: один seed даёт один и тот же набор")
	holdout := flag.String("holdout", "", "куда записать отложенную выборку; пусто — не записывать")
	holdoutN := flag.Int("holdout-n", 0, "число строк отложенной выборки; ноль — десятая часть от -n")
	lowercase := flag.Bool("lowercase", false, "весь набор в нижнем регистре отдельным срезом")
	quiet := flag.Bool("quiet", false, "не печатать сводку по категориям")
	flag.Parse()

	if err := run(*out, *holdout, *n, *holdoutN, *seed, *lowercase, *quiet); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

// run порождает основной набор и, если задан путь, отложенную выборку.
func run(out, holdout string, n, holdoutN int, seed uint64, lowercase, quiet bool) error {
	stats, err := writeDataset(out, n, seed, false, lowercase)
	if err != nil {
		return err
	}
	if !quiet {
		printStats(out, stats)
	}
	if holdout == "" {
		return nil
	}
	if holdoutN <= 0 {
		holdoutN = n / 10
	}
	heldStats, err := writeDataset(holdout, holdoutN, seed, true, lowercase)
	if err != nil {
		return err
	}
	if !quiet {
		printStats(holdout, heldStats)
	}
	return nil
}

// stats — сводка по выпуску набора.
type stats struct {
	records    int
	spans      int
	bytes      int
	elapsed    time.Duration
	byCategory map[string]int
	byType     map[string]int
}

// writeDataset записывает набор в файл построчно. Запись потоковая: длинные
// тексты занимают сотни килобайт каждый, и держать их все в памяти незачем.
func writeDataset(path string, n int, seed uint64, holdout, lowercase bool) (stats, error) {
	st := stats{byCategory: make(map[string]int), byType: make(map[string]int)}
	started := time.Now()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return st, err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return st, err
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(w)
	// Экранирование угловых скобок и амперсанда испортило бы тексты анкет,
	// поэтому оно отключено.
	enc.SetEscapeHTML(false)

	g := NewGenerator(seed, holdout)
	if lowercase {
		g.lowercase = true
	}
	err = g.Each(n, func(r Record) error {
		st.records++
		st.spans += len(r.Spans)
		st.bytes += len(r.Text)
		st.byCategory[r.Category]++
		for _, sp := range r.Spans {
			st.byType[sp.Type]++
		}
		return enc.Encode(r)
	})
	if err != nil {
		return st, err
	}
	if err := w.Flush(); err != nil {
		return st, err
	}
	if err := f.Close(); err != nil {
		return st, err
	}
	st.elapsed = time.Since(started)
	return st, nil
}

// printStats печатает сводку: сколько строк в каждой категории и в каждом
// типе, сколько фрагментов, какого объёма получился набор и сколько заняло
// порождение. Время нужно для отчёта: набор на тридцать тысяч строк собирают
// заново при каждой правке правил.
func printStats(path string, st stats) {
	fmt.Printf("%s: строк %d, фрагментов %d, объём %d КБ, порождение %s\n",
		path, st.records, st.spans, st.bytes/1024, st.elapsed.Round(time.Millisecond))
	fmt.Println("  по категориям:")
	printCounts(st.byCategory)
	fmt.Println("  по типам:")
	printCounts(st.byType)
}

// printCounts печатает счётчики в устойчивом порядке имён.
func printCounts(counts map[string]int) {
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("    %-28s %6d\n", name, counts[name])
	}
}
