package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync/atomic"
)

// HeaderRequestID — заголовок сквозного идентификатора запроса. Имя выбрано по
// сложившейся практике: его же ставят большинство балансировщиков и клиентов.
const HeaderRequestID = "X-Request-Id"

// maxIDLen — предел длины идентификатора, пришедшего от клиента. Чужая строка
// произвольной длины в журнале это и мусор, и способ раздуть запись.
const maxIDLen = 96

type ctxKey struct{}

// reqInfo — данные запроса, которые журнал добавляет в каждую запись сам.
// Идентификатор известен с первой строки обработчика, а имя системы
// выясняется позже, при опознании ключа, поэтому оно изменяемое.
type reqInfo struct {
	id     string
	system atomic.Pointer[string]
}

// WithRequestID кладёт идентификатор запроса в контекст. Дальше его не нужно
// передавать руками: обработчик журнала достаёт значение из контекста сам,
// достаточно вызывать методы с суффиксом Context.
func WithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	info := &reqInfo{id: id}
	return context.WithValue(ctx, ctxKey{}, info)
}

// RequestID возвращает идентификатор запроса из контекста.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	info, _ := ctx.Value(ctxKey{}).(*reqInfo)
	if info == nil {
		return ""
	}
	return info.id
}

// SetSystem записывает имя опознанной системы-потребителя в данные запроса.
// Вызывается один раз, сразу после проверки ключа доступа.
func SetSystem(ctx context.Context, name string) {
	if ctx == nil || name == "" {
		return
	}
	info, _ := ctx.Value(ctxKey{}).(*reqInfo)
	if info == nil {
		return
	}
	info.system.Store(&name)
}

// System возвращает имя системы-потребителя из контекста.
func System(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	info, _ := ctx.Value(ctxKey{}).(*reqInfo)
	if info == nil {
		return ""
	}
	if p := info.system.Load(); p != nil {
		return *p
	}
	return ""
}

// NewID порождает идентификатор запроса: шестнадцать случайных байтов в
// шестнадцатеричной записи. Ставка на формат UUID не делается: клиенты
// присылают свои идентификаторы в разных формах, и сервис их принимает.
func NewID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Источник случайности в Go не отказывает, но если это случится,
		// молчать нельзя: пустой идентификатор рвёт сквозную трассировку.
		return "0000000000000000"
	}
	return hex.EncodeToString(buf[:])
}

// EnsureID выбирает идентификатор запроса: берёт присланный клиентом, если он
// пригоден, иначе порождает свой. Возвращает также признак того, что
// идентификатор наш: по нему видно, доходит ли трассировка от клиента.
func EnsureID(r *http.Request) (string, bool) {
	if r != nil {
		if id, ok := SafeID(r.Header.Get(HeaderRequestID)); ok {
			return id, false
		}
	}
	return NewID(), true
}

// SafeID проверяет, что строка пригодна на роль идентификатора: непустая, не
// длиннее предела и состоит только из букв, цифр и знаков -_.:. Всё
// остальное отбрасывается. Так чужая строка не подменит разметку журнала и не
// протащит в него значение персональных данных.
func SafeID(s string) (string, bool) {
	if s == "" || len(s) > maxIDLen {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9',
			c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c == '-', c == '_', c == '.', c == ':':
		default:
			return "", false
		}
	}
	return s, true
}

// Propagate переносит идентификатор запроса в исходящий запрос: обращение к
// языковой модели должно быть видно в её журнале под тем же номером, что и
// запрос клиента у нас.
func Propagate(ctx context.Context, req *http.Request) {
	if req == nil {
		return
	}
	if id := RequestID(ctx); id != "" {
		req.Header.Set(HeaderRequestID, id)
	}
}
