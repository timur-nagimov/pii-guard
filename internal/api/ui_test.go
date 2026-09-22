package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// newUIServer собирает сервис для проверки страницы. Маршрут страницы
// добавляет сборка сервиса целиком, поэтому обработчик здесь вызывается
// напрямую: тест не зависит от того, по какому адресу страницу повесили.
func newUIServer(t *testing.T) *Server {
	t.Helper()

	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 16})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	cfg := &config.Config{
		Limits: config.Limits{Inflight: 4, HeavyInflight: 2, MaxWait: time.Second},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, st, engine.New(pii.NewRegistry()), metrics.New(), log)
}

// getUI запрашивает страницу выбранным методом и возвращает ответ целиком.
func getUI(t *testing.T, method string) *http.Response {
	t.Helper()

	srv := newUIServer(t)
	rec := httptest.NewRecorder()
	srv.handleUI(rec, httptest.NewRequest(method, "/ui", nil))
	return rec.Result()
}

// TestUIPageServed проверяет главное: страница отдаётся, опознаётся браузером
// как разметка и не оседает в кеше.
func TestUIPageServed(t *testing.T) {
	resp := getUI(t, http.MethodGet)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("код ответа %d, ожидался 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("тип содержимого %q, ожидался text/html; charset=utf-8", got)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("кеширование не запрещено: Cache-Control = %q", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("тело не прочиталось: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("страница пустая: встраивание файла не сработало")
	}
}

// TestUIPageElements проверяет, что на странице есть всё, ради чего её
// открывают: поле ввода, кнопка разбора и выбор вида маскирования.
func TestUIPageElements(t *testing.T) {
	body := uiBody(t)

	required := []string{
		`<textarea id="text"`,
		`<select id="preset"`,
		`<button id="run"`,
		`<button id="roundtrip"`,
		`id="highlight"`,
		`id="masked"`,
		`id="found"`,
		`id="skipped"`,
		`/v1/inspect`,
		`/process`,
	}
	for _, needle := range required {
		if !strings.Contains(body, needle) {
			t.Errorf("на странице нет обязательного элемента %q", needle)
		}
	}

	// Все виды маскирования должны быть доступны выбором, иначе страницей
	// не сравнить виды на одном и том же тексте.
	for _, preset := range []string{"full", "full_ws", "partial", "initials", "token", "synthetic"} {
		if !strings.Contains(body, `value="`+preset+`"`) {
			t.Errorf("в выборе вида маскирования нет значения %q", preset)
		}
	}
}

// TestUIRejectsNonGet проверяет, что страница отвечает только на GET.
func TestUIRejectsNonGet(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		resp := getUI(t, method)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("метод %s дал код %d, ожидался 405", method, resp.StatusCode)
		}
		if got := resp.Header.Get("Allow"); got != http.MethodGet {
			t.Errorf("метод %s: заголовок Allow = %q, ожидался GET", method, got)
		}
		if !strings.Contains(string(body), "method_not_allowed") {
			t.Errorf("метод %s: в ответе нет кода ошибки method_not_allowed", method)
		}
	}
}

// TestUIPageHasNoExternalLinks — проверка закрытого контура: страница не имеет
// права ходить наружу ни за стилями, ни за шрифтами, ни за сценариями.
// Внешних ресурсов там просто нет, поэтому ищем любое упоминание сетевой схемы.
func TestUIPageHasNoExternalLinks(t *testing.T) {
	body := uiBody(t)

	for _, scheme := range []string{"http://", "https://"} {
		if idx := strings.Index(body, scheme); idx >= 0 {
			t.Errorf("страница ссылается наружу: найдено %q в позиции %d, фрагмент: %q",
				scheme, idx, snippet(body, idx))
		}
	}
	// Встроенная страница обязана быть самодостаточной, поэтому подключений
	// внешних файлов на ней быть не может в принципе.
	for _, tag := range []string{"<link ", "<script src", "@import"} {
		if strings.Contains(body, tag) {
			t.Errorf("страница подключает внешний файл через %q", tag)
		}
	}
}

// TestUIPageBuildsNoMarkupFromInput закрепляет способ вставки текста на
// страницу. Пользователь вставляет произвольный текст, и он не должен
// становиться разметкой, поэтому строковой сборки разметки на странице нет:
// весь текст попадает на экран только значением узла.
func TestUIPageBuildsNoMarkupFromInput(t *testing.T) {
	body := uiBody(t)

	forbidden := []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("}
	for _, call := range forbidden {
		if strings.Contains(body, call) {
			t.Errorf("страница собирает разметку строкой через %q: вставленный текст может стать разметкой", call)
		}
	}
	if !strings.Contains(body, "textContent") {
		t.Error("страница не использует textContent: непонятно, чем она выводит текст")
	}
}

// uiBody возвращает тело страницы так, как его получает браузер.
func uiBody(t *testing.T) string {
	t.Helper()

	resp := getUI(t, http.MethodGet)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("тело не прочиталось: %v", err)
	}
	return string(body)
}

// snippet вырезает окрестность позиции, чтобы в отчёте теста было видно,
// какая именно ссылка нашлась.
func snippet(body string, idx int) string {
	from := idx - 40
	if from < 0 {
		from = 0
	}
	to := idx + 60
	if to > len(body) {
		to = len(body)
	}
	return body[from:to]
}
