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

	// Узлы живут в разметке, а обращения к сервису — в сценарии, поэтому
	// после разделения страницы каждый признак ищется в своём файле: искать
	// их вместе значило бы не заметить, что один из файлов подменили пустым.
	required := []string{
		`<textarea id="text"`,
		`<select id="preset"`,
		`<button id="run"`,
		`<button id="roundtrip"`,
		`id="highlight"`,
		`id="masked"`,
		`id="found"`,
		`id="skipped"`,
		`id="panel-coverage"`,
		`id="coverage-body"`,
	}
	for _, needle := range required {
		if !strings.Contains(body, needle) {
			t.Errorf("на странице нет обязательного элемента %q", needle)
		}
	}

	script := uiScriptBody(t)

	requiredScript := []string{
		`/v1/inspect`,
		`/process`,
		`/v1/coverage`,
		`setInterval(refreshCoverage, 5000)`,
	}
	for _, needle := range requiredScript {
		if !strings.Contains(script, needle) {
			t.Errorf("в сценарии страницы нет обязательного обращения %q", needle)
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
// Внешних ресурсов там просто нет, поэтому ищем любое упоминание сетевой схемы
// сразу во всех трёх файлах: после разделения ссылка наружу могла бы
// завестись не только в разметке.
func TestUIPageHasNoExternalLinks(t *testing.T) {
	for path, body := range uiFiles(t) {
		for _, scheme := range []string{"http://", "https://"} {
			if idx := strings.Index(body, scheme); idx >= 0 {
				t.Errorf("%s ссылается наружу: найдено %q в позиции %d, фрагмент: %q",
					path, scheme, idx, snippet(body, idx))
			}
		}
	}
	// Стили подключаются одним файлом, и подтягивать из него что-то ещё
	// незачем: @import — это поход за файлом уже из стилей, мимо разметки.
	if strings.Contains(uiStylesBody(t), "@import") {
		t.Error("стили подключают ещё один файл через @import")
	}

	// Дальше — все адреса в разметке. Страница собрана из трёх файлов, и два
	// из них подключаются тегами, поэтому запрет на подключения целиком снят
	// быть не может: допустимы ровно два вида адреса — свой файл этого же
	// сервиса и встроенная строкой картинка. Любой другой адрес означал бы
	// поход за файлом наружу.
	body := uiBody(t)
	for _, attr := range []string{`src="`, `href="`} {
		for rest := body; ; {
			idx := strings.Index(rest, attr)
			if idx < 0 {
				break
			}
			rest = rest[idx+len(attr):]
			end := strings.Index(rest, `"`)
			if end < 0 {
				t.Fatalf("незакрытый адрес в разметке: %q", snippet(rest, 0))
			}
			addr := rest[:end]
			if !strings.HasPrefix(addr, "/ui/") && !strings.HasPrefix(addr, "data:") {
				t.Errorf("страница подключает посторонний файл: %s%s", attr, addr)
			}
		}
	}
	// Подключения своих файлов обязаны быть на месте: без них страница
	// осталась бы голой разметкой без стилей и без единого действия.
	// Сценарий подключается модулем: в модуле строгий режим включён всегда,
	// поэтому директива 'use strict' внутри него не нужна и была убрана.
	// Проверка следит и за этим: без type="module" файл выполнится в нестрогом
	// режиме, и снятая директива тихо изменит поведение.
	for _, tag := range []string{`<link rel="stylesheet" href="/ui/app.css">`, `<script type="module" src="/ui/app.js"></script>`} {
		if !strings.Contains(body, tag) {
			t.Errorf("страница не подключает свой файл тегом %q", tag)
		}
	}
}

// TestUIAssetsServed проверяет, что сценарий и стили отдаются своими адресами
// и своим типом содержимого. Тип здесь не мелочь: сервис отвечает с nosniff,
// поэтому сценарий, отданный не как сценарий, браузер просто не выполнит —
// и страница молча перестанет работать.
//
// Запрос идёт через сборку маршрутов, а не прямо в обработчик: адреса вписаны
// в разметку страницы, и проверять их в обход маршрута бессмысленно.
func TestUIAssetsServed(t *testing.T) {
	assets := []struct {
		path string
		mime string
	}{
		{"/ui/app.js", "text/javascript; charset=utf-8"},
		{"/ui/app.css", "text/css; charset=utf-8"},
	}

	for _, a := range assets {
		resp := getUIAsset(t, http.MethodGet, a.path)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: тело не прочиталось: %v", a.path, err)
		}

		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: код ответа %d, ожидался 200", a.path, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != a.mime {
			t.Errorf("%s: тип содержимого %q, ожидался %q", a.path, got, a.mime)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: заголовок X-Content-Type-Options = %q, ожидался nosniff", a.path, got)
		}
		if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
			t.Errorf("%s: кеширование не запрещено: Cache-Control = %q", a.path, got)
		}
		if len(body) == 0 {
			t.Errorf("%s: файл пустой: встраивание не сработало", a.path)
		}
	}
}

// TestUIAssetsRejectNonGet проверяет, что сценарий и стили, как и сама
// страница, отвечают только на GET: менять их через сервис нельзя.
func TestUIAssetsRejectNonGet(t *testing.T) {
	for _, path := range []string{"/ui/app.js", "/ui/app.css"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			resp := getUIAsset(t, method, path)
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: код %d, ожидался 405", method, path, resp.StatusCode)
			}
			if got := resp.Header.Get("Allow"); got != http.MethodGet {
				t.Errorf("%s %s: заголовок Allow = %q, ожидался GET", method, path, got)
			}
			if !strings.Contains(string(body), "method_not_allowed") {
				t.Errorf("%s %s: в ответе нет кода ошибки method_not_allowed", method, path)
			}
		}
	}
}

// TestUIPolicyAllowsOwnFilesOnly закрепляет политику безопасности страницы:
// после выноса стилей и сценария в отдельные файлы браузер обязан их взять
// у этого же сервиса и не обязан больше исполнять ничего встроенного.
func TestUIPolicyAllowsOwnFilesOnly(t *testing.T) {
	resp := getUI(t, http.MethodGet)
	resp.Body.Close()

	policy := resp.Header.Get("Content-Security-Policy")
	for _, part := range []string{"default-src 'none'", "style-src 'self'", "script-src 'self'", "connect-src 'self'"} {
		if !strings.Contains(policy, part) {
			t.Errorf("в политике страницы нет %q: политика = %q", part, policy)
		}
	}
	// Встроенных стилей и сценариев на странице не осталось, поэтому
	// разрешение на них только ослабило бы политику.
	if strings.Contains(policy, "unsafe-inline") {
		t.Errorf("политика разрешает встроенный код: %q", policy)
	}
	// Файлам страницы политика документа не нужна и ничего не ограничивает:
	// браузер применяет её к документу, а не к тому, что документ подтянул.
	for _, path := range []string{"/ui/app.js", "/ui/app.css"} {
		resp := getUIAsset(t, http.MethodGet, path)
		if got := resp.Header.Get("Content-Security-Policy"); got != "" {
			t.Errorf("%s: политика документа на файле лишняя: %q", path, got)
		}
		resp.Body.Close()
	}
}

// TestUIPageBuildsNoMarkupFromInput закрепляет способ вставки текста на
// страницу. Пользователь вставляет произвольный текст, и он не должен
// становиться разметкой, поэтому строковой сборки разметки на странице нет:
// весь текст попадает на экран только значением узла.
func TestUIPageBuildsNoMarkupFromInput(t *testing.T) {
	forbidden := []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("}
	for path, body := range uiFiles(t) {
		for _, call := range forbidden {
			if strings.Contains(body, call) {
				t.Errorf("%s собирает разметку строкой через %q: вставленный текст может стать разметкой", path, call)
			}
		}
	}
	// Выводит текст сценарий, поэтому и способ вывода ищется в нём.
	if !strings.Contains(uiScriptBody(t), "textContent") {
		t.Error("сценарий не использует textContent: непонятно, чем он выводит текст")
	}
}

// TestUIPageHasRuleManagement закрепляет раздел управления правилами: свои
// типы, их удаление и выбор типов у каждой системы.
//
// Раздел легко потерять при правке страницы, а без него матрица покрытия
// только показывает дыры, но не даёт их закрыть.
func TestUIPageHasRuleManagement(t *testing.T) {
	body := uiBody(t)

	required := []string{
		`id="panel-rules"`,
		`id="rules-custom"`,  // перечень своих типов
		`id="rules-systems"`, // типы у каждой системы
		`id="rule-add"`,      // добавление типа
		`id="rule-pattern"`,  // выражение нового типа
		`id="rules-state"`,   // видно ли, что правка недоступна
		`id="rules-token-input"`,
	}
	for _, needle := range required {
		if !strings.Contains(body, needle) {
			t.Errorf("в разделе правил нет обязательного элемента %q", needle)
		}
	}

	script := uiScriptBody(t)

	// Сами действия над правилами выполняет сценарий, поэтому их наличие
	// проверяется в нём: узлы без обращений были бы мёртвой разметкой.
	requiredScript := []string{
		`'/v1/rules'`,
		`op: 'add_type'`,
		`op: 'remove_type'`,
		`op: 'set_system_types'`,
		`op: 'set_system_enabled'`,
		`X-Admin-Token`,
	}
	for _, needle := range requiredScript {
		if !strings.Contains(script, needle) {
			t.Errorf("в сценарии правил нет обязательного действия %q", needle)
		}
	}
}

// TestUIPageKeepsAdminTokenOutOfBrowserStorage закрепляет обращение с
// признаком управления: он секрет, и хранилищу браузера его доверять нельзя —
// оно переживает закрытие вкладки и доступно любому сценарию на странице.
func TestUIPageKeepsAdminTokenOutOfBrowserStorage(t *testing.T) {
	for path, body := range uiFiles(t) {
		for _, storage := range []string{"localStorage", "sessionStorage", "document.cookie"} {
			if strings.Contains(body, storage) {
				t.Errorf("%s обращается к %q: признак управления может там осесть", path, storage)
			}
		}
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

// getUIAsset запрашивает файл страницы через сборку маршрутов. В отличие от
// самой страницы, адреса сценария и стилей вписаны в разметку, поэтому
// проверять их в обход маршрута нельзя: ошибка в адресе осталась бы незамеченной
// ровно до открытия страницы в браузере.
func getUIAsset(t *testing.T, method, path string) *http.Response {
	t.Helper()

	srv := newUIServer(t)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Result()
}

// uiScriptBody возвращает сценарий страницы так, как его получает браузер.
func uiScriptBody(t *testing.T) string {
	t.Helper()
	return uiAssetBody(t, "/ui/app.js")
}

// uiStylesBody возвращает стили страницы так, как их получает браузер.
func uiStylesBody(t *testing.T) string {
	t.Helper()
	return uiAssetBody(t, "/ui/app.css")
}

// uiAssetBody читает тело файла страницы целиком.
func uiAssetBody(t *testing.T, path string) string {
	t.Helper()

	resp := getUIAsset(t, http.MethodGet, path)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: код ответа %d, ожидался 200", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: тело не прочиталось: %v", path, err)
	}
	return string(body)
}

// uiFiles возвращает все файлы страницы разом, подписанные названием. Требования
// закрытого контура и безопасной вставки текста одинаковы для разметки,
// сценария и стилей, а разнесены они по трём файлам — значит и проверять их
// нужно все три, иначе запрет обойдётся переносом строки в соседний файл.
func uiFiles(t *testing.T) map[string]string {
	t.Helper()

	return map[string]string{
		"страница": uiBody(t),
		"сценарий": uiScriptBody(t),
		"стили":    uiStylesBody(t),
	}
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
