// Package store хранит соответствие «идентификатор запроса — исходный текст и
// его маска». Исходный текст лежит только в зашифрованном виде: ключ берётся
// из переменной окружения и в образ не попадает.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"hash/maphash"
	"sync"
	"time"
)

// shardCount — число независимых сегментов хранилища. Каждый сегмент имеет
// свою блокировку, поэтому запись не становится узким местом под нагрузкой.
const shardCount = 256

// Ошибки хранилища.
var (
	ErrNotFound  = errors.New("запись не найдена")
	ErrKeySize   = errors.New("ключ шифрования должен быть длиной 32 байта")
	ErrCorrupted = errors.New("не удалось расшифровать сохранённое значение")
)

// SpanMeta — сведения о найденном фрагменте без самого значения. Сохраняются
// для журнала и для восстановления по смещениям; персональные данные сюда
// не попадают.
type SpanMeta struct {
	Type  string
	Start int
	End   int
}

// Entry — запись соответствия для одного идентификатора.
type Entry struct {
	// Mask — выданная маска. Хранится открытым текстом: она не содержит
	// персональных данных.
	Mask string
	// System — система, создавшая запись. Демаскировать может только она.
	System string
	// OrigHash — хеш исходного текста, отличает повтор маскирования.
	OrigHash [32]byte
	// MaskHash — хеш выданной маски, отличает запрос на демаскирование.
	MaskHash [32]byte
	// Spans — метаданные найденных фрагментов без значений.
	Spans []SpanMeta
	// ExpiresAt — момент истечения записи.
	ExpiresAt time.Time

	origCT []byte
}

type shard struct {
	mu   sync.RWMutex
	data map[string]*Entry
}

// Store — сегментированное хранилище соответствий в памяти.
type Store struct {
	shards [shardCount]*shard
	gcm    cipher.AEAD
	ttl    time.Duration
	max    int
	seed   maphash.Seed

	countMu sync.Mutex
	count   int

	stopOnce sync.Once
	stop     chan struct{}
}

// Config — параметры хранилища.
type Config struct {
	// Key — ключ шифрования длиной 32 байта. Пустой ключ означает, что нужно
	// сгенерировать временный: сервис продолжит работу, но записи не переживут
	// перезапуск.
	Key []byte
	// TTL — срок жизни записи.
	TTL time.Duration
	// MaxRecords — верхняя граница числа записей.
	MaxRecords int
}

// New создаёт хранилище и запускает уборку просроченных записей.
func New(cfg Config) (*Store, error) {
	key := cfg.Key
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}
	if len(key) != 32 {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	maxRecords := cfg.MaxRecords
	if maxRecords <= 0 {
		maxRecords = 1_000_000
	}

	s := &Store{gcm: gcm, ttl: ttl, max: maxRecords, seed: maphash.MakeSeed(), stop: make(chan struct{})}
	for i := range s.shards {
		s.shards[i] = &shard{data: make(map[string]*Entry)}
	}
	go s.sweepLoop()
	return s, nil
}

// Close останавливает уборку.
func (s *Store) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *Store) shardFor(id string) *shard {
	h := maphash.String(s.seed, id)
	return s.shards[h%shardCount]
}

// Put сохраняет соответствие. Исходный текст шифруется перед записью.
func (s *Store) Put(id, original, maskText, system string, spans []SpanMeta) (*Entry, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := s.gcm.Seal(nonce, nonce, []byte(original), nil)

	e := &Entry{
		Mask:      maskText,
		System:    system,
		OrigHash:  sha256.Sum256([]byte(original)),
		MaskHash:  sha256.Sum256([]byte(maskText)),
		Spans:     spans,
		ExpiresAt: time.Now().Add(s.ttl),
		origCT:    ct,
	}

	sh := s.shardFor(id)
	sh.mu.Lock()
	_, existed := sh.data[id]
	sh.data[id] = e
	sh.mu.Unlock()

	if !existed {
		s.countMu.Lock()
		s.count++
		over := s.count > s.max
		s.countMu.Unlock()
		if over {
			s.evictOldest()
		}
	}
	return e, nil
}

// Get возвращает запись по идентификатору, если она не просрочена.
func (s *Store) Get(id string) (*Entry, bool) {
	sh := s.shardFor(id)
	sh.mu.RLock()
	e, ok := sh.data[id]
	sh.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(e.ExpiresAt) {
		return nil, false
	}
	return e, true
}

// Original расшифровывает исходный текст записи.
func (s *Store) Original(e *Entry) (string, error) {
	if e == nil || len(e.origCT) < s.gcm.NonceSize() {
		return "", ErrNotFound
	}
	nonce := e.origCT[:s.gcm.NonceSize()]
	pt, err := s.gcm.Open(nil, nonce, e.origCT[s.gcm.NonceSize():], nil)
	if err != nil {
		return "", ErrCorrupted
	}
	return string(pt), nil
}

// Len возвращает число записей в хранилище.
func (s *Store) Len() int {
	s.countMu.Lock()
	defer s.countMu.Unlock()
	return s.count
}

func (s *Store) sweepLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.sweep()
		}
	}
}

// sweep удаляет просроченные записи.
func (s *Store) sweep() {
	now := time.Now()
	removed := 0
	for _, sh := range s.shards {
		sh.mu.Lock()
		for id, e := range sh.data {
			if now.After(e.ExpiresAt) {
				delete(sh.data, id)
				removed++
			}
		}
		sh.mu.Unlock()
	}
	if removed > 0 {
		s.countMu.Lock()
		s.count -= removed
		if s.count < 0 {
			s.count = 0
		}
		s.countMu.Unlock()
	}
}

// evictOldest удаляет самые ранние записи, когда превышен предел их числа.
func (s *Store) evictOldest() {
	for _, sh := range s.shards {
		sh.mu.Lock()
		var oldestID string
		var oldest time.Time
		for id, e := range sh.data {
			if oldest.IsZero() || e.ExpiresAt.Before(oldest) {
				oldest, oldestID = e.ExpiresAt, id
			}
		}
		if oldestID != "" {
			delete(sh.data, oldestID)
			sh.mu.Unlock()
			s.countMu.Lock()
			s.count--
			s.countMu.Unlock()
			continue
		}
		sh.mu.Unlock()
	}
}

// HashOf возвращает хеш строки — тем же способом, что и хеши внутри записи.
func HashOf(s string) [32]byte { return sha256.Sum256([]byte(s)) }
