// Команда score измеряет качество обнаружения прямо на размеченном наборе,
// без сети и без запуска сервиса. Нужна для быстрых итераций: полный прогон
// имитатора проверяющей системы занимает минуты, а этот замер — секунды.
//
// Помимо базового замера по одному набору команда умеет:
//
//	-datasets  прогон по нескольким наборам с общей таблицей тип×набор;
//	-preset    выбор вида маскирования (по умолчанию full);
//	-lower     замер на тексте, приведённом к нижнему регистру;
//	-split     разделить типы задания и наши добавления сверх него.
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

// stat копит показатели по одному срезу: типу, категории или источнику.
//
// Пропуск и ложное срабатывание считаются раздельно. Пропуск — эталонный
// фрагмент, который не замаскирован целиком: полностью (ratio == 0) или
// частично (0 < ratio < 1). Ложное срабатывание — изменённые руны за
// пределами эталонных фрагментов.
type stat struct {
	fragments int
	changed   float64
	touched   int
	missed    int // ratio == 0: фрагмент не затронут вовсе
	partial   int // 0 < ratio < 1: фрагмент замаскирован не полностью
	full      int // ratio == 1: фрагмент замаскирован целиком
	extra     int // ложные срабатывания: изменённые руны вне эталона
	outside   int // всего рун вне эталона
}

// taskTypes — типы из раздела 4.1 технического задания. Остальные типы,
// которые умеет сервис, — наши добавления сверх задания.
var taskTypes = map[string]bool{
	"FIO": true, "DOB": true, "BIRTH_PLACE": true, "PASSPORT": true,
	"CITIZENSHIP": true, "ISSUER": true, "DEPT_CODE": true, "ISSUE_DATE": true,
	"DRIVER_LICENSE": true, "ADDRESS": true, "EMAIL": true, "PHONE": true,
	"INN": true, "CARD": true, "CVV": true, "PIN": true, "CARDHOLDER": true,
}

func main() {
	datasetPath := flag.String("dataset", "corpus/dataset.jsonl", "путь к размеченному набору")
	datasets := flag.String("datasets", "", "список наборов через запятую: общая таблица тип×набор")
	onlyType := flag.String("type", "", "показать примеры ошибок только по этому типу")
	onlyCategory := flag.String("category", "", "ограничить набор одной категорией")
	examples := flag.Int("examples", 0, "сколько примеров ошибок напечатать")
	limit := flag.Int("limit", 0, "обработать не больше стольких элементов")
	minConf := flag.Float64("min-conf", 0.5, "порог уверенности: 0.5 профиль полноты, 0.75 и выше профиль точности")
	presetName := flag.String("preset", "full", "вид маскирования: full, full_ws, partial, initials, token, synthetic")
	lower := flag.Bool("lower", false, "привести текст к нижнему регистру перед замером")
	split := flag.Bool("split", false, "разделить типы задания и наши добавления сверх него")
	flag.Parse()

	preset := mask.Preset(*presetName)
	if !preset.Valid() {
		fmt.Fprintf(os.Stderr, "неизвестный пресет %q\n", *presetName)
		os.Exit(1)
	}

	if *datasets != "" {
		runDatasets(*datasets, *minConf, preset, *lower, *split)
		return
	}

	samples, err := load(*datasetPath, *onlyCategory, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	eng, sys, defs := buildEngine(*minConf, preset)
	byType := map[string]*stat{}
	byCategory := map[string]*stat{}
	bySource := map[string]*stat{}
	shown := 0

	for _, s := range samples {
		text := s.Text
		if *lower {
			text = strings.ToLower(text)
		}
		res := eng.Mask(text, sys, defs)
		cat := ensure(byCategory, s.Category)

		origRunes := []rune(text)
		maskRunes := []rune(res.Text)
		idx := runeIndex(text)
		inside := make([]bool, len(origRunes))

		for _, g := range s.Spans {
			if g.Start < 0 || g.End > len(text) || g.Start >= g.End {
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
			switch {
			case ratio == 0:
				st.missed++
				cat.missed++
				src.missed++
			case ratio < 1:
				st.partial++
				cat.partial++
				src.partial++
			default:
				st.full++
				cat.full++
				src.full++
			}
			if ratio < 0.5 && *examples > 0 && shown < *examples &&
				(*onlyType == "" || *onlyType == g.Type) {
				shown++
				fmt.Printf("ПРОПУСК %-16s [%s] %q\n   текст: %s\n   маска: %s\n",
					g.Type, s.Category, text[g.Start:g.End], cut(text), cut(res.Text))
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
			fmt.Printf("ЛИШНЕЕ  [%s]\n   текст: %s\n   маска: %s\n", s.Category, cut(text), cut(res.Text))
		}
	}

	report("Типы", byType, *onlyType, *split)
	report("Источники", bySource, "", false)
	report("Категории", byCategory, "", false)
}

// runDatasets прогоняет замер по нескольким наборам и печатает общую таблицу
// тип×набор: доля изменённого, доля затронутых, число фрагментов.
func runDatasets(list string, minConf float64, preset mask.Preset, lower, split bool) {
	paths := strings.Split(list, ",")
	eng, sys, defs := buildEngine(minConf, preset)

	// byType[тип][набор] = stat
	byType := map[string]map[string]*stat{}
	// bySet[набор] = stat (итог по набору)
	bySet := map[string]*stat{}

	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		setName := setLabel(path)
		samples, err := load(path, "", 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "набор %s: %v\n", path, err)
			continue
		}
		for _, s := range samples {
			text := s.Text
			if lower {
				text = strings.ToLower(text)
			}
			res := eng.Mask(text, sys, defs)
			origRunes := []rune(text)
			maskRunes := []rune(res.Text)
			idx := runeIndex(text)
			inside := make([]bool, len(origRunes))
			set := ensure(bySet, setName)

			for _, g := range s.Spans {
				if g.Start < 0 || g.End > len(text) || g.Start >= g.End {
					continue
				}
				startRune, endRune := idx[g.Start], idx[g.End]
				for i := startRune; i < endRune && i < len(inside); i++ {
					inside[i] = true
				}
				ratio := changedRatio(origRunes, maskRunes, startRune, endRune)
				st := ensureType(byType, g.Type, setName)
				st.fragments++
				set.fragments++
				st.changed += ratio
				set.changed += ratio
				if ratio > 0 {
					st.touched++
					set.touched++
				}
				switch {
				case ratio == 0:
					st.missed++
					set.missed++
				case ratio < 1:
					st.partial++
					set.partial++
				default:
					st.full++
					set.full++
				}
			}

			if !s.PartialLabels {
				var outside int
				extra, outside := outsideChanges(origRunes, maskRunes, inside)
				set.extra += extra
				set.outside += outside
			}
		}
	}

	// Заголовок: тип, набор, изменено, затронуто, фрагментов.
	fmt.Printf("\nКачество по типам и наборам (пресет %s%s)\n", preset, lowerLabel(lower))
	fmt.Printf("%-16s %-22s %10s %10s %10s\n", "тип", "набор", "изменено", "затронуто", "фрагментов")

	types := sortedTypeKeys(byType)
	for _, t := range types {
		ts := byType[t]
		sets := sortedKeys(ts)
		for _, n := range sets {
			s := ts[n]
			touched := 0.0
			if s.fragments > 0 {
				touched = 100 * float64(s.touched) / float64(s.fragments)
			}
			mark := ""
			if split {
				mark = "  [задание]"
				if !taskTypes[t] {
					mark = "  [добавление]"
				}
			}
			fmt.Printf("%-16s %-22s %10.4f %9.1f%% %10d%s\n",
				t, n, avg(s), touched, s.fragments, mark)
		}
	}

	// Итог по наборам.
	fmt.Printf("\nИтог по наборам\n%-22s %10s %10s %10s %10s\n", "набор", "изменено", "затронуто", "фрагментов", "ложных")
	sets := sortedKeys(bySet)
	for _, n := range sets {
		s := bySet[n]
		touched := 0.0
		if s.fragments > 0 {
			touched = 100 * float64(s.touched) / float64(s.fragments)
		}
		fp := 0.0
		if s.outside > 0 {
			fp = 100 * float64(s.extra) / float64(s.outside)
		}
		fmt.Printf("%-22s %10.4f %9.1f%% %10d %9.2f%%\n",
			n, avg(s), touched, s.fragments, fp)
	}
}

// setLabel превращает путь к набору в короткое имя для таблицы.
func setLabel(path string) string {
	base := path
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSuffix(base, ".jsonl")
	return base
}

func lowerLabel(lower bool) string {
	if lower {
		return ", нижний регистр"
	}
	return ""
}

// buildEngine собирает конвейер с теми же детекторами, что и сервис, и с
// профилем проверяющей системы.
func buildEngine(minConf float64, preset mask.Preset) (*engine.Engine, config.System, config.Defaults) {
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
		Preset:            preset,
		MinConfidence:     minConf,
		DateWithoutAnchor: config.DatePIIContext,
	}
	sys := config.System{
		Name:     "alfasonar",
		Enabled:  true,
		AllTypes: true,
		Demask:   true,
		Preset:   preset,
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

func report(title string, m map[string]*stat, only string, split bool) {
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
	fmt.Printf("\n%s\n%-24s %10s %10s %10s %10s %10s %10s\n",
		title, "срез", "изменено", "затронуто", "пропущено", "частично", "ложных", "фрагментов")
	for _, k := range keys {
		s := m[k]
		touched := 0.0
		missed := 0.0
		partial := 0.0
		if s.fragments > 0 {
			touched = 100 * float64(s.touched) / float64(s.fragments)
			missed = 100 * float64(s.missed) / float64(s.fragments)
			partial = 100 * float64(s.partial) / float64(s.fragments)
		}
		extra := ""
		if s.outside > 0 {
			extra = fmt.Sprintf("%9.2f%%", 100*float64(s.extra)/float64(s.outside))
		}
		mark := ""
		if split {
			mark = "  [задание]"
			if !taskTypes[k] {
				mark = "  [добавление]"
			}
		}
		fmt.Printf("%-24s %10.4f %9.1f%% %9.1f%% %9.1f%% %10s %10d%s\n",
			k, avg(s), touched, missed, partial, extra, s.fragments, mark)
	}
}

func avg(s *stat) float64 {
	if s.fragments == 0 {
		return 0
	}
	return s.changed / float64(s.fragments)
}

func sortedKeys(m map[string]*stat) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedTypeKeys(m map[string]map[string]*stat) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ensureType достаёт статистику по паре тип×набор, создавая промежуточные
// карты по мере необходимости.
func ensureType(m map[string]map[string]*stat, typ, setName string) *stat {
	ts, ok := m[typ]
	if !ok {
		ts = map[string]*stat{}
		m[typ] = ts
	}
	return ensure(ts, setName)
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
