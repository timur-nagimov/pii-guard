package store

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"time"
)

// Ошибки клиента протокола RESP.
var (
	ErrProtocol   = errors.New("неожиданный ответ Redis")
	ErrPoolClosed = errors.New("пул соединений закрыт")
	ErrPoolBusy   = errors.New("свободных соединений с Redis нет")
)

// RedisError — ответ Redis об ошибке, вид «-ERR текст». Соединение после
// такого ответа остаётся рабочим, поэтому его возвращают в пул.
type RedisError struct {
	Msg string
}

// Error возвращает текст ошибки Redis.
func (e *RedisError) Error() string { return "redis: " + e.Msg }

// Виды ответов протокола RESP.
const (
	kindStatus  = '+'
	kindError   = '-'
	kindInt     = ':'
	kindBulk    = '$'
	kindArray   = '*'
	maxBulkSize = 64 << 20
)

// reply — разобранный ответ Redis. Объёмная строка и массив могут быть
// пустыми: в протоколе это отдельное значение с длиной минус один.
type reply struct {
	kind byte
	str  []byte
	num  int64
	arr  []reply
	null bool
}

// text отдаёт ответ строкой.
func (r reply) text() string { return string(r.str) }

// respConn — одно соединение с Redis вместе с буферами чтения и записи.
type respConn struct {
	nc  net.Conn
	br  *bufio.Reader
	buf []byte
}

// dialRESP открывает соединение и при необходимости проходит проверку пароля.
func dialRESP(cfg *RedisConfig) (*respConn, error) {
	dialer := net.Dialer{Timeout: cfg.DialTimeout}
	nc, err := dialer.DialContext(context.Background(), "tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	c := &respConn{nc: nc, br: bufio.NewReaderSize(nc, 4096), buf: make([]byte, 0, 1024)}
	if cfg.Password == "" {
		return c, nil
	}
	if _, err := c.do(cfg, "AUTH", cfg.Password); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// close закрывает соединение.
func (c *respConn) close() {
	if c.nc != nil {
		_ = c.nc.Close()
	}
}

// do отправляет команду и читает ответ.
func (c *respConn) do(cfg *RedisConfig, args ...string) (reply, error) {
	if err := c.write(cfg, args); err != nil {
		return reply{}, err
	}
	if err := c.nc.SetReadDeadline(time.Now().Add(cfg.ReadTimeout)); err != nil {
		return reply{}, err
	}
	return readReply(c.br)
}

// write собирает команду в виде массива объёмных строк и отправляет её целиком.
func (c *respConn) write(cfg *RedisConfig, args []string) error {
	c.buf = c.buf[:0]
	c.buf = append(c.buf, kindArray)
	c.buf = strconv.AppendInt(c.buf, int64(len(args)), 10)
	c.buf = append(c.buf, '\r', '\n')
	for _, a := range args {
		c.buf = append(c.buf, kindBulk)
		c.buf = strconv.AppendInt(c.buf, int64(len(a)), 10)
		c.buf = append(c.buf, '\r', '\n')
		c.buf = append(c.buf, a...)
		c.buf = append(c.buf, '\r', '\n')
	}
	if err := c.nc.SetWriteDeadline(time.Now().Add(cfg.WriteTimeout)); err != nil {
		return err
	}
	_, err := c.nc.Write(c.buf)
	return err
}

// readReply разбирает один ответ любого вида.
func readReply(br *bufio.Reader) (reply, error) {
	line, err := readLine(br)
	if err != nil {
		return reply{}, err
	}
	if len(line) == 0 {
		return reply{}, ErrProtocol
	}
	kind, body := line[0], line[1:]
	switch kind {
	case kindStatus:
		return reply{kind: kind, str: body}, nil
	case kindError:
		return reply{}, &RedisError{Msg: string(body)}
	case kindInt:
		n, perr := parseRespInt(body)
		return reply{kind: kind, num: n}, perr
	case kindBulk:
		return readBulk(br, body)
	case kindArray:
		return readArray(br, body)
	default:
		return reply{}, fmt.Errorf("%w: вид ответа %q", ErrProtocol, string(kind))
	}
}

// readLine читает строку до пары возврат каретки и перевод строки.
func readLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, ErrProtocol
	}
	return line[:len(line)-2], nil
}

// readBulk читает объёмную строку по уже прочитанной длине.
func readBulk(br *bufio.Reader, head []byte) (reply, error) {
	n, err := parseRespInt(head)
	if err != nil {
		return reply{}, err
	}
	if n < 0 {
		return reply{kind: kindBulk, null: true}, nil
	}
	if n > maxBulkSize {
		return reply{}, fmt.Errorf("%w: слишком длинное значение %d", ErrProtocol, n)
	}
	body := make([]byte, n+2)
	if _, err := io.ReadFull(br, body); err != nil {
		return reply{}, err
	}
	if body[n] != '\r' || body[n+1] != '\n' {
		return reply{}, ErrProtocol
	}
	return reply{kind: kindBulk, str: body[:n]}, nil
}

// readArray читает массив ответов по уже прочитанной длине.
func readArray(br *bufio.Reader, head []byte) (reply, error) {
	n, err := parseRespInt(head)
	if err != nil {
		return reply{}, err
	}
	if n < 0 {
		return reply{kind: kindArray, null: true}, nil
	}
	items := make([]reply, 0, min(n, 1024))
	for i := int64(0); i < n; i++ {
		item, ierr := readReply(br)
		if ierr != nil {
			return reply{}, ierr
		}
		items = append(items, item)
	}
	return reply{kind: kindArray, arr: items}, nil
}

// parseRespInt разбирает целое число из заголовка ответа.
func parseRespInt(b []byte) (int64, error) {
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: не число %q", ErrProtocol, string(b))
	}
	return n, nil
}

// respPool — пул соединений с Redis. Число живых соединений ограничено
// размером пула: разрешение на новое соединение берётся из канала sem, а
// свободные соединения ждут в канале idle.
type respPool struct {
	cfg    RedisConfig
	idle   chan *respConn
	sem    chan struct{}
	closed atomic.Bool
}

// newRespPool готовит пул. Соединения открываются по мере надобности.
func newRespPool(cfg *RedisConfig) *respPool {
	p := &respPool{
		cfg:  *cfg,
		idle: make(chan *respConn, cfg.PoolSize),
		sem:  make(chan struct{}, cfg.PoolSize),
	}
	for i := 0; i < cfg.PoolSize; i++ {
		p.sem <- struct{}{}
	}
	return p
}

// acquire берёт свободное соединение или открывает новое.
func (p *respPool) acquire() (*respConn, error) {
	if p.closed.Load() {
		return nil, ErrPoolClosed
	}
	select {
	case c := <-p.idle:
		return c, nil
	default:
	}
	timer := time.NewTimer(p.cfg.DialTimeout)
	defer timer.Stop()
	select {
	case c := <-p.idle:
		return c, nil
	case <-p.sem:
		c, err := dialRESP(&p.cfg)
		if err != nil {
			p.sem <- struct{}{}
			return nil, err
		}
		return c, nil
	case <-timer.C:
		return nil, ErrPoolBusy
	}
}

// release возвращает исправное соединение в пул.
func (p *respPool) release(c *respConn) {
	if p.closed.Load() {
		p.discard(c)
		return
	}
	select {
	case p.idle <- c:
	default:
		p.discard(c)
	}
}

// discard закрывает негодное соединение и освобождает разрешение на новое.
func (p *respPool) discard(c *respConn) {
	c.close()
	select {
	case p.sem <- struct{}{}:
	default:
	}
}

// do выполняет команду на свободном соединении. Ответ Redis об ошибке не
// портит соединение, сетевая ошибка закрывает его.
func (p *respPool) do(args ...string) (reply, error) {
	c, err := p.acquire()
	if err != nil {
		return reply{}, err
	}
	rep, err := c.do(&p.cfg, args...)
	var rerr *RedisError
	if err == nil || errors.As(err, &rerr) {
		p.release(c)
		return rep, err
	}
	p.discard(c)
	return reply{}, err
}

// Close закрывает пул и все свободные соединения.
func (p *respPool) Close() {
	if p.closed.Swap(true) {
		return
	}
	for {
		select {
		case c := <-p.idle:
			c.close()
		default:
			return
		}
	}
}
