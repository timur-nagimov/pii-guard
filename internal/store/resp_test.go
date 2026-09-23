package store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// listenLocal поднимает слушатель на свободном порту петлевого адреса.
func listenLocal() (net.Listener, error) {
	var lc net.ListenConfig
	return lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
}

// cannedServer — заглушка Redis: на каждое соединение отдаёт заранее заданные
// байты и запоминает всё, что ей прислали. Так проверяется разбор ответов без
// настоящего Redis.
type cannedServer struct {
	ln   net.Listener
	mu   sync.Mutex
	got  bytes.Buffer
	mode cannedMode
}

// cannedMode задаёт поведение заглушки после ответа.
type cannedMode int

const (
	// modeKeep оставляет соединение открытым.
	modeKeep cannedMode = iota
	// modeCloseAtOnce закрывает соединение, не отвечая: проверка обрыва связи.
	modeCloseAtOnce
	// modeCloseAfterWrite закрывает соединение сразу после ответа: так
	// проверяется недочитанный ответ.
	modeCloseAfterWrite
)

// startCanned поднимает заглушку, отвечающую заранее заданными байтами.
func startCanned(t *testing.T, canned []byte, mode cannedMode) *cannedServer {
	t.Helper()
	ln, err := listenLocal()
	if err != nil {
		t.Fatalf("не удалось поднять заглушку: %v", err)
	}
	s := &cannedServer{ln: ln, mode: mode}
	go s.serve(canned)
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

// serve принимает соединения и обслуживает каждое в своей горутине.
func (s *cannedServer) serve(canned []byte) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn, canned)
	}
}

// handle дожидается команды, отдаёт заготовленный ответ и дочитывает остальное.
func (s *cannedServer) handle(conn net.Conn, canned []byte) {
	defer func() { _ = conn.Close() }()
	if s.mode == modeCloseAtOnce {
		return
	}
	if !s.readOnce(conn) {
		return
	}
	if len(canned) > 0 {
		_, _ = conn.Write(canned)
	}
	if s.mode == modeCloseAfterWrite {
		return
	}
	for s.readOnce(conn) {
	}
}

// readOnce читает очередную порцию от клиента и запоминает её.
func (s *cannedServer) readOnce(conn net.Conn) bool {
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if n > 0 {
		s.mu.Lock()
		s.got.Write(buf[:n])
		s.mu.Unlock()
	}
	return err == nil
}

// addr возвращает адрес заглушки.
func (s *cannedServer) addr() string { return s.ln.Addr().String() }

// received возвращает всё, что заглушка получила от клиента.
func (s *cannedServer) received() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got.String()
}

// testRedisConfig возвращает параметры с коротким ожиданием: тест не должен
// висеть, если заглушка молчит.
func testRedisConfig(addr string) RedisConfig {
	cfg := DefaultRedisConfig()
	cfg.Addr = addr
	cfg.PoolSize = 2
	cfg.DialTimeout = 300 * time.Millisecond
	cfg.ReadTimeout = 300 * time.Millisecond
	cfg.WriteTimeout = 300 * time.Millisecond
	return cfg
}

// dialTo открывает соединение с заглушкой и закрывает его по окончании теста.
func dialTo(t *testing.T, cfg *RedisConfig) *respConn {
	t.Helper()
	c, err := dialRESP(cfg)
	if err != nil {
		t.Fatalf("соединение не открылось: %v", err)
	}
	t.Cleanup(c.close)
	return c
}

// TestRespReplyKinds проверяет разбор всех видов ответов протокола.
func TestRespReplyKinds(t *testing.T) {
	canned := "+OK\r\n" +
		":42\r\n" +
		"$4\r\nзд\r\n" +
		"$-1\r\n" +
		"*2\r\n$3\r\nfoo\r\n:7\r\n" +
		"*-1\r\n" +
		"*0\r\n"
	srv := startCanned(t, []byte(canned), modeKeep)
	cfg := testRedisConfig(srv.addr())
	c := dialTo(t, &cfg)

	status, err := c.do(&cfg, "PING")
	if err != nil || status.kind != kindStatus || status.text() != "OK" {
		t.Fatalf("простая строка разобрана как %+v, ошибка %v", status, err)
	}

	num, err := c.do(&cfg, "DEL", "k")
	if err != nil || num.kind != kindInt || num.num != 42 {
		t.Fatalf("целое разобрано как %+v, ошибка %v", num, err)
	}

	bulk, err := c.do(&cfg, "GET", "k")
	if err != nil || bulk.kind != kindBulk || bulk.text() != "зд" {
		t.Fatalf("объёмная строка разобрана как %+v, ошибка %v", bulk, err)
	}

	empty, err := c.do(&cfg, "GET", "missing")
	if err != nil || !empty.null {
		t.Fatalf("пустая объёмная строка разобрана как %+v, ошибка %v", empty, err)
	}

	arr, err := c.do(&cfg, "MGET", "a", "b")
	if err != nil || len(arr.arr) != 2 || arr.arr[0].text() != "foo" || arr.arr[1].num != 7 {
		t.Fatalf("массив разобран как %+v, ошибка %v", arr, err)
	}

	nullArr, err := c.do(&cfg, "MGET", "none")
	if err != nil || !nullArr.null {
		t.Fatalf("пустой массив разобран как %+v, ошибка %v", nullArr, err)
	}

	zeroArr, err := c.do(&cfg, "MGET")
	if err != nil || zeroArr.kind != kindArray || len(zeroArr.arr) != 0 {
		t.Fatalf("массив нулевой длины разобран как %+v, ошибка %v", zeroArr, err)
	}
}

// TestRespCommandEncoding проверяет, что команда уходит массивом объёмных
// строк: именно этого ждёт Redis.
func TestRespCommandEncoding(t *testing.T) {
	srv := startCanned(t, []byte("+OK\r\n"), modeKeep)
	cfg := testRedisConfig(srv.addr())
	c := dialTo(t, &cfg)
	if _, err := c.do(&cfg, "SET", "pii:id", "значение", "PX", "1000"); err != nil {
		t.Fatalf("команда не выполнилась: %v", err)
	}
	want := "*5\r\n$3\r\nSET\r\n$6\r\npii:id\r\n$16\r\nзначение\r\n$2\r\nPX\r\n$4\r\n1000\r\n"
	if got := waitFor(t, srv, len(want)); got != want {
		t.Fatalf("на заглушку пришло %q, ожидалось %q", got, want)
	}
}

// waitFor ждёт, пока заглушка получит нужное число байт.
func waitFor(t *testing.T, srv *cannedServer, n int) string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		got := srv.received()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRespServerError проверяет ответ об ошибке: он превращается в RedisError
// и не портит соединение.
func TestRespServerError(t *testing.T) {
	srv := startCanned(t, []byte("-ERR unknown command\r\n+PONG\r\n"), modeKeep)
	cfg := testRedisConfig(srv.addr())
	c := dialTo(t, &cfg)

	_, err := c.do(&cfg, "BADCMD")
	var rerr *RedisError
	if !errors.As(err, &rerr) {
		t.Fatalf("получена ошибка %v, ожидался RedisError", err)
	}
	if !strings.Contains(rerr.Error(), "unknown command") {
		t.Errorf("текст ошибки %q не содержит ответ сервера", rerr.Error())
	}
	if rep, perr := c.do(&cfg, "PING"); perr != nil || rep.text() != "PONG" {
		t.Fatalf("после ошибки сервера соединение не работает: %+v, %v", rep, perr)
	}
}

// TestRespBrokenReplies проверяет ответы, которые разобрать нельзя.
func TestRespBrokenReplies(t *testing.T) {
	cases := []struct {
		name   string
		canned string
		want   error
	}{
		{"нет возврата каретки", "+OK\n", ErrProtocol},
		{"неизвестный вид ответа", "%2\r\n", ErrProtocol},
		{"длина не число", "$abc\r\n", ErrProtocol},
		{"слишком длинное значение", "$134217728\r\n", ErrProtocol},
		{"объёмная строка без конца", "$5\r\nab", io.ErrUnexpectedEOF},
		{"объёмная строка без перевода", "$2\r\nabxx", ErrProtocol},
		{"пустая строка ответа", "\r\n", ErrProtocol},
		{"обрыв посреди массива", "*2\r\n$3\r\nfoo\r\n", io.EOF},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := startCanned(t, []byte(c.canned), modeCloseAfterWrite)
			cfg := testRedisConfig(srv.addr())
			conn := dialTo(t, &cfg)
			_, err := conn.do(&cfg, "PING")
			if !errors.Is(err, c.want) {
				t.Fatalf("получена ошибка %v, ожидалась %v", err, c.want)
			}
		})
	}
}

// TestRespConnectionClosed проверяет обрыв соединения: заглушка закрывает его
// сразу, клиент обязан вернуть ошибку, а не зависнуть.
func TestRespConnectionClosed(t *testing.T) {
	srv := startCanned(t, nil, modeCloseAtOnce)
	cfg := testRedisConfig(srv.addr())
	c := dialTo(t, &cfg)
	if _, err := c.do(&cfg, "PING"); err == nil {
		t.Fatal("обрыв соединения не дал ошибки")
	}
}

// TestRespDialUnreachable проверяет, что недоступный адрес даёт ошибку за
// отведённое время, а не блокирует вызывающего.
func TestRespDialUnreachable(t *testing.T) {
	cfg := testRedisConfig(unusedAddr(t))
	started := time.Now()
	if _, err := dialRESP(&cfg); err == nil {
		t.Fatal("соединение с закрытым портом внезапно удалось")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("ожидание соединения заняло %v", elapsed)
	}
}

// unusedAddr возвращает адрес, на котором заведомо никто не слушает.
func unusedAddr(t *testing.T) string {
	t.Helper()
	ln, err := listenLocal()
	if err != nil {
		t.Fatalf("не удалось занять порт: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestRespAuth проверяет вход по паролю: команда AUTH уходит сразу после
// открытия соединения.
func TestRespAuth(t *testing.T) {
	srv := startCanned(t, []byte("+OK\r\n"), modeKeep)
	cfg := testRedisConfig(srv.addr())
	cfg.Password = "секрет"
	c := dialTo(t, &cfg)
	_ = c
	want := "*2\r\n$4\r\nAUTH\r\n$12\r\nсекрет\r\n"
	if got := waitFor(t, srv, len(want)); got != want {
		t.Fatalf("на заглушку пришло %q, ожидалось %q", got, want)
	}
}

// TestRespAuthRejected проверяет отказ по паролю: соединение не отдаётся.
func TestRespAuthRejected(t *testing.T) {
	srv := startCanned(t, []byte("-ERR invalid password\r\n"), modeKeep)
	cfg := testRedisConfig(srv.addr())
	cfg.Password = "неверный"
	if _, err := dialRESP(&cfg); err == nil {
		t.Fatal("отказ по паролю не дал ошибки")
	}
}

// TestRespPoolReuse проверяет, что соединение возвращается в пул и второй
// запрос идёт по нему же.
func TestRespPoolReuse(t *testing.T) {
	srv := startCanned(t, []byte("+PONG\r\n+PONG\r\n"), modeKeep)
	cfg := testRedisConfig(srv.addr())
	p := newRespPool(&cfg)
	defer p.Close()
	for i := 0; i < 2; i++ {
		rep, err := p.do("PING")
		if err != nil || rep.text() != "PONG" {
			t.Fatalf("запрос %d вернул %+v, ошибка %v", i, rep, err)
		}
	}
	if len(p.idle) != 1 {
		t.Fatalf("в пуле %d свободных соединений, ожидалось 1", len(p.idle))
	}
}

// TestRespPoolDiscardsBroken проверяет, что оборванное соединение в пул не
// возвращается.
func TestRespPoolDiscardsBroken(t *testing.T) {
	srv := startCanned(t, nil, modeCloseAtOnce)
	cfg := testRedisConfig(srv.addr())
	p := newRespPool(&cfg)
	defer p.Close()
	if _, err := p.do("PING"); err == nil {
		t.Fatal("обрыв соединения не дал ошибки")
	}
	if len(p.idle) != 0 {
		t.Fatal("оборванное соединение вернулось в пул")
	}
	if len(p.sem) != p.cfg.PoolSize {
		t.Fatalf("разрешений на соединение %d, ожидалось %d", len(p.sem), p.cfg.PoolSize)
	}
}

// TestRespPoolBusy проверяет предел числа соединений: когда все заняты, вызов
// не ждёт вечно, а возвращает ошибку.
func TestRespPoolBusy(t *testing.T) {
	srv := startCanned(t, []byte("+PONG\r\n"), modeKeep)
	cfg := testRedisConfig(srv.addr())
	cfg.PoolSize = 1
	cfg.DialTimeout = 50 * time.Millisecond
	p := newRespPool(&cfg)
	defer p.Close()

	held, err := p.acquire()
	if err != nil {
		t.Fatalf("первое соединение не открылось: %v", err)
	}
	if _, err := p.acquire(); !errors.Is(err, ErrPoolBusy) {
		t.Fatalf("получена ошибка %v, ожидалась %v", err, ErrPoolBusy)
	}
	p.release(held)
	if _, err := p.acquire(); err != nil {
		t.Fatalf("освободившееся соединение не выдано: %v", err)
	}
}

// TestRespPoolClosed проверяет работу после закрытия пула.
func TestRespPoolClosed(t *testing.T) {
	srv := startCanned(t, []byte("+PONG\r\n"), modeKeep)
	cfg := testRedisConfig(srv.addr())
	p := newRespPool(&cfg)
	if _, err := p.do("PING"); err != nil {
		t.Fatalf("запрос до закрытия не прошёл: %v", err)
	}
	p.Close()
	p.Close()
	if _, err := p.do("PING"); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("получена ошибка %v, ожидалась %v", err, ErrPoolClosed)
	}
}
