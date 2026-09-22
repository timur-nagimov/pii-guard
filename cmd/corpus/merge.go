package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"sort"
)

// tally копит счётчики по строковому ключу для сводки.
type tally map[string]int

// merger собирает общий набор из нескольких файлов и ведёт сводку.
type merger struct {
	defaultSource string
	seen          map[string]bool
	recs          []Record
	bySource      tally
	byType        tally
	dropped       tally
	brokenLines   int
}

// newMerger создаёт сборщик с пустыми счётчиками.
func newMerger(defaultSource string) *merger {
	return &merger{
		defaultSource: defaultSource,
		seen:          map[string]bool{},
		bySource:      tally{},
		byType:        tally{},
		dropped:       tally{},
	}
}

// addFile читает файл и добавляет в общий набор годные элементы.
func (mg *merger) addFile(path string) error {
	recs, broken, err := readRecords(path)
	if err != nil {
		return err
	}
	mg.brokenLines += broken
	if broken > 0 {
		mg.dropped[reasonBadLine] += broken
	}
	for _, r := range recs {
		mg.add(r)
	}
	return nil
}

// add проверяет элемент и либо берёт его, либо считает причину отбраковки.
func (mg *merger) add(r Record) {
	if r.Source == "" {
		r.Source = mg.defaultSource
	}
	if reason := validate(r); reason != "" {
		mg.dropped[reason]++
		return
	}
	if mg.seen[r.ID] {
		mg.dropped[reasonDupID]++
		return
	}
	mg.seen[r.ID] = true
	mg.bySource[r.Source]++
	for _, s := range r.Spans {
		mg.byType[s.Type]++
	}
	mg.recs = append(mg.recs, r)
}

// shuffle перемешивает набор по зерну. Перемешивание нужно, чтобы элементы
// одного источника не шли подряд: иначе замер на первой тысяче строк меряет
// один источник, а не набор целиком.
func (mg *merger) shuffle(seed uint64) {
	//nolint:gosec // перемешивание обязано быть воспроизводимым по seed
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	r.Shuffle(len(mg.recs), func(i, j int) {
		mg.recs[i], mg.recs[j] = mg.recs[j], mg.recs[i]
	})
}

// printCounts печатает счётчики по убыванию значения.
func printCounts(title string, counts tally) {
	if len(counts) == 0 {
		return
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	fmt.Printf("\n%s\n", title)
	for _, k := range keys {
		if counts[k] == 0 {
			continue
		}
		fmt.Printf("  %-24s %7d\n", k, counts[k])
	}
}

// summary печатает сводку сборки: что взято, что отброшено и с какой разметкой.
func (mg *merger) summary(out string) {
	partial := 0
	for _, r := range mg.recs {
		if r.PartialLabels {
			partial++
		}
	}
	fmt.Printf("merge: %s, элементов %d, из них с частичной разметкой %d, отброшено %d\n",
		out, len(mg.recs), partial, mg.total(mg.dropped))
	printCounts("Источники", mg.bySource)
	printCounts("Отброшено", mg.dropped)
	printCounts("Фрагменты по типам", mg.byType)
}

// total складывает все счётчики.
func (mg *merger) total(t tally) int {
	sum := 0
	for _, v := range t {
		sum += v
	}
	return sum
}

// sampleRecords отбирает пробу набора поровну от каждого источника. Проба
// нужна репозиторию: сам набор туда не кладётся, а посмотреть на формат и
// прогнать проверки на паре сотен строк надо без выгрузки из сети.
func sampleRecords(recs []Record, n int) []Record {
	if n <= 0 || len(recs) <= n {
		return recs
	}
	bySource := map[string][]Record{}
	order := make([]string, 0, 8)
	for _, r := range recs {
		if _, seen := bySource[r.Source]; !seen {
			order = append(order, r.Source)
		}
		bySource[r.Source] = append(bySource[r.Source], r)
	}
	sort.Strings(order)
	out := make([]Record, 0, n)
	for round := 0; len(out) < n; round++ {
		added := false
		for _, src := range order {
			if round >= len(bySource[src]) || len(out) >= n {
				continue
			}
			out = append(out, bySource[src][round])
			added = true
		}
		if !added {
			break
		}
	}
	return out
}

// runMerge разбирает флаги подкоманды merge и сводит файлы в один набор.
func runMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ExitOnError)
	out := fs.String("out", "corpus/merged.jsonl", "куда записать общий набор")
	seed := fs.Uint64("seed", 42, "зерно перемешивания, один seed даёт один и тот же порядок")
	defaultSource := fs.String("default-source", sourceSynthetic,
		"чем заполнять пустое поле source у старых файлов")
	sample := fs.String("sample", "", "куда записать пробу набора, пусто означает не записывать")
	sampleN := fs.Int("sample-n", 200, "сколько строк класть в пробу")
	if err := fs.Parse(args); err != nil {
		return err
	}
	files := fs.Args()
	if len(files) == 0 {
		return fmt.Errorf("не заданы файлы для сборки")
	}
	if !knownSources[*defaultSource] {
		return fmt.Errorf("источник %q не входит в перечень допустимых", *defaultSource)
	}

	mg := newMerger(*defaultSource)
	for _, path := range files {
		if err := mg.addFile(path); err != nil {
			return err
		}
	}
	if len(mg.recs) == 0 {
		return fmt.Errorf("годных элементов не нашлось")
	}
	mg.shuffle(*seed)
	if err := writeRecords(*out, mg.recs); err != nil {
		return err
	}
	if *sample != "" {
		probe := sampleRecords(mg.recs, *sampleN)
		if err := writeRecords(*sample, probe); err != nil {
			return err
		}
		fmt.Printf("проба: %s, строк %d\n", *sample, len(probe))
	}
	mg.summary(*out)
	return nil
}
