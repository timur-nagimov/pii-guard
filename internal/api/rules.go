package api

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/logging"
	"pii-guard/internal/pii"
)

// Управление правилами через интерфейс.
//
// Почему это отдельная ручка, а не правка файла руками. Файл настроек —
// единственный источник правды, и так остаётся: ручка правит именно его, а
// дальше срабатывает обычное перечитывание. Но требовать доступа к машине
// ради того, чтобы выключить один тип, означает, что этого не сделают вовсе.
//
// Почему права строже, чем у остальных ручек. Ключ системы-потребителя даёт
// право слать текст на маскирование — и только. Правка правил меняет то, что
// сервис защищает, и система, которая могла бы выключить собственное
// маскирование, обесценивает саму защиту. Поэтому запись доступна только с
// самой машины либо по отдельному признаку управления.

// ruleWriteTokenEnv — переменная окружения с признаком управления правилами.
// Пустая переменная означает, что править правила можно только с самой
// машины: так ручка безопасна по умолчанию, без настройки.
const ruleWriteTokenEnv = "PII_ADMIN_TOKEN"

// ruleWriteHeader — заголовок, которым предъявляется признак управления.
const ruleWriteHeader = "X-Admin-Token"

// customTypeView — описание своего типа для интерфейса.
type customTypeView struct {
	Name          string   `json:"name"`
	Pattern       string   `json:"pattern"`
	Group         int      `json:"group"`
	Validator     string   `json:"validator,omitempty"`
	Anchors       []string `json:"anchors,omitempty"`
	RequireAnchor bool     `json:"require_anchor"`
	AnchorWindow  int      `json:"anchor_window,omitempty"`
}

// ruleSystemView — какие типы маскирует система.
//
// Ключей доступа и адресов модели здесь нет намеренно: ручка чтения открыта,
// как и матрица покрытия, а секреты остаются на сервере.
type ruleSystemView struct {
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	AllTypes     bool     `json:"all_types"`
	Types        []string `json:"types"`
	AllowPersons []string `json:"allow_persons"`
}

// handleRules отдаёт и меняет правила.
func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleRulesGet(w, r)
	case http.MethodPost:
		s.handleRulesPost(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "поддерживаются GET и POST")
	}
}

// handleRulesGet перечисляет действующие правила.
func (s *Server) handleRulesGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()

	custom := make([]customTypeView, 0, len(cfg.CustomTypes))
	for _, ct := range cfg.CustomTypes {
		custom = append(custom, customTypeView{
			Name:          string(ct.Name),
			Pattern:       ct.Pattern,
			Group:         ct.Group,
			Validator:     ct.Validator,
			Anchors:       ct.Anchors,
			RequireAnchor: ct.RequireAnchor,
			AnchorWindow:  ct.AnchorWindow,
		})
	}

	systems := make([]ruleSystemView, 0, len(cfg.Systems))
	for name, sys := range cfg.Systems {
		types := make([]string, 0, len(sys.Types))
		for _, t := range sys.Types {
			if !strings.EqualFold(t, "all") {
				types = append(types, strings.ToUpper(t))
			}
		}
		systems = append(systems, ruleSystemView{
			Name:         name,
			Enabled:      sys.Enabled,
			AllTypes:     sys.AllTypes,
			Types:        types,
			AllowPersons: sys.Exclusions.AllowPersons,
		})
	}
	// Порядок в настройках не определён, а список в интерфейсе должен быть
	// одинаковым между обновлениями страницы.
	sort.Slice(systems, func(i, j int) bool { return systems[i].Name < systems[j].Name })

	builtin := make([]string, 0)
	customNames := make(map[string]bool, len(custom))
	for _, c := range custom {
		customNames[strings.ToUpper(c.Name)] = true
	}
	for _, t := range pii.AllTypes() {
		if !customNames[strings.ToUpper(string(t))] {
			builtin = append(builtin, string(t))
		}
	}
	sort.Strings(builtin)

	s.writeJSON(w, http.StatusOK, map[string]any{
		"builtin_types": builtin,
		"custom_types":  custom,
		"systems":       systems,
		// Может ли этот запрос менять правила. Интерфейс по этому признаку
		// показывает кнопки правки или объясняет, чего не хватает: кнопка,
		// которая всегда отвечает отказом, хуже её отсутствия.
		"writable":      s.ruleWriteAllowed(r),
		"write_enabled": s.rulesWritable(),
	})
}

// ruleChange — запрошенная правка.
type ruleChange struct {
	Op      string `json:"op"`
	Name    string `json:"name"`
	System  string `json:"system"`
	Enabled *bool  `json:"enabled"`
	Types   []string
	Persons []string
	Type    *customTypeView `json:"type"`
}

// UnmarshalJSON разбирает правку вручную, чтобы отличить незаданный список
// типов от пустого: первое — ошибка в запросе, второе — попытка снять все
// галочки, и отвечать на них надо по-разному.
func (c *ruleChange) UnmarshalJSON(data []byte) error {
	type alias struct {
		Op      string          `json:"op"`
		Name    string          `json:"name"`
		System  string          `json:"system"`
		Enabled *bool           `json:"enabled"`
		Types   *[]string       `json:"types"`
		Persons *[]string       `json:"persons"`
		Type    *customTypeView `json:"type"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	c.Op, c.Name, c.System, c.Enabled, c.Type = a.Op, a.Name, a.System, a.Enabled, a.Type
	if a.Types != nil {
		c.Types = *a.Types
		if c.Types == nil {
			c.Types = []string{}
		}
	}
	if a.Persons != nil {
		c.Persons = *a.Persons
		if c.Persons == nil {
			c.Persons = []string{}
		}
	}
	return nil
}

// handleRulesPost применяет правку.
func (s *Server) handleRulesPost(w http.ResponseWriter, r *http.Request) {
	started := time.Now()

	if !s.rulesWritable() {
		s.writeError(w, r, http.StatusNotImplemented, "rules_readonly",
			"сервис запущен без пути к файлу настроек, править правила через интерфейс нельзя")
		return
	}
	actor, ok := s.ruleWriteActor(r)
	if !ok {
		s.auditRuleChange(r, "", actor, "denied", started)
		s.writeError(w, r, http.StatusForbidden, "forbidden",
			"правка правил доступна с самой машины либо по признаку управления в заголовке "+ruleWriteHeader)
		return
	}

	var change ruleChange
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&change); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "запрос не разобран: "+err.Error())
		return
	}

	res, err := s.applyRuleChange(change)
	if err != nil {
		s.auditRuleChange(r, change.Op, actor, "error", started)
		s.log.Warn("правка правил отклонена",
			logging.Component("rules"), slog.String("op", change.Op),
			slog.String("actor", actor), logging.Err(err))
		s.writeError(w, r, http.StatusBadRequest, "rule_rejected", err.Error())
		return
	}

	// Файл записан; теперь настройки надо применить. Пока этого не сделано,
	// правка существует только на диске: сервис продолжает работать со
	// старыми правилами.
	effective := "within_5s"
	var warning string
	if s.reload != nil {
		if rerr := s.reload(); rerr != nil {
			// Файл уже изменён, и откатывать его нельзя: человек мог править
			// его и руками. Говорим прямо, что записано, но не применено.
			effective = "failed"
			warning = "правка записана в файл, но применить настройки не удалось: " + rerr.Error()
			s.log.Error("настройки не применены после правки правил",
				logging.Component("rules"), slog.String("op", change.Op),
				slog.String("actor", actor), logging.Err(rerr))
		} else {
			effective = "immediately"
		}
	}

	s.auditRuleChange(r, change.Op, actor, "ok", started)
	s.log.Info("правила изменены",
		logging.Component("rules"), slog.String("op", change.Op),
		slog.String("actor", actor), slog.String("change", res.Message),
		slog.String("effective", effective))

	out := map[string]any{
		"applied": true,
		"message": res.Message,
		// Когда правка начнёт действовать: сразу, в течение пяти секунд при
		// обходе файла или никогда. Интерфейс обязан показывать это как есть,
		// а не писать «готово» раньше, чем оно стало правдой.
		"effective": effective,
	}
	if warning != "" {
		out["warning"] = warning
	}
	s.writeJSON(w, http.StatusOK, out)
}

// applyRuleChange исполняет одну операцию над файлом настроек.
func (s *Server) applyRuleChange(c ruleChange) (config.EditResult, error) {
	path := s.configPath
	switch c.Op {
	case "add_type":
		return ruleAddType(path, c)

	case "remove_type":
		if strings.TrimSpace(c.Name) == "" {
			return config.EditResult{}, errRule("для удаления типа нужно поле name")
		}
		return config.RemoveCustomType(path, strings.ToUpper(strings.TrimSpace(c.Name)))

	case "set_system_types":
		return ruleSetSystemTypes(path, c)

	case "set_system_enabled":
		return ruleSetSystemEnabled(path, c)

	case "set_system_allow_persons":
		if strings.TrimSpace(c.System) == "" {
			return config.EditResult{}, errRule("для правки системы нужно поле system")
		}
		if c.Persons == nil {
			return config.EditResult{}, errRule("для правки списка имён нужно поле persons")
		}
		return config.SetSystemAllowPersons(path, c.System, c.Persons)

	default:
		return config.EditResult{}, errRule("неизвестная операция " + quote(c.Op) +
			"; доступны: add_type, remove_type, set_system_types, set_system_enabled, set_system_allow_persons")
	}
}

// ruleAddType добавляет свой тип. Имя приводится к верхнему регистру: типы
// сравниваются по имени, и «badge» с «BADGE» обязаны означать одно и то же.
func ruleAddType(path string, c ruleChange) (config.EditResult, error) {
	if c.Type == nil {
		return config.EditResult{}, errRule("для добавления типа нужно поле type")
	}
	return config.AddCustomType(path, config.CustomType{
		Name:          pii.Type(strings.ToUpper(strings.TrimSpace(c.Type.Name))),
		Pattern:       c.Type.Pattern,
		Group:         c.Type.Group,
		Validator:     c.Type.Validator,
		Anchors:       c.Type.Anchors,
		RequireAnchor: c.Type.RequireAnchor,
		AnchorWindow:  c.Type.AnchorWindow,
	})
}

// ruleSetSystemTypes задаёт список типов системы. Пустой список и отсутствие
// поля означают разное, поэтому проверяется именно nil: пустым списком систему
// осознанно оставляют без типов, а пропущенное поле это ошибка вызова.
func ruleSetSystemTypes(path string, c ruleChange) (config.EditResult, error) {
	if strings.TrimSpace(c.System) == "" {
		return config.EditResult{}, errRule("для правки системы нужно поле system")
	}
	if c.Types == nil {
		return config.EditResult{}, errRule("для правки типов системы нужно поле types")
	}
	return config.SetSystemTypes(path, c.System, c.Types)
}

// ruleSetSystemEnabled включает или выключает систему. Признак — указатель:
// пропущенное поле нельзя принять за «выключить», это отключило бы маскирование
// целой системе по недосмотру.
func ruleSetSystemEnabled(path string, c ruleChange) (config.EditResult, error) {
	if strings.TrimSpace(c.System) == "" {
		return config.EditResult{}, errRule("для правки системы нужно поле system")
	}
	if c.Enabled == nil {
		return config.EditResult{}, errRule("для включения или выключения системы нужно поле enabled")
	}
	return config.SetSystemEnabled(path, c.System, *c.Enabled)
}

// rulesWritable сообщает, знает ли сервис, какой файл править.
func (s *Server) rulesWritable() bool { return s.configPath != "" }

// ruleWriteActor опознаёт того, кто правит правила.
//
// Возвращает пометку для журнала и право на правку. Пометка заполняется и при
// отказе: запись об отказанной попытке нужна не меньше, чем об удавшейся.
func (s *Server) ruleWriteActor(r *http.Request) (string, bool) {
	if r == nil {
		return "unknown", false
	}
	if isLoopback(r.RemoteAddr) {
		return "loopback", true
	}
	want := strings.TrimSpace(os.Getenv(ruleWriteTokenEnv))
	got := strings.TrimSpace(r.Header.Get(ruleWriteHeader))
	if want == "" {
		return "remote:no_token_configured", false
	}
	// Сравнение за постоянное время: обычное сравнение строк выдаёт длину
	// совпавшего начала временем работы, и признак управления подбирается
	// посимвольно.
	if subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1 {
		return "token", true
	}
	return "remote:bad_token", false
}

// ruleWriteAllowed сообщает, вправе ли этот запрос менять правила.
func (s *Server) ruleWriteAllowed(r *http.Request) bool {
	_, ok := s.ruleWriteActor(r)
	return ok
}

// auditRuleChange записывает попытку правки в журнал аудита.
//
// Пишется и отказ, и успех: правка правил меняет то, что сервис защищает, и
// след от неё обязан оставаться независимо от исхода.
func (s *Server) auditRuleChange(r *http.Request, op, actor, result string, started time.Time) {
	if !s.audit.Enabled() {
		return
	}
	// У отказа по правам операция неизвестна: тело неавторизованного запроса
	// не разбирается вовсе. Писать «rules:» с пустым хвостом значило бы
	// делать вид, что операцию знали и не записали.
	name := "rules"
	if op != "" {
		name += ":" + op
	}
	s.audit.Write(r.Context(), logging.AuditEvent{
		Op:       name,
		Actor:    actor,
		Result:   result,
		Duration: time.Since(started),
	})
}

// ruleError — отказ правки, который можно показать человеку целиком.
type ruleError string

func (e ruleError) Error() string { return string(e) }

func errRule(msg string) error { return ruleError(msg) }

func quote(s string) string {
	if s == "" {
		return "«»"
	}
	return "«" + s + "»"
}
