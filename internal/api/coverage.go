package api

import (
	"net/http"

	"pii-guard/internal/config"
	"pii-guard/internal/pii"
)

// Источники типов в матрице покрытия. Разделение нужно, чтобы с одного взгляда
// было видно, какие типы пришли из технического задания, какие добавлены ядром
// сверх задания, а какие — настройкой через custom_types без правки кода.
const (
	coverageSourceTask   = "task"
	coverageSourceExtra  = "extra"
	coverageSourceCustom = "custom"

	// coverageSourcePart — выделяемая часть типа из задания, а не отдельный
	// тип. Раздел 4.1 перечисляет семнадцать типов, и десятый записан так:
	// «Адрес (в т.ч. отдельно: страна, индекс, город, улица, дом, квартира
	// и т.п.)». Индекс мы выделяем отдельным типом, потому что он находится
	// и маскируется самостоятельно, но считать его восемнадцатым типом
	// задания нельзя: получилось бы, что покрытие шире требуемого.
	coverageSourcePart = "task_part"
)

// coverageParts — типы, которые задание называет частями других типов.
var coverageParts = map[pii.Type]bool{
	pii.TypePostcode: true,
}

// Значения клетки матрицы, когда пресета нет.
const (
	// coverageDisabled — система выключена настройкой целиком.
	coverageDisabled = "disabled"
	// coverageOff — система включена, но этот тип ей не маскируется.
	coverageOff = "off"
)

// coverageSystem — одна система-потребитель в матрице покрытия.
type coverageSystem struct {
	Name          string  `json:"name"`
	Enabled       bool    `json:"enabled"`
	Demask        bool    `json:"demask"`
	MinConfidence float64 `json:"min_confidence"`
}

// coverageType — один тип персональных данных в матрице.
type coverageType struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// coverageResponse — матрица покрытия: какие типы каким системам маскируются,
// каким видом маски, что выключено и чего нет вовсе.
type coverageResponse struct {
	Systems []coverageSystem `json:"systems"`
	Types   []coverageType   `json:"types"`
	// Cells — вид маски для пары тип-система. Внешний ключ — имя типа,
	// внутренний — имя системы. Значение — пресет, либо coverageDisabled для
	// выключенной системы, либо coverageOff для типа, который система не
	// маскирует.
	Cells map[string]map[string]string `json:"cells"`
}

// handleCoverage отдаёт матрицу покрытия. Ручка читает действующие настройки
// на каждый запрос, поэтому отражает перечитывание конфигурации без
// перезапуска: страница опрашивает её раз в пять секунд, в такт с самими
// настройками.
func (s *Server) handleCoverage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		s.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "поддерживается только GET")
		return
	}
	s.writeJSON(w, http.StatusOK, buildCoverage(s.Config()))
}

// buildCoverage собирает матрицу из уже разобранных настроек. Новой логики
// здесь нет: только раскладка того, что сервис и так знает о системах и типах.
func buildCoverage(cfg *config.Config) coverageResponse {
	systems := make([]coverageSystem, 0, len(cfg.Systems))
	for name, sys := range cfg.Systems {
		systems = append(systems, coverageSystem{
			Name:          name,
			Enabled:       sys.Enabled,
			Demask:        sys.Demask,
			MinConfidence: sys.MinConf(cfg.Defaults),
		})
	}

	types := collectCoverageTypes(cfg)

	cells := make(map[string]map[string]string, len(types))
	for _, t := range types {
		row := make(map[string]string, len(systems))
		for _, sys := range systems {
			row[sys.Name] = coverageCell(cfg, sys.Name, t.Name)
		}
		cells[t.Name] = row
	}

	return coverageResponse{Systems: systems, Types: types, Cells: cells}
}

// collectCoverageTypes собирает все типы матрицы: из задания, добавленные
// ядром сверх задания и добавленные настройкой через custom_types.
func collectCoverageTypes(cfg *config.Config) []coverageType {
	seen := make(map[pii.Type]bool)
	var out []coverageType

	add := func(t pii.Type, source string) {
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, coverageType{Name: string(t), Source: source})
	}

	for _, t := range pii.AllTypes() {
		if coverageParts[t] {
			add(t, coverageSourcePart)
			continue
		}
		add(t, coverageSourceTask)
	}
	for _, t := range extraTypes() {
		add(t, coverageSourceExtra)
	}
	for _, ct := range cfg.CustomTypes {
		add(ct.Name, coverageSourceCustom)
	}
	return out
}

// extraTypes — типы, добавленные ядром сверх раздела 4.1 технического задания:
// документы, удостоверяющие личность, кроме паспорта РФ.
func extraTypes() []pii.Type {
	return []pii.Type{
		pii.TypeSNILS, pii.TypeForeignPassport, pii.TypeResidencePermit,
		pii.TypeBirthCert, pii.TypeMilitaryID,
	}
}

// coverageCell возвращает вид маски для пары тип-система. Выключенная система
// помечается целиком; включённая, но не маскирующая тип, — отдельно.
func coverageCell(cfg *config.Config, systemName, typeName string) string {
	sys, ok := cfg.Systems[systemName]
	if !ok || !sys.Enabled {
		return coverageDisabled
	}
	t := pii.Type(typeName)
	if !sys.Allows(t) {
		return coverageOff
	}
	return string(sys.MaskOptions(cfg.Defaults).PresetFor(t))
}
