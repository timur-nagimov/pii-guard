package api

import (
	_ "embed"
	"net/http"
	"strconv"

	"pii-guard/internal/logging"
)

// uiPage — встроенная страница проверки. Файл лежит рядом с кодом и попадает
// в двоичный файл при сборке, поэтому сервису нечего искать на диске и незачем
// ходить наружу: в закрытом контуре внешние ресурсы недоступны.
//
//go:embed ui/index.html
var uiPage string

// uiContentSecurityPolicy закрепляет самодостаточность страницы на уровне
// браузера: загрузка чего-либо извне запрещена правилом, а не только
// договорённостью. Встроенные стили и сценарий разрешены явно, обращения
// разрешены только к своему же сервису.
//
// Картинки разрешены только схемой data: значок вкладки лежит строкой в самой
// разметке, и это единственная картинка на странице; сетевые схемы остаются
// запрещёнными, поэтому послабление не открывает дорогу наружу.
const uiContentSecurityPolicy = "default-src 'none'; " +
	"style-src 'unsafe-inline'; " +
	"script-src 'unsafe-inline'; " +
	"img-src data:; " +
	"connect-src 'self'; " +
	"form-action 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'"

// handleUI отдаёт страницу проверки. Страница показывает, что именно нашлось
// в тексте, где и по какому признаку: это нужно для отладки правил, потому
// что рабочий контракт возвращает только готовый результат.
//
// Кеширование запрещено: страницу правят по ходу отладки, и браузер не должен
// показывать вчерашнюю разметку поверх сегодняшней сборки.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "поддерживается только GET")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(uiPage)))
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	h.Set("Pragma", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", uiContentSecurityPolicy)

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(uiPage)); err != nil {
		s.log.WarnContext(r.Context(), "не удалось отдать страницу проверки",
			logging.Component("api"), logging.Err(err))
	}
}
