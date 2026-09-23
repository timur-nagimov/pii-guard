package api

import (
	_ "embed"
	"log/slog"
	"net/http"
	"strconv"

	"pii-guard/internal/logging"
)

// uiPage — встроенная разметка страницы проверки. Файл лежит рядом с кодом и
// попадает в двоичный файл при сборке, поэтому сервису нечего искать на диске
// и незачем ходить наружу: в закрытом контуре внешние ресурсы недоступны.
//
//go:embed ui/index.html
var uiPage string

// uiScript и uiStyles — сценарий и стили той же страницы. Раньше они лежали
// прямо в разметке, но одним файлом страница выросла до длины, на которой её
// неудобно ни читать, ни разбирать. Встраиваются они ровно так же, как и
// разметка, поэтому самодостаточность страницы от разделения не пострадала:
// все три файла лежат в двоичном файле и отдаются этим же сервисом.
//
//go:embed ui/app.js
var uiScript string

//go:embed ui/app.css
var uiStyles string

// uiContentSecurityPolicy закрепляет самодостаточность страницы на уровне
// браузера: загрузка чего-либо извне запрещена правилом, а не только
// договорённостью. Стили и сценарий разрешены только со своего же источника,
// обращения — тоже.
//
// Разрешения 'unsafe-inline' здесь больше нет: после выноса стилей и сценария
// в отдельные файлы встроенных стилей и сценариев на странице не осталось,
// а разрешение, которое ничего не разрешает, только ослабляет политику.
//
// Картинки разрешены только схемой data: значок вкладки лежит строкой в самой
// разметке, и это единственная картинка на странице; сетевые схемы остаются
// запрещёнными, поэтому послабление не открывает дорогу наружу.
const uiContentSecurityPolicy = "default-src 'none'; " +
	"style-src 'self'; " +
	"script-src 'self'; " +
	"img-src data:; " +
	"connect-src 'self'; " +
	"form-action 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'"

// uiAsset — встроенный файл страницы и то, чем он является для браузера.
//
// Тип содержимого у каждого файла свой и указывается явно: сервис отвечает
// с X-Content-Type-Options: nosniff, поэтому браузер не станет угадывать тип
// сам и просто не применит файл, отданный не тем, чем он является.
type uiAsset struct {
	body string
	// mime — значение заголовка Content-Type.
	mime string
	// policy — политика безопасности; задаётся только у самой страницы,
	// потому что браузер применяет её к документу, а не к файлам, которые
	// документ подтянул.
	policy string
}

// handleUI отдаёт разметку страницы проверки. Страница показывает, что именно
// нашлось в тексте, где и по какому признаку: это нужно для отладки правил,
// потому что рабочий контракт возвращает только готовый результат.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	s.writeUIAsset(w, r, uiAsset{
		body:   uiPage,
		mime:   "text/html; charset=utf-8",
		policy: uiContentSecurityPolicy,
	})
}

// handleUIScript отдаёт сценарий страницы.
func (s *Server) handleUIScript(w http.ResponseWriter, r *http.Request) {
	s.writeUIAsset(w, r, uiAsset{body: uiScript, mime: "text/javascript; charset=utf-8"})
}

// handleUIStyles отдаёт стили страницы.
func (s *Server) handleUIStyles(w http.ResponseWriter, r *http.Request) {
	s.writeUIAsset(w, r, uiAsset{body: uiStyles, mime: "text/css; charset=utf-8"})
}

// writeUIAsset отдаёт встроенный файл страницы. Правила одинаковы для всех
// трёх файлов, поэтому они собраны здесь, а не размножены по обработчикам.
//
// Кеширование запрещено: файлы правят по ходу отладки, и браузер не должен
// показывать вчерашнюю страницу поверх сегодняшней сборки. Особенно это важно
// после разделения: разметка, стили и сценарий приходят разными ответами,
// и подмешанный из кеша старый сценарий не совпал бы с новой разметкой.
func (s *Server) writeUIAsset(w http.ResponseWriter, r *http.Request, a uiAsset) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "поддерживается только GET")
		return
	}

	h := w.Header()
	h.Set("Content-Type", a.mime)
	h.Set("Content-Length", strconv.Itoa(len(a.body)))
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	h.Set("Pragma", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	if a.policy != "" {
		h.Set("Content-Security-Policy", a.policy)
	}

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(a.body)); err != nil {
		s.log.WarnContext(r.Context(), "не удалось отдать файл страницы проверки",
			logging.Component("api"), slog.String(logging.FieldPath, r.URL.Path),
			logging.Err(err))
	}
}
