package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// rulesConfigYAML — настройки для проверок управления правилами: одна система
// без ключа и один свой тип, который можно убрать.
const rulesConfigYAML = `# Заголовок, который обязан пережить правку.
defaults:
  preset: full
  min_confidence: 0.5

systems:
  alfasonar:
    enabled: true
    types: [all]

custom_types:
  - name: SNILS
    pattern: '(\d{3}-\d{3}-\d{3}[ -]?\d{2})'
    group: 1
    validator: snils
`

// newRulesServer поднимает сервер с настройками во временном файле и
// возвращает обработчик вместе с путём к этому файлу.
func newRulesServer(t *testing.T) (http.Handler, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(rulesConfigYAML), 0o600); err != nil {
		t.Fatalf("настройки не записаны: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	st, err := store.New(store.Config{Key: make([]byte, 32), TTL: time.Hour, MaxRecords: 100})
	if err != nil {
		t.Fatalf("хранилище не создалось: %v", err)
	}
	t.Cleanup(st.Close)

	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	eng := engine.New(reg)
	srv := New(cfg, st, eng, metrics.New(), log)
	srv.SetConfigPath(path)
	// Тот же порядок, что и при запуске сервиса: правка применяется сразу,
	// иначе перечень правил показывал бы старое ещё пять секунд.
	srv.SetReloader(func() error {
		fresh, err := config.Load(path)
		if err != nil {
			return err
		}
		if err := applyCustomTypes(reg, fresh); err != nil {
			return err
		}
		srv.SetConfig(fresh)
		eng.ResetFilters()
		return nil
	})
	return srv.Routes(), path
}

// applyCustomTypes повторяет пересборку детектора своих типов так же, как это
// делает сервис при запуске. Нужна здесь, чтобы проверка шла по настоящему
// пути применения правки, а не по упрощённому.
func applyCustomTypes(reg *pii.Registry, cfg *config.Config) error {
	if len(cfg.CustomTypes) == 0 {
		reg.SetCustom(nil)
		return nil
	}
	rules := make([]pii.CustomRule, 0, len(cfg.CustomTypes))
	for _, ct := range cfg.CustomTypes {
		rules = append(rules, pii.CustomRule{
			Name: ct.Name, Pattern: ct.Pattern, Group: ct.Group,
			Validator: ct.Validator, Anchors: ct.Anchors,
			RequireAnchor: ct.RequireAnchor, AnchorWindow: ct.AnchorWindow,
		})
	}
	det, err := pii.NewCustomDetector(rules)
	if err != nil {
		return err
	}
	reg.SetCustom(det)
	return nil
}

// post собирает запрос правки. Адрес отправителя задаётся явно: по умолчанию
// httptest подставляет внешний адрес, и это ровно тот случай, который надо
// проверять, — правка не с самой машины.
func post(t *testing.T, h http.Handler, remote string, headers map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("запрос не собрался: %v", err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/rules", bytes.NewReader(raw))
	r.RemoteAddr = remote
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// Ключ системы-потребителя не даёт права менять правила. Иначе система могла
// бы выключить собственное маскирование, и защита перестала бы что-либо
// значить.
func TestRulesWriteDeniedWithoutRights(t *testing.T) {
	h, path := newRulesServer(t)
	before, _ := os.ReadFile(path)

	cases := []struct {
		name    string
		remote  string
		headers map[string]string
	}{
		{"без признака управления", "203.0.113.7:5555", nil},
		{"с пустым признаком", "203.0.113.7:5555", map[string]string{ruleWriteHeader: ""}},
		{"с чужим признаком", "203.0.113.7:5555", map[string]string{ruleWriteHeader: "угадал"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, h, tc.remote, tc.headers, map[string]any{
				"op": "remove_type", "name": "SNILS",
			})
			if rec.Code != http.StatusForbidden {
				t.Errorf("код %d, ожидался 403; тело %s", rec.Code, rec.Body.String())
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Error("отказанная правка изменила файл настроек")
			}
		})
	}
}

// Признак управления задан в окружении — правка принимается и с чужой машины.
func TestRulesWriteAllowedWithToken(t *testing.T) {
	h, path := newRulesServer(t)
	t.Setenv(ruleWriteTokenEnv, "признак-управления")

	// Неверный признак по-прежнему отвергается.
	rec := post(t, h, "203.0.113.7:5555",
		map[string]string{ruleWriteHeader: "признак-управлени"}, // на символ короче
		map[string]any{"op": "remove_type", "name": "SNILS"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("почти верный признак принят: код %d", rec.Code)
	}

	rec = post(t, h, "203.0.113.7:5555",
		map[string]string{ruleWriteHeader: "признак-управления"},
		map[string]any{"op": "remove_type", "name": "SNILS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("верный признак отвергнут: код %d, тело %s", rec.Code, rec.Body.String())
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "SNILS") {
		t.Error("тип остался в файле после удаления")
	}
}

// Правка с самой машины разрешена без всякой настройки: так ручка полезна
// сразу, а снаружи по умолчанию закрыта.
func TestRulesWriteAllowedFromLoopback(t *testing.T) {
	h, path := newRulesServer(t)

	rec := post(t, h, "127.0.0.1:44444", nil, map[string]any{
		"op": "add_type",
		"type": map[string]any{
			"name": "badge", "pattern": `(\d{6})`, "group": 1,
			"anchors": []string{"пропуск"}, "require_anchor": true,
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, ожидался 200; тело %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Applied bool   `json:"applied"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("ответ не разобран: %v", err)
	}
	if !out.Applied || out.Message == "" {
		t.Errorf("ответ не подтверждает правку: %+v", out)
	}

	// Имя приводится к верхнему регистру: типы в ответах сервиса записаны так.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("настройки после правки не читаются: %v", err)
	}
	var found bool
	for _, ct := range cfg.CustomTypes {
		if ct.Name == "BADGE" {
			found = true
		}
	}
	if !found {
		t.Errorf("добавленного типа нет в настройках: %+v", cfg.CustomTypes)
	}
	// Комментарий исходного файла обязан пережить правку через интерфейс.
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "# Заголовок, который обязан пережить правку.") {
		t.Error("правка через интерфейс стёрла комментарии файла настроек")
	}
}

// Сервер, поднятый без пути к настройкам, не должен делать вид, что правит.
func TestRulesWriteRefusedWithoutConfigPath(t *testing.T) {
	h := newInspectServer(t) // поднят без SetConfigPath
	rec := post(t, h, "127.0.0.1:44444", nil, map[string]any{
		"op": "remove_type", "name": "SNILS",
	})
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("код %d, ожидался 501; тело %s", rec.Code, rec.Body.String())
	}
}

// Неверные запросы должны отвергаться с объяснением, а не молча.
func TestRulesRejectsBadRequests(t *testing.T) {
	h, path := newRulesServer(t)
	before, _ := os.ReadFile(path)

	cases := []struct {
		name string
		body any
		want string
	}{
		{"неизвестная операция", map[string]any{"op": "drop_everything"}, "неизвестная операция"},
		{"добавление без описания", map[string]any{"op": "add_type"}, "нужно поле type"},
		{"удаление без имени", map[string]any{"op": "remove_type"}, "нужно поле name"},
		{"правка системы без имени", map[string]any{"op": "set_system_types", "types": []string{"FIO"}}, "нужно поле system"},
		{"правка типов без списка", map[string]any{"op": "set_system_types", "system": "alfasonar"}, "нужно поле types"},
		{"включение без признака", map[string]any{"op": "set_system_enabled", "system": "alfasonar"}, "нужно поле enabled"},
		{"пустой список типов", map[string]any{"op": "set_system_types", "system": "alfasonar", "types": []string{}}, "означает «все типы»"},
		{"несуществующий тип", map[string]any{"op": "set_system_types", "system": "alfasonar", "types": []string{"ФИО"}}, "неизвестные типы"},
		{"несуществующая система", map[string]any{"op": "set_system_enabled", "system": "нет-такой", "enabled": false}, "не найдена"},
		{"сломанное выражение", map[string]any{"op": "add_type", "type": map[string]any{"name": "X", "pattern": `(\d{3}`}}, "правило не принято"},
		{"встроенный тип не убрать", map[string]any{"op": "remove_type", "name": "FIO"}, "не найден среди своих"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, h, "127.0.0.1:44444", nil, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("код %d, ожидался 400; тело %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("отказ не объясняет причину, ожидалось %q:\n%s", tc.want, rec.Body.String())
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Error("отвергнутая правка изменила файл настроек")
			}
		})
	}
}

// Поле, которого нет в описании правки, — почти всегда опечатка в имени.
// Молча её проглотить значит применить не то, о чём просили.
func TestRulesRejectsUnknownFields(t *testing.T) {
	h, _ := newRulesServer(t)
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/rules",
		strings.NewReader(`{"op":"remove_type","naem":"SNILS"}`))
	r.RemoteAddr = "127.0.0.1:44444"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("код %d, ожидался 400; тело %s", rec.Code, rec.Body.String())
	}
}

// Чтение правил открыто, как и матрица покрытия, поэтому секретов в ответе
// быть не должно.
func TestRulesGetListsRulesWithoutSecrets(t *testing.T) {
	h, _ := newRulesServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/rules", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, ожидался 200; тело %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, secret := range []string{"key_hash", "upstream", "auth", "pin_sha256"} {
		if strings.Contains(raw, secret) {
			t.Errorf("в открытом ответе встретилось %q:\n%s", secret, raw)
		}
	}

	var out struct {
		BuiltinTypes []string         `json:"builtin_types"`
		CustomTypes  []customTypeView `json:"custom_types"`
		Systems      []ruleSystemView `json:"systems"`
		Writable     bool             `json:"writable"`
	}
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&out); err != nil {
		t.Fatalf("ответ не разобран: %v", err)
	}
	if len(out.BuiltinTypes) == 0 {
		t.Error("перечень встроенных типов пуст")
	}
	if len(out.CustomTypes) != 1 || out.CustomTypes[0].Name != "SNILS" {
		t.Errorf("свои типы перечислены неверно: %+v", out.CustomTypes)
	}
	if len(out.Systems) != 1 || !out.Systems[0].AllTypes {
		t.Errorf("системы перечислены неверно: %+v", out.Systems)
	}
	// Запрос пришёл с внешнего адреса без признака управления, значит править
	// он не вправе, и интерфейс не должен показывать ему кнопки.
	if out.Writable {
		t.Error("внешнему запросу обещано право правки")
	}
}

// Ручка отвечает только на GET и POST.
func TestRulesRejectsOtherMethods(t *testing.T) {
	h, _ := newRulesServer(t)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		r := httptest.NewRequestWithContext(t.Context(), m, "/v1/rules", nil)
		r.RemoteAddr = "127.0.0.1:44444"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("метод %s дал код %d, ожидался 405", m, rec.Code)
		}
	}
}

// Полный круг: добавить тип, увидеть его в перечне, убрать — и получить
// исходный файл. Проверка того, что интерфейс не накапливает мусор.
func TestRulesAddThenRemoveRestoresFile(t *testing.T) {
	h, path := newRulesServer(t)
	before, _ := os.ReadFile(path)

	rec := post(t, h, "127.0.0.1:1", nil, map[string]any{
		"op": "add_type",
		"type": map[string]any{
			"name": "BADGE", "pattern": `(\d{6})`, "group": 1,
			"anchors": []string{"пропуск"}, "require_anchor": true,
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("тип не добавлен: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/rules", nil))
	if !strings.Contains(rec.Body.String(), "BADGE") {
		t.Errorf("добавленного типа нет в перечне: %s", rec.Body.String())
	}

	rec = post(t, h, "127.0.0.1:1", nil, map[string]any{"op": "remove_type", "name": "BADGE"})
	if rec.Code != http.StatusOK {
		t.Fatalf("тип не убран: %s", rec.Body.String())
	}

	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Errorf("файл не вернулся к исходному виду:\n--- было ---\n%s\n--- стало ---\n%s", before, after)
	}
}

// Главное обещание интерфейса: добавленное правило начинает работать сразу.
//
// Без этой проверки всё остальное — красивый файл на диске. Именно здесь
// ловится случай, когда сервис отвечает «готово», записывает настройки и
// продолжает работать со старым набором детекторов.
func TestRuleAddedThroughAPIStartsMaskingImmediately(t *testing.T) {
	h, _ := newRulesServer(t)
	const text = "Пропуск 123456 выдан на проходной"

	// Идентификатор у каждого вызова свой. Маска по одному и тому же
	// идентификатору сохраняется намеренно: обратное преобразование обязано
	// работать и после смены правил, поэтому повтор вернул бы старый ответ из
	// хранилища, а не новый разбор.
	mask := func(id string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"payload": text, "payload_id": id})
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/process", bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("маскирование ответило кодом %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Result string `json:"result"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("ответ не разобран: %v", err)
		}
		return out.Result
	}

	// До правила номер пропуска — обычное число, его никто не трогает.
	if got := mask("before-rule"); !strings.Contains(got, "123456") {
		t.Fatalf("номер пропуска замаскирован до добавления правила: %q", got)
	}

	rec := post(t, h, "127.0.0.1:1", nil, map[string]any{
		"op": "add_type",
		"type": map[string]any{
			"name": "BADGE", "pattern": `(\d{6})`, "group": 1,
			"anchors": []string{"пропуск"}, "require_anchor": true,
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("правило не добавлено: %s", rec.Body.String())
	}
	var added struct {
		Effective string `json:"effective"`
		Warning   string `json:"warning"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&added); err != nil {
		t.Fatalf("ответ не разобран: %v", err)
	}
	if added.Effective != "immediately" {
		t.Errorf("сервис не обещает немедленного действия: %+v", added)
	}

	// После правила тот же текст обязан вернуться без номера.
	if got := mask("after-add"); strings.Contains(got, "123456") {
		t.Errorf("добавленное правило не действует: %q", got)
	}

	// Правило убрано — номер снова виден.
	rec = post(t, h, "127.0.0.1:1", nil, map[string]any{"op": "remove_type", "name": "BADGE"})
	if rec.Code != http.StatusOK {
		t.Fatalf("правило не убрано: %s", rec.Body.String())
	}
	if got := mask("after-remove"); !strings.Contains(got, "123456") {
		t.Errorf("убранное правило продолжает действовать: %q", got)
	}
}

// Выключение типа для системы должно доходить до маскирования, а не только до
// файла: ровно ради этого в интерфейсе есть галочки.
func TestSystemTypesChangeAffectsMasking(t *testing.T) {
	h, _ := newRulesServer(t)
	const text = "Телефон +79161234567 и почта ivan@example.com"

	mask := func(id string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"payload": text, "payload_id": id})
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/process", bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("маскирование ответило кодом %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Result string `json:"result"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("ответ не разобран: %v", err)
		}
		return out.Result
	}

	if got := mask("before"); strings.Contains(got, "+79161234567") {
		t.Fatalf("телефон не замаскирован при настройке «все типы»: %q", got)
	}

	// Оставляем системе только почту: телефон маскироваться перестаёт.
	rec := post(t, h, "127.0.0.1:1", nil, map[string]any{
		"op": "set_system_types", "system": "alfasonar", "types": []string{"EMAIL"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("список типов не изменён: %s", rec.Body.String())
	}

	got := mask("after")
	if !strings.Contains(got, "+79161234567") {
		t.Errorf("телефон замаскирован, хотя тип выключен: %q", got)
	}
	if strings.Contains(got, "ivan@example.com") {
		t.Errorf("почта не замаскирована, хотя тип оставлен: %q", got)
	}
}
