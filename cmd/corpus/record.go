package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// Span задаёт границы персональных данных в байтах исходного текста.
// Байты, а не символы: движок работает с байтовыми смещениями, и набор
// должен сравниваться с его выводом без пересчёта координат.
type Span struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Type  string `json:"type"`
}

// Record описывает один элемент набора в едином формате проекта.
type Record struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	// Source хранит происхождение элемента: собственная генерация или один из
	// открытых источников. По нему отчёт различает качество на своих и на
	// чужих данных.
	Source string `json:"source"`
	Text   string `json:"text"`
	Spans  []Span `json:"spans"`
	// PartialLabels означает, что размечены не все персональные данные текста.
	// Так помечаются настоящие тексты из открытых источников: в них попадаются
	// чужие неразмеченные имена, и наказывать за них нельзя.
	PartialLabels bool `json:"partial_labels"`
}

// Допустимые значения поля source.
const (
	sourceSynthetic = "synthetic"
	sourceWikipedia = "wikipedia"
	sourceHFRuPII   = "hf_ru_pii"
	sourceReviews   = "reviews"
	sourceAI4P      = "ai4privacy"
)

// knownSources перечисляет разрешённые происхождения элементов.
var knownSources = map[string]bool{
	sourceSynthetic: true,
	sourceWikipedia: true,
	sourceHFRuPII:   true,
	sourceReviews:   true,
	sourceAI4P:      true,
}

// knownTypes перечисляет разрешённые типы персональных данных. Список
// повторяет перечень технического задания и держится здесь строкой, чтобы
// сборщик набора не зависел от пакета движка.
var knownTypes = map[string]bool{
	"FIO": true, "DOB": true, "BIRTH_PLACE": true, "PASSPORT": true,
	"CITIZENSHIP": true, "ISSUER": true, "DEPT_CODE": true, "ISSUE_DATE": true,
	"DRIVER_LICENSE": true, "ADDRESS": true, "POSTCODE": true, "EMAIL": true,
	"PHONE": true, "INN": true, "CARD": true, "CVV": true, "PIN": true,
	"CARDHOLDER": true, "SNILS": true, "FOREIGN_PASSPORT": true,
	"RESIDENCE_PERMIT": true, "BIRTH_CERT": true, "MILITARY_ID": true,
}

// Причины, по которым элемент отбрасывается при сборке.
const (
	reasonBadLine   = "строка не разобрана"
	reasonNoID      = "пустой идентификатор"
	reasonDupID     = "повтор идентификатора"
	reasonNoText    = "пустой текст"
	reasonBadSource = "неизвестный источник"
	reasonBadBounds = "границы вне текста"
	reasonBadSlice  = "срез не декодируется"
	reasonBadType   = "неизвестный тип"
)

// validate проверяет элемент целиком и возвращает причину отбраковки.
// Пустая строка означает, что элемент годен.
func validate(r Record) string {
	switch {
	case r.ID == "":
		return reasonNoID
	case r.Text == "":
		return reasonNoText
	case !knownSources[r.Source]:
		return reasonBadSource
	default:
		// Шапка записи в порядке, отбраковывать не за что: разбор
		// продолжается проверкой фрагментов ниже.
	}
	for _, s := range r.Spans {
		if reason := checkSpan(r.Text, s); reason != "" {
			return reason
		}
	}
	return ""
}

// checkSpan проверяет один фрагмент: границы лежат внутри текста, срез
// начинается и кончается на границе символа и декодируется целиком.
func checkSpan(text string, s Span) string {
	if !knownTypes[s.Type] {
		return reasonBadType
	}
	if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
		return reasonBadBounds
	}
	if !utf8.RuneStart(text[s.Start]) {
		return reasonBadSlice
	}
	if s.End < len(text) && !utf8.RuneStart(text[s.End]) {
		return reasonBadSlice
	}
	if !utf8.ValidString(text[s.Start:s.End]) {
		return reasonBadSlice
	}
	return ""
}

// readRecords читает набор построчно. Испорченные строки не роняют чтение:
// их номера возвращаются отдельным счётчиком, а годные элементы доходят до
// сборки. Файл с одной битой строкой тогда остаётся пригодным.
func readRecords(path string) (recs []Record, broken int, err error) {
	f, err := os.Open(path) //nolint:gosec // путь задаёт разработчик
	if err != nil {
		return nil, 0, fmt.Errorf("файл %s не открывается: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			broken++
			continue
		}
		if r.Spans == nil {
			r.Spans = []Span{}
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, broken, fmt.Errorf("файл %s не дочитан: %w", path, err)
	}
	return recs, broken, nil
}

// writeRecords записывает набор построчно в формате JSON Lines.
func writeRecords(path string, recs []Record) error {
	if path == "" {
		return errors.New("не задан путь выходного файла")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	f, err := os.Create(path) //nolint:gosec // путь задаёт разработчик
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(w)
	// Экранирование угловых скобок и амперсанда испортило бы настоящие
	// тексты, поэтому оно отключено.
	enc.SetEscapeHTML(false)
	for i := range recs {
		if recs[i].Spans == nil {
			recs[i].Spans = []Span{}
		}
		if err := enc.Encode(recs[i]); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}

// writeFile кладёт готовый текст в файл. Нужен сборке проб и проверкам.
func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
