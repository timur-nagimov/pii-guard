package store

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// testKey возвращает ключ шифрования нужной длины. Значение произвольное:
// тесты проверяют механику, а не стойкость конкретного ключа.
func testKey(fill byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill + byte(i)
	}
	return key
}

// newTestStore создаёт хранилище в памяти и закрывает его по окончании теста,
// чтобы уборщик просроченных записей не пережил тест.
func newTestStore(t *testing.T, cfg Config) *MemoryStore {
	t.Helper()
	if cfg.Key == nil {
		cfg.Key = testKey(1)
	}
	s, err := NewMemory(cfg)
	if err != nil {
		t.Fatalf("не удалось создать хранилище: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestPutAndGet проверяет запись и чтение соответствия вместе с хешами,
// по которым сервис отличает повтор маскирования от обратного преобразования.
func TestPutAndGet(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	original := "Иванов Иван, паспорт 4509 123456"
	maskText := "****** ****, паспорт **** ******"
	spans := []SpanMeta{{Type: "FIO", Start: 0, End: 11}, {Type: "PASSPORT", Start: 21, End: 32}}

	put, err := s.Put("id-1", original, maskText, "alfasonar", spans)
	if err != nil {
		t.Fatalf("Put вернула ошибку: %v", err)
	}
	if put.Mask != maskText || put.System != "alfasonar" {
		t.Fatalf("запись сохранена с другими полями: %+v", put)
	}

	got, err := s.Get("id-1")
	if err != nil {
		t.Fatalf("запись не найдена сразу после сохранения: %v", err)
	}
	if got.Mask != maskText {
		t.Errorf("маска %q, ожидалась %q", got.Mask, maskText)
	}
	if got.OrigHash != HashOf(original) {
		t.Error("хеш исходного текста не совпал")
	}
	if got.MaskHash != HashOf(maskText) {
		t.Error("хеш маски не совпал")
	}
	if len(got.Spans) != 2 || got.Spans[0].Type != "FIO" {
		t.Errorf("метаданные фрагментов потеряны: %+v", got.Spans)
	}

	decrypted, err := s.Original(got)
	if err != nil {
		t.Fatalf("Original вернула ошибку: %v", err)
	}
	if decrypted != original {
		t.Fatalf("расшифровано %q, ожидалось %q", decrypted, original)
	}
	if n := s.Len(); n != 1 {
		t.Fatalf("в хранилище %d записей, ожидалась одна", n)
	}
}

// TestGetMissing проверяет чтение по неизвестному идентификатору.
func TestGetMissing(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	if _, err := s.Get("нет такого"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("получена ошибка %v, ожидалась %v", err, ErrNotFound)
	}
	if _, err := s.Get(""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("получена ошибка %v, ожидалась %v", err, ErrNotFound)
	}
}

// TestPutOverwrite проверяет, что повторная запись по тому же идентификатору
// заменяет значение и не удваивает счётчик записей.
func TestPutOverwrite(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	if _, err := s.Put("id", "первый", "******", "sys", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("id", "второй", "******", "sys", nil); err != nil {
		t.Fatal(err)
	}
	if n := s.Len(); n != 1 {
		t.Fatalf("счётчик записей %d, ожидалась одна запись", n)
	}
	e, err := s.Get("id")
	if err != nil {
		t.Fatal("запись пропала после перезаписи")
	}
	text, err := s.Original(e)
	if err != nil || text != "второй" {
		t.Fatalf("прочитано %q (ошибка %v), ожидалось %q", text, err, "второй")
	}
}

// TestOriginalIsEncrypted проверяет, что исходный текст не встречается в
// сериализованном представлении записи. Это главное требование к хранилищу:
// персональные данные лежат только зашифрованными.
func TestOriginalIsEncrypted(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	original := "Иванов Иван Иванович, карта 4111 1111 1111 1111"
	e, err := s.Put("id", original, "маска", "sys", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(e.origCT, []byte(original)) {
		t.Fatal("исходный текст лежит в записи открытым")
	}
	for _, part := range []string{"Иванов", "Иванович", "4111", "1111 1111"} {
		if bytes.Contains(e.origCT, []byte(part)) {
			t.Fatalf("кусок %q исходного текста лежит в записи открытым", part)
		}
	}
	dump := fmt.Sprintf("%#v", *e)
	if strings.Contains(dump, original) || strings.Contains(dump, "Иванов") {
		t.Fatalf("исходный текст попал в сериализованное представление записи: %s", dump)
	}
	if len(e.origCT) <= len(original) {
		t.Fatal("шифротекст короче исходного текста, метка целостности отсутствует")
	}
}

// TestOriginalDifferentKey проверяет, что запись, зашифрованную одним ключом,
// нельзя прочитать другим: подмена ключа обнаруживается, а не отдаёт мусор.
func TestOriginalDifferentKey(t *testing.T) {
	first := newTestStore(t, Config{Key: testKey(1), TTL: time.Hour})
	second := newTestStore(t, Config{Key: testKey(200), TTL: time.Hour})
	e, err := first.Put("id", "секрет", "маска", "sys", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Original(e); err != ErrCorrupted {
		t.Fatalf("получена ошибка %v, ожидалась %v", err, ErrCorrupted)
	}
}

// TestOriginalBroken проверяет чтение испорченной или отсутствующей записи.
func TestOriginalBroken(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	if _, err := s.Original(nil); err != ErrNotFound {
		t.Errorf("для пустой записи получена ошибка %v, ожидалась %v", err, ErrNotFound)
	}
	if _, err := s.Original(&Entry{}); err != ErrNotFound {
		t.Errorf("для записи без шифротекста получена ошибка %v, ожидалась %v", err, ErrNotFound)
	}
	e, err := s.Put("id", "текст", "маска", "sys", nil)
	if err != nil {
		t.Fatal(err)
	}
	e.origCT[len(e.origCT)-1] ^= 0xFF
	if _, err := s.Original(e); err != ErrCorrupted {
		t.Errorf("испорченный шифротекст дал ошибку %v, ожидалась %v", err, ErrCorrupted)
	}
}

// TestNewKeySize проверяет требования к ключу шифрования.
func TestNewKeySize(t *testing.T) {
	cases := []struct {
		name    string
		key     []byte
		wantErr error
	}{
		{"пустой ключ заменяется случайным", nil, nil},
		{"ключ нулевой длины заменяется случайным", []byte{}, nil},
		{"ключ нужной длины", make([]byte, 32), nil},
		{"короткий ключ", make([]byte, 16), ErrKeySize},
		{"длинный ключ", make([]byte, 64), ErrKeySize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := New(Config{Key: c.key, TTL: time.Hour})
			if err != c.wantErr {
				t.Fatalf("получена ошибка %v, ожидалась %v", err, c.wantErr)
			}
			if err == nil {
				defer s.Close()
				if _, putErr := s.Put("id", "текст", "маска", "sys", nil); putErr != nil {
					t.Fatalf("запись не удалась: %v", putErr)
				}
			}
		})
	}
}

// TestTTLExpired проверяет срок жизни записи: просроченная запись не отдаётся,
// даже если она ещё лежит в памяти до ближайшей уборки.
func TestTTLExpired(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	e, err := s.Put("id", "текст", "маска", "sys", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("id"); err != nil {
		t.Fatal("свежая запись не найдена")
	}
	// Срок сдвигается в прошлое вручную: ждать настоящего истечения в тесте
	// нельзя, а время наступления события важно задать точно.
	e.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := s.Get("id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("просроченная запись отдана из хранилища: %v", err)
	}
}

// TestSweepRemovesExpired проверяет, что уборка освобождает память и счётчик.
func TestSweepRemovesExpired(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	for i := 0; i < 5; i++ {
		e, err := s.Put(fmt.Sprintf("id-%d", i), "текст", "маска", "sys", nil)
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			e.ExpiresAt = time.Now().Add(-time.Minute)
		}
	}
	s.sweep()
	if n := s.Len(); n != 2 {
		t.Fatalf("после уборки осталось %d записей, ожидалось 2", n)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Get(fmt.Sprintf("id-%d", i)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("просроченная запись id-%d пережила уборку: %v", i, err)
		}
	}
	for i := 3; i < 5; i++ {
		if _, err := s.Get(fmt.Sprintf("id-%d", i)); err != nil {
			t.Fatalf("действующая запись id-%d удалена уборкой: %v", i, err)
		}
	}
}

// TestTTLDefault проверяет, что неположительный срок жизни заменяется часом.
func TestTTLDefault(t *testing.T) {
	s := newTestStore(t, Config{TTL: 0})
	e, err := s.Put("id", "текст", "маска", "sys", nil)
	if err != nil {
		t.Fatal(err)
	}
	left := time.Until(e.ExpiresAt)
	if left < 55*time.Minute || left > time.Hour {
		t.Fatalf("срок жизни по умолчанию %v, ожидался около часа", left)
	}
}

// TestMaxRecords проверяет предел числа записей. Предел защищает сервис от
// исчерпания памяти, поэтому важно, что он соблюдается при любом числе записей.
func TestMaxRecords(t *testing.T) {
	const limit = 10
	s := newTestStore(t, Config{TTL: time.Hour, MaxRecords: limit})
	for i := 0; i < 200; i++ {
		if _, err := s.Put(fmt.Sprintf("id-%d", i), "текст", "маска", "sys", nil); err != nil {
			t.Fatal(err)
		}
		if n := s.Len(); n > limit {
			t.Fatalf("после %d записей в хранилище %d записей, предел %d", i+1, n, limit)
		}
	}
	// Последняя запись обязана быть доступна: вытесняются самые ранние.
	if _, err := s.Get("id-199"); err != nil {
		t.Fatalf("последняя записанная пара вытеснена: %v", err)
	}
}

// TestMaxRecordsDefault проверяет, что нулевой предел заменяется значением по
// умолчанию и не вытесняет записи сразу же.
func TestMaxRecordsDefault(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour, MaxRecords: 0})
	for i := 0; i < 50; i++ {
		if _, err := s.Put(fmt.Sprintf("id-%d", i), "текст", "маска", "sys", nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.Len(); n != 50 {
		t.Fatalf("в хранилище %d записей, ожидалось 50", n)
	}
}

// putAndVerify сохраняет запись и тут же читает её обратно. Расхождение
// возвращается ошибкой, а не валит тест на месте: функция работает внутри
// рабочей горутины, а звать оттуда t.Fatalf нельзя.
func putAndVerify(s *MemoryStore, id string) error {
	original := fmt.Sprintf("текст %s", id)
	if _, err := s.Put(id, original, "маска "+id, "sys", nil); err != nil {
		return err
	}
	e, err := s.Get(id)
	if err != nil {
		return fmt.Errorf("запись %s пропала сразу после сохранения: %w", id, err)
	}
	got, err := s.Original(e)
	if err != nil {
		return err
	}
	if got != original {
		return fmt.Errorf("запись %s расшифрована как %q", id, got)
	}
	return nil
}

// readBack читает запись, если она уже сохранена. ErrNotFound ошибкой не
// считается: читатель ходит по тем же идентификаторам и законно опережает
// писателя, а вот частично записанное значение обязано выдать себя ошибкой
// расшифровки. Длина опрашивается следом, потому что счётчик живёт под тем же
// замком и ловит гонку не хуже самого чтения.
func readBack(s *MemoryStore, id string) error {
	if e, err := s.Get(id); err == nil {
		if _, err := s.Original(e); err != nil {
			return err
		}
	}
	s.Len()
	return nil
}

// spawnWorkers поднимает n горутин, каждая прогоняет step по своим per
// идентификаторам. Все ждут общего сигнала start: без него горутины успевают
// отработать по очереди и гонка не воспроизводится. Первая ошибка
// останавливает свою горутину — дальше пошли бы сообщения про ту же гонку.
func spawnWorkers(wg *sync.WaitGroup, start <-chan struct{}, errs chan<- error, n, per int, step func(id string) error) {
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < per; i++ {
				if err := step(fmt.Sprintf("w%d-%d", w, i)); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
}

// TestConcurrentPutGet проверяет одновременную запись и чтение. Хранилище
// обслуживает все запросы сервиса сразу, поэтому тест обязан проходить с
// ключом проверки гонок.
func TestConcurrentPutGet(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour, MaxRecords: 100000})
	const writers = 8
	const perWriter = 200

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, writers*2)

	spawnWorkers(&wg, start, errs, writers, perWriter, func(id string) error {
		return putAndVerify(s, id)
	})
	// Читатели ходят по тем же идентификаторам и не должны ни падать, ни
	// видеть частично записанные значения.
	spawnWorkers(&wg, start, errs, writers, perWriter, func(id string) error {
		return readBack(s, id)
	})

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("одновременная работа дала ошибку: %v", err)
	}
	if n := s.Len(); n != writers*perWriter {
		t.Fatalf("сохранено %d записей, ожидалось %d", n, writers*perWriter)
	}
}

// TestConcurrentSweep проверяет, что уборка просроченных записей может идти
// одновременно с записью и чтением.
func TestConcurrentSweep(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	var wg sync.WaitGroup
	start := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 500; i++ {
			if _, err := s.Put(fmt.Sprintf("id-%d", i), "текст", "маска", "sys", nil); err != nil {
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 50; i++ {
			s.sweep()
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 500; i++ {
			// Здесь важен только факт вызова: проверяется, что чтение не
			// конфликтует с вытеснением записей, а не результат чтения.
			_, _ = s.Get(fmt.Sprintf("id-%d", i))
			s.Len()
		}
	}()

	close(start)
	wg.Wait()
}

// TestCloseTwice проверяет, что повторное закрытие не приводит к панике:
// сервис закрывает хранилище и при завершении работы, и при ошибке настроек.
func TestCloseTwice(t *testing.T) {
	s, err := New(Config{Key: testKey(3), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Close()
}

// TestHashOf проверяет, что хеш считается одинаково внутри записи и снаружи:
// по нему сервис определяет направление обработки запроса.
func TestHashOf(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	e, err := s.Put("id", "исходный текст", "маска", "sys", nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.OrigHash != HashOf("исходный текст") {
		t.Error("хеш исходного текста считается по-разному")
	}
	if e.MaskHash != HashOf("маска") {
		t.Error("хеш маски считается по-разному")
	}
	if HashOf("а") == HashOf("б") {
		t.Error("разные строки дали одинаковый хеш")
	}
}

// TestMemoryDelete проверяет удаление записи из памяти вместе со счётчиком.
func TestMemoryDelete(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	if _, err := s.Put("id-1", "текст", "маска", "sys", nil); err != nil {
		t.Fatal(err)
	}
	s.Delete("id-1")
	if _, err := s.Get("id-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("запись пережила удаление: %v", err)
	}
	if n := s.Len(); n != 0 {
		t.Fatalf("после удаления в хранилище %d записей, ожидалось 0", n)
	}
	s.Delete("id-1")
	s.Delete("неизвестный")
	if n := s.Len(); n != 0 {
		t.Fatalf("удаление отсутствующей записи изменило счётчик: %d", n)
	}
}

// TestMemoryPutOverwriteKeepsCount проверяет, что повторная запись по тому же
// идентификатору не удваивает счётчик.
func TestMemoryPutOverwriteKeepsCount(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	for i := 0; i < 5; i++ {
		if _, err := s.Put("id", "текст", "маска", "sys", nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.Len(); n != 1 {
		t.Fatalf("в хранилище %d записей, ожидалась одна", n)
	}
}

// TestMemorySpansPreserved проверяет, что метаданные фрагментов возвращаются
// как есть: по ним сервис считает число найденных типов.
func TestMemorySpansPreserved(t *testing.T) {
	s := newTestStore(t, Config{TTL: time.Hour})
	spans := []SpanMeta{{Type: "FIO", Start: 0, End: 5}, {Type: "FIO", Start: 7, End: 9}}
	if _, err := s.Put("id", "текст", "маска", "sys", spans); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get("id")
	if err != nil {
		t.Fatal("запись не найдена")
	}
	if len(e.Spans) != 2 || e.Spans[0].Type != "FIO" || e.Spans[1].End != 9 {
		t.Fatalf("метаданные фрагментов искажены: %+v", e.Spans)
	}
}
