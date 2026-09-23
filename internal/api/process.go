package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/logging"
	"pii-guard/internal/mask"
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
	pr, err := s.processOne(id, payload, sys, cfg)
	if err != nil {
		// Сбой обработки, при котором частичного результата нет: хранилище
		// недоступно или обработчик упал вне детекторов. Открытый текст не
		// возвращается ни в каком режиме: персональные данные не выходят наружу.
		w.Header().Set("Retry-After", "1")
		s.writeError(w, r, http.StatusServiceUnavailable, "internal_degraded", "временная ошибка обработки")
		return
	}
	if pr.code != http.StatusOK {
		s.writeError(w, r, pr.code, pr.errCode, pr.errMsg)
		// Отказанная попытка развернуть маску это тоже обращение к
		// персональным данным, и для службы контроля оно интереснее
		// удавшегося: видно, кто просил чужое или уже несуществующее.
		s.auditProcess(r, sys, id, pr.dir, len(payload), nil, time.Since(started), pr.auditResult)
		return
	}

	took := time.Since(started)
	// Деградация: часть детекторов или кусков отказала, текст обработан
	// остальными. В щадящем режиме отдаём частичную маску с признаком
	// деградации, в строгом — просим повторить. Открытый текст не отдаётся
	// ни в одном из режимов.
	if pr.out.degraded {
		if sys.ErrorMode(cfg.Defaults) == config.OnErrorClosed {
			w.Header().Set("Retry-After", "1")
			s.writeError(w, r, http.StatusServiceUnavailable, "internal_degraded", "временная ошибка обработки")
			return
		}
		s.metrics.ObserveDegraded(sys.Name)
		w.Header().Set("X-PII-Degraded", "1")
		s.writeJSON(w, http.StatusOK, processResponse{Result: pr.out.text})
		s.logProcess(r, sys.Name, id, pr.dir, len(payload), pr.out.counts, took, true, pr.out.failedTypes)
		s.auditProcess(r, sys, id, pr.dir, len(payload), pr.out.counts, took, "degraded")
		return
	}

	if pr.unknownID {
		w.Header().Set("X-PII-Unknown-Id", "1")
	}
	if pr.ambiguous {
		w.Header().Set("X-PII-Ambiguous", "1")
	}
	s.writeJSON(w, http.StatusOK, processResponse{Result: pr.out.text})
	s.metrics.ObserveProcess(sys.Name, string(pr.dir), took, len(payload), pr.out.counts)
	s.logProcess(r, sys.Name, id, pr.dir, len(payload), pr.out.counts, took, false, nil)
	s.auditProcess(r, sys, id, pr.dir, len(payload), pr.out.counts, took, "ok")
}

// auditProcess записывает обращение к персональным данным в журнал аудита.
// В записи только типы, их число и размер текста: ни исходного текста, ни
// найденных значений, ни самого ключа доступа — от ключа остаётся отпечаток,
// по которому видно, что ключ тот же, но не видно самого ключа.
func (s *Server) auditProcess(r *http.Request, sys config.System, payloadID string, dir direction, size int, counts map[string]int, took time.Duration, result string) {
	if !s.audit.Enabled() {
		return
	}
	ctx := r.Context()
	s.audit.Write(ctx, logging.AuditEvent{
		Op:        string(dir),
		System:    sys.Name,
		Actor:     logging.ActorFromKey(r.Header.Get(sys.Auth.Header)),
		PayloadID: payloadID,
		Bytes:     size,
		Types:     counts,
		Result:    result,
		Duration:  took,
	})
}

// outcome — результат обработки одного запроса.
type outcome struct {
	text   string
	counts map[string]int
	// degraded — признак того, что часть детекторов или кусков отказала и
	// текст обработан не полностью.
	degraded bool
	// failedTypes — имена типов, чьи детекторы отказали. Только имена, без
	// значений: список безопасно писать в журнал.
	failedTypes []string
}

// processResult — итог обработки одного запроса: либо успешный результат с
// направлением, либо отказ с кодом и описанием.
type processResult struct {
	out outcome
	dir direction
	// code — код ответа. Для успеха это 200, для отказа — 403, 404 или 409.
	code int
	// errCode и errMsg — код и описание ошибки для отказа.
	errCode string
	errMsg  string
	// auditResult — чем закончилось обращение для журнала аудита.
	auditResult string
	// unknownID — признак того, что текст вернулся как есть, потому что
	// запись не найдена, а режим on_unknown_id требует пропустить текст.
	unknownID bool
	// ambiguous — признак неоднозначного повтора: тот же идентификатор с
	// третьим текстом.
	ambiguous bool
}

// processOne определяет направление и выполняет маскирование либо обратное
// преобразование. Одновременные одинаковые запросы объединяются, чтобы повтор
// от проверяющей системы не считал маску дважды.
func (s *Server) processOne(id, payload string, sys config.System, cfg *config.Config) (processResult, error) {
	hash := sha256.Sum256([]byte(payload))
	key := id + "\x00" + hex.EncodeToString(hash[:8])

	v, err, _ := s.flight.Do(key, func() (any, error) {
		entry, gerr := s.store.Get(id)
		switch {
		case errors.Is(gerr, store.ErrCorrupted):
			// Запись есть, но не расшифровывается: чужой ключ шифрования или
			// испорченное значение. Маскировать повторно нельзя — текст мог
			// быть уже замаскирован, и звёздочки стали бы «исходником».
			return processResult{dir: dirDemask, code: http.StatusConflict,
				errCode: "payload_corrupted", errMsg: "запись повреждена или зашифрована другим ключом",
				auditResult: "corrupted"}, nil

		case errors.Is(gerr, store.ErrNotFound):
			// Записи нет: либо идентификатор новый, либо срок хранения истёк.
			// Если присланный текст уже похож на маску, маскировать его
			// повторно нельзя: звёздочки стали бы «исходником», и оригинал
			// был бы потерян. Поведение задаёт режим on_unknown_id.
			if mask.LooksMasked(payload, sys.MaskOptions(cfg.Defaults).Default) {
				switch sys.UnknownIDMode(cfg.Defaults) {
				case config.OnUnknownPassthrough:
					return processResult{out: outcome{text: payload}, dir: dirUnknownMask,
						code: http.StatusOK, unknownID: true, auditResult: "unknown_id"}, nil
				default:
					return processResult{dir: dirDemask, code: http.StatusNotFound,
						errCode: "payload_not_found", errMsg: "срок хранения истёк или идентификатор неизвестен",
						auditResult: "not_found"}, nil
				}
			}
			res, e := s.maskAndStore(id, payload, sys, cfg)
			if e != nil {
				return nil, e
			}
			return processResult{out: res, dir: dirMask, code: http.StatusOK, auditResult: "ok"}, nil

		case gerr != nil:
			// Временная ошибка хранилища: частичного результата нет.
			return nil, gerr

		case entry == nil:
			// Запись не найдена без ошибки: считаем её отсутствующей.
			res, e := s.maskAndStore(id, payload, sys, cfg)
			if e != nil {
				return nil, e
			}
			return processResult{out: res, dir: dirMask, code: http.StatusOK, auditResult: "ok"}, nil

		case entry.OrigHash == hash:
			// Повтор запроса на маскирование: отдаём ту же маску и ничего
			// не перезаписываем.
			return processResult{out: outcome{text: entry.Mask, counts: countsOf(entry)}, dir: dirMaskRetry,
				code: http.StatusOK, auditResult: "ok"}, nil

		case entry.MaskHash == hash:
			if !sys.Demask {
				return processResult{dir: dirDemask, code: http.StatusForbidden,
					errCode: "demask_forbidden", errMsg: "демаскирование недоступно для этой системы",
					auditResult: "forbidden"}, nil
			}
			if entry.System != "" && entry.System != sys.Name {
				return processResult{dir: dirDemask, code: http.StatusForbidden,
					errCode: "demask_forbidden", errMsg: "демаскирование недоступно для этой системы",
					auditResult: "forbidden"}, nil
			}
			original, e := s.store.Original(entry)
			if e != nil {
				return nil, e
			}
			return processResult{out: outcome{text: original, counts: countsOf(entry)}, dir: dirDemask,
				code: http.StatusOK, auditResult: "ok"}, nil

		default:
			// Тот же идентификатор, но текст не совпадает ни с исходным, ни с
			// маской: маскируем присланное и запись не портим. Ответ несёт
			// заголовок X-PII-Ambiguous, чтобы клиент видел неоднозначность.
			res := s.maskOnly(payload, sys, cfg)
			s.metrics.ObserveAmbiguous(sys.Name)
			return processResult{out: res, dir: dirAmbiguous, code: http.StatusOK,
				ambiguous: true, auditResult: "ok"}, nil
		}
	})
	if err != nil {
		return processResult{}, err
	}
	pr, ok := v.(processResult)
	if !ok {
		return processResult{}, errors.New("внутренняя ошибка объединения запросов")
	}
	return pr, nil
}

// maskAndStore маскирует текст и сохраняет соответствие.
func (s *Server) maskAndStore(id, payload string, sys config.System, cfg *config.Config) (outcome, error) {
	res := s.engine.Mask(payload, sys, cfg.Defaults)
	if _, err := s.store.Put(id, payload, res.Text, sys.Name, res.Meta()); err != nil {
		return outcome{}, err
	}
	return outcome{text: res.Text, counts: countsToStrings(res), degraded: res.Degraded, failedTypes: res.FailedTypes}, nil
}

// maskOnly маскирует текст, не трогая хранилище.
func (s *Server) maskOnly(payload string, sys config.System, cfg *config.Config) outcome {
	res := s.engine.Mask(payload, sys, cfg.Defaults)
	return outcome{text: res.Text, counts: countsToStrings(res), degraded: res.Degraded, failedTypes: res.FailedTypes}
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
