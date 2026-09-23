package store

import (
	"errors"
	"strconv"
	"sync/atomic"
	"time"
)

// Значения по умолчанию для общего хранилища.
const (
	defaultPoolSize     = 64
	defaultDialTimeout  = 300 * time.Millisecond
	defaultReadTimeout  = 300 * time.Millisecond
	defaultWriteTimeout = 300 * time.Millisecond
	defaultPrefix       = "pii:"
	// defaultRetryAfter — пауза, на которую сервис перестаёт ходить в Redis
	// после сетевой ошибки. Без паузы каждый запрос ждал бы таймаут
	// соединения, и деградация стоила бы дороже самой обработки.
	defaultRetryAfter = time.Second
	// replyPong — ответ Redis на PING. Проверяется дословно: подменённый или
	// чужой сервис на том же порту отвечает иначе, и лучше считать его
	// недоступным, чем складывать записи неизвестно куда.
	replyPong = "PONG"
)

// RedisConfig — параметры общего хранилища поверх Redis. Поля размечены для
// файла настроек: читать их будет пакет настроек, сам store файлов не знает.
type RedisConfig struct {
	// Addr — адрес вида host:port. Пустое значение выключает общее хранилище.
	Addr string `yaml:"addr"`
	// Password — пароль для команды AUTH. Пустой означает вход без пароля.
	// Значение приходит из окружения и в журнал не попадает.
	Password string `yaml:"password"` //nolint:gosec // это поле пароля, а не зашитый пароль
	// Prefix — общий префикс ключей.
	Prefix string `yaml:"prefix"`
	// PoolSize — число одновременных соединений.
	PoolSize int `yaml:"pool_size"`
	// DialTimeout — предел ожидания соединения и свободного места в пуле.
	DialTimeout time.Duration `yaml:"dial_timeout"`
	// ReadTimeout — предел ожидания ответа.
	ReadTimeout time.Duration `yaml:"read_timeout"`
	// WriteTimeout — предел отправки команды.
	WriteTimeout time.Duration `yaml:"write_timeout"`
	// RetryAfter — пауза перед новой попыткой после сетевой ошибки.
	RetryAfter time.Duration `yaml:"retry_after"`
}

// DefaultRedisConfig возвращает параметры по умолчанию без адреса: адрес
// задаёт тот, кто включает общее хранилище.
func DefaultRedisConfig() RedisConfig {
	return RedisConfig{
		Prefix:       defaultPrefix,
		PoolSize:     defaultPoolSize,
		DialTimeout:  defaultDialTimeout,
		ReadTimeout:  defaultReadTimeout,
		WriteTimeout: defaultWriteTimeout,
		RetryAfter:   defaultRetryAfter,
	}
}

// withDefaults подставляет значения по умолчанию вместо пустых полей.
func (c RedisConfig) withDefaults() RedisConfig {
	def := DefaultRedisConfig()
	if c.Prefix == "" {
		c.Prefix = def.Prefix
	}
	if c.PoolSize <= 0 {
		c.PoolSize = def.PoolSize
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = def.DialTimeout
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = def.ReadTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = def.WriteTimeout
	}
	if c.RetryAfter <= 0 {
		c.RetryAfter = def.RetryAfter
	}
	return c
}

// Stats — счётчики работы с общим хранилищем.
type Stats struct {
	// Degraded — число обращений, обслуженных запасным путём.
	Degraded uint64
	// Failures — число ошибок обращения к Redis.
	Failures uint64
	// Puts — число записей, сохранённых в Redis.
	Puts uint64
	// Hits — число записей, прочитанных из Redis.
	Hits uint64
	// Misses — число чтений, не нашедших запись в Redis.
	Misses uint64
	// Available — доступен ли Redis прямо сейчас.
	Available bool
	// Fallback — число записей, лежащих на запасном пути.
	Fallback int
}

// RedisStore — общее хранилище соответствий поверх Redis. Все копии сервиса
// видят одни и те же записи, поэтому обратное преобразование выполняет любая
// из них. Значение уходит в Redis зашифрованным: ключ шифрования остаётся
// только у сервиса.
//
// При недоступности Redis сервис не отвечает ошибкой, а переходит на запасной
// путь в памяти и отмечает деградацию счётчиком.
type RedisStore struct {
	pool     *respPool
	codec    *codec
	fallback *MemoryStore
	ttl      time.Duration
	prefix   string
	retry    time.Duration

	onDegraded atomic.Pointer[degradedHook]

	downUntil atomic.Int64
	degraded  atomic.Uint64
	failures  atomic.Uint64
	puts      atomic.Uint64
	hits      atomic.Uint64
	misses    atomic.Uint64
}

// NewRedis создаёт общее хранилище. Соединение здесь не проверяется: сервис
// обязан подняться и в отсутствие Redis, а недоступность видна в счётчиках.
func NewRedis(cfg Config) (*RedisStore, error) {
	c, err := newCodec(cfg.Key)
	if err != nil {
		return nil, err
	}
	rcfg := cfg.Redis.withDefaults()
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	s := &RedisStore{
		pool:     newRespPool(&rcfg),
		codec:    c,
		fallback: newMemoryWithCodec(c, &cfg),
		ttl:      ttl,
		prefix:   rcfg.Prefix,
		retry:    rcfg.RetryAfter,
	}
	s.SetDegradedHook(cfg.OnDegraded)
	return s, nil
}

// degradedHook — обработчик перехода на запасной путь.
type degradedHook func(op string, err error)

// SetDegradedHook задаёт обработчик деградации. Вызывается при запуске, когда
// набор показателей уже собран, поэтому порядок создания сервиса не важен.
func (r *RedisStore) SetDegradedHook(fn func(op string, err error)) {
	if fn == nil {
		r.onDegraded.Store(nil)
		return
	}
	h := degradedHook(fn)
	r.onDegraded.Store(&h)
}

// notifyDegraded зовёт обработчик деградации, если он задан.
func (r *RedisStore) notifyDegraded(op string, err error) {
	if h := r.onDegraded.Load(); h != nil {
		(*h)(op, err)
	}
}

// Close закрывает соединения и запасное хранилище.
func (r *RedisStore) Close() {
	r.pool.Close()
	r.fallback.Close()
}

// key собирает ключ Redis с общим префиксом.
func (r *RedisStore) key(id string) string { return r.prefix + id }

// Put сохраняет соответствие в общем хранилище, а при его недоступности в
// памяти процесса. Ошибку наружу не отдаёт: потеря общего хранилища не должна
// превращаться в ошибку ответа.
func (r *RedisStore) Put(id, original, maskText, system string, spans []SpanMeta) (*Entry, error) {
	e, err := r.codec.newEntry(original, maskText, system, spans, time.Now().Add(r.ttl))
	if err != nil {
		return nil, err
	}
	if !r.available() {
		r.markDegraded("put", nil)
		r.fallback.store(id, e)
		return e, nil
	}
	if err := r.put(id, e); err != nil {
		r.markDegraded("put", err)
		r.fallback.store(id, e)
		return e, nil
	}
	r.puts.Add(1)
	return e, nil
}

// put выполняет сохранение командой SET со сроком жизни в миллисекундах.
func (r *RedisStore) put(id string, e *Entry) error {
	blob, err := r.codec.encode(e)
	if err != nil {
		return err
	}
	ms := strconv.FormatInt(r.ttl.Milliseconds(), 10)
	rep, err := r.pool.do("SET", r.key(id), string(blob), "PX", ms)
	if err != nil {
		return err
	}
	if rep.kind != kindStatus || rep.text() != "OK" {
		return ErrProtocol
	}
	return nil
}

// Get читает запись из общего хранилища, а при его недоступности из памяти.
// Запасной путь проверяется и после промаха: там лежат записи, созданные во
// время деградации.
//
// Испорченная запись (чужой ключ шифрования или повреждённое значение) не
// прячется за запасным путём: она возвращается как ErrCorrupted, чтобы
// обработчик не маскировал повторно текст, который уже был замаскирован.
func (r *RedisStore) Get(id string) (*Entry, error) {
	if !r.available() {
		r.markDegraded("get", nil)
		return r.fallback.Get(id)
	}
	e, ok, err := r.get(id)
	switch {
	case errors.Is(err, ErrCorrupted):
		return nil, ErrCorrupted
	case err != nil:
		r.markDegraded("get", err)
	case ok:
		r.hits.Add(1)
		return e, nil
	default:
		r.misses.Add(1)
	}
	return r.fallback.Get(id)
}

// get выполняет чтение командой GET и разбирает значение.
func (r *RedisStore) get(id string) (*Entry, bool, error) {
	rep, err := r.pool.do("GET", r.key(id))
	if err != nil {
		return nil, false, err
	}
	if rep.null || rep.kind != kindBulk {
		return nil, false, nil
	}
	e, err := r.codec.decode(rep.str)
	if err != nil {
		return nil, false, err
	}
	if time.Now().After(e.ExpiresAt) {
		return nil, false, nil
	}
	return e, true, nil
}

// Delete удаляет запись из общего хранилища и из памяти.
func (r *RedisStore) Delete(id string) error {
	r.fallback.Delete(id)
	if !r.available() {
		return nil
	}
	if _, err := r.pool.do("DEL", r.key(id)); err != nil {
		r.markDegraded("del", err)
		return err
	}
	return nil
}

// Ping проверяет доступность Redis и снимает паузу, если он снова отвечает.
func (r *RedisStore) Ping() error {
	rep, err := r.pool.do("PING")
	if err != nil {
		r.markDegraded("ping", err)
		return err
	}
	if rep.kind != kindStatus || rep.text() != replyPong {
		return ErrProtocol
	}
	r.downUntil.Store(0)
	return nil
}

// Original расшифровывает исходный текст записи.
func (r *RedisStore) Original(e *Entry) (string, error) { return r.codec.original(e) }

// Len возвращает число записей на запасном пути. Записи в Redis принадлежат
// всем копиям сервиса сразу, поэтому в счётчик одного экземпляра не входят.
func (r *RedisStore) Len() int { return r.fallback.Len() }

// Stats возвращает счётчики работы с общим хранилищем.
func (r *RedisStore) Stats() Stats {
	return Stats{
		Degraded:  r.degraded.Load(),
		Failures:  r.failures.Load(),
		Puts:      r.puts.Load(),
		Hits:      r.hits.Load(),
		Misses:    r.misses.Load(),
		Available: r.available(),
		Fallback:  r.fallback.Len(),
	}
}

// available сообщает, можно ли сейчас обращаться к Redis. После сетевой ошибки
// обращения приостанавливаются на время паузы.
func (r *RedisStore) available() bool {
	until := r.downUntil.Load()
	return until == 0 || time.Now().UnixNano() >= until
}

// markDegraded отмечает обращение, обслуженное запасным путём. Сетевая ошибка
// вдобавок включает паузу, ответ Redis об ошибке паузу не включает.
func (r *RedisStore) markDegraded(op string, err error) {
	r.degraded.Add(1)
	if err != nil {
		r.failures.Add(1)
		if breaksConnection(err) {
			r.downUntil.Store(time.Now().Add(r.retry).UnixNano())
		}
	}
	r.notifyDegraded(op, err)
}

// breaksConnection отличает беду со связью от ошибки в одном значении. Ответ
// Redis об ошибке и непрочитанное значение паузу не включают: Redis жив.
func breaksConnection(err error) bool {
	var rerr *RedisError
	if errors.As(err, &rerr) {
		return false
	}
	return !errors.Is(err, ErrCorrupted) && !errors.Is(err, ErrBadRecord) && !errors.Is(err, ErrNotFound)
}
