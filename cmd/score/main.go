// Команда score измеряет качество обнаружения прямо на размеченном наборе,
// без сети и без запуска сервиса. Нужна для быстрых итераций: полный прогон
// имитатора проверяющей системы занимает минуты, а этот замер — секунды.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/mask"
	"pii-guard/internal/pii"
)

// sample — элемент размеченного набора.
type sample struct {
	ID       string     `json:"id"`
	Category string     `json:"category"`
	Text     string     `json:"text"`
	Spans    []goldSpan `json:"spans"`
	// Source — происхождение элемента: собственная генерация или открытый
	// источник. Нужен, чтобы отчёт различал качество на своих и на чужих данных.
	Source string `json:"source,omitempty"`
	// PartialLabels означает, что размечены не все персональные данные текста.
	// Так помечаются настоящие тексты из открытых источников: в них могут
	// встретиться неразмеченные имена и адреса, поэтому изменения за пределами
	// размеченных фрагментов у таких элементов не считаются ошибкой.
	PartialLabels bool `json:"partial_labels,omitempty"`
}

type goldSpan struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Type  string `json:"type"`
}

// stat копит показатели по одному срезу: типу или категории.
type stat struct {
	fragments int
	changed   float64
	touched   int
	extra     int
	outside   int
}

func main() {
	datasetPath := flag.String("dataset", "corpus/dataset.jsonl", "путь к размеченному набору")
	onlyType := flag.String("type", "", "показать примеры ошибок только по этому типу")
	onlyCategory := flag.String("category", "", "ограничить набор одной категорией")
	examples := flag.Int("examples", 0, "сколько примеров ошибок напечатать")
	limit := flag.Int("limit", 0, "обработать не больше стольких элементов")
	flag.Parse()

	samples, err := load(*datasetPath, *onlyCategory, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	eng, sys, defs := buildEngine()
	byType := map[string]*stat{}
	byCategory := map[string]*stat{}
	bySource := map[string]*stat{}
	shown := 0

	for _, s := range samples {
		res := eng.Mask(s.Text, sys, defs)
		cat := ensure(byCategory, s.Category)

		origRunes := []rune(s.Text)
		maskRunes := []rune(res.Text)
		idx := runeIndex(s.Text)
		inside := make([]bool, len(origRunes))

		for _, g := range s.Spans {
			if g.Start < 0 || g.End > len(s.Text) || g.Start >= g.End {
				continue
			}
			startRune, endRune := idx[g.Start], idx[g.End]
			for i := startRune; i < endRune && i < len(inside); i++ {
				inside[i] = true
			}
			st := ensure(byType, g.Type)
			src := ensure(bySource, s.Source)
			ratio := changedRatio(origRunes, maskRunes, startRune, endRune)
			st.fragments++
			cat.fragments++
			src.fragments++
			st.changed += ratio
			cat.changed += ratio
			src.changed += ratio
			if ratio > 0 {
				st.touched++
				cat.touched++
				src.touched++
			}
			if ratio < 0.5 && *examples > 0 && shown < *examples &&
				(*onlyType == "" || *onlyType == g.Type) {
				shown++
				fmt.Printf("ПРОПУСК %-16s [%s] %q\n   текст: %s\n   маска: %s\n",
					g.Type, s.Category, s.Text[g.Start:g.End], cut(s.Text), cut(res.Text))
			}
		}

		extra := 0
		if !s.PartialLabels {
			var outside int
			extra, outside = outsideChanges(origRunes, maskRunes, inside)
			cat.extra += extra
			cat.outside += outside
			src := ensure(bySource, s.Source)
			src.extra += extra
			src.outside += outside
		}
		if extra > 0 && *examples > 0 && shown < *examples && *onlyType == "" && len(s.Spans) == 0 {
			shown++
			fmt.Printf("ЛИШНЕЕ  [%s]\n   текст: %s\n   маска: %s\n", s.Category, cut(s.Text), cut(res.Text))
		}
	}

	report("Типы", byType, *onlyType)
	report("Источники", bySource, "")
	report("Категории", byCategory, "")
}

// buildEngine собирает конвейер с теми же детекторами, что и сервис, и с
// профилем проверяющей системы.
func buildEngine() (*engine.Engine, config.System, config.Defaults) {
	reg := pii.NewRegistry()
	reg.Register(
		pii.NewNumericDetector(),
		pii.NewEmailDetector(),
		pii.NewFIODetector(),
		pii.NewDateDetector(config.DatePIIContext),
		pii.NewAddressDetector(),
		pii.NewBirthPlaceDetector(),
		pii.NewIssuerDetector(),
		pii.NewCitizenshipDetector(),
		pii.NewDriverLicenseLettersDetector(),
		pii.NewCardHolderDetector(),
		pii.NewExtraDocumentsDetector(),
	)
	defs := config.Defaults{
		Preset:            mask.PresetFull,
		MinConfidence:     0.5,
		DateWithoutAnchor: config.DatePIIContext,
	}
	sys := config.System{
		Name:     "alfasonar",
		Enabled:  true,
		AllTypes: true,
		Demask:   true,
		Preset:   mask.PresetFull,
		Exclusions: config.Exclusions{
			PublicFigures: true,
			OrgAddresses:  true,
		},
	}
	return engine.New(reg), sys, defs
}

// changedRatio считает долю изменённых рун внутри эталонного фрагмента.
//
// Сравнение идёт в координатах рун, а не байтов: маска сохраняет число рун,
// но звёздочка занимает один байт, а кириллическая буква два, поэтому
// байтовые смещения в маске смещаются, а рунные совпадают.
func changedRatio(origRunes, maskRunes []rune, startRune, endRune int) float64 {
	if startRune < 0 || endRune > len(origRunes) || startRune >= endRune {
		return 0
	}
	if len(origRunes) != len(maskRunes) {
		return 1
	}
	diff := 0
	for i := startRune; i < endRune; i++ {
		if origRunes[i] != maskRunes[i] {
			diff++
		}
	}
	return float64(diff) / float64(endRune-startRune)
}

// runeIndex строит перевод байтового смещения в номер руны.
func runeIndex(text string) []int {
	idx := make([]int, len(text)+1)
	n := 0
	for i := range text {
		idx[i] = n
		n++
	}
	for i := len(text); i >= 0; i-- {
		if i == len(text) || idx[i] == 0 && i > 0 {
			idx[i] = n
		} else {
			break
		}
	}
	// Заполняем продолжения многобайтовых рун номером их начала.
	cur := 0
	for i := 0; i <= len(text); i++ {
		if i < len(text) && isRuneStart(text[i]) {
			cur = idx[i]
		} else if i < len(text) {
			idx[i] = cur
		}
	}
	idx[len(text)] = n
	return idx
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// outsideChanges считает изменённые руны за пределами эталонных фрагментов.
func outsideChanges(origRunes, maskRunes []rune, inside []bool) (changed, total int) {
	if len(origRunes) != len(maskRunes) {
		return 0, len(origRunes)
	}
	for i := range origRunes {
		if i < len(inside) && inside[i] {
			continue
		}
		total++
		if origRunes[i] != maskRunes[i] {
			changed++
		}
	}
	return changed, total
}

func ensure(m map[string]*stat, key string) *stat {
	if key == "" {
		key = "(без категории)"
	}
	if s, ok := m[key]; ok {
		return s
	}
	s := &stat{}
	m[key] = s
	return s
}

func report(title string, m map[string]*stat, only string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		if only != "" && k != only {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := m[keys[i]], m[keys[j]]
		return avg(a) < avg(b)
	})
	fmt.Printf("\n%s\n%-24s %10s %10s %10s\n", title, "срез", "изменено", "затронуто", "фрагментов")
	for _, k := range keys {
		s := m[k]
		touched := 0.0
		if s.fragments > 0 {
			touched = float64(s.touched) / float64(s.fragments)
		}
		extra := ""
		if s.outside > 0 && s.extra > 0 {
			extra = fmt.Sprintf("  лишних байт %.2f%%", 100*float64(s.extra)/float64(s.outside))
		}
		fmt.Printf("%-24s %10.4f %9.1f%% %10d%s\n", k, avg(s), 100*touched, s.fragments, extra)
	}
}

func avg(s *stat) float64 {
	if s.fragments == 0 {
		return 0
	}
	return s.changed / float64(s.fragments)
}

func cut(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) > 150 {
		return string(r[:150]) + "…"
	}
	return s
}

func load(path, category string, limit int) ([]sample, error) {
	f, err := os.Open(path) //nolint:gosec // путь задаёт разработчик
	if err != nil {
		return nil, fmt.Errorf("набор не открывается: %w", err)
	}
	defer func() { _ = f.Close() }()

	var out []sample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var s sample
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			continue
		}
		if category != "" && s.Category != category {
			continue
		}
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, sc.Err()
}
