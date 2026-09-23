package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"pii-guard/internal/capture"
	"pii-guard/internal/config"
	"pii-guard/internal/logging"
	"pii-guard/internal/store"
)

// processRequest — тело запроса проверяющей системы.
type processRequest struct {
	Payload   *string `json:"payload"`
	PayloadID *string `json:"payload_id"`
}

// processResponse — тело успешного ответа.
type processResponse struct {
	Result string `json:"result"`
}

// direction — что именно делает обработчик с запросом.
type direction string

const (
	dirMask        direction = "mask"
	dirMaskRetry   direction = "mask_retry"
	dirDemask      direction = "demask"
	dirAmbiguous   direction = "ambiguous"
	dirUnknownMask direction = "unknown_id"
)

// handleProcess реализует единый контракт POST /process: первый запрос с новым
// идентификатором маскирует текст, повторный с тем же идентификатором и ранее
// выданной маской — восстанавливает исходный текст.
//
// Направление определяется содержимым тела, а не счётчиком запросов:
// проверяющая система повторяет запросы при ошибках и таймаутах, поэтому один
// и тот же запрос может прийти несколько раз.
func (s *Server) handleProcess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "поддерживается только POST")
		return
	}

	cfg := s.Config()
	sys, ok := s.resolveSystem(r, cfg)
	if !ok {
		s.writeError(w, r, http.StatusForbidden, "system_not_allowed", "система не опознана или отключена")
		return
	}

	body := http.MaxBytesReader(w, r.Body, cfg.Server.MaxBodyBytes)
	dec := json.NewDecoder(body)
	var req processRequest
	if err := dec.Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "тело запроса превышает допустимый размер")
			return
		}
		s.writeValidation(w, r, "json_invalid", []string{"body"}, "не удалось разобрать JSON")
		return
	}
	if req.Payload == nil {
		s.writeValidation(w, r, "missing", []string{"body", "payload"}, "поле payload обязательно")
		return
	}
	if req.PayloadID == nil {
		s.writeValidation(w, r, "missing", []string{"body", "payload_id"}, "поле payload_id обязательно")
		return
	}

	payload := *req.Payload
	id := *req.PayloadID

	release, ok := s.acquire(r.Context(), len(payload) >= cfg.Limits.HeavyThresholdBytes, cfg.Limits)
	if !ok {
		w.Header().Set("Retry-After", "1")
		s.writeError(w, r, http.StatusTooManyRequests, "overloaded", "сервис перегружен, повторите запрос")
		return
	}
	defer release()

	started := time.Now()
	result, dir, code, err := s.processOne(id, payload, sys, cfg)
	if err != nil {
		// Профиль проверяющей системы никогда не отвечает пятисотой ошибкой:
		// пять подряд невалидных ответов останавливают весь прогон.
		if sys.ErrorMode(cfg.Defaults) == config.OnErrorOpen {
			took := time.Since(started)
			s.metrics.ObserveDegraded(sys.Name)
			w.Header().Set("X-PII-Degraded", "1")
			s.writeJSON(w, http.StatusOK, processResponse{Result: payload})
			// Счётчиков нет: разбор не дошёл до конца, и найденное считать не по
			// чему.
			ev := processEvent{payloadID: id, dir: dirMask, size: len(payload), took: took}
			s.logProcess(r, sys.Name, ev, true)
			// Текст ушёл назад неизменённым, то есть персональные данные в нём
			// остались. Для отчётности это событие важнее удачного
			// маскирования, поэтому в аудит оно идёт обязательно.
			s.auditProcess(r, sys, ev, "degraded")
			return
		}
		w.Header().Set("Retry-After", "1")
		s.writeError(w, r, http.StatusServiceUnavailable, "internal_degraded", "временная ошибка обработки")
		return
	}
	if code != http.StatusOK {
		s.writeError(w, r, code, "demask_forbidden", "демаскирование недоступно для этой системы")
		// Отказанная попытка развернуть маску это тоже обращение к
		// персональным данным, и для службы контроля оно интереснее
		// удавшегося: видно, кто просил чужое.
		s.auditProcess(r, sys, processEvent{
			payloadID: id, dir: dirDemask, size: len(payload), took: time.Since(started),
		}, "forbidden")
		return
	}

	took := time.Since(started)
	s.writeJSON(w, http.StatusOK, processResponse{Result: result.text})
	s.metrics.ObserveProcess(sys.Name, string(dir), took, len(payload), result.counts)
	ev := processEvent{payloadID: id, dir: dir, size: len(payload), counts: result.counts, took: took}
	s.logProcess(r, sys.Name, ev, false)
	s.auditProcess(r, sys, ev, "ok")
	s.captureProcess(sys.Name, id, dir, payload, result.text, result.counts, took)
}

// captureProcess сохраняет обработку в отдельный файл для последующей
// проверки качества на настоящих текстах.
//
// Канал отдельный от журнала намеренно: в журнал и показатели исходные данные
// попадать не должны, это требование задания и отдельная проверка ворот. Сюда
// они попадают только при двух включённых признаках сразу — самом захвате и
// разрешении сохранять текст.
//
// Вызов не блокирует обработчик: запись уходит в очередь, а при полной
// очереди отбрасывается и считается.
func (s *Server) captureProcess(system, payloadID string, dir direction, payload, result string, counts map[string]int, took time.Duration) {
	if !s.capture.Enabled() {
		return
	}
	s.capture.Write(capture.Record{
		Time:       time.Now().UTC(),
		System:     system,
		PayloadID:  payloadID,
		Direction:  string(dir),
		PayloadLen: len(payload),
		Counts:     counts,
		TookMS:     float64(took.Nanoseconds()) / 1e6,
		Payload:    payload,
		Result:     result,
	})
}

// processEvent — сведения об одной обработке: что за текст, в какую сторону
// его вели, сколько нашли и сколько это заняло. Журнал и аудит описывают одно
// и то же событие и раньше принимали эти пять значений по отдельности —
// соседними параметрами одного типа, которые легко переставить местами и
// ничего при этом не заметить. Собранные вместе, они ещё и считаются один раз
// на месте вызова и уходят обоим получателям.
type processEvent struct {
	payloadID string
	dir       direction
	size      int
	counts    map[string]int
	took      time.Duration
}

// auditProcess записывает обращение к персональным данным в журнал аудита.
// В записи только типы, их число и размер текста: ни исходного текста, ни
// найденных значений, ни самого ключа доступа — от ключа остаётся отпечаток,
// по которому видно, что ключ тот же, но не видно самого ключа.
func (s *Server) auditProcess(r *http.Request, sys config.System, ev processEvent, result string) {
	if !s.audit.Enabled() {
		return
	}
	ctx := r.Context()
	s.audit.Write(ctx, logging.AuditEvent{
		Op:        string(ev.dir),
		System:    sys.Name,
		Actor:     logging.ActorFromKey(r.Header.Get(sys.Auth.Header)),
		PayloadID: ev.payloadID,
		Bytes:     ev.size,
		Types:     ev.counts,
		Result:    result,
		Duration:  ev.took,
	})
}

// outcome — результат обработки одного запроса.
type outcome struct {
	text   string
	counts map[string]int
}

// flightResult — решение по одному запросу, которое объединение одинаковых
// запросов отдаёт сразу всем, кто его ждал. Тип лежит на уровне пакета, потому
// что часть решений принимает demaskEntry, а разбирает их processOne.
type flightResult struct {
	out  outcome
	dir  direction
	code int
}

// processOne определяет направление и выполняет маскирование либо обратное
// преобразование. Одновременные одинаковые запросы объединяются, чтобы повтор
// от проверяющей системы не считал маску дважды.
func (s *Server) processOne(id, payload string, sys config.System, cfg *config.Config) (outcome, direction, int, error) {
	hash := sha256.Sum256([]byte(payload))
	key := id + "\x00" + hex.EncodeToString(hash[:8])

	v, err, _ := s.flight.Do(key, func() (any, error) {
		entry, found := s.store.Get(id)
		switch {
		case !found:
			res, e := s.maskAndStore(id, payload, sys, cfg)
			if e != nil {
				return nil, e
			}
			return flightResult{out: res, dir: dirMask, code: http.StatusOK}, nil

		case entry.OrigHash == hash:
			// Повтор запроса на маскирование: отдаём ту же маску и ничего
			// не перезаписываем.
			return flightResult{out: outcome{text: entry.Mask, counts: countsOf(entry)}, dir: dirMaskRetry, code: http.StatusOK}, nil

		case entry.MaskHash == hash:
			return s.demaskEntry(entry, sys.Demask, sys.Name)

		default:
			// Тот же идентификатор, но текст не совпадает ни с исходным, ни с
			// маской: маскируем присланное и запись не портим.
			res := s.maskOnly(payload, sys, cfg)
			s.metrics.ObserveAmbiguous(sys.Name)
			return flightResult{out: res, dir: dirAmbiguous, code: http.StatusOK}, nil
		}
	})
	if err != nil {
		return outcome{}, dirMask, http.StatusOK, err
	}
	fr, ok := v.(flightResult)
	if !ok {
		return outcome{}, dirMask, http.StatusOK, errors.New("внутренняя ошибка объединения запросов")
	}
	return fr.out, fr.dir, fr.code, nil
}

// demaskEntry разворачивает маску обратно в исходный текст. Вынесено из
// processOne, потому что это единственное направление с проверкой прав:
// развернуть маску можно только системе, которой она выдана, и только если
// демаскирование ей разрешено настройкой. Система передана двумя полями, а не
// целиком: больше для решения ничего не нужно.
func (s *Server) demaskEntry(entry *store.Entry, demaskAllowed bool, system string) (flightResult, error) {
	if !demaskAllowed {
		return flightResult{code: http.StatusForbidden}, nil
	}
	if entry.System != "" && entry.System != system {
		return flightResult{code: http.StatusForbidden}, nil
	}
	original, err := s.store.Original(entry)
	if err != nil {
		return flightResult{}, err
	}
	return flightResult{out: outcome{text: original, counts: countsOf(entry)}, dir: dirDemask, code: http.StatusOK}, nil
}

// maskAndStore маскирует текст и сохраняет соответствие.
func (s *Server) maskAndStore(id, payload string, sys config.System, cfg *config.Config) (outcome, error) {
	res := s.engine.Mask(payload, sys, cfg.Defaults)
	if _, err := s.store.Put(id, payload, res.Text, sys.Name, res.Meta()); err != nil {
		return outcome{}, err
	}
	return outcome{text: res.Text, counts: countsToStrings(res)}, nil
}

// maskOnly маскирует текст, не трогая хранилище.
func (s *Server) maskOnly(payload string, sys config.System, cfg *config.Config) outcome {
	res := s.engine.Mask(payload, sys, cfg.Defaults)
	return outcome{text: res.Text, counts: countsToStrings(res)}
}

// countsOf восстанавливает число фрагментов по метаданным записи.
func countsOf(e *store.Entry) map[string]int {
	out := make(map[string]int, len(e.Spans))
	for _, sp := range e.Spans {
		out[sp.Type]++
	}
	return out
}

// resolveSystem опознаёт систему по заголовку с ключом. Проверяющая система
// заголовков не шлёт, поэтому запросы без ключа достаются единственной
// анонимной системе из настроек.
//
// Здесь же имя опознанной системы кладётся в данные запроса. Дальше его не
// нужно передавать руками: обработчик журнала берёт его из контекста сам, и
// поле system появляется во всех записях запроса, включая те, что пишет
// прослойка уже после возврата из обработчика. Точка одна на три входа:
// /process, /v1/inspect и /v1/chat/completions зовут resolveSystem.
func (s *Server) resolveSystem(r *http.Request, cfg *config.Config) (config.System, bool) {
	sys, ok := keyedSystem(r, cfg)
	if !ok {
		// Ключ прислали, но он не подошёл ни одной системе.
		if headerPresent(r, cfg) {
			return config.System{}, false
		}
		sys, ok = cfg.AnonymousSystem()
	}
	if ok {
		logging.SetSystem(r.Context(), sys.Name)
	}
	return sys, ok
}

// keyedSystem ищет систему, чей ключ доступа совпал с присланным. Вынесено
// отдельно, потому что этой же проверкой закрыта служебная ручка смены
// уровня журнала: запасного пути в анонимную систему там быть не должно.
func keyedSystem(r *http.Request, cfg *config.Config) (config.System, bool) {
	for _, sys := range cfg.Systems {
		if !sys.Enabled || sys.Auth.None || sys.Auth.Header == "" {
			continue
		}
		provided := strings.TrimSpace(r.Header.Get(sys.Auth.Header))
		if provided == "" {
			continue
		}
		sum := sha256.Sum256([]byte(provided))
		if hex.EncodeToString(sum[:]) == sys.Auth.KeySHA256 {
			return sys, true
		}
	}
	return config.System{}, false
}

// headerPresent сообщает, что в запросе есть хотя бы один известный заголовок
// с ключом доступа.
func headerPresent(r *http.Request, cfg *config.Config) bool {
	for _, sys := range cfg.Systems {
		if sys.Auth.Header != "" && r.Header.Get(sys.Auth.Header) != "" {
			return true
		}
	}
	return false
}
