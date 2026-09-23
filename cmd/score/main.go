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

	sc := newScanner(*minConf, preset)
	acc := newSliceStats()
	ex := &examplePrinter{limit: *examples, onlyType: *onlyType}
	for _, s := range samples {
		scoreSample(sc, &s, *lower, acc, ex)
	}

	report("Типы", acc.byType, *onlyType, *split)
	report("Источники", acc.bySource, "", false)
	report("Категории", acc.byCategory, "", false)
}

// sliceStats — показатели одного прогона сразу в трёх срезах: по типу
// персональных данных, по категории элемента и по источнику. Срезы заполняются
// за один проход: один и тот же фрагмент учитывается в каждом из них, и
// считать их по отдельности означало бы маскировать набор трижды.
type sliceStats struct {
	byType     map[string]*stat
	byCategory map[string]*stat
	bySource   map[string]*stat
}

func newSliceStats() *sliceStats {
	return &sliceStats{
		byType:     map[string]*stat{},
		byCategory: map[string]*stat{},
		bySource:   map[string]*stat{},
	}
}

// scoreSample сверяет один элемент набора с эталонной разметкой и разносит
// результат по срезам. Вынесен из main отдельно, чтобы разбор одного элемента
// читался целиком и не перемешивался с разбором ключей и печатью отчёта.
func scoreSample(sc *scanner, s *sample, lower bool, acc *sliceStats, ex *examplePrinter) {
	// Категория заводится на каждом элементе, даже если размеченных фрагментов
	// в нём нет: отрицательные категории тем и ценны, что находиться в них
	// нечему, и в отчёте они обязаны быть видны.
	cat := ensure(acc.byCategory, s.Category)

	p := scanSample(sc, s, lower)
	p.eachSpan(s.Spans, func(g goldSpan, ratio float64) {
		addRatio(ensure(acc.byType, g.Type), ratio)
		addRatio(cat, ratio)
		addRatio(ensure(acc.bySource, s.Source), ratio)
		if ratio < 0.5 {
			ex.miss(g, s.Category, p.text, p.masked)
		}
	})

	extra := 0
	if !s.PartialLabels {
		var outside int
		extra, outside = p.falsePositives()
		cat.extra += extra
		cat.outside += outside
		src := ensure(acc.bySource, s.Source)
		src.extra += extra
		src.outside += outside
	}
	// Элемент без единого размеченного фрагмента, в котором что-то изменилось,
	// — самый наглядный пример ложного срабатывания: менять там было нечего.
	if extra > 0 && len(s.Spans) == 0 {
		ex.unexpected(s.Category, p.text, p.masked)
	}
}

// runDatasets прогоняет замер по нескольким наборам и печатает общую таблицу
// тип×набор: доля изменённого, доля затронутых, число фрагментов.
func runDatasets(list string, minConf float64, preset mask.Preset, lower, split bool) {
	acc := scoreDatasets(strings.Split(list, ","), newScanner(minConf, preset), lower)
	printTypeSets(acc.byType, preset, lower, split)
	printSetTotals(acc.bySet)
}

// setStats — показатели прогона по нескольким наборам: отдельно по паре тип и
// набор, отдельно по набору целиком. Две карты, а не одна, потому что таблица
// тип×набор отвечает на вопрос, где именно слабее, а итог по набору — какой
// набор тяжелее.
type setStats struct {
	byType map[string]map[string]*stat
	bySet  map[string]*stat
}

// scoreDatasets прогоняет замер по каждому набору из списка. Нечитаемый набор
// прогон не останавливает: список путей задаётся руками, опечатка в одном из
// них — дело обычное, а остальные наборы измерить всё равно надо.
func scoreDatasets(paths []string, sc *scanner, lower bool) *setStats {
	acc := &setStats{
		byType: map[string]map[string]*stat{},
		bySet:  map[string]*stat{},
	}
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
			scoreSetSample(sc, &s, lower, acc, setName)
		}
	}
	return acc
}

// scoreSetSample сверяет один элемент с разметкой и учитывает его дважды: в
// паре тип и набор и в итоге по набору.
func scoreSetSample(sc *scanner, s *sample, lower bool, acc *setStats, setName string) {
	set := ensure(acc.bySet, setName)
	p := scanSample(sc, s, lower)
	p.eachSpan(s.Spans, func(g goldSpan, ratio float64) {
		addRatio(ensureType(acc.byType, g.Type, setName), ratio)
		addRatio(set, ratio)
	})
	if !s.PartialLabels {
		extra, outside := p.falsePositives()
		set.extra += extra
		set.outside += outside
	}
}

// printTypeSets печатает таблицу тип×набор: по каждому типу видно, на каком
// наборе он проседает.
func printTypeSets(byType map[string]map[string]*stat, preset mask.Preset, lower, split bool) {
	// Заголовок: тип, набор, изменено, затронуто, фрагментов.
	fmt.Printf("\nКачество по типам и наборам (пресет %s%s)\n", preset, lowerLabel(lower))
	fmt.Printf("%-16s %-22s %10s %10s %10s\n", "тип", "набор", "изменено", "затронуто", "фрагментов")

	for _, t := range sortedTypeKeys(byType) {
		ts := byType[t]
		for _, n := range sortedKeys(ts) {
			s := ts[n]
			fmt.Printf("%-16s %-22s %10.4f %9.1f%% %10d%s\n",
				t, n, avg(s), percent(s.touched, s.fragments), s.fragments, typeMark(t, split))
		}
	}
}

// printSetTotals печатает итог по каждому набору целиком: ради него наборы и
// прогоняются вместе, поодиночке их не сравнить.
func printSetTotals(bySet map[string]*stat) {
	fmt.Printf("\nИтог по наборам\n%-22s %10s %10s %10s %10s\n", "набор", "изменено", "затронуто", "фрагментов", "ложных")
	for _, n := range sortedKeys(bySet) {
		s := bySet[n]
		fmt.Printf("%-22s %10.4f %9.1f%% %10d %9.2f%%\n",
			n, avg(s), percent(s.touched, s.fragments), s.fragments, percent(s.extra, s.outside))
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

// typeMark помечает, из задания тип или добавлен нами сверх него. Без ключа
// -split пометки нет: она нужна ровно тогда, когда обязательную часть
// сравнивают с добавленной, и в остальное время только мешает читать таблицу.
func typeMark(typ string, split bool) string {
	if !split {
		return ""
	}
	if taskTypes[typ] {
		return "  [задание]"
	}
	return "  [добавление]"
}

// percent переводит часть в проценты и отдаёт ноль при пустом целом. Пустое
// целое здесь обычное дело: в отрицательных категориях размеченных фрагментов
// нет вовсе, и деление на ноль поставило бы в таблицу NaN.
func percent(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(part) / float64(total)
}

// addRatio учитывает один эталонный фрагмент в срезе: долю изменённого и то,
// как фрагмент закрыт — целиком, частично или вовсе пропущен. Один и тот же
// фрагмент попадает сразу в несколько срезов (тип, категория, источник,
// набор), и правило учёта у них общее.
func addRatio(s *stat, ratio float64) {
	s.fragments++
	s.changed += ratio
	if ratio > 0 {
		s.touched++
	}
	switch {
	case ratio == 0:
		s.missed++
	case ratio < 1:
		s.partial++
	default:
		s.full++
	}
}

// scanner — собранный конвейер маскирования вместе с профилем проверяющей
// системы. Три части всегда ходят вместе и порознь смысла не имеют, поэтому
// передаются одним значением.
type scanner struct {
	eng  *engine.Engine
	sys  config.System
	defs config.Defaults
}

// newScanner собирает конвейер с теми же детекторами, что и сервис, и с
// профилем проверяющей системы.
func newScanner(minConf float64, preset mask.Preset) *scanner {
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
		pii.NewExtraDetector(),
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
	return &scanner{eng: engine.New(reg), sys: sys, defs: defs}
}

// mask отдаёт только замаскированный текст: остальные поля ответа движка при
// замере не нужны, качество считается сравнением текста с исходным.
func (sc *scanner) mask(text string) string {
	return sc.eng.Mask(text, sc.sys, sc.defs).Text
}

// sampleScan — подготовленный к сверке элемент: текст, его маска и оба в
// рунах. Рунные представления считаются один раз на элемент, потому что
// сверка идёт в координатах рун (смотри changedRatio), а перевод смещений
// стоит прохода по всему тексту.
type sampleScan struct {
	text      string // текст после приведения регистра: на нём считались смещения
	masked    string
	origRunes []rune
	maskRunes []rune
	idx       []int
	inside    []bool // руны, попавшие в эталонные фрагменты
}

// scanSample маскирует элемент и готовит его к сверке с разметкой. Общий шаг
// обоих режимов замера — по одному набору и по нескольким сразу: правило
// сверки должно быть одно на оба, иначе их таблицы начнут расходиться.
func scanSample(sc *scanner, s *sample, lower bool) *sampleScan {
	text := s.Text
	if lower {
		text = strings.ToLower(text)
	}
	masked := sc.mask(text)
	origRunes := []rune(text)
	return &sampleScan{
		text:      text,
		masked:    masked,
		origRunes: origRunes,
		maskRunes: []rune(masked),
		idx:       runeIndex(text),
		inside:    make([]bool, len(origRunes)),
	}
}

// eachSpan перебирает пригодные эталонные фрагменты и отдаёт по каждому долю
// изменённых рун. Негодный фрагмент (выход за границы текста, пустой или
// вывернутый промежуток) пропускается: разметка приходит из внешних наборов, и
// одна испорченная строка не должна ронять весь замер.
//
// Заодно перебор отмечает руны, попавшие в разметку: по этой отметке потом
// считаются ложные срабатывания, поэтому перебирать надо все фрагменты, даже
// если вызывающему интересна лишь часть.
func (p *sampleScan) eachSpan(spans []goldSpan, fn func(g goldSpan, ratio float64)) {
	for _, g := range spans {
		if g.Start < 0 || g.End > len(p.text) || g.Start >= g.End {
			continue
		}
		startRune, endRune := p.idx[g.Start], p.idx[g.End]
		for i := startRune; i < endRune && i < len(p.inside); i++ {
			p.inside[i] = true
		}
		fn(g, changedRatio(p.origRunes, p.maskRunes, startRune, endRune))
	}
}

// falsePositives считает изменённые руны за пределами эталонных фрагментов.
// Имеет смысл только после eachSpan: до перебора отметка разметки пуста и
// ложным срабатыванием окажется весь замаскированный текст.
func (p *sampleScan) falsePositives() (extra, outside int) {
	return outsideChanges(p.origRunes, p.maskRunes, p.inside)
}

// examplePrinter печатает примеры ошибок и следит за их числом. На большом
// наборе пропусков тысячи, а разобрать руками удаётся десяток, поэтому печать
// ограничена ключами -examples и -type.
type examplePrinter struct {
	limit    int
	onlyType string
	shown    int
}

// miss печатает пример пропуска: эталонный фрагмент, замаскированный меньше
// чем наполовину.
func (p *examplePrinter) miss(g goldSpan, category, text, masked string) {
	if p.onlyType != "" && p.onlyType != g.Type {
		return
	}
	if !p.take() {
		return
	}
	fmt.Printf("ПРОПУСК %-16s [%s] %q\n   текст: %s\n   маска: %s\n",
		g.Type, category, text[g.Start:g.End], cut(text), cut(masked))
}

// unexpected печатает пример ложного срабатывания. Ключ -type такие примеры
// прячет: у них нет типа, и в выборке по одному типу им не место.
func (p *examplePrinter) unexpected(category, text, masked string) {
	if p.onlyType != "" {
		return
	}
	if !p.take() {
		return
	}
	fmt.Printf("ЛИШНЕЕ  [%s]\n   текст: %s\n   маска: %s\n", category, cut(text), cut(masked))
}

// take занимает место под ещё один пример, если оно осталось.
func (p *examplePrinter) take() bool {
	if p.limit <= 0 || p.shown >= p.limit {
		return false
	}
	p.shown++
	return true
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
		a, b := avg(m[keys[i]]), avg(m[keys[j]])
		if a != b {
			return a < b
		}
		// При равном качестве порядок задаёт имя. Без этого sort.Slice
		// переставляет равные строки как придётся, и два прогона одного и
		// того же замера отличаются порядком: у отрицательных срезов
		// качество всегда ноль, поэтому равны они все разом. Сравнение
		// «до и после» на таком выводе тонет в перестановках — на этом я
		// сама однажды решила, что восемь чистых правок сломали разбор.
		return keys[i] < keys[j]
	})
	fmt.Printf("\n%s\n%-24s %10s %10s %10s %10s %10s %10s\n",
		title, "срез", "изменено", "затронуто", "пропущено", "частично", "ложных", "фрагментов")
	for _, k := range keys {
		s := m[k]
		// Доля ложных печатается только там, где есть что делить: у среза без
		// рун вне эталона она не ноль, а неизвестна, и ноль тут соврал бы.
		extra := ""
		if s.outside > 0 {
			extra = fmt.Sprintf("%9.2f%%", percent(s.extra, s.outside))
		}
		fmt.Printf("%-24s %10.4f %9.1f%% %9.1f%% %9.1f%% %10s %10d%s\n",
			k, avg(s), percent(s.touched, s.fragments), percent(s.missed, s.fragments),
			percent(s.partial, s.fragments), extra, s.fragments, typeMark(k, split))
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
