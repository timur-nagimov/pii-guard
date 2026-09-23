package store

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Проверка на этапе сборки: обе реализации и обёртка подходят под контракт.
var (
	_ Interface = (*MemoryStore)(nil)
	_ Interface = (*RedisStore)(nil)
	_ Interface = (*Store)(nil)
)

// fakeRedis — заглушка Redis с разбором команд. Нужна, чтобы проверить работу
// хранилища целиком: запись одной копией сервиса, чтение другой.
type fakeRedis struct {
	ln       net.Listener
	password string

	mu   sync.Mutex
	data map[string][]byte
	px   map[string]int64
	cmds []string
}

// startFakeRedis поднимает заглушку и останавливает её по окончании теста.
func startFakeRedis(t *testing.T, password string) *fakeRedis {
	t.Helper()
	ln, err := listenLocal()
	if err != nil {
		t.Fatalf("не удалось поднять заглушку Redis: %v", err)
	}
	f := &fakeRedis{ln: ln, password: password, data: map[string][]byte{}, px: map[string]int64{}}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

// addr возвращает адрес заглушки.
func (f *fakeRedis) addr() string { return f.ln.Addr().String() }

// serve принимает соединения.
func (f *fakeRedis) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

// handle обслуживает одно соединение: читает команды и отвечает на них.
func (f *fakeRedis) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	for {
		rep, err := readReply(br)
		if err != nil {
			return
		}
		args := make([]string, 0, len(rep.arr))
		for _, a := range rep.arr {
			args = append(args, a.text())
		}
		if len(args) == 0 {
			return
		}
		if _, err := conn.Write(f.exec(args)); err != nil {
			return
		}
	}
}

// exec выполняет команду и возвращает готовый ответ.
func (f *fakeRedis) exec(args []string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, strings.ToUpper(args[0]))
	switch strings.ToUpper(args[0]) {
	case "PING":
		return []byte("+PONG\r\n")
	case "AUTH":
		if len(args) > 1 && args[1] == f.password {
			return []byte("+OK\r\n")
		}
		return []byte("-ERR invalid password\r\n")
	case "SET":
		return f.set(args)
	case "GET":
		return f.get(args)
	case "DEL":
		delete(f.data, args[1])
		return []byte(":1\r\n")
	default:
		return []byte("-ERR unknown command\r\n")
	}
}

// set сохраняет значение и запоминает заданный срок жизни.
func (f *fakeRedis) set(args []string) []byte {
	if len(args) < 3 {
		return []byte("-ERR wrong number of arguments\r\n")
	}
	f.data[args[1]] = []byte(args[2])
	if len(args) >= 5 && strings.EqualFold(args[3], "PX") {
		ms, err := strconv.ParseInt(args[4], 10, 64)
		if err != nil {
			return []byte("-ERR value is not an integer\r\n")
		}
		f.px[args[1]] = ms
	}
	return []byte("+OK\r\n")
}

// get отдаёт значение или пустую объёмную строку.
func (f *fakeRedis) get(args []string) []byte {
	v, ok := f.data[args[1]]
	if !ok {
		return []byte("$-1\r\n")
	}
	out := []byte("$" + strconv.Itoa(len(v)) + "\r\n")
	out = append(out, v...)
	return append(out, '\r', '\n')
}

// value возвращает сохранённое значение по ключу.
func (f *fakeRedis) value(key string) ([]byte, int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	return v, f.px[key], ok
}

// put кладёт значение мимо сервиса: так готовится испорченная запись.
func (f *fakeRedis) put(key string, v []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = v
}

// keys возвращает список ключей заглушки.
func (f *fakeRedis) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.data))
	for k := range f.data {
		out = append(out, k)
	}
	return out
}

// newRedisStore собирает хранилище поверх заданного адреса.
func newRedisStore(t *testing.T, addr string, key []byte) *RedisStore {
	t.Helper()
	cfg := Config{Key: key, TTL: time.Hour, MaxRecords: 1000, Redis: testRedisConfig(addr)}
	cfg.Redis.Prefix = "pii-test:"
	s, err := NewRedis(cfg)
	if err != nil {
		t.Fatalf("не удалось создать общее хранилище: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestRedisSharedBetweenInstances проверяет главное ради чего затеяно общее
// хранилище: запись, созданная одной копией сервиса, читается другой.
func TestRedisSharedBetweenInstances(t *testing.T) {
	f := startFakeRedis(t, "")
	key := testKey(7)
	first := newRedisStore(t, f.addr(), key)
	second := newRedisStore(t, f.addr(), key)

	original := "Иванов Иван, паспорт 4509 123456"
	maskText := "****** ****, паспорт **** ******"
	spans := []SpanMeta{{Type: "FIO", Start: 0, End: 11}, {Type: "PASSPORT", Start: 21, End: 32}}
	if _, err := first.Put("id-1", original, maskText, "alfasonar", spans); err != nil {
		t.Fatalf("запись не удалась: %v", err)
	}

	got, err := second.Get("id-1")
	if err != nil {
		t.Fatalf("вторая копия сервиса не нашла запись первой: %v", err)
	}
	if got.Mask != maskText || got.System != "alfasonar" {
		t.Fatalf("запись прочитана с другими полями: %+v", got)
	}
	if got.OrigHash != HashOf(original) || got.MaskHash != HashOf(maskText) {
		t.Fatal("хеши не совпали, направление обработки определить нельзя")
	}
	if len(got.Spans) != 2 || got.Spans[1].Type != "PASSPORT" || got.Spans[1].End != 32 {
		t.Fatalf("метаданные фрагментов потеряны: %+v", got.Spans)
	}
	back, err := second.Original(got)
	if err != nil || back != original {
		t.Fatalf("обратное преобразование вернуло %q, ошибка %v", back, err)
	}
	if st := second.Stats(); st.Hits != 1 || !st.Available {
		t.Fatalf("счётчики чтения неверны: %+v", st)
	}
}

// TestRedisValueIsEncrypted проверяет, что Redis видит шифртекст. Это условие
// задачи: персональные данные не покидают сервис в открытом виде.
func TestRedisValueIsEncrypted(t *testing.T) {
	f := startFakeRedis(t, "")
	s := newRedisStore(t, f.addr(), testKey(11))
	original := "Петров Пётр, карта 4111 1111 1111 1111"
	if _, err := s.Put("id-2", original, "маска ****", "sys", []SpanMeta{{Type: "CARD"}}); err != nil {
		t.Fatalf("запись не удалась: %v", err)
	}
	raw, _, ok := f.value("pii-test:id-2")
	if !ok {
		t.Fatal("значение не дошло до Redis")
	}
	for _, part := range []string{original, "Петров", "4111", "маска", "CARD", "sys"} {
		if bytes.Contains(raw, []byte(part)) {
			t.Fatalf("кусок %q лежит в Redis открытым", part)
		}
	}
}

// TestRedisKeyPrefixAndTTL проверяет общий префикс ключей и срок жизни,
// заданный параметром PX.
func TestRedisKeyPrefixAndTTL(t *testing.T) {
	f := startFakeRedis(t, "")
	cfg := Config{Key: testKey(13), TTL: 90 * time.Second, Redis: testRedisConfig(f.addr())}
	cfg.Redis.Prefix = "своя-ветка:"
	s, err := NewRedis(cfg)
	if err != nil {
		t.Fatalf("не удалось создать хранилище: %v", err)
	}
	defer s.Close()
	if _, err := s.Put("id-3", "текст", "маска", "sys", nil); err != nil {
		t.Fatalf("запись не удалась: %v", err)
	}
	keys := f.keys()
	if len(keys) != 1 || keys[0] != "своя-ветка:id-3" {
		t.Fatalf("ключи в Redis %v, ожидался один с префиксом", keys)
	}
	if _, px, _ := f.value("своя-ветка:id-3"); px != 90_000 {
		t.Fatalf("срок жизни задан как %d мс, ожидалось 90000", px)
	}
}

// TestRedisAuth проверяет вход по паролю при открытии соединения.
func TestRedisAuth(t *testing.T) {
	f := startFakeRedis(t, "пароль")
	cfg := Config{Key: testKey(17), TTL: time.Hour, Redis: testRedisConfig(f.addr())}
	cfg.Redis.Password = "пароль"
	s, err := NewRedis(cfg)
	if err != nil {
		t.Fatalf("не удалось создать хранилище: %v", err)
	}
	defer s.Close()
	if err := s.Ping(); err != nil {
		t.Fatalf("проверка связи не прошла: %v", err)
	}
	if _, err := s.Put("id-4", "текст", "маска", "sys", nil); err != nil {
		t.Fatalf("запись не удалась: %v", err)
	}
	if st := s.Stats(); st.Degraded != 0 {
		t.Fatalf("вход по паролю привёл к деградации: %+v", st)
	}
}

// TestRedisWrongPassword проверяет, что отказ по паролю не роняет сервис:
// работа продолжается на запасном пути.
func TestRedisWrongPassword(t *testing.T) {
	f := startFakeRedis(t, "пароль")
	cfg := Config{Key: testKey(19), TTL: time.Hour, Redis: testRedisConfig(f.addr())}
	cfg.Redis.Password = "не тот"
	s, err := NewRedis(cfg)
	if err != nil {
		t.Fatalf("не удалось создать хранилище: %v", err)
	}
	defer s.Close()
	if _, err := s.Put("id-5", "текст", "маска", "sys", nil); err != nil {
		t.Fatalf("запись обязана пройти запасным путём, получена ошибка %v", err)
	}
	if _, err := s.Get("id-5"); err != nil {
		t.Fatalf("запись, ушедшая на запасной путь, не читается: %v", err)
	}
	if st := s.Stats(); st.Degraded == 0 || st.Failures == 0 {
		t.Fatalf("деградация не отмечена: %+v", st)
	}
}

// TestRedisFallbackWhenDown проверяет главное требование к отказу общего
// хранилища: сервис продолжает работать, ошибка наружу не уходит, а деградация
// видна в счётчиках.
func TestRedisFallbackWhenDown(t *testing.T) {
	var degraded int
	var mu sync.Mutex
	cfg := Config{
		Key:   testKey(23),
		TTL:   time.Hour,
		Redis: testRedisConfig(unusedAddr(t)),
		OnDegraded: func(string, error) {
			mu.Lock()
			degraded++
			mu.Unlock()
		},
	}
	s, err := NewRedis(cfg)
	if err != nil {
		t.Fatalf("не удалось создать хранилище: %v", err)
	}
	defer s.Close()

	original := "Сидоров Сидор, снилс 112-233-445 95"
	if _, err := s.Put("id-6", original, "маска", "sys", nil); err != nil {
		t.Fatalf("запись при недоступном Redis вернула ошибку: %v", err)
	}
	e, err := s.Get("id-6")
	if err != nil {
		t.Fatal("запись при недоступном Redis не читается")
	}
	back, err := s.Original(e)
	if err != nil || back != original {
		t.Fatalf("обратное преобразование вернуло %q, ошибка %v", back, err)
	}
	if s.Len() != 1 {
		t.Fatalf("на запасном пути %d записей, ожидалась одна", s.Len())
	}
	st := s.Stats()
	if st.Degraded < 2 || st.Available {
		t.Fatalf("счётчики деградации неверны: %+v", st)
	}
	mu.Lock()
	defer mu.Unlock()
	if degraded == 0 {
		t.Fatal("обработчик деградации не вызван")
	}
}

// TestRedisPauseAfterFailure проверяет паузу после сетевой ошибки: сервис не
// ходит в недоступный Redis на каждом запросе, иначе деградация стоила бы
// дороже самой обработки.
func TestRedisPauseAfterFailure(t *testing.T) {
	cfg := Config{Key: testKey(29), TTL: time.Hour, Redis: testRedisConfig(unusedAddr(t))}
	cfg.Redis.RetryAfter = 50 * time.Millisecond
	s, err := NewRedis(cfg)
	if err != nil {
		t.Fatalf("не удалось создать хранилище: %v", err)
	}
	defer s.Close()

	if _, err := s.Put("id-7", "текст", "маска", "sys", nil); err != nil {
		t.Fatal(err)
	}
	if s.available() {
		t.Fatal("после сетевой ошибки пауза не включилась")
	}
	started := time.Now()
	for i := 0; i < 50; i++ {
		// Здесь важен только факт вызова: проверяется, что во время паузы
		// чтение не уходит в Redis, а не результат чтения.
		_, _ = s.Get("id-7")
	}
	if elapsed := time.Since(started); elapsed > cfg.Redis.DialTimeout {
		t.Fatalf("во время паузы запросы шли в Redis: 50 чтений заняли %v", elapsed)
	}
	time.Sleep(60 * time.Millisecond)
	if !s.available() {
		t.Fatal("пауза не закончилась")
	}
}

// TestRedisBadValue проверяет чтение испорченного значения: запись не отдаётся
// как действующая, а сообщается как повреждённая, связь с Redis при этом не
// обрывается.
func TestRedisBadValue(t *testing.T) {
	f := startFakeRedis(t, "")
	s := newRedisStore(t, f.addr(), testKey(31))
	f.put("pii-test:id-8", []byte("мусор, который не расшифровать"))
	if _, err := s.Get("id-8"); !errors.Is(err, ErrCorrupted) {
		t.Fatalf("испорченное значение дало ошибку %v, ожидалась %v", err, ErrCorrupted)
	}
	if !s.available() {
		t.Fatal("испорченное значение оборвало работу с Redis, хотя он жив")
	}
}

// TestRedisForeignKey проверяет, что запись, зашифрованную другим ключом,
// прочитать нельзя: подмена ключа шифрования обнаруживается.
func TestRedisForeignKey(t *testing.T) {
	f := startFakeRedis(t, "")
	first := newRedisStore(t, f.addr(), testKey(37))
	second := newRedisStore(t, f.addr(), testKey(200))
	if _, err := first.Put("id-9", "секрет", "маска", "sys", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Get("id-9"); !errors.Is(err, ErrCorrupted) {
		t.Fatalf("чужой ключ шифрования дал ошибку %v, ожидалась %v", err, ErrCorrupted)
	}
}

// TestRedisDelete проверяет удаление записи из общего хранилища.
func TestRedisDelete(t *testing.T) {
	f := startFakeRedis(t, "")
	s := newRedisStore(t, f.addr(), testKey(41))
	if _, err := s.Put("id-10", "текст", "маска", "sys", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("id-10"); err != nil {
		t.Fatalf("удаление вернуло ошибку: %v", err)
	}
	if _, err := s.Get("id-10"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("запись пережила удаление: %v", err)
	}
	if keys := f.keys(); len(keys) != 0 {
		t.Fatalf("в Redis остались ключи %v", keys)
	}
}

// TestRedisExpired проверяет, что просроченная запись не отдаётся, даже если
// Redis ещё не успел её убрать.
func TestRedisExpired(t *testing.T) {
	f := startFakeRedis(t, "")
	cfg := Config{Key: testKey(43), TTL: time.Millisecond, Redis: testRedisConfig(f.addr())}
	cfg.Redis.Prefix = "pii-test:"
	s, err := NewRedis(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Put("id-11", "текст", "маска", "sys", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := s.Get("id-11"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("просроченная запись отдана сервису: %v", err)
	}
}

// TestRedisConcurrent проверяет одновременную работу многих запросов через
// общий пул соединений. Тест обязан проходить с ключом проверки гонок.
func TestRedisConcurrent(t *testing.T) {
	f := startFakeRedis(t, "")
	s := newRedisStore(t, f.addr(), testKey(47))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				id := "id-" + strconv.Itoa(worker) + "-" + strconv.Itoa(j)
				if _, err := s.Put(id, "текст "+id, "маска", "sys", nil); err != nil {
					t.Errorf("запись %s не удалась: %v", id, err)
					return
				}
				e, err := s.Get(id)
				if err != nil {
					t.Errorf("запись %s не найдена: %v", id, err)
					return
				}
				if back, err := s.Original(e); err != nil || back != "текст "+id {
					t.Errorf("запись %s прочитана как %q, ошибка %v", id, back, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if st := s.Stats(); st.Degraded != 0 {
		t.Fatalf("под нагрузкой началась деградация: %+v", st)
	}
}

// TestStoreChoosesBackend проверяет выбор реализации по настройкам.
func TestStoreChoosesBackend(t *testing.T) {
	mem, err := New(Config{Key: testKey(53), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	if mem.Shared() {
		t.Fatal("без адреса Redis хранилище считает себя общим")
	}
	if _, ok := mem.Backend().(*MemoryStore); !ok {
		t.Fatalf("выбрана реализация %T, ожидалась память", mem.Backend())
	}
	if mem.Stats() != (Stats{}) {
		t.Fatalf("для памяти счётчики общего хранилища не пусты: %+v", mem.Stats())
	}

	f := startFakeRedis(t, "")
	shared, err := New(Config{Key: testKey(53), TTL: time.Hour, Redis: testRedisConfig(f.addr())})
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	if !shared.Shared() {
		t.Fatal("с адресом Redis хранилище не считает себя общим")
	}
	if _, err := shared.Put("id", "текст", "маска", "sys", nil); err != nil {
		t.Fatal(err)
	}
	e, err := shared.Get("id")
	if err != nil {
		t.Fatal("запись не найдена через обёртку")
	}
	if back, err := shared.Original(e); err != nil || back != "текст" {
		t.Fatalf("обёртка вернула %q, ошибка %v", back, err)
	}
	if shared.Len() != 0 {
		t.Fatalf("на запасном пути %d записей, ожидалось ноль", shared.Len())
	}
	if st := shared.Stats(); st.Puts != 1 {
		t.Fatalf("счётчики обёртки неверны: %+v", st)
	}
}

// TestRedisConfigDefaults проверяет подстановку значений по умолчанию.
func TestRedisConfigDefaults(t *testing.T) {
	got := RedisConfig{Addr: "localhost:6379"}.withDefaults()
	def := DefaultRedisConfig()
	if got.Prefix != def.Prefix || got.PoolSize != def.PoolSize {
		t.Fatalf("префикс и размер пула не подставлены: %+v", got)
	}
	if got.DialTimeout != def.DialTimeout || got.ReadTimeout != def.ReadTimeout ||
		got.WriteTimeout != def.WriteTimeout || got.RetryAfter != def.RetryAfter {
		t.Fatalf("таймауты не подставлены: %+v", got)
	}
	own := RedisConfig{Addr: "a:1", Prefix: "x:", PoolSize: 3, DialTimeout: time.Second,
		ReadTimeout: 2 * time.Second, WriteTimeout: 3 * time.Second, RetryAfter: 4 * time.Second}
	if own.withDefaults() != own {
		t.Fatalf("заданные значения перебиты умолчаниями: %+v", own.withDefaults())
	}
}

// TestRedisRealServer проверяет работу против настоящего Redis. Без адреса в
// переменной окружения PII_REDIS_ADDR тест пропускается.
func TestRedisRealServer(t *testing.T) {
	addr := os.Getenv("PII_REDIS_ADDR")
	if addr == "" {
		t.Skip("PII_REDIS_ADDR не задан, настоящий Redis не проверяем")
	}
	cfg := Config{Key: testKey(59), TTL: time.Minute, Redis: DefaultRedisConfig()}
	cfg.Redis.Addr = addr
	cfg.Redis.Password = os.Getenv("PII_REDIS_PASSWORD")
	cfg.Redis.Prefix = "pii-test:"
	s, err := NewRedis(cfg)
	if err != nil {
		t.Fatalf("не удалось создать хранилище: %v", err)
	}
	defer s.Close()
	if err := s.Ping(); err != nil {
		t.Fatalf("Redis по адресу %s не отвечает: %v", addr, err)
	}
	id := "real-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	original := "Иванов Иван, телефон +7 999 123-45-67"
	if _, err := s.Put(id, original, "маска", "sys", []SpanMeta{{Type: "PHONE", Start: 13, End: 30}}); err != nil {
		t.Fatalf("запись не удалась: %v", err)
	}
	otherCfg := cfg
	other, err := NewRedis(otherCfg)
	if err != nil {
		t.Fatalf("не удалось создать вторую копию хранилища: %v", err)
	}
	defer other.Close()
	e, err := other.Get(id)
	if err != nil {
		t.Fatal("вторая копия сервиса не нашла запись в настоящем Redis")
	}
	if back, err := other.Original(e); err != nil || back != original {
		t.Fatalf("обратное преобразование вернуло %q, ошибка %v", back, err)
	}
	if err := s.Delete(id); err != nil {
		t.Fatalf("удаление вернуло ошибку: %v", err)
	}
	if st := s.Stats(); st.Degraded != 0 {
		t.Fatalf("работа с настоящим Redis шла с деградацией: %+v", st)
	}
}

// TestRedisSetDegradedHook проверяет, что обработчик деградации можно задать
// после создания хранилища и снять обратно.
func TestRedisSetDegradedHook(t *testing.T) {
	s, err := New(Config{Key: testKey(61), TTL: time.Hour, Redis: testRedisConfig(unusedAddr(t))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var mu sync.Mutex
	var ops []string
	s.SetDegradedHook(func(op string, _ error) {
		mu.Lock()
		ops = append(ops, op)
		mu.Unlock()
	})
	if _, err := s.Put("id", "текст", "маска", "sys", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := len(ops)
	mu.Unlock()
	if got == 0 {
		t.Fatal("обработчик деградации не вызван")
	}

	s.SetDegradedHook(nil)
	// Здесь важен только факт вызова: проверяется, что снятый обработчик
	// больше не вызывается, а не результат чтения.
	_, _ = s.Get("id")
	mu.Lock()
	defer mu.Unlock()
	if len(ops) != got {
		t.Fatalf("снятый обработчик всё ещё вызывается: %v", ops)
	}
}

// TestMemoryStoreHookIsNoop проверяет, что обработчик деградации для памяти
// просто ничего не делает.
func TestMemoryStoreHookIsNoop(t *testing.T) {
	s, err := New(Config{Key: testKey(67), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetDegradedHook(func(string, error) {
		t.Error("память не должна сообщать о деградации")
	})
	if _, err := s.Put("id", "текст", "маска", "sys", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("id"); err != nil {
		t.Fatalf("запись не найдена: %v", err)
	}
}
