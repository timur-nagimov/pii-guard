package api

import (
	"net/http"
	"sort"

	"pii-guard/internal/config"
	"pii-guard/internal/logging"
)

// systemInfo — публичные сведения о системе-потребителе для страницы проверки.
// Секретов здесь нет: ни ключей доступа, ни адресов модели. Странице нужно
// знать, какие системы есть и чем они различаются, чтобы дать выбрать профиль,
// а ключи и адреса ей не нужны — они остаются на сервере.
type systemInfo struct {
	Name           string   `json:"name"`
	Preset         string   `json:"preset"`
	Demask         bool     `json:"demask"`
	Types          []string `json:"types"`
	HasUpstream    bool     `json:"has_upstream"`
	InspectEnabled bool     `json:"inspect_enabled"`
}

// handleSystems отдаёт список включённых систем-потребителей. Страница проверки
// заполняет им выбор профиля: переключение между системами показывает, как
// меняются вид маски, набор типов и право на обратное преобразование.
func (s *Server) handleSystems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "поддерживается только GET")
		return
	}

	cfg := s.Config()
	out := make([]systemInfo, 0, len(cfg.Systems))
	for name, sys := range cfg.Systems {
		if !sys.Enabled {
			continue
		}
		types := sys.Types
		if len(types) == 0 {
			types = []string{"all"}
		}
		out = append(out, systemInfo{
			Name:           name,
			Preset:         string(sys.MaskOptions(cfg.Defaults).Default),
			Demask:         sys.Demask,
			Types:          types,
			HasUpstream:    sys.Upstream.URL != "",
			InspectEnabled: sys.InspectOn(cfg.Defaults),
		})
	}
	// Порядок систем в настройках не гарантирован, а выбор на странице должен
	// быть стабильным между перезагрузками.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	s.writeJSON(w, http.StatusOK, map[string]any{"systems": out})
}

// systemByName возвращает включённую систему по имени. Страница проверки
// выбирает профиль системы-потребителя и передаёт его имя в запросе; сервер
// сам находит настройки, ключ доступа странице не нужен.
func (s *Server) systemByName(name string, cfg *config.Config) (config.System, bool) {
	if name == "" {
		return config.System{}, false
	}
	sys, ok := cfg.System(name)
	if !ok || !sys.Enabled {
		return config.System{}, false
	}
	return sys, true
}

// applySystemOverride подменяет систему запроса на выбранную страницей.
// Возвращает false, когда имя указано, но такой системы нет: это отказ, а не
// молчаливый возврат к системе по ключу. Вызывается только с непустым именем.
func (s *Server) applySystemOverride(r *http.Request, cfg *config.Config, name string) (config.System, bool) {
	sys, ok := s.systemByName(name, cfg)
	if !ok {
		return config.System{}, false
	}
	logging.SetSystem(r.Context(), sys.Name)
	return sys, true
}

// resolveRequestSystem определяет систему для запроса. Страница проверки
// передаёт имя профиля и не знает ключей доступа; имя задано — берём систему
// по имени, имени нет — опознаём по ключу, как обычный потребитель.
func (s *Server) resolveRequestSystem(r *http.Request, cfg *config.Config, name string) (config.System, bool) {
	if name != "" {
		return s.applySystemOverride(r, cfg, name)
	}
	return s.resolveSystem(r, cfg)
}
