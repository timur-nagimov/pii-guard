// Package store хранит соответствие «идентификатор запроса — исходный текст и
// его маска». Исходный текст лежит только в зашифрованном виде: ключ берётся
// из переменной окружения и в образ не попадает.
//
// Хранилище описано интерфейсом Interface. Реализаций две: память процесса
// (выбор по умолчанию) и общий Redis, который позволяет держать несколько
// копий сервиса за балансировщиком. Тип Store выбирает реализацию по
// настройкам и остаётся единственной точкой входа для остального кода.
package store

import (
	"crypto/sha256"
	"errors"
	"time"
)

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

// Interface — контракт хранилища соответствий. Сервис работает только через
// него, поэтому реализацию можно менять без правок обработчиков.
type Interface interface {
	// Put сохраняет соответствие и возвращает созданную запись.
	Put(id, original, maskText, system string, spans []SpanMeta) (*Entry, error)
	// Get отдаёт запись по идентификатору, если она есть и не просрочена.
	Get(id string) (*Entry, bool)
	// Original расшифровывает исходный текст записи.
	Original(e *Entry) (string, error)
	// Len возвращает число записей, за которые отвечает этот экземпляр.
	Len() int
	// Close освобождает ресурсы реализации.
	Close()
}

// Config — параметры хранилища.
type Config struct {
	// Key — ключ шифрования длиной 32 байта. Пустой ключ означает, что нужно
	// сгенерировать временный: сервис продолжит работу, но записи не переживут
	// перезапуск. Для общего Redis ключ обязан совпадать у всех копий сервиса,
	// иначе чужую запись не получится расшифровать.
	Key []byte
	// TTL — срок жизни записи.
	TTL time.Duration
	// MaxRecords — верхняя граница числа записей в памяти.
	MaxRecords int
	// Redis — параметры общего хранилища. Пустой адрес означает работу только
	// в памяти процесса.
	Redis RedisConfig
	// OnDegraded вызывается при каждом переходе на запасной путь: Redis
	// недоступен, запись ушла в память. Нужен, чтобы деградация была видна в
	// показателях. Может быть пустым.
	OnDegraded func(op string, err error)
}

// Store — хранилище, которым пользуется сервис. Тонкая обёртка над выбранной
// реализацией: она выбирается один раз при запуске и дальше не меняется.
type Store struct {
	backend Interface
	shared  bool
}

// New создаёт хранилище по настройкам: общее поверх Redis, если задан адрес,
// иначе в памяти процесса.
func New(cfg Config) (*Store, error) {
	if cfg.Redis.Addr == "" {
		mem, err := NewMemory(cfg)
		if err != nil {
			return nil, err
		}
		return &Store{backend: mem}, nil
	}
	shared, err := NewRedis(cfg)
	if err != nil {
		return nil, err
	}
	return &Store{backend: shared, shared: true}, nil
}

// Backend возвращает выбранную реализацию. Нужен для показателей и тестов.
func (s *Store) Backend() Interface { return s.backend }

// Shared сообщает, работает ли сервис с общим хранилищем. Только в этом
// режиме горизонтальный рост даёт согласованное обратное преобразование.
func (s *Store) Shared() bool { return s.shared }

// Put сохраняет соответствие.
func (s *Store) Put(id, original, maskText, system string, spans []SpanMeta) (*Entry, error) {
	return s.backend.Put(id, original, maskText, system, spans)
}

// Get возвращает запись по идентификатору.
func (s *Store) Get(id string) (*Entry, bool) { return s.backend.Get(id) }

// Original расшифровывает исходный текст записи.
func (s *Store) Original(e *Entry) (string, error) { return s.backend.Original(e) }

// Len возвращает число записей, за которые отвечает этот экземпляр.
func (s *Store) Len() int { return s.backend.Len() }

// Close освобождает ресурсы реализации.
func (s *Store) Close() { s.backend.Close() }

// SetDegradedHook задаёт обработчик деградации уже после создания хранилища.
// Для памяти вызов ничего не меняет: запасному пути неоткуда деградировать.
func (s *Store) SetDegradedHook(fn func(op string, err error)) {
	if r, ok := s.backend.(*RedisStore); ok {
		r.SetDegradedHook(fn)
	}
}

// Stats возвращает счётчики работы с общим хранилищем. Для памяти счётчики
// нулевые.
func (s *Store) Stats() Stats {
	if r, ok := s.backend.(*RedisStore); ok {
		return r.Stats()
	}
	return Stats{}
}

// HashOf возвращает хеш строки — тем же способом, что и хеши внутри записи.
func HashOf(s string) [32]byte { return sha256.Sum256([]byte(s)) }
