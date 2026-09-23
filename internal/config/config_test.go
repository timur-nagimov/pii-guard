package config

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pii-guard/internal/mask"
	"pii-guard/internal/pii"
)

// hashOfKey повторяет способ, которым сервис сверяет ключ доступа системы.
func hashOfKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// setSampleEnv выставляет переменные окружения, на которые ссылается пример
// настроек. Без них хеши ключей раскроются в пустые строки и проверка настроек
// справедливо откажет.
func setSampleEnv(t *testing.T) map[string]string {
	t.Helper()
	keys := map[string]string{
		"KILO_KEY_SHA256":  "ключ-kilo",
		"JURY_KEY_SHA256":  "ключ-жюри",
		"JURY2_KEY_SHA256": "ключ-жюри-без-демаскирования",
	}
	hashes := make(map[string]string, len(keys))
	for env, key := range keys {
		hashes[env] = hashOfKey(key)
		t.Setenv(env, hashes[env])
	}
	t.Setenv("ALFAGEN_URL", "https://example.invalid/v1/chat/completions")
	return hashes
}

// TestLoadSampleConfig разбирает пример настроек из репозитория. Этот файл
// уходит в поставку, поэтому он обязан проходить проверку без правок.
func TestLoadSampleConfig(t *testing.T) {
	hashes := setSampleEnv(t)

	cfg, err := Load(filepath.Join("..", "..", "configs", "config.yaml"))
	if err != nil {
		t.Fatalf("пример настроек не разобрался: %v", err)
	}

	checkSampleServer(t, cfg)
	checkSampleStoreAndDefaults(t, cfg)

	if len(cfg.Systems) != 4 {
		t.Fatalf("систем в примере %d, ожидалось 4", len(cfg.Systems))
	}
	checkSampleAnonymousSystem(t, cfg)
	checkSampleKiloSystem(t, cfg, hashes)
	checkSampleJurySystems(t, cfg)

	if len(cfg.CustomTypes) != 1 || cfg.CustomTypes[0].Name != pii.TypeSNILS {
		t.Errorf("добавленные настройкой типы разобраны неверно: %+v", cfg.CustomTypes)
	}
}

// checkSampleServer сверяет раздел слушателей: адреса и пределы задаются
// примером настроек, а не кодом, поэтому опечатка в файле тихо поменяла бы
// поведение поставки.
func checkSampleServer(t *testing.T, cfg *Config) {
	t.Helper()
	if cfg.Server.HTTP != ":8080" || cfg.Server.HTTPS != ":8443" {
		t.Errorf("адреса слушателей разобраны неверно: %+v", cfg.Server)
	}
	if cfg.Server.MaxBodyBytes != 4194304 {
		t.Errorf("предел тела запроса %d", cfg.Server.MaxBodyBytes)
	}
	if cfg.Server.WriteTimeout != 9*time.Second {
		t.Errorf("таймаут записи %v, ожидалось 9s", cfg.Server.WriteTimeout)
	}
}

// checkSampleStoreAndDefaults сверяет хранилище и значения по умолчанию: на них
// опирается каждая система, у которой свои настройки не заданы.
func checkSampleStoreAndDefaults(t *testing.T, cfg *Config) {
	t.Helper()
	// Пятнадцать минут, а не час: срок жизни записи согласован с пределом их
	// числа. Миллион записей при плановой тысяче запросов в секунду
	// набирается за 16.7 минуты, поэтому час был бы обещанием, которого
	// хранилище не выполняет.
	if cfg.Store.TTL != 15*time.Minute || cfg.Store.KeyEnv != "PII_STORE_KEY" {
		t.Errorf("настройки хранилища разобраны неверно: %+v", cfg.Store)
	}
	if cfg.Defaults.Preset != mask.PresetFull || cfg.Defaults.MinConfidence != 0.6 {
		t.Errorf("значения по умолчанию разобраны неверно: %+v", cfg.Defaults)
	}
	if cfg.Defaults.ContextRulesEnabled {
		t.Error("контекстные правила в примере должны быть выключены")
	}
}

// checkSampleAnonymousSystem сверяет систему без ключа: заголовков она не шлёт,
// поэтому именно её настройки достаются проверяющей стороне.
func checkSampleAnonymousSystem(t *testing.T, cfg *Config) {
	t.Helper()
	anon, ok := cfg.AnonymousSystem()
	if !ok {
		t.Fatal("в примере нет системы без ключа, а проверяющая система заголовков не шлёт")
	}
	if anon.Name != "alfasonar" {
		t.Errorf("система без ключа %q, ожидалась alfasonar", anon.Name)
	}
	if !anon.AllTypes || !anon.Allows(pii.TypeFIO) {
		t.Error("система без ключа должна маскировать все типы")
	}
	if !anon.Preset.PreservesLength() {
		t.Error("пресет системы без ключа обязан сохранять длину")
	}
	if got := anon.MinConf(cfg.Defaults); got != 0.5 {
		t.Errorf("порог уверенности %v, ожидалось 0.5", got)
	}
	if got := anon.ErrorMode(cfg.Defaults); got != OnErrorOpen {
		t.Errorf("поведение при ошибке %q, ожидалось %q", got, OnErrorOpen)
	}
	if got := anon.UnknownIDMode(cfg.Defaults); got != OnUnknownPassthrough {
		t.Errorf("поведение при неизвестном идентификаторе %q", got)
	}
}

// checkSampleKiloSystem сверяет систему с ключом: у неё проверяется вся цепочка
// от заголовка до подставленного из окружения хеша.
func checkSampleKiloSystem(t *testing.T, cfg *Config, hashes map[string]string) {
	t.Helper()
	kilo, ok := cfg.System("kilo")
	if !ok {
		t.Fatal("система kilo не разобралась")
	}
	if kilo.Auth.None {
		t.Error("система kilo обязана требовать ключ")
	}
	if kilo.Auth.Header != "X-System-Key" {
		t.Errorf("заголовок ключа %q", kilo.Auth.Header)
	}
	if kilo.Auth.KeySHA256 != hashes["KILO_KEY_SHA256"] {
		t.Errorf("хеш ключа %q, ожидался %q", kilo.Auth.KeySHA256, hashes["KILO_KEY_SHA256"])
	}
	if kilo.Preset != mask.PresetToken {
		t.Errorf("пресет системы kilo %q, ожидался %q", kilo.Preset, mask.PresetToken)
	}
	if kilo.Upstream.Model == "" || kilo.Upstream.Timeout != time.Minute {
		t.Errorf("настройки языковой модели разобраны неверно: %+v", kilo.Upstream)
	}
}

// checkSampleJurySystems сверяет профили жюри: они маскируют звёздочками все
// типы без исключений, потому что видимый кусок паспорта в ответе модуля
// защиты выглядит как утечка.
func checkSampleJurySystems(t *testing.T, cfg *Config) {
	t.Helper()
	jury, _ := cfg.System("jury_demo")
	if jury.Preset != mask.PresetFull {
		t.Errorf("пресет системы jury_demo %q, ожидался %q", jury.Preset, mask.PresetFull)
	}
	if len(jury.PerType) != 0 {
		t.Errorf("у системы жюри не должно быть исключений по типам: %+v", jury.PerType)
	}
	opts := jury.MaskOptions(cfg.Defaults)
	if opts.PresetFor(pii.TypeFIO) != mask.PresetFull || opts.PresetFor(pii.TypePhone) != mask.PresetFull {
		t.Errorf("настройки маскирования собраны неверно: %+v", opts)
	}

	noDemask, _ := cfg.System("jury_nodemask")
	if noDemask.Demask {
		t.Error("системе jury_nodemask обратное преобразование запрещено настройками")
	}
}

// TestEnvExpansionInKeyHash проверяет подстановку переменной окружения в хеш
// ключа: в файл настроек секреты не пишутся.
func TestEnvExpansionInKeyHash(t *testing.T) {
	hash := hashOfKey("секретный ключ")
	t.Setenv("TEST_SYSTEM_KEY_SHA256", strings.ToUpper(hash))

	cfg, err := Parse([]byte(`
systems:
  demo:
    enabled: true
    auth:
      header: X-Demo-Key
      key_sha256: "${TEST_SYSTEM_KEY_SHA256}"
    preset: full
`))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	sys, _ := cfg.System("demo")
	// Хеш приводится к нижнему регистру, иначе сверка с посчитанным хешем
	// ключа никогда не сойдётся.
	if sys.Auth.KeySHA256 != hash {
		t.Fatalf("хеш ключа %q, ожидался %q", sys.Auth.KeySHA256, hash)
	}
}

// TestEnvExpansionMissing проверяет главное свойство: система, у которой
// переменная окружения с хешем ключа не задана, НЕ ДОЛЖНА пускать запросы.
// Пустой хеш означал бы, что подойдёт любой ключ, то есть система открыта.
//
// Способ, которым свойство обеспечивается, поменялся. Раньше настройки
// отвергались целиком, и свежий клон с пустым .env не запускался вовсе.
// Теперь такая система выключается с предупреждением: свойство сохранено,
// запуск не ломается. Проверяем именно свойство, а не способ.
func TestEnvExpansionMissing(t *testing.T) {
	t.Setenv("TEST_MISSING_KEY_SHA256", "")
	cfg, err := Parse([]byte(`
systems:
  demo:
    enabled: true
    auth:
      header: X-Demo-Key
      key_sha256: "${TEST_MISSING_KEY_SHA256}"
`))
	if err != nil {
		t.Fatalf("настройки должны приниматься, получена ошибка: %v", err)
	}
	if cfg.Systems["demo"].Enabled {
		t.Fatal("система с пустым хешем ключа осталась включённой: она пускала бы всех")
	}
	if cfg.Systems["demo"].Auth.KeySHA256 != "" {
		t.Fatal("пустой хеш ключа не должен подменяться значением")
	}
	if len(cfg.Warnings) == 0 {
		t.Fatal("о выключенной системе не предупредили")
	}
}

// TestDefaults проверяет значения по умолчанию: без них сервис пришлось бы
// настраивать целиком, а любая незаполненная строка меняла бы поведение.
func TestDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
systems:
  demo:
    enabled: true
`))
	if err != nil {
		t.Fatalf("минимальные настройки не разобрались: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"адрес слушателя", cfg.Server.HTTP, ":8080"},
		{"предел тела запроса", cfg.Server.MaxBodyBytes, int64(4 << 20)},
		{"таймаут чтения заголовков", cfg.Server.ReadHeaderTimeout, 5 * time.Second},
		{"таймаут чтения", cfg.Server.ReadTimeout, 15 * time.Second},
		{"таймаут записи", cfg.Server.WriteTimeout, 9 * time.Second},
		{"таймаут простоя", cfg.Server.IdleTimeout, 90 * time.Second},
		{"таймаут завершения", cfg.Server.ShutdownTimeout, 15 * time.Second},
		{"предел одновременных запросов", cfg.Limits.Inflight, 96},
		{"предел тяжёлых запросов", cfg.Limits.HeavyInflight, 6},
		{"порог тяжёлого запроса", cfg.Limits.HeavyThresholdBytes, 64 << 10},
		{"предельное ожидание", cfg.Limits.MaxWait, 500 * time.Millisecond},
		{"срок жизни записи", cfg.Store.TTL, 15 * time.Minute},
		{"предел числа записей", cfg.Store.MaxRecords, 1_000_000},
		{"переменная с ключом хранилища", cfg.Store.KeyEnv, "PII_STORE_KEY"},
		{"пресет по умолчанию", cfg.Defaults.Preset, mask.PresetFull},
		{"поведение при ошибке", cfg.Defaults.OnError, OnErrorClosed},
		{"поведение при неизвестном идентификаторе", cfg.Defaults.OnUnknownID, OnUnknown404},
		{"порог уверенности", cfg.Defaults.MinConfidence, 0.6},
		{"режим дат без якоря", cfg.Defaults.DateWithoutAnchor, DatePIIContext},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Fatalf("получено %v, ожидалось %v", c.got, c.want)
			}
		})
	}

	sys, _ := cfg.System("demo")
	if !sys.AllTypes {
		t.Error("система без перечня типов должна маскировать все типы")
	}
	if !sys.Auth.None {
		t.Error("система без раздела auth считается работающей без ключа")
	}
	if got := sys.MinConf(cfg.Defaults); got != 0.6 {
		t.Errorf("порог уверенности системы %v", got)
	}
	if sys.ContextRulesOn(cfg.Defaults) {
		t.Error("контекстные правила по умолчанию выключены")
	}
	if got := sys.DateMode(cfg.Defaults); got != DatePIIContext {
		t.Errorf("режим дат %q", got)
	}
	if got := sys.MaskOptions(cfg.Defaults).Default; got != mask.PresetFull {
		t.Errorf("пресет системы %q", got)
	}
}

// TestValidateRejects проверяет противоречия в настройках, из-за которых
// сервис повёл бы себя не так, как ожидает проверяющая система или жюри.
func TestValidateRejects(t *testing.T) {
	validHash := hashOfKey("ключ")
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "ни одной системы",
			yaml:    "defaults:\n  preset: full\n",
			wantErr: "не задана ни одна система",
		},
		{
			name: "две системы без ключа",
			yaml: `
systems:
  first:
    enabled: true
    auth: none
  second:
    enabled: true
    auth: none
`,
			wantErr: "без ключа доступа может работать только одна система",
		},
		{
			name: "пресет без сохранения длины у системы без ключа",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
    preset: initials
`,
			wantErr: "не сохраняет длину",
		},
		{
			name: "пресет плейсхолдеров у системы без ключа",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
    preset: token
`,
			wantErr: "не сохраняет длину",
		},
		{
			name: "переопределение по типу без сохранения длины у системы без ключа",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
    preset: full
    per_type:
      FIO: initials
`,
			wantErr: "не сохраняет длину",
		},
		{
			name: "неизвестный пресет",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
    preset: звёздочки
`,
			wantErr: "неизвестный пресет",
		},
		{
			name: "неизвестный пресет для типа",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
    per_type:
      FIO: звёздочки
`,
			wantErr: "неизвестный пресет",
		},
		{
			name: "хеш ключа неверной длины",
			yaml: `
systems:
  demo:
    enabled: true
    auth:
      header: X-Demo-Key
      key_sha256: "abcdef"
`,
			wantErr: "64 символа",
		},
		{
			name: "неизвестное значение auth",
			yaml: `
systems:
  demo:
    enabled: true
    auth: всех пускать
`,
			wantErr: "неизвестное значение auth",
		},
		{
			name: "неизвестное поведение при ошибке",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
    on_error: молчать
`,
			wantErr: "неизвестное значение on_error",
		},
		{
			name: "неизвестное поведение при неизвестном идентификаторе",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
    on_unknown_id: "500"
`,
			wantErr: "неизвестное значение on_unknown_id",
		},
		{
			name: "неизвестный режим дат",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
    date_without_anchor: иногда
`,
			wantErr: "неизвестное значение date_without_anchor",
		},
		{
			name: "добавленный тип без выражения",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
custom_types:
  - name: TICKET
`,
			wantErr: "custom_types",
		},
		{
			name:    "негодный YAML",
			yaml:    "systems: [не, объект\n",
			wantErr: "разбор настроек",
		},
		{
			name: "хеш ключа с недостающей переменной окружения",
			yaml: `
systems:
  demo:
    enabled: true
    auth:
      header: X-Demo-Key
      key_sha256: "` + validHash[:60] + `"
`,
			wantErr: "64 символа",
		},
		{
			name: "адрес модели со схемой file",
			yaml: `
systems:
  demo:
    enabled: true
    auth: none
    upstream:
      url: file:///etc/passwd
`,
			wantErr: "допускает только http и https",
		},
		{
			name: "адрес модели без хоста",
			yaml: `
systems:
  demo:
    enabled: true
    auth: none
    upstream:
      url: https:///v1
`,
			wantErr: "без хоста",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil {
				t.Fatalf("настройки приняты, ожидалась ошибка про %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("ошибка %q не содержит %q", err, c.wantErr)
			}
		})
	}
}

// TestValidateAccepts проверяет настройки, которые должны приниматься.
func TestValidateAccepts(t *testing.T) {
	validHash := hashOfKey("ключ")
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "одна система без ключа и одна с ключом",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
    preset: partial
  demo:
    enabled: true
    auth:
      header: X-Demo-Key
      key_sha256: "` + validHash + `"
    preset: token
`,
		},
		{
			name: "вторая система без ключа отключена",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
  disabled:
    enabled: false
    auth: none
`,
		},
		{
			name: "отключённая система с негодным пресетом не мешает",
			yaml: `
systems:
  anon:
    enabled: true
    auth: none
  broken:
    enabled: false
    auth: none
    preset: звёздочки
`,
		},
		{
			name: "адрес модели с хостом и схемой",
			yaml: `
systems:
  demo:
    enabled: true
    auth: none
    upstream:
      url: https://host/v1
`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse([]byte(c.yaml)); err != nil {
				t.Fatalf("настройки отклонены: %v", err)
			}
		})
	}
}

// TestSystemTypeFilter проверяет перечень типов системы: система маскирует
// только то, что ей разрешено настройками.
func TestSystemTypeFilter(t *testing.T) {
	cfg, err := Parse([]byte(`
systems:
  anon:
    enabled: true
    auth: none
    types: [fio, phone, EMAIL]
`))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	sys, _ := cfg.System("anon")
	if sys.AllTypes {
		t.Fatal("перечень типов задан, признак «все типы» должен быть снят")
	}
	for _, tp := range []pii.Type{pii.TypeFIO, pii.TypePhone, pii.TypeEmail} {
		if !sys.Allows(tp) {
			t.Errorf("тип %s должен быть разрешён", tp)
		}
	}
	for _, tp := range []pii.Type{pii.TypeCard, pii.TypePassport, pii.TypeCVV} {
		if sys.Allows(tp) {
			t.Errorf("тип %s не должен быть разрешён", tp)
		}
	}
}

// TestSystemOverrides проверяет, что настройки системы перекрывают общие.
func TestSystemOverrides(t *testing.T) {
	cfg, err := Parse([]byte(`
defaults:
  preset: full
  min_confidence: 0.6
  context_rules_enabled: false
  date_without_anchor: pii_context
  on_error: closed
  on_unknown_id: "404"
systems:
  anon:
    enabled: true
    auth: none
    preset: partial
    min_confidence: 0.4
    context_rules_enabled: true
    date_without_anchor: any
    on_error: open
    on_unknown_id: passthrough
    context_rules:
      - mask: PIN
        requires: [CARD]
`))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	sys, _ := cfg.System("anon")
	if got := sys.MinConf(cfg.Defaults); got != 0.4 {
		t.Errorf("порог уверенности %v, ожидалось 0.4", got)
	}
	if !sys.ContextRulesOn(cfg.Defaults) {
		t.Error("контекстные правила системы должны быть включены")
	}
	if got := sys.DateMode(cfg.Defaults); got != DateAny {
		t.Errorf("режим дат %q, ожидалось %q", got, DateAny)
	}
	if got := sys.ErrorMode(cfg.Defaults); got != OnErrorOpen {
		t.Errorf("поведение при ошибке %q", got)
	}
	if got := sys.UnknownIDMode(cfg.Defaults); got != OnUnknownPassthrough {
		t.Errorf("поведение при неизвестном идентификаторе %q", got)
	}
	if len(sys.ContextRules) != 1 || sys.ContextRules[0].Mask != pii.TypePIN {
		t.Fatalf("контекстные правила разобраны неверно: %+v", sys.ContextRules)
	}
	if len(sys.ContextRules[0].Requires) != 1 || sys.ContextRules[0].Requires[0] != pii.TypeCard {
		t.Fatalf("условие контекстного правила разобрано неверно: %+v", sys.ContextRules[0])
	}
}

// TestLoadMissingFile проверяет понятную ошибку при отсутствии файла настроек.
func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "нет-такого.yaml"))
	if err == nil {
		t.Fatal("отсутствующий файл настроек не вызвал ошибки")
	}
	if !strings.Contains(err.Error(), "чтение файла настроек") {
		t.Fatalf("ошибка %q не объясняет причину", err)
	}
}

// TestAnonymousSystemAbsent проверяет, что при отсутствии системы без ключа
// это видно вызывающему: запросы без заголовка приписывать будет некому.
func TestAnonymousSystemAbsent(t *testing.T) {
	cfg, err := Parse([]byte(`
systems:
  demo:
    enabled: true
    auth:
      header: X-Demo-Key
      key_sha256: "` + hashOfKey("ключ") + `"
`))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	if _, ok := cfg.AnonymousSystem(); ok {
		t.Fatal("найдена система без ключа, хотя её нет")
	}
	if _, ok := cfg.System("нет такой"); ok {
		t.Fatal("найдена система, которой нет")
	}
}

// TestSystemWithoutKeyIsDisabled закрепляет поведение, которое важнее удобства:
// система без ключа доступа ВЫКЛЮЧАЕТСЯ, а не роняет запуск целиком.
//
// Прежде настройки отвергались, и свежий клон с пустым .env не стартовал: тот,
// кто копировал .env.example и запускал по инструкции, получал отказ. При этом
// анонимный профиль проверяющей системы ключа не требует, то есть контракт
// работал бы и без остальных.
//
// Выключение это безопасное направление. Опасным было бы обратное, включить
// систему без проверки ключа, и тест проверяет, что этого не происходит.
func TestSystemWithoutKeyIsDisabled(t *testing.T) {
	cfg, err := Parse([]byte(`
systems:
  anon:
    enabled: true
    auth: none
    types: [all]
  demo:
    enabled: true
    auth:
      header: X-Demo-Key
`))
	if err != nil {
		t.Fatalf("настройки должны приниматься, получена ошибка: %v", err)
	}

	if cfg.Systems["demo"].Enabled {
		t.Error("система без ключа доступа осталась включённой")
	}
	if !cfg.Systems["anon"].Enabled {
		t.Error("анонимная система выключилась, хотя ключа не требует")
	}

	var found bool
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "demo") {
			found = true
		}
	}
	if !found {
		t.Errorf("о выключенной системе не предупредили, предупреждения: %v", cfg.Warnings)
	}
}

// TestDuplicateKeyHashRejected закрепляет защиту от случайного поведения.
// Один хеш ключа у двух систем делает выбор системы зависящим от порядка
// обхода карты, а он в Go не определён: на одинаковых запросах сервис
// опознавал бы то одну систему, то другую. Снаружи это выглядит как плавающее
// поведение без причины.
func TestDuplicateKeyHashRejected(t *testing.T) {
	const h = "1111111111111111111111111111111111111111111111111111111111111111"
	_, err := Parse([]byte(`
systems:
  alpha:
    enabled: true
    auth:
      header: X-Key
      key_sha256: "` + h + `"
  beta:
    enabled: true
    auth:
      header: X-Key
      key_sha256: "` + h + `"
`))
	if err == nil {
		t.Fatal("настройки с одинаковым хешем у двух систем приняты")
	}
	if !strings.Contains(err.Error(), "делят один хеш") {
		t.Fatalf("сообщение %q не объясняет причину", err)
	}
	// Сообщение должно быть устойчивым: имена перечисляются по порядку.
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("в сообщении нет обеих систем: %v", err)
	}
}

// TestLimitOrderingChecked закрепляет проверку соотношений между сроками и
// пределами. По отдельности каждое значение выглядит разумным, а вместе они
// дают поведение, которое никто не закладывал, и в бою это не видно: сервис
// работает, просто не так, как написано в документах.
func TestLimitOrderingChecked(t *testing.T) {
	t.Run("ожидание дольше срока записи ответа", func(t *testing.T) {
		// Запрос успеет получить обрыв соединения раньше, чем честный отказ.
		_, err := Parse([]byte(`
server:
  write_timeout: 1s
limits:
  inflight: 10
  heavy_inflight: 2
  max_wait: 2s
systems:
  anon:
    enabled: true
    auth: none
    types: [all]
`))
		if err == nil {
			t.Fatal("настройки приняты, хотя ожидание места дольше срока записи ответа")
		}
		if !strings.Contains(err.Error(), "обрыв соединения") {
			t.Errorf("сообщение %q не объясняет последствие", err)
		}
	})

	t.Run("тяжёлый предел не меньше общего", func(t *testing.T) {
		// Это не ошибка, а бессмыслица: ограничитель ничего не ограничивает.
		// Поэтому предупреждение, а не отказ.
		cfg, err := Parse([]byte(`
server:
  write_timeout: 9s
limits:
  inflight: 10
  heavy_inflight: 20
  max_wait: 500ms
systems:
  anon:
    enabled: true
    auth: none
    types: [all]
`))
		if err != nil {
			t.Fatalf("настройки должны приниматься с предупреждением, получена ошибка: %v", err)
		}
		var found bool
		for _, w := range cfg.Warnings {
			if strings.Contains(w, "ничего не ограничивает") {
				found = true
			}
		}
		if !found {
			t.Errorf("о бессмысленном ограничителе не предупредили: %v", cfg.Warnings)
		}
	})

	t.Run("рабочие настройки принимаются", func(t *testing.T) {
		if _, err := Parse([]byte(`
server:
  write_timeout: 9s
limits:
  inflight: 96
  heavy_inflight: 6
  max_wait: 500ms
systems:
  anon:
    enabled: true
    auth: none
    types: [all]
`)); err != nil {
			t.Fatalf("согласованные настройки отвергнуты: %v", err)
		}
	})
}

// TestPerTypePresetParsing проверяет разбор исключений по типам на своём
// примере. Раньше эту возможность проверял боевой файл настроек: там стояло
// единственное исключение. Когда профили жюри перевели на звёздочки без
// исключений, разбор остался без проверки вовсе — теперь он не зависит от
// того, какие настройки выбраны для продукта.
func TestPerTypePresetParsing(t *testing.T) {
	const src = `
defaults:
  preset: full
systems:
  demo:
    enabled: true
    auth:
      none: true
    types: [all]
    preset: token
    per_type:
      FIO: initials
      PHONE: partial
`
	cfg, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("настройки не разобрались: %v", err)
	}
	sys, ok := cfg.System("demo")
	if !ok {
		t.Fatal("система demo не разобралась")
	}
	if sys.PerType[pii.TypeFIO] != mask.PresetInitials {
		t.Errorf("исключение для имени %q, ожидалось %q", sys.PerType[pii.TypeFIO], mask.PresetInitials)
	}
	opts := sys.MaskOptions(cfg.Defaults)
	if opts.PresetFor(pii.TypeFIO) != mask.PresetInitials {
		t.Errorf("имя маскируется как %q, ожидалось %q", opts.PresetFor(pii.TypeFIO), mask.PresetInitials)
	}
	if opts.PresetFor(pii.TypePhone) != mask.PresetPartial {
		t.Errorf("телефон маскируется как %q, ожидалось %q", opts.PresetFor(pii.TypePhone), mask.PresetPartial)
	}
	// Тип без исключения берёт общий пресет системы, а не пресет по умолчанию.
	if opts.PresetFor(pii.TypeCard) != mask.PresetToken {
		t.Errorf("карта маскируется как %q, ожидалось %q", opts.PresetFor(pii.TypeCard), mask.PresetToken)
	}
}
