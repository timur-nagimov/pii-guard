package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"time"
)

// recordVersion — версия формата сериализации записи. Первый байт значения,
// чтобы будущая смена формата не ломала уже лежащие в общем хранилище записи.
const recordVersion byte = 1

// ErrBadRecord возвращается, когда расшифрованное значение не разбирается по
// формату записи.
var ErrBadRecord = errors.New("сохранённое значение имеет неизвестный формат")

// codec шифрует значения и переводит запись в компактный набор байт. Один и
// тот же codec обслуживает обе реализации хранилища, поэтому запись, созданная
// в памяти, читается из общего хранилища и наоборот.
type codec struct {
	gcm cipher.AEAD
}

// newCodec готовит шифрование на заданном ключе. Пустой ключ заменяется
// случайным: сервис продолжит работу, но записи не переживут перезапуск и не
// будут видны соседним копиям.
func newCodec(key []byte) (*codec, error) {
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
	return &codec{gcm: gcm}, nil
}

// seal шифрует байты и приписывает разовое значение в начало.
func (c *codec) seal(pt []byte) ([]byte, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.gcm.Seal(nonce, nonce, pt, nil), nil
}

// open расшифровывает байты, полученные от seal.
func (c *codec) open(ct []byte) ([]byte, error) {
	if len(ct) < c.gcm.NonceSize() {
		return nil, ErrNotFound
	}
	nonce := ct[:c.gcm.NonceSize()]
	pt, err := c.gcm.Open(nil, nonce, ct[c.gcm.NonceSize():], nil)
	if err != nil {
		return nil, ErrCorrupted
	}
	return pt, nil
}

// newEntry собирает запись и шифрует исходный текст.
func (c *codec) newEntry(original, maskText, system string, spans []SpanMeta, expiresAt time.Time) (*Entry, error) {
	ct, err := c.seal([]byte(original))
	if err != nil {
		return nil, err
	}
	return &Entry{
		Mask:      maskText,
		System:    system,
		OrigHash:  HashOf(original),
		MaskHash:  HashOf(maskText),
		Spans:     spans,
		ExpiresAt: expiresAt,
		origCT:    ct,
	}, nil
}

// original расшифровывает исходный текст записи.
func (c *codec) original(e *Entry) (string, error) {
	if e == nil {
		return "", ErrNotFound
	}
	pt, err := c.open(e.origCT)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// encode переводит запись в компактный набор байт и шифрует его целиком.
// Наружу уходит только шифртекст: общее хранилище видит длину значения, но не
// сам текст, маску и типы найденных фрагментов.
func (c *codec) encode(e *Entry) ([]byte, error) {
	original, err := c.original(e)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, len(original)+len(e.Mask)+len(e.System)+32+16*len(e.Spans))
	buf = append(buf, recordVersion)
	buf = appendString(buf, e.Mask)
	buf = appendString(buf, e.System)
	buf = binary.AppendUvarint(buf, millisToUint(e.ExpiresAt.UnixMilli()))
	buf = binary.AppendUvarint(buf, offsetToUint(len(e.Spans)))
	for _, sp := range e.Spans {
		buf = appendString(buf, sp.Type)
		buf = binary.AppendUvarint(buf, offsetToUint(sp.Start))
		buf = binary.AppendUvarint(buf, offsetToUint(sp.End))
	}
	buf = appendString(buf, original)
	return c.seal(buf)
}

// decode восстанавливает запись из значения, записанного encode. Исходный
// текст сразу шифруется заново, поэтому в памяти он открытым не остаётся.
func (c *codec) decode(blob []byte) (*Entry, error) {
	pt, err := c.open(blob)
	if err != nil {
		return nil, err
	}
	r := &reader{buf: pt}
	if r.byteAt() != recordVersion {
		return nil, ErrBadRecord
	}
	mask := r.str()
	system := r.str()
	expires := r.uint()
	spans := decodeSpans(r)
	original := r.str()
	if r.err != nil {
		return nil, r.err
	}
	if expires > math.MaxInt64 {
		return nil, ErrBadRecord
	}
	return c.newEntry(original, mask, system, spans, time.UnixMilli(int64(expires)))
}

// decodeSpans читает метаданные найденных фрагментов.
func decodeSpans(r *reader) []SpanMeta {
	n := r.uint()
	if r.err != nil || n == 0 {
		return nil
	}
	if n > uint64(len(r.buf)) {
		r.err = ErrBadRecord
		return nil
	}
	spans := make([]SpanMeta, 0, n)
	for i := uint64(0); i < n; i++ {
		sp := SpanMeta{Type: r.str()}
		sp.Start = uintToOffset(r.uint())
		sp.End = uintToOffset(r.uint())
		if r.err != nil {
			return nil
		}
		spans = append(spans, sp)
	}
	return spans
}

// offsetToUint переводит смещение в число для записи. Отрицательных смещений
// быть не может, но проверка дешевле разбора испорченного значения.
func offsetToUint(v int) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// millisToUint переводит момент истечения в число для записи.
func millisToUint(ms int64) uint64 {
	if ms < 0 {
		return 0
	}
	return uint64(ms)
}

// uintToOffset переводит прочитанное число обратно в смещение. Невозможные
// значения обнуляются: сместиться дальше длины текста фрагмент не может.
func uintToOffset(v uint64) int {
	if v > math.MaxInt32 {
		return 0
	}
	return int(v)
}

// appendString дописывает строку с длиной впереди.
func appendString(buf []byte, s string) []byte {
	buf = binary.AppendUvarint(buf, uint64(len(s)))
	return append(buf, s...)
}

// reader разбирает набор байт по формату записи. Первая же ошибка запоминается,
// дальнейшие чтения возвращают пустые значения.
type reader struct {
	buf []byte
	err error
}

// byteAt снимает один байт.
func (r *reader) byteAt() byte {
	if len(r.buf) == 0 {
		r.err = ErrBadRecord
		return 0
	}
	b := r.buf[0]
	r.buf = r.buf[1:]
	return b
}

// uint снимает число переменной длины.
func (r *reader) uint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.buf)
	if n <= 0 {
		r.err = ErrBadRecord
		return 0
	}
	r.buf = r.buf[n:]
	return v
}

// str снимает строку с длиной впереди.
func (r *reader) str() string {
	n := r.uint()
	if r.err != nil {
		return ""
	}
	if n > uint64(len(r.buf)) {
		r.err = ErrBadRecord
		return ""
	}
	s := string(r.buf[:n])
	r.buf = r.buf[n:]
	return s
}
