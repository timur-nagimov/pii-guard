package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pii-guard/internal/api"
	"pii-guard/internal/config"
	"pii-guard/internal/engine"
	"pii-guard/internal/metrics"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
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
	}
}

// dobCount считает фрагменты даты рождения в результате разбора.
func dobCount(spans []pii.Span) int {
	n := 0
	for _, s := range spans {
		if s.Type == pii.TypeDOB {
			n++
		}
	}
	return n
}

// Перечитывание настроек обязано менять режим дат по умолчанию на лету:
// ослабление режима начинает находить даты без якоря без перезапуска.
//
// Раньше детектор дат собирался один раз при запуске, и ослабление режима не
// действовало до перезапуска: движок лишь дополнительно фильтровал, поэтому
// ужесточение работало, а ослабление нет.
func TestApplyConfigChangesDateMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write := func(mode string) {
		t.Helper()
		cfg := "server:\n  http: \":0\"\n" +
			"defaults:\n  date_without_anchor: " + mode + "\n" +
			"systems:\n  alfasonar:\n    enabled: true\n    auth: none\n    types: [all]\n"
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatalf("настройки не записаны: %v", err)
		}
	}
	write("anchor_only")

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
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector(), pii.NewFIODetector())
	dateDet := pii.NewDateDetector(cfg.Defaults.DateWithoutAnchor)
	reg.Register(dateDet)
	eng := engine.New(reg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := api.New(cfg, st, eng, metrics.New(), log)

	apply := newConfigApplier(path, srv, eng, reg, dateDet, log)

	text := "Клиент Иванов Иван Иванович, 12.03.1985"
	// В строгом режиме дата без якоря не находится.
	if got := dobCount(reg.Detect(pii.NewDoc(text))); got != 0 {
		t.Fatalf("в строгом режиме найдена дата без якоря: %d фрагментов", got)
	}

	// Ослабляем режим и применяем настройки: дата обязана начать находиться.
	write("pii_context")
	if err := apply("test"); err != nil {
		t.Fatalf("настройки не применены: %v", err)
	}
	if got := dobCount(reg.Detect(pii.NewDoc(text))); got != 1 {
		t.Fatalf("после ослабления режима дата не найдена: %d фрагментов", got)
	}
}

// Наблюдение за файлом обязано завершаться вместе с сервисом: без контекста
// горутина жила бы после остановки и держала бы файл настроек открытым.
func TestWatchConfigStopsOnContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("systems:\n  alfasonar:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatalf("настройки не записаны: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	apply := func(string) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchConfig(ctx, path, apply, log, make(chan os.Signal))
		close(done)
	}()

	// Отмена контекста обязана завершить наблюдение.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("наблюдение не завершилось после отмены контекста")
	}
}

// restartRequiredChanges обязана называть изменённые поля, которые требуют
// перезапуска, и молчать о тех, что применяются на лету. На этом сравнении
// держится честный журнал: сервис не говорит «применены» про то, чего не
// сделал.
func TestRestartRequiredChanges(t *testing.T) {
	old := &config.Config{}
	new := &config.Config{}
	// Поле, требующее перезапуска.
	new.Server.WriteTimeout = 10 * time.Second
	// Поле, применяемое на лету: его в списке быть не должно.
	new.Defaults.DateWithoutAnchor = "any"

	fields := restartRequiredChanges(old, new)
	if len(fields) != 1 || fields[0] != "server.write_timeout" {
		t.Fatalf("поля: %v, ожидалось только server.write_timeout", fields)
	}
}
