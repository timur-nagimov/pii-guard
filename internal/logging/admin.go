package logging

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// maxLevelTTL — предел срока, на который поднимают подробность журнала.
// Забытый отладочный уровень на потолке нагрузки это семь мегабайт записей в
// секунду, поэтому уровень возвращается сам.
const maxLevelTTL = time.Hour

// LevelHandler отдаёт ручку управления уровнем журнала. Она закрывает
// требование менять подробность без перезапуска:
//
//	GET  /admin/loglevel                      текущий уровень
//	POST /admin/loglevel?level=debug&ttl=5m   поднять подробность на пять минут
//	POST /admin/loglevel?level=info           вернуть обратно
//
// Без срока уровень держится до следующей команды; со сроком возвращается
// прежний сам. Это защита от забытого отладочного уровня на боевой машине.
//
// Ручка меняет поведение сервиса, поэтому её нельзя выставлять наружу.
// Допустимые способы закрыть: отдельный слушатель на петлевом адресе,
// проверка ключа доступа через allow, запрет на балансировщике.
func (l *Logger) LevelHandler(allow func(*http.Request) bool) http.Handler {
	c := &levelControl{log: l}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allow != nil && !allow(r) {
			http.Error(w, "нет доступа", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeLevel(w, http.StatusOK, l.LevelName(), "")
		case http.MethodPost, http.MethodPut:
			c.set(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		}
	})
}

// levelControl хранит отложенный возврат уровня.
type levelControl struct {
	log   *Logger
	mu    sync.Mutex
	timer *time.Timer
}

func (c *levelControl) set(w http.ResponseWriter, r *http.Request) {
	prev := c.log.LevelName()
	if err := c.log.SetLevel(r.URL.Query().Get("level")); err != nil {
		writeLevel(w, http.StatusBadRequest, prev, err.Error())
		return
	}
	c.schedule(prev, parseTTL(r.URL.Query().Get("ttl")))
	writeLevel(w, http.StatusOK, c.log.LevelName(), "")
}

// schedule заводит возврат к прежнему уровню. Прежний таймер снимается,
// иначе две команды подряд вернули бы уровень дважды и не туда.
func (c *levelControl) schedule(prev string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if ttl <= 0 {
		return
	}
	c.timer = time.AfterFunc(ttl, func() {
		if err := c.log.SetLevel(prev); err != nil {
			c.log.Slog().Warn("не удалось вернуть уровень журнала",
				slog.String(FieldEvent, EventLogLevel), Err(err))
		}
	})
}

// LevelName возвращает имя действующего уровня.
func (l *Logger) LevelName() string { return LevelName(l.level.Level()) }

func parseTTL(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0
	}
	if d > maxLevelTTL {
		return maxLevelTTL
	}
	return d
}

func writeLevel(w http.ResponseWriter, code int, level, errText string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	body := map[string]string{"level": level}
	if errText != "" {
		body["error"] = errText
	}
	_ = json.NewEncoder(w).Encode(body)
}
