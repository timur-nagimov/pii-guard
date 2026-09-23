package main

import (
	"strings"
	"testing"

	"pii-guard/internal/config"
	"pii-guard/internal/pii"
)

// Перечитывание настроек обязано пересобирать детектор своих типов.
//
// Раньше он собирался один раз при запуске: добавленный в настройки тип не
// действовал до перезапуска сервиса, а журнал при этом писал «настройки
// применены». Снаружи это выглядело как молчаливая потеря правки.
func TestApplyCustomTypesRebuildsDetector(t *testing.T) {
	reg := pii.NewRegistry()
	reg.Register(pii.NewEmailDetector())

	doc := pii.NewDoc("пропуск 123456")

	// Пустые настройки: своего типа нет.
	if err := applyCustomTypes(reg, &config.Config{}); err != nil {
		t.Fatalf("пустые настройки отвергнуты: %v", err)
	}
	if got := len(reg.Detect(doc)); got != 0 {
		t.Fatalf("на пустых настройках найдено фрагментов: %d, ожидалось ноль", got)
	}

	// Тип добавлен — он должен искаться сразу.
	withBadge := &config.Config{CustomTypes: []config.CustomType{{
		Name: "BADGE", Pattern: `(\d{6})`, Group: 1,
		Anchors: []string{"пропуск"}, RequireAnchor: true,
	}}}
	if err := applyCustomTypes(reg, withBadge); err != nil {
		t.Fatalf("тип не применён: %v", err)
	}
	spans := reg.Detect(doc)
	if len(spans) != 1 || spans[0].Type != "BADGE" {
		t.Fatalf("добавленный тип не ищется: %+v", spans)
	}

	// Тип убран — искать его больше нечем.
	if err := applyCustomTypes(reg, &config.Config{}); err != nil {
		t.Fatalf("снятие типа отвергнуто: %v", err)
	}
	if got := len(reg.Detect(doc)); got != 0 {
		t.Errorf("убранный тип продолжает искаться: %d фрагментов", got)
	}
}

// Неверное правило не должно обесточивать уже работающие типы: применение
// настроек отвергается целиком, прежний детектор остаётся на месте.
func TestApplyCustomTypesKeepsPreviousOnError(t *testing.T) {
	reg := pii.NewRegistry()
	doc := pii.NewDoc("пропуск 123456")

	good := &config.Config{CustomTypes: []config.CustomType{{
		Name: "BADGE", Pattern: `(\d{6})`, Group: 1,
		Anchors: []string{"пропуск"}, RequireAnchor: true,
	}}}
	if err := applyCustomTypes(reg, good); err != nil {
		t.Fatalf("рабочее правило отвергнуто: %v", err)
	}

	broken := &config.Config{CustomTypes: []config.CustomType{
		good.CustomTypes[0],
		{Name: "BROKEN", Pattern: `(\d{3}`},
	}}
	err := applyCustomTypes(reg, broken)
	if err == nil {
		t.Fatal("неверное правило принято")
	}
	if !strings.Contains(err.Error(), "BROKEN") {
		t.Errorf("отказ не называет сломанное правило: %v", err)
	}

	spans := reg.Detect(doc)
	if len(spans) != 1 || spans[0].Type != "BADGE" {
		t.Errorf("прежний тип перестал искаться из-за чужой опечатки: %+v", spans)
	}
}

// customRules обязана переносить все поля описания: потерянное поле меняет
// поведение типа молча, без единой ошибки.
func TestCustomRulesCarriesEveryField(t *testing.T) {
	cfg := &config.Config{CustomTypes: []config.CustomType{{
		Name: "BADGE", Pattern: `(\d{6})`, Group: 1,
		Validator: "none", Anchors: []string{"пропуск", "бейдж"},
		RequireAnchor: true, AnchorWindow: 24,
	}}}

	rules := customRules(cfg)
	if len(rules) != 1 {
		t.Fatalf("правил получилось %d, ожидалось одно", len(rules))
	}
	r := rules[0]
	switch {
	case r.Name != "BADGE":
		t.Errorf("имя: %q", r.Name)
	case r.Pattern != `(\d{6})`:
		t.Errorf("выражение: %q", r.Pattern)
	case r.Group != 1:
		t.Errorf("группа: %d", r.Group)
	case r.Validator != "none":
		t.Errorf("контрольная сумма: %q", r.Validator)
	case len(r.Anchors) != 2:
		t.Errorf("якоря: %v", r.Anchors)
	case !r.RequireAnchor:
		t.Error("требование якоря потеряно")
	case r.AnchorWindow != 24:
		t.Errorf("окно якоря: %d", r.AnchorWindow)
	default:
		// Все поля описания доехали без потерь — сообщать не о чем.
	}
}
