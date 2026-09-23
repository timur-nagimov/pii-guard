package api

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// corruptedRedis — заглушка Redis, которая на чтение отдаёт испорченное
// значение. Нужна, чтобы проверить поведение сервиса при ошибке расшифровки
// записи: чужой ключ шифрования или повреждённое значение.
type corruptedRedis struct {
	ln net.Listener
}

// startCorruptedRedis поднимает заглушку и останавливает её по окончании теста.
func startCorruptedRedis(t *testing.T) *corruptedRedis {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("не удалось поднять заглушку Redis: %v", err)
	}
	f := &corruptedRedis{ln: ln}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *corruptedRedis) addr() string { return f.ln.Addr().String() }

func (f *corruptedRedis) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *corruptedRedis) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	for {
		args, err := readRESPArray(br)
		if err != nil {
			return
		}
		if len(args) == 0 {
			return
		}
		var resp []byte
		switch strings.ToUpper(args[0]) {
		case "PING":
			resp = []byte("+PONG\r\n")
		case "SET":
			resp = []byte("+OK\r\n")
		case "GET":
			// Испорченное значение: длиннее метки целостности, но не
			// расшифровывается. Хранилище обязано сообщить о повреждении.
			resp = []byte("$20\r\nxxxxxxxxxxxxxxxxxxxx\r\n")
		default:
			resp = []byte("-ERR unknown command\r\n")
		}
		if _, err := conn.Write(resp); err != nil {
			return
		}
	}
}

// readRESPArray читает массив объёмных строк протокола RESP.
func readRESPArray(br *bufio.Reader) ([]string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 || line[0] != '*' {
		return nil, io.ErrUnexpectedEOF
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		head, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if len(head) < 3 || head[0] != '$' {
			return nil, io.ErrUnexpectedEOF
		}
		length, err := strconv.Atoi(strings.TrimSpace(head[1:]))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, length+2)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:length]))
	}
	return args, nil
}

// TestProcessCorruptedPayload проверяет поведение при ошибке расшифровки
// записи: запись есть, но не расшифровывается (чужой ключ или испорченное
// значение). Сервис отвечает 409 с кодом payload_corrupted, а не маскирует
// текст повторно.
func TestProcessCorruptedPayload(t *testing.T) {
	f := startCorruptedRedis(t)

	cfg, err := config.Parse([]byte(testConfigYAML()))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{
		Key:   make([]byte, 32),
		TTL:   time.Hour,
		Redis: store.RedisConfig{Addr: f.addr()},
	})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, st, engine.New(reg), metrics.New(), log)

	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	code, body := do(t, ts, processCall{body: processBody(t, "Телефон +79161234567", "payload-corrupted")})
	if code != http.StatusConflict {
		t.Fatalf("получен код %d вместо 409: %s", code, body)
	}
	if !strings.Contains(body, "payload_corrupted") {
		t.Fatalf("в ответе нет кода ошибки payload_corrupted: %s", body)
	}
	if strings.Contains(body, "+79161234567") {
		t.Fatalf("исходное значение утекло в ответ об ошибке: %s", body)
	}
}
