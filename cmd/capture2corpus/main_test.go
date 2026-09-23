package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadFiltersAndDedups: из файла берутся только записи маскирования с
// текстом, повторы одного текста отбрасываются. Обратный шаг ничего нового о
// текстах не говорит — на вход ему идёт наша же маска.
func TestReadFiltersAndDedups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.jsonl")
	lines := `{"direction":"mask","payload":"Иванов Иван","payload_len":21,"counts":{"FIO":1}}
{"direction":"mask","payload":"Иванов Иван","payload_len":21,"counts":{"FIO":1}}
{"direction":"demask","payload":"***********","payload_len":11}
{"direction":"mask_retry","payload":"Петров Пётр","payload_len":21,"counts":{"FIO":1}}
{"direction":"mask","payload":"","payload_len":0}
не json вовсе
{"direction":"mask","payload":"Заказ 44412","payload_len":11,"counts":{}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatalf("файл не записался: %v", err)
	}
	recs, err := read(path)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	want := []string{"Иванов Иван", "Петров Пётр", "Заказ 44412"}
	if len(recs) != len(want) {
		t.Fatalf("записей %d, ожидалось %d: %+v", len(recs), len(want), recs)
	}
	for i, w := range want {
		if recs[i].Payload != w {
			t.Errorf("запись %d: %q, ожидалось %q", i, recs[i].Payload, w)
		}
	}
}

// TestDraftSpansFindsTypes: черновик размечается тем же разбором, что в
// сервисе, и границы в нём байтовые — как в наборе.
func TestDraftSpansFindsTypes(t *testing.T) {
	text := "Клиент Иванов Иван, паспорт 4509 123456"
	spans := draftSpans(text)
	if len(spans) == 0 {
		t.Fatal("черновик пуст")
	}
	found := map[string]string{}
	for _, s := range spans {
		if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			t.Fatalf("границы за пределами текста: %+v", s)
		}
		found[s.Type] = text[s.Start:s.End]
	}
	if found["FIO"] != "Иванов Иван" {
		t.Errorf("имя размечено как %q", found["FIO"])
	}
	if found["PASSPORT"] != "4509 123456" {
		t.Errorf("паспорт размечен как %q", found["PASSPORT"])
	}
}

// TestWriteLeavesSpansEmpty: без явной просьбы метки пустые. Иначе набор
// оказался бы размечен нашими же решениями, и проверка на нём ничего бы не
// показала.
func TestWriteLeavesSpansEmpty(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "set.jsonl")
	recs := []captureRecord{{Direction: "mask", Payload: "Клиент Иванов Иван, паспорт 4509 123456"}}
	if _, err := write(out, recs, false); err != nil {
		t.Fatalf("запись: %v", err)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if !contains(string(body), `"spans":[]`) {
		t.Fatalf("метки не пустые: %s", body)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
