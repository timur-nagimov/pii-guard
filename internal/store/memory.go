package store

import (
	"hash/maphash"
	"sync"
	"time"
)

// shardCount — число независимых сегментов хранилища. Каждый сегмент имеет
// свою блокировку, поэтому запись не становится узким местом под нагрузкой.
const shardCount = 256

// defaultTTL и defaultMaxRecords подставляются вместо неположительных
// значений настроек.
const (
	defaultTTL        = time.Hour
	defaultMaxRecords = 1_000_000
)

type shard struct {
	mu   sync.RWMutex
	data map[string]*Entry
}

// MemoryStore — сегментированное хранилище соответствий в памяти процесса.
// Выбор по умолчанию и запасной путь для общего хранилища.
type MemoryStore struct {
	shards [shardCount]*shard
	codec  *codec
	ttl    time.Duration
	max    int
	seed   maphash.Seed

	countMu sync.Mutex
	count   int

	stopOnce sync.Once
	stop     chan struct{}
}

// NewMemory создаёт хранилище в памяти и запускает уборку просроченных записей.
func NewMemory(cfg Config) (*MemoryStore, error) {
	c, err := newCodec(cfg.Key)
	if err != nil {
		return nil, err
	}
	return newMemoryWithCodec(c, cfg), nil
}

// newMemoryWithCodec собирает хранилище на готовом шифровании. Нужен, чтобы
// память и общее хранилище пользовались одним ключом.
func newMemoryWithCodec(c *codec, cfg Config) *MemoryStore {
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	maxRecords := cfg.MaxRecords
	if maxRecords <= 0 {
		maxRecords = defaultMaxRecords
	}
	s := &MemoryStore{codec: c, ttl: ttl, max: maxRecords, seed: maphash.MakeSeed(), stop: make(chan struct{})}
	for i := range s.shards {
		s.shards[i] = &shard{data: make(map[string]*Entry)}
	}
	go s.sweepLoop()
	return s
}

// Close останавливает уборку.
func (s *MemoryStore) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *MemoryStore) shardFor(id string) *shard {
	h := maphash.String(s.seed, id)
	return s.shards[h%shardCount]
}

// Put сохраняет соответствие. Исходный текст шифруется перед записью.
func (s *MemoryStore) Put(id, original, maskText, system string, spans []SpanMeta) (*Entry, error) {
	e, err := s.codec.newEntry(original, maskText, system, spans, time.Now().Add(s.ttl))
	if err != nil {
		return nil, err
	}
	s.store(id, e)
	return e, nil
}

// store кладёт готовую запись и следит за пределом их числа.
func (s *MemoryStore) store(id string, e *Entry) {
	sh := s.shardFor(id)
	sh.mu.Lock()
	_, existed := sh.data[id]
	sh.data[id] = e
	sh.mu.Unlock()

	if existed {
		return
	}
	s.countMu.Lock()
	s.count++
	over := s.count > s.max
	s.countMu.Unlock()
	if over {
		s.evictOldest()
	}
}

// Delete удаляет запись по идентификатору.
func (s *MemoryStore) Delete(id string) {
	sh := s.shardFor(id)
	sh.mu.Lock()
	_, existed := sh.data[id]
	delete(sh.data, id)
	sh.mu.Unlock()
	if !existed {
		return
	}
	s.countMu.Lock()
	s.count--
	if s.count < 0 {
		s.count = 0
	}
	s.countMu.Unlock()
}

// Get возвращает запись по идентификатору, если она не просрочена. Отсутствие
// записи и истёкший срок жизни дают ErrNotFound: для обработчика это одно и то
// же — соответствия больше нет.
func (s *MemoryStore) Get(id string) (*Entry, error) {
	sh := s.shardFor(id)
	sh.mu.RLock()
	e, ok := sh.data[id]
	sh.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if time.Now().After(e.ExpiresAt) {
		return nil, ErrNotFound
	}
	return e, nil
}

// Original расшифровывает исходный текст записи.
func (s *MemoryStore) Original(e *Entry) (string, error) {
	return s.codec.original(e)
}

// Len возвращает число записей в хранилище.
func (s *MemoryStore) Len() int {
	s.countMu.Lock()
	defer s.countMu.Unlock()
	return s.count
}

func (s *MemoryStore) sweepLoop() {
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
func (s *MemoryStore) sweep() {
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

// evictOldest удаляет самую раннюю запись, когда превышен предел их числа.
// Вытесняется ровно одна запись — глобально самая старая, а не по одной из
// каждого сегмента: иначе один запрос сверх предела вытеснял бы до двухсот
// пятидесяти шести действующих записей и счётчик уходил бы в минус.
func (s *MemoryStore) evictOldest() {
	var oldestShard *shard
	var oldestID string
	var oldest time.Time
	for _, sh := range s.shards {
		sh.mu.Lock()
		id, exp := oldestIn(sh.data)
		if id != "" && (oldest.IsZero() || exp.Before(oldest)) {
			oldest, oldestID, oldestShard = exp, id, sh
		}
		sh.mu.Unlock()
	}
	if oldestShard == nil {
		return
	}
	oldestShard.mu.Lock()
	delete(oldestShard.data, oldestID)
	oldestShard.mu.Unlock()
	s.countMu.Lock()
	s.count--
	s.countMu.Unlock()
}

// oldestIn находит идентификатор записи с ближайшим сроком истечения и сам
// этот срок: по нему сравниваются сегменты при поиске глобально самой старой.
func oldestIn(data map[string]*Entry) (string, time.Time) {
	var oldestID string
	var oldest time.Time
	for id, e := range data {
		if oldest.IsZero() || e.ExpiresAt.Before(oldest) {
			oldest, oldestID = e.ExpiresAt, id
		}
	}
	return oldestID, oldest
}
