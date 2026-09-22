package logging

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestMiddlewareRequestID проверяет три правила сразу: присланный клиентом
// идентификатор сохраняется, отсутствующий порождается, ответ всегда несёт
// заголовок с идентификатором.
func TestMiddlewareRequestID(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	var seen string
	h := Middleware(l, DefaultMiddlewareOptions())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
		SetSystem(r.Context(), "crm")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/process", nil)
	req.Header.Set(HeaderRequestID, "client-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen != "client-123" {
		t.Errorf("идентификатор клиента не подхвачен: %q", seen)
	}
	if got := rec.Header().Get(HeaderRequestID); got != "client-123" {
		t.Errorf("идентификатор не вернулся в заголовке: %q", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/process", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if len(rec.Header().Get(HeaderRequestID)) != 32 {
		t.Errorf("идентификатор не порождён: %q", rec.Header().Get(HeaderRequestID))
	}

	recs := records(t, buf)
	if len(recs) != 2 {
		t.Fatalf("ожидались две записи о запросах, получено %d", len(recs))
	}
	if recs[0][FieldSystem] != "crm" {
		t.Errorf("система не попала в запись: %v", recs[0])
	}
	if recs[0][FieldStatus] != float64(http.StatusOK) {
		t.Errorf("код ответа не записан: %v", recs[0])
	}
}

// TestMiddlewareRejectsBadID проверяет, что чужая строка не попадает в журнал
// как есть: непригодный идентификатор заменяется своим.
func TestMiddlewareRejectsBadID(t *testing.T) {
	l, _ := newTestLogger(t, DefaultConfig())
	h := Middleware(l, DefaultMiddlewareOptions())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/process", nil)
	req.Header.Set(HeaderRequestID, "Иванов Иван Иванович")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get(HeaderRequestID); len(got) != 32 {
		t.Fatalf("непригодный идентификатор принят: %q", got)
	}
}

// TestMiddlewareSampling проверяет прореживание: успешные запросы пишутся
// через одного, ошибки пишутся всегда.
func TestMiddlewareSampling(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	opts := DefaultMiddlewareOptions()
	opts.SampleN = 10
	code := http.StatusOK
	h := Middleware(l, opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
	for i := 0; i < 30; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/process", nil))
	}
	if n := len(records(t, buf)); n != 3 {
		t.Fatalf("прореживание один к десяти дало %d записей на тридцать запросов", n)
	}
	// Отказы прореживание не трогает, но их подхватывает глушитель повторов:
	// на потолке нагрузки поток отказов иначе затопил бы журнал. Первый отказ
	// выходит сразу, остальные считаются и выходят числом.
	code = http.StatusTooManyRequests
	for i := 0; i < 5; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/process", nil))
	}
	if n := len(records(t, buf)); n != 4 {
		t.Fatalf("первый отказ должен выйти сразу, остальные заглушаются: получено %d записей", n)
	}
	if got := l.Stats().Suppressed.Load(); got != 4 {
		t.Fatalf("заглушено %d повторов вместо четырёх", got)
	}
}

// TestMiddlewareSkipsService проверяет, что служебные ручки не засоряют журнал.
func TestMiddlewareSkipsService(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	h := Middleware(l, DefaultMiddlewareOptions())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/metrics", "/healthz", "/readyz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if n := len(records(t, buf)); n != 0 {
		t.Fatalf("служебные ручки попали в журнал: %d записей", n)
	}
}

// TestMiddlewareSlow проверяет, что медленный запрос пишется вопреки прореживанию.
func TestMiddlewareSlow(t *testing.T) {
	l, buf := newTestLogger(t, DefaultConfig())
	opts := DefaultMiddlewareOptions()
	opts.SampleN = 1000
	opts.Slow = time.Millisecond
	h := Middleware(l, opts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/process", nil))
	if n := len(records(t, buf)); n != 1 {
		t.Fatalf("медленный запрос не записан: %d записей", n)
	}
}

// TestPropagate проверяет сквозной идентификатор для обращения к языковой модели.
func TestPropagate(t *testing.T) {
	ctx := WithRequestID(t.Context(), "abc")
	req := httptest.NewRequest(http.MethodPost, "http://model/v1/chat/completions", nil)
	Propagate(ctx, req)
	if got := req.Header.Get(HeaderRequestID); got != "abc" {
		t.Fatalf("идентификатор не ушёл к модели: %q", got)
	}
}
