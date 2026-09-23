// Package capture сохраняет запросы к сервису для последующей проверки
// качества на настоящих текстах.
//
// Зачем он есть. Свой набор данных порождён генератором: в нём ровно те
// случаи, которые мы придумали. Тексты проверяющей системы — единственные
// настоящие, какие решение видит, и по ним видно, чего генератор не знает.
// Один прогон нагрузочного тестирования даёт несколько тысяч живых записей.
//
// Почему это не журнал. В журнал и в показатели исходные данные попадать не
// должны — это требование задания и отдельная проверка ворот. Захват живёт
// своим файлом, по умолчанию выключен, а сохранение самого текста включается
// ещё одним признаком поверх. Без него в файл идут только размеры и типы:
// по такой записи видно, что текст был обработан и что в нём нашли, но не
// видно ни одного значения.
package capture

import (
	"encoding/json"
	"io"
	"sync/atomic"
	"time"
)

// Config — настройки канала захвата.
type Config struct {
	// Enabled включает захват.
	Enabled bool
	// Path — файл, куда пишутся записи, по одной в строке.
	Path string
	// MaxBytes — размер, после которого файл переименовывается.
	MaxBytes int64
	// Keep — сколько переименованных файлов хранить.
	Keep int
	// WithPayload разрешает сохранять сам текст запроса и ответа. Признак
	// отдельный намеренно: включая его, человек соглашается хранить
	// персональные данные на диске.
	WithPayload bool
	// Queue — глубина очереди записи. Ноль означает значение по умолчанию.
	Queue int
}

// Значения по умолчанию.
const (
	defaultMaxBytes = 512 << 20
	defaultKeep     = 4
	defaultQueue    = 4096
)

// Record — одна сохранённая обработка.
type Record struct {
	// Time — момент обработки.
	Time time.Time `json:"time"`
	// System — система-потребитель.
	System string `json:"system"`
	// PayloadID — идентификатор корреляции из контракта.
	PayloadID string `json:"payload_id"`
	// Direction — что делали: mask, demask и прочее.
	Direction string `json:"direction"`
	// PayloadLen — размер входного текста в байтах.
	PayloadLen int `json:"payload_len"`
	// Counts — сколько фрагментов каждого типа нашли.
	Counts map[string]int `json:"counts,omitempty"`
	// TookMS — длительность обработки в миллисекундах.
	TookMS float64 `json:"took_ms"`
	// Payload и Result заполняются только при включённом WithPayload.
	Payload string `json:"payload,omitempty"`
	Result  string `json:"result,omitempty"`
}

// Writer — канал захвата. Запись идёт в отдельной горутине через очередь
// ограниченной длины: обработчик запроса не должен ждать диска. Когда очередь
// полна, запись отбрасывается и считается — потерянный образец хуже
// просевшей задержки, но врать о потерях нельзя, поэтому их видно в
// показателях.
type Writer struct {
	ch          chan Record
	done        chan struct{}
	out         io.WriteCloser
	withPayload bool
	on          bool

	written atomic.Int64
	dropped atomic.Int64
}

// New создаёт канал захвата. Выключенный канал возвращается без файла и
// ничего не пишет: вызывающему не нужно проверять признак самому.
func New(cfg Config, open func(path string, max int64, keep int) (io.WriteCloser, error)) (*Writer, error) {
	if !cfg.Enabled || cfg.Path == "" {
		return &Writer{}, nil
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultMaxBytes
	}
	if cfg.Keep <= 0 {
		cfg.Keep = defaultKeep
	}
	if cfg.Queue <= 0 {
		cfg.Queue = defaultQueue
	}
	out, err := open(cfg.Path, cfg.MaxBytes, cfg.Keep)
	if err != nil {
		return nil, err
	}
	w := &Writer{
		ch:          make(chan Record, cfg.Queue),
		done:        make(chan struct{}),
		out:         out,
		withPayload: cfg.WithPayload,
		on:          true,
	}
	go w.loop()
	return w, nil
}

// Enabled сообщает, что канал пишет.
func (w *Writer) Enabled() bool { return w != nil && w.on }

// WithPayload сообщает, что в файл идёт и сам текст.
func (w *Writer) WithPayload() bool { return w != nil && w.withPayload }

// Write ставит запись в очередь. Вызов не блокирует: при полной очереди
// запись отбрасывается.
func (w *Writer) Write(rec Record) {
	if !w.Enabled() {
		return
	}
	if !w.withPayload {
		rec.Payload, rec.Result = "", ""
	}
	select {
	case w.ch <- rec:
	default:
		w.dropped.Add(1)
	}
}

// Stats возвращает число записанных и отброшенных записей.
func (w *Writer) Stats() (written, dropped int64) {
	if w == nil {
		return 0, 0
	}
	return w.written.Load(), w.dropped.Load()
}

// Close дописывает очередь и закрывает файл.
func (w *Writer) Close() error {
	if !w.Enabled() {
		return nil
	}
	close(w.ch)
	<-w.done
	return w.out.Close()
}

// loop пишет записи по одной в строке. Ошибка записи не останавливает сервис:
// захват — вспомогательная задача, и падать из-за него нельзя.
func (w *Writer) loop() {
	defer close(w.done)
	enc := json.NewEncoder(w.out)
	for rec := range w.ch {
		if err := enc.Encode(rec); err != nil {
			w.dropped.Add(1)
			continue
		}
		w.written.Add(1)
	}
}
