// Команда gen порождает синтетический размеченный набор данных для проверки
// качества маскирования. У организаторов нет ни эталонного набора, ни
// примеров, поэтому собственный набор — единственная опора для измерения
// качества и для отчёта на защите.
//
// Запуск: go run ./cmd/gen -out corpus/dataset.jsonl -n 8000 -seed 42
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func main() {
	out := flag.String("out", "corpus/dataset.jsonl", "куда записать набор в формате JSON Lines")
	n := flag.Int("n", 8000, "число строк набора")
	seed := flag.Uint64("seed", 42, "зерно источника случайности: один seed даёт один и тот же набор")
	holdout := flag.String("holdout", "", "куда записать отложенную выборку; пусто — не записывать")
	holdoutN := flag.Int("holdout-n", 0, "число строк отложенной выборки; ноль — десятая часть от -n")
	quiet := flag.Bool("quiet", false, "не печатать сводку по категориям")
	flag.Parse()

	if err := run(*out, *holdout, *n, *holdoutN, *seed, *quiet); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

// run порождает основной набор и, если задан путь, отложенную выборку.
func run(out, holdout string, n, holdoutN int, seed uint64, quiet bool) error {
	stats, err := writeDataset(out, n, seed, false)
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
	heldStats, err := writeDataset(holdout, holdoutN, seed, true)
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
	byCategory map[string]int
}

// writeDataset записывает набор в файл построчно. Запись потоковая: длинные
// тексты занимают сотни килобайт каждый, и держать их все в памяти незачем.
func writeDataset(path string, n int, seed uint64, holdout bool) (stats, error) {
	st := stats{byCategory: make(map[string]int)}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return st, err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return st, err
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(w)
	// Экранирование угловых скобок и амперсанда испортило бы тексты анкет,
	// поэтому оно отключено.
	enc.SetEscapeHTML(false)

	g := NewGenerator(seed, holdout)
	err = g.Each(n, func(r Record) error {
		st.records++
		st.spans += len(r.Spans)
		st.bytes += len(r.Text)
		st.byCategory[r.Category]++
		return enc.Encode(r)
	})
	if err != nil {
		return st, err
	}
	if err := w.Flush(); err != nil {
		return st, err
	}
	return st, f.Close()
}

// printStats печатает сводку: сколько строк в каждой категории, сколько
// фрагментов и какого объёма получился набор.
func printStats(path string, st stats) {
	fmt.Printf("%s: строк %d, фрагментов %d, объём %d КБ\n", path, st.records, st.spans, st.bytes/1024)
	names := make([]string, 0, len(st.byCategory))
	for name := range st.byCategory {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("  %-28s %5d\n", name, st.byCategory[name])
	}
}
