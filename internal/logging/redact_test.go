package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// lines возвращает строки вывода. Разбор в map, как в records, для проверки
// состава ключей не годится: повторный ключ он молча схлопывает, а именно
// повторный ключ здесь и проверяется.
func lines(t *testing.T, buf *syncBuffer) []string {
	t.Helper()
	out := make([]string, 0, 4)
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// keyCounts считает, сколько раз каждый ключ встречается на верхнем уровне
// записи. Заодно проверяет, что запись вообще разбирается как JSON.
func keyCounts(t *testing.T, line string) map[string]int {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(line))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("запись не разобрана: %v: %s", err, line)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("запись не объект: %s", line)
	}
	counts := make(map[string]int)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("ключ не прочитан: %v: %s", err, line)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("ключ не строка: %v: %s", tok, line)
		}
		var value any
		if err := dec.Decode(&value); err != nil {
			t.Fatalf("значение ключа %q не прочитано: %v: %s", key, err, line)
		}
		counts[key]++
	}
	return counts
}

// TestNoDuplicateFields — дефект первый. Место вызова передаёт system и
// request_id явно, те же значения лежат в контексте запроса. В записи должно
// остаться по одному ключу: JSON с двумя ключами одного имени разные читатели
// разбирают по-разному, а чаще не разбирают вовсе.
func TestNoDuplicateFields(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	ctx := WithRequestID(context.Background(), "req-1")
	SetSystem(ctx, "alfasonar")

	// Обычная запись: system и request_id переданы явно.
	l.Slog().LogAttrs(ctx, slog.LevelInfo, "обработан запрос",
		Event(EventProcess),
		slog.String(FieldSystem, "alfasonar"),
		slog.String(FieldRequestID, "req-1"),
		Took(360*time.Microsecond))

	// Запись аудита: имя системы передаётся местом вызова всегда, и оно же
	// есть в контексте. Именно так дефект воспроизводился живьём.
	l.Audit().Write(ctx, AuditEvent{
		Op: "mask", System: "alfasonar", Actor: ActorFromKey("k"),
		PayloadID: "payload-42", Bytes: 247, Result: "ok",
		Types: map[string]int{"PHONE": 1}, Duration: 360 * time.Microsecond,
	})

	// Запись с вычищенным значением: состав собирается вторым путём, через
	// сборку новой записи, и его тоже надо проверить.
	l.Slog().LogAttrs(ctx, slog.LevelWarn, "значение не разобрано",
		Event(EventProcess),
		slog.String(FieldSystem, "alfasonar"),
		slog.String("value", "Иванов Иван Иванович"),
		Took(0))

	got := lines(t, buf)
	if len(got) != 3 {
		t.Fatalf("ожидались три записи, получено %d: %s", len(got), buf.String())
	}
	for i, line := range got {
		counts := keyCounts(t, line)
		for _, key := range []string{FieldSystem, FieldRequestID, FieldEvent, FieldDuration} {
			if counts[key] != 1 {
				t.Errorf("запись %d: ключ %q встречается %d раз: %s", i, key, counts[key], line)
			}
		}
	}
}

// TestRequiredFieldsStillAdded проверяет обратную сторону починки: поле,
// которого место вызова не передало, обработчик по-прежнему дописывает.
func TestRequiredFieldsStillAdded(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	ctx := WithRequestID(context.Background(), "req-7")
	SetSystem(ctx, "crm")

	l.Slog().InfoContext(ctx, "обработан запрос", Event(EventProcess))

	recs := records(t, buf)
	if len(recs) != 1 {
		t.Fatalf("ожидалась одна запись, получено %d", len(recs))
	}
	rec := recs[0]
	if rec[FieldRequestID] != "req-7" || rec[FieldSystem] != "crm" {
		t.Errorf("поля из контекста не дописаны: %v", rec)
	}
	if _, ok := rec[FieldDuration]; !ok {
		t.Errorf("длительность не дописана: %v", rec)
	}
}

// checkPayloadIDRecord проверяет одну запись против политики. Отпечаток
// обязателен не сам по себе: по нему записи одного запроса всё ещё сходятся,
// а исходное значение по нему не восстановить.
func checkPayloadIDRecord(t *testing.T, i int, rec map[string]any, id string, literal bool) {
	t.Helper()
	got, _ := rec[FieldPayloadID].(string)
	switch {
	case literal && got != id:
		t.Errorf("запись %d: годный идентификатор изменён: %q", i, got)
	case !literal && got == id:
		t.Errorf("запись %d: идентификатор попал в журнал дословно: %q", i, got)
	case !literal && !strings.HasPrefix(got, idHashPrefix):
		t.Errorf("запись %d: идентификатор записан не отпечатком: %q", i, got)
	}
}

// checkPayloadIDCounters проверяет счётчики после одного случая. Замена
// идентификатора считается своим счётчиком: счётчик вычищенных значений
// означает ошибку в нашем коде, а непригодный идентификатор присылает клиент.
func checkPayloadIDCounters(t *testing.T, l *Logger, literal bool) {
	t.Helper()
	replaced, redactions := l.Stats().IDReplaced.Load(), l.Stats().Redactions.Load()
	if literal && replaced != 0 {
		t.Errorf("годный идентификатор посчитан заменённым: %d", replaced)
	}
	if !literal && replaced != 2 {
		t.Errorf("счётчик заменённых идентификаторов равен %d, ожидалось 2", replaced)
	}
	if redactions != 0 {
		t.Errorf("замена идентификатора попала в счётчик вычищенных значений: %d", redactions)
	}
}

// runPayloadIDCase проводит один идентификатор обоими путями записи. Пути
// разные, поэтому и проверяются оба: политику легко починить в одном месте и
// забыть о втором.
func runPayloadIDCase(t *testing.T, id string, literal bool) {
	t.Helper()
	l, buf := newTestLogger(t, DefaultConfig())

	// Путь первый: журнал аудита, уровень info, поле собирает пакет.
	l.Audit().Write(context.Background(), AuditEvent{
		Op: "mask", System: "crm", Result: "ok", PayloadID: id,
	})
	// Путь второй: обычная запись, поле собирает место вызова.
	l.Slog().LogAttrs(context.Background(), slog.LevelInfo, "обработан запрос",
		Event(EventProcess), slog.String(FieldPayloadID, id), Took(0))

	recs := records(t, buf)
	if len(recs) != 2 {
		t.Fatalf("ожидались две записи, получено %d", len(recs))
	}
	for i, rec := range recs {
		checkPayloadIDRecord(t, i, rec, id, literal)
	}
	if !literal && strings.Contains(buf.String(), id) {
		t.Fatalf("значение %q осталось в журнале: %s", id, buf.String())
	}
	checkPayloadIDCounters(t, l, literal)
}

// TestPayloadIDPolicy — дефект второй. Поле payload_id приходит от клиента.
// Идентификатор, похожий на номер карты, телефон или почту, в журнал
// дословно не попадает; обычный шестнадцатеричный идентификатор попадает.
func TestPayloadIDPolicy(t *testing.T) {
	const hexID = "4f3c2b1a9d8e7c6b5a4f3c2b1a9d8e7c"
	cases := []struct {
		name    string
		id      string
		literal bool
	}{
		{"номер карты", "4276380012345678", false},
		{"номер карты с разделителями", "4276-3800-1234-5678", false},
		{"телефон", "+79161234567", false},
		{"почта", "ivanov@mail.ru", false},
		{"СНИЛС", "112-233-445.95", false},
		{"обычный идентификатор", hexID, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runPayloadIDCase(t, c.id, c.literal)
		})
	}
}

// TestPayloadIDHashKeepsRecordsTogether проверяет, ради чего выбран
// отпечаток: записи одного запроса по-прежнему сходятся по идентификатору, а
// разные запросы по-прежнему различимы.
func TestPayloadIDHashKeepsRecordsTogether(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	for _, id := range []string{"4276380012345678", "4276380012345678", "4276380099999999"} {
		l.Audit().Write(context.Background(), AuditEvent{
			Op: "mask", System: "crm", Result: "ok", PayloadID: id,
		})
	}
	recs := records(t, buf)
	if len(recs) != 3 {
		t.Fatalf("ожидались три записи, получено %d", len(recs))
	}
	first, second, third := recs[0][FieldPayloadID], recs[1][FieldPayloadID], recs[2][FieldPayloadID]
	if first != second {
		t.Errorf("один и тот же идентификатор дал разные отпечатки: %v и %v", first, second)
	}
	if first == third {
		t.Errorf("разные идентификаторы дали один отпечаток: %v", first)
	}
}

// TestRequestIDPolicy проверяет, что идентификатор запроса из контекста
// проходит ту же политику: он тоже приходит от клиента, заголовком.
func TestRequestIDPolicy(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	ctx := WithRequestID(context.Background(), "4276380012345678")
	l.Slog().InfoContext(ctx, "обработан запрос", Event(EventProcess), Took(0))

	recs := records(t, buf)
	if len(recs) != 1 {
		t.Fatalf("ожидалась одна запись, получено %d", len(recs))
	}
	got, _ := recs[0][FieldRequestID].(string)
	if !strings.HasPrefix(got, idHashPrefix) {
		t.Fatalf("идентификатор запроса записан дословно: %q", got)
	}
}

// TestLogID проверяет политику идентификатора отдельно от журнала.
func TestLogID(t *testing.T) {
	keep := []string{
		"",
		"payload-42",
		"4f3c2b1a9d8e7c6b5a4f3c2b1a9d8e7c",
		"01HZX-9K",
		"trace:abc.def",
		"12345",
		"rt1",
	}
	for _, v := range keep {
		if got := LogID(v); got != v {
			t.Errorf("годный идентификатор %q изменён на %q", v, got)
		}
	}
	replace := []string{
		"4276380012345678",
		"4276 3800 1234 5678",
		"79161234567",
		"+7 (916) 123-45-67",
		"11223344595",
		"500100732259",
		"ivanov@mail.ru",
		"Иванов Иван Иванович",
		strings.Repeat("a", maxIDLen+1),
	}
	for _, v := range replace {
		got := LogID(v)
		if got == v {
			t.Errorf("подозрительный идентификатор %q записан дословно", v)
			continue
		}
		if !strings.HasPrefix(got, idHashPrefix) {
			t.Errorf("идентификатор %q заменён не отпечатком: %q", v, got)
		}
		// Политика применяется повторно там, где значение проходит и место
		// вызова, и обработчик журнала: второй раз она ничего не меняет.
		if again := LogID(got); again != got {
			t.Errorf("повторное применение политики изменило отпечаток: %q против %q", again, got)
		}
	}
}
