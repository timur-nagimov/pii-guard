// Команда capture2corpus превращает захваченные запросы в набор для проверки
// качества и показывает, где решение слепое.
//
// Зачем. Свой набор порождён генератором: в нём ровно те случаи, которые мы
// придумали. Тексты проверяющей системы — единственные настоящие, и по ним
// видно, чего генератор не знает.
//
// Главный режим здесь не превращение в набор, а отчёт: он показывает записи,
// в которых решение не нашло НИЧЕГО. Такие тексты — прямое указание на
// пробел, и для них разметка не нужна.
//
// Осторожно с разметкой. Заполнять метки своими же находками и потом
// проверяться на них бессмысленно: получится сто процентов и ноль знания.
// Поэтому по умолчанию набор выходит с пустыми метками, под разметку руками,
// а признак -prefill заполняет их только как черновик для правки.
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
	"pii-guard/internal/pii"
)

// captureRecord — запись файла захвата. Читаются только нужные поля.
type captureRecord struct {
	System     string         `json:"system"`
	PayloadID  string         `json:"payload_id"`
	Direction  string         `json:"direction"`
	PayloadLen int            `json:"payload_len"`
	Counts     map[string]int `json:"counts"`
	Payload    string         `json:"payload"`
	Result     string         `json:"result"`
}

// corpusSpan — метка набора: границы в БАЙТАХ и тип.
type corpusSpan struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Type  string `json:"type"`
}

// corpusRecord — запись набора в том же виде, что порождает cmd/gen.
type corpusRecord struct {
	ID       string       `json:"id"`
	Category string       `json:"category"`
	Source   string       `json:"source"`
	Text     string       `json:"text"`
	Spans    []corpusSpan `json:"spans"`
}

func main() {
	in := flag.String("in", "var/capture/requests.jsonl", "файл захвата")
	out := flag.String("out", "", "файл набора; пусто — набор не создаётся")
	prefill := flag.Bool("prefill", false, "заполнить метки нашими находками как черновик для правки")
	empty := flag.Int("empty", 10, "сколько показать текстов, где не найдено ничего")
	flag.Parse()

	recs, err := read(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "не прочитать %s: %v\n", *in, err)
		os.Exit(2)
	}
	if len(recs) == 0 {
		fmt.Fprintln(os.Stderr, "в файле нет записей маскирования с текстом")
		fmt.Fprintln(os.Stderr, "захват включается разделом capture, текст — признаком with_payload")
		os.Exit(1)
	}

	report(recs, *empty)

	if *out != "" {
		n, err := write(*out, recs, *prefill)
		if err != nil {
			fmt.Fprintf(os.Stderr, "не записать %s: %v\n", *out, err)
			os.Exit(2)
		}
		fmt.Printf("\nнабор записан: %s, записей %d\n", *out, n)
		if *prefill {
			fmt.Println("метки заполнены НАШИМИ находками: это черновик под правку руками,")
			fmt.Println("проверяться на нём как есть бессмысленно — выйдет сто процентов и ноль знания")
		} else {
			fmt.Println("метки пустые: набор под разметку руками")
		}
	}
}

// read читает файл захвата и оставляет только маскирование с текстом,
// отбрасывая повторы одного и того же текста.
func read(path string) ([]captureRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	seen := make(map[string]bool)
	var out []captureRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec captureRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		// Обратный шаг ничего нового о текстах не говорит: на вход ему идёт
		// наша же маска.
		if !strings.HasPrefix(rec.Direction, "mask") || rec.Payload == "" {
			continue
		}
		if seen[rec.Payload] {
			continue
		}
		seen[rec.Payload] = true
		out = append(out, rec)
	}
	return out, sc.Err()
}

// report печатает, что вообще пришло и где решение промолчало.
func report(recs []captureRecord, showEmpty int) {
	types := map[string]int{}
	var emptyRecs []captureRecord
	total, minLen, maxLen := 0, recs[0].PayloadLen, 0
	for _, r := range recs {
		total += r.PayloadLen
		if r.PayloadLen < minLen {
			minLen = r.PayloadLen
		}
		if r.PayloadLen > maxLen {
			maxLen = r.PayloadLen
		}
		if len(r.Counts) == 0 {
			emptyRecs = append(emptyRecs, r)
		}
		for t, n := range r.Counts {
			types[t] += n
		}
	}
	fmt.Printf("Записей с разным текстом: %d\n", len(recs))
	fmt.Printf("Длина текста: от %d до %d байт, в среднем %d\n", minLen, maxLen, total/len(recs))

	fmt.Println("\nНайденные типы:")
	names := make([]string, 0, len(types))
	for t := range types {
		names = append(names, t)
	}
	sort.Slice(names, func(i, j int) bool { return types[names[i]] > types[names[j]] })
	for _, t := range names {
		fmt.Printf("  %-18s %d\n", t, types[t])
	}

	fmt.Printf("\nТекстов без единой находки: %d из %d\n", len(emptyRecs), len(recs))
	if len(emptyRecs) == 0 {
		return
	}
	fmt.Println("Это прямое указание на пробел: разметка для них не нужна.")
	for i, r := range emptyRecs {
		if i >= showEmpty {
			fmt.Printf("  ... и ещё %d\n", len(emptyRecs)-showEmpty)
			break
		}
		fmt.Printf("  %s\n", short(r.Payload))
	}
}

// short укорачивает текст до одной строки: отчёт читает человек.
func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		return s[:157] + "…"
	}
	return s
}

// write записывает набор. Метки пустые, если не просили черновик.
func write(path string, recs []captureRecord, prefill bool) (int, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i, r := range recs {
		rec := corpusRecord{
			ID:       fmt.Sprintf("capture-%04d", i+1),
			Category: "capture",
			Source:   "sonar",
			Text:     r.Payload,
			Spans:    []corpusSpan{},
		}
		if prefill {
			rec.Spans = draftSpans(r.Payload)
		}
		if err := enc.Encode(rec); err != nil {
			return 0, err
		}
	}
	return len(recs), w.Flush()
}

// draftSpans размечает текст тем же разбором, что стоит в сервисе. Это
// черновик: он повторяет наши же решения, и проверяться на нём как есть
// бессмысленно — выйдет сто процентов и ноль знания. Смысл в другом: человеку
// проще править готовую разметку, чем ставить её с нуля.
//
// Границы по маске не выводятся намеренно: маскирование звёздочками ставит
// одну звезду на букву, и для кириллицы длина текста не сохраняется.
func draftSpans(text string) []corpusSpan {
	d := pii.NewDoc(text)
	var out []corpusSpan
	for _, det := range draftDetectors() {
		for _, sp := range det.Detect(d) {
			out = append(out, corpusSpan{Start: sp.Start, End: sp.End, Type: string(sp.Type)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// draftDetectors перечисляет тот же состав, что собирает сервис.
func draftDetectors() []pii.Detector {
	return []pii.Detector{
		pii.NewNumericDetector(), pii.NewEmailDetector(), pii.NewFIODetector(),
		pii.NewDateDetector(config.DatePIIContext), pii.NewAddressDetector(),
		pii.NewBirthPlaceDetector(), pii.NewIssuerDetector(), pii.NewCitizenshipDetector(),
		pii.NewDriverLicenseLettersDetector(), pii.NewCardHolderDetector(),
		pii.NewExtraDocumentsDetector(), pii.NewExtraDetector(),
	}
}
