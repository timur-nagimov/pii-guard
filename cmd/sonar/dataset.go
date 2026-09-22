package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Fragment описывает эталонный фрагмент персональных данных внутри текста.
// Границы заданы байтовыми смещениями исходной строки, End в диапазон не
// входит. Байты, а не руны, потому что именно в байтах считается доля
// изменённого текста вне эталонных фрагментов.
type Fragment struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Sample описывает один элемент набора: текст, его идентификатор, категорию
// сценария и разметку эталонных фрагментов.
type Sample struct {
	PayloadID string     `json:"payload_id"`
	Category  string     `json:"category"`
	Text      string     `json:"text"`
	Fragments []Fragment `json:"spans"`
}

// rawSample принимает набор в нескольких написаниях полей, чтобы имитатор не
// ломался от того, каким генератором набор собран.
type rawSample struct {
	PayloadID string        `json:"payload_id"`
	ID        string        `json:"id"`
	Category  string        `json:"category"`
	Text      string        `json:"text"`
	Payload   string        `json:"payload"`
	Spans     []rawFragment `json:"spans"`
	Fragments []rawFragment `json:"fragments"`
	Entities  []rawFragment `json:"entities"`
}

// rawFragment принимает разметку фрагмента как по смещениям, так и по одному
// лишь значению: во втором случае смещение ищется в тексте.
type rawFragment struct {
	Start *int   `json:"start"`
	End   *int   `json:"end"`
	Type  string `json:"type"`
	Label string `json:"label"`
	Value string `json:"value"`
	Text  string `json:"text"`
}

// LoadDataset читает набор из файла. Поддержаны два написания: массив JSON и
// построчный JSON, по объекту на строку.
func LoadDataset(path string) ([]Sample, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать набор %s: %w", path, err)
	}
	items, err := decodeSamples(raw)
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(items))
	for i, item := range items {
		s, err := item.normalize()
		if err != nil {
			return nil, fmt.Errorf("элемент %d: %w", i+1, err)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("набор пуст")
	}
	return out, nil
}

// decodeSamples выбирает разбор по первому значимому символу файла.
func decodeSamples(raw []byte) ([]rawSample, error) {
	trimmed := bytes.TrimLeft(raw, " \t\r\n\uFEFF")
	if len(trimmed) == 0 {
		return nil, errors.New("файл набора пуст")
	}
	if trimmed[0] == '[' {
		var arr []rawSample
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("набор не разобран как массив JSON: %w", err)
		}
		return arr, nil
	}
	return decodeJSONL(trimmed)
}

// decodeJSONL разбирает построчный JSON и указывает номер испорченной строки,
// чтобы не искать ошибку в наборе руками.
func decodeJSONL(raw []byte) ([]rawSample, error) {
	var out []rawSample
	for i, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var item rawSample
		if err := json.Unmarshal(line, &item); err != nil {
			return nil, fmt.Errorf("строка %d набора не разобрана: %w", i+1, err)
		}
		out = append(out, item)
	}
	return out, nil
}

// normalize приводит элемент набора к единому виду и проверяет разметку.
func (r rawSample) normalize() (Sample, error) {
	s := Sample{
		PayloadID: firstNonEmpty(r.PayloadID, r.ID),
		Category:  firstNonEmpty(r.Category, "без категории"),
		Text:      firstNonEmpty(r.Text, r.Payload),
	}
	if s.Text == "" {
		return Sample{}, errors.New("не задан текст")
	}
	if s.PayloadID == "" {
		return Sample{}, errors.New("не задан payload_id")
	}
	for i, rf := range r.markup() {
		f, err := rf.normalize(s.Text)
		if err != nil {
			return Sample{}, fmt.Errorf("фрагмент %d: %w", i+1, err)
		}
		s.Fragments = append(s.Fragments, f)
	}
	return s, nil
}

// markup выбирает первое непустое поле с разметкой фрагментов.
func (r rawSample) markup() []rawFragment {
	switch {
	case len(r.Spans) > 0:
		return r.Spans
	case len(r.Fragments) > 0:
		return r.Fragments
	default:
		return r.Entities
	}
}

// normalize восстанавливает границы фрагмента и его значение по тексту.
func (rf rawFragment) normalize(text string) (Fragment, error) {
	f := Fragment{Type: firstNonEmpty(rf.Type, rf.Label, "UNKNOWN")}
	value := firstNonEmpty(rf.Value, rf.Text)
	start, end, err := rf.bounds(text, value)
	if err != nil {
		return Fragment{}, err
	}
	f.Start, f.End = start, end
	f.Value = text[start:end]
	return f, nil
}

// bounds считает границы фрагмента: по смещениям, по смещению начала и длине
// значения либо поиском значения в тексте.
func (rf rawFragment) bounds(text, value string) (int, int, error) {
	switch {
	case rf.Start != nil && rf.End != nil:
		return checkBounds(*rf.Start, *rf.End, len(text))
	case rf.Start != nil && value != "":
		return checkBounds(*rf.Start, *rf.Start+len(value), len(text))
	case value != "":
		idx := strings.Index(text, value)
		if idx < 0 {
			return 0, 0, fmt.Errorf("значение %q не найдено в тексте", value)
		}
		return idx, idx + len(value), nil
	default:
		return 0, 0, errors.New("не заданы ни границы, ни значение")
	}
}

// checkBounds отбрасывает разметку, выходящую за пределы текста: такой
// фрагмент нельзя оценить, и молча подрезать его опаснее, чем упасть.
func checkBounds(start, end, size int) (int, int, error) {
	if start < 0 || end > size || end <= start {
		return 0, 0, fmt.Errorf("границы [%d,%d) не укладываются в текст длиной %d", start, end, size)
	}
	return start, end, nil
}

// firstNonEmpty возвращает первое непустое значение из перечисленных.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
