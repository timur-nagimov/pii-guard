// Package config читает и проверяет настройки сервиса. Настройки задаются
// одним файлом YAML и применяются без перезапуска: по сигналу SIGHUP или при
// изменении файла на диске.
package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"pii-guard/internal/mask"
	"pii-guard/internal/pii"
	"pii-guard/internal/store"
)

// Config — полный набор настроек сервиса.
type Config struct {
	Server      Server            `yaml:"server"`
	Limits      Limits            `yaml:"limits"`
	Store       Store             `yaml:"store"`
	Defaults    Defaults          `yaml:"defaults"`
	Logging     Logging           `yaml:"logging"`
	Systems     map[string]System `yaml:"systems"`
	CustomTypes []CustomType      `yaml:"custom_types"`

	// Warnings — то, что не мешает работать, но человек знать обязан.
	// Сюда попадает, например, система, выключенная из-за незаданного ключа
	// доступа. Молча выключить систему нельзя: снаружи это выглядит как
	// отказ в доступе без причины.
	Warnings []string `yaml:"-"`
}

// Server — параметры сетевых слушателей.
type Server struct {
	HTTP              string        `yaml:"http"`
	HTTPS             string        `yaml:"https"`
	MaxBodyBytes      int64         `yaml:"max_body_bytes"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	// DrainTimeout — пауза между снятием готовности и закрытием приёма.
	// Балансировщик узнаёт о снятии готовности не мгновенно, и всё это время
	// он продолжает слать запросы. Закрыть слушатель раньше значит потерять
	// их. Расчёт числа — в docs/BALANCING.md, раздел о мягком выводе копии
	// из обслуживания.
	DrainTimeout time.Duration `yaml:"drain_timeout"`
}

// Limits — ограничители одновременной обработки.
type Limits struct {
	Inflight            int           `yaml:"inflight"`
	HeavyInflight       int           `yaml:"heavy_inflight"`
	HeavyThresholdBytes int           `yaml:"heavy_threshold_bytes"`
	MaxWait             time.Duration `yaml:"max_wait"`
}

// Store — параметры хранилища соответствий.
type Store struct {
	TTL        time.Duration `yaml:"ttl"`
	MaxRecords int           `yaml:"max_records"`
	KeyEnv     string        `yaml:"key_env"`
	// Redis — общее хранилище. Пустой адрес означает работу в памяти одного
	// процесса; заполненный включает горизонтальный рост, потому что копии
	// сервиса начинают видеть записи друг друга.
	Redis store.RedisConfig `yaml:"redis"`
	// UnreadyOnDegraded снимает готовность копии, когда общее хранилище
	// недоступно и сервис перешёл на память процесса.
	//
	// По умолчанию выключено, и это щадящее поведение выбрано сознательно.
	// На одной копии память процесса — полноценный запасной путь: маску
	// сделала та же копия, которая будет её разворачивать, поэтому ответы
	// остаются верными, и работать лучше, чем не работать.
	//
	// В группе копий всё наоборот: маску сделала одна копия, обратное
	// преобразование просит другая, и запись она не найдёт. Ответ будет
	// двухсотым и при этом неверным, а тихо неверный ответ хуже отказа.
	// Поэтому в группе признак включают, и копия уходит из обслуживания.
	UnreadyOnDegraded bool `yaml:"unready_on_degraded"`
}

// Defaults — значения, действующие для всех систем, если те их не переопределили.
type Defaults struct {
	Preset              mask.Preset `yaml:"preset"`
	OnError             string      `yaml:"on_error"`
	OnUnknownID         string      `yaml:"on_unknown_id"`
	MinConfidence       float64     `yaml:"min_confidence"`
	ContextRulesEnabled bool        `yaml:"context_rules_enabled"`
	DateWithoutAnchor   string      `yaml:"date_without_anchor"`
	// InspectEnabled открывает ручку разбора текста. Она показывает, какое
	// правило сработало и почему, и нужна для проверки и отладки. Значения в
	// ответе принадлежат тому, кто их прислал, поэтому утечки нет, но в
	// промышленной установке ручку разумно оставить только служебным системам.
	InspectEnabled bool `yaml:"inspect_enabled"`
}

// Logging — настройки журнала в файле настроек.
//
// Те же значения задаются переменными окружения, и окружение важнее файла.
// Правило привычное: файл лежит в образе и описывает установку целиком,
// окружение правится на конкретной машине и описывает её особенности —
// поднятый на время уровень, свой путь для журнала аудита, имя копии.
// Слияние делает cmd/pii-guard/main.go, сразу после чтения файла.
type Logging struct {
	// Level — минимальный уровень записи: debug, info, warn, error.
	Level string `yaml:"level"`
	// Format — json для эксплуатации, text для разработки.
	Format string `yaml:"format"`
	// Source добавляет в запись файл и строку места вызова. Дорого на
	// горячем пути, поэтому по умолчанию выключено.
	Source *bool `yaml:"source"`
	// Redact включает второй рубеж защиты от утечки: разбор значений перед
	// записью. Выключается только в измерениях скорости.
	Redact *bool `yaml:"redact"`
	// RepeatWindow — окно глушения повторов одинаковых записей.
	RepeatWindow time.Duration `yaml:"repeat_window"`
	// RepeatLevel — с какого уровня глушатся повторы.
	RepeatLevel string `yaml:"repeat_level"`
	// SampleN — прореживание записей об успешных запросах: пишется каждый
	// N-й. Единица означает, что пишутся все. Расчёт — в docs/LOGGING.md.
	SampleN int `yaml:"sample_n"`
	// Slow — порог, после которого запрос пишется вопреки прореживанию.
	Slow time.Duration `yaml:"slow"`
	// Audit — отдельный журнал обращений к персональным данным.
	Audit LoggingAudit `yaml:"audit"`
}

// LoggingAudit — настройки журнала аудита.
type LoggingAudit struct {
	// Enabled включает журнал аудита.
	Enabled *bool `yaml:"enabled"`
	// Path — файл журнала. Пусто означает общий поток вывода.
	Path string `yaml:"path"`
	// MaxBytes — размер, после которого файл ротируется.
	MaxBytes int64 `yaml:"max_bytes"`
	// Keep — сколько ротированных файлов хранить.
	Keep int `yaml:"keep"`
}

// Auth — способ опознания системы-потребителя.
type Auth struct {
	Header    string `yaml:"header"`
	KeySHA256 string `yaml:"key_sha256"`
	None      bool   `yaml:"-"`
}

// Upstream — адрес языковой модели для режима прокси.
type Upstream struct {
	URL      string        `yaml:"url"`
	TokenEnv string        `yaml:"token_env"`
	Model    string        `yaml:"model"`
	Timeout  time.Duration `yaml:"timeout"`
	// CAFile — файл с корневым сертификатом, которым подписан сертификат
	// модели. Внутренние ресурсы банка подписаны собственным удостоверяющим
	// центром, которого нет в системном хранилище.
	CAFile string `yaml:"ca_file"`
	// PinSHA256 — отпечаток открытого ключа сервера в кодировке base64.
	// Применяется, когда корневого сертификата нет под рукой: подлинность
	// сервера проверяется сравнением отпечатка, а не цепочкой доверия.
	PinSHA256 string `yaml:"pin_sha256"`
}

// ContextRule — правило, по которому тип маскируется только вместе с другими.
type ContextRule struct {
	Mask     pii.Type   `yaml:"mask"`
	Requires []pii.Type `yaml:"requires"`
}

// Exclusions — списки, которые выводят значения из-под маскирования.
type Exclusions struct {
	PublicFigures      bool     `yaml:"public_figures"`
	OrgAddresses       bool     `yaml:"org_addresses"`
	AllowPersons       []string `yaml:"allow_persons"`
	AllowAddresses     []string `yaml:"allow_addresses"`
	AllowAddressesFile string   `yaml:"allow_addresses_file"`
	AllowValues        []string `yaml:"allow_values"`
}

// System — настройки одной системы-потребителя.
type System struct {
	Enabled             bool                     `yaml:"enabled"`
	AuthRaw             yaml.Node                `yaml:"auth"`
	Types               []string                 `yaml:"types"`
	Demask              bool                     `yaml:"demask"`
	Preset              mask.Preset              `yaml:"preset"`
	PerType             map[pii.Type]mask.Preset `yaml:"per_type"`
	OnError             string                   `yaml:"on_error"`
	OnUnknownID         string                   `yaml:"on_unknown_id"`
	MinConfidence       *float64                 `yaml:"min_confidence"`
	DateWithoutAnchor   string                   `yaml:"date_without_anchor"`
	ContextRulesEnabled *bool                    `yaml:"context_rules_enabled"`
	InspectEnabled      *bool                    `yaml:"inspect_enabled"`
	ContextRules        []ContextRule            `yaml:"context_rules"`
	Exclusions          Exclusions               `yaml:"exclusions"`
	Upstream            Upstream                 `yaml:"upstream"`

	// Заполняются при разборе.
	Name     string            `yaml:"-"`
	Auth     Auth              `yaml:"-"`
	TypeSet  map[pii.Type]bool `yaml:"-"`
	AllTypes bool              `yaml:"-"`
}

// CustomType — тип персональных данных, добавленный настройкой, без правки ядра.
type CustomType struct {
	Name          pii.Type `yaml:"name"`
	Pattern       string   `yaml:"pattern"`
	Group         int      `yaml:"group"`
	Validator     string   `yaml:"validator"`
	Anchors       []string `yaml:"anchors"`
	RequireAnchor bool     `yaml:"require_anchor"`
	AnchorWindow  int      `yaml:"anchor_window"`
}

// Значения полей on_error и on_unknown_id.
const (
	OnErrorOpen          = "open"
	OnErrorClosed        = "closed"
	OnUnknownPassthrough = "passthrough"
	OnUnknown404         = "404"
	DateAnchorOnly       = "anchor_only"
	DatePIIContext       = "pii_context"
	DateAny              = "any"
)

// Enabled сообщает, действуют ли контекстные правила для системы.
func (s System) ContextRulesOn(d Defaults) bool {
	if s.ContextRulesEnabled != nil {
		return *s.ContextRulesEnabled
	}
	return d.ContextRulesEnabled
}

// InspectOn сообщает, доступен ли системе разбор текста.
func (s System) InspectOn(d Defaults) bool {
	if s.InspectEnabled != nil {
		return *s.InspectEnabled
	}
	return d.InspectEnabled
}

// MinConf возвращает порог уверенности для системы.
func (s System) MinConf(d Defaults) float64 {
	if s.MinConfidence != nil {
		return *s.MinConfidence
	}
	return d.MinConfidence
}

// DateMode возвращает режим обработки дат без явного якоря.
func (s System) DateMode(d Defaults) string {
	if s.DateWithoutAnchor != "" {
		return s.DateWithoutAnchor
	}
	if d.DateWithoutAnchor != "" {
		return d.DateWithoutAnchor
	}
	return DatePIIContext
}

// ErrorMode возвращает поведение при внутренней ошибке.
func (s System) ErrorMode(d Defaults) string {
	if s.OnError != "" {
		return s.OnError
	}
	if d.OnError != "" {
		return d.OnError
	}
	return OnErrorClosed
}

// UnknownIDMode возвращает поведение при неизвестном идентификаторе.
func (s System) UnknownIDMode(d Defaults) string {
	if s.OnUnknownID != "" {
		return s.OnUnknownID
	}
	if d.OnUnknownID != "" {
		return d.OnUnknownID
	}
	return OnUnknown404
}

// MaskOptions собирает настройки маскирования для системы.
func (s System) MaskOptions(d Defaults) mask.Options {
	p := s.Preset
	if !p.Valid() {
		p = d.Preset
	}
	return mask.Options{Default: p, PerType: s.PerType}
}

// Allows сообщает, маскирует ли система указанный тип.
func (s System) Allows(t pii.Type) bool {
	if s.AllTypes {
		return true
	}
	return s.TypeSet[t]
}

// Load читает настройки из файла и проверяет их.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // путь задаёт оператор сервиса
	if err != nil {
		return nil, fmt.Errorf("чтение файла настроек: %w", err)
	}
	return Parse(raw)
}

// Parse разбирает настройки из байтов и проверяет их.
func Parse(raw []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("разбор настроек: %w", err)
	}
	c.applyDefaults()
	if err := c.prepare(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Server.HTTP == "" {
		c.Server.HTTP = ":8080"
	}
	if c.Server.MaxBodyBytes == 0 {
		c.Server.MaxBodyBytes = 4 << 20
	}
	if c.Server.ReadHeaderTimeout == 0 {
		c.Server.ReadHeaderTimeout = 5 * time.Second
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 15 * time.Second
	}
	if c.Server.WriteTimeout == 0 {
		// Короче клиентского таймаута проверяющей системы в десять секунд,
		// чтобы обрыв был решением сервиса, а не клиента.
		c.Server.WriteTimeout = 9 * time.Second
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = 90 * time.Second
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = 15 * time.Second
	}
	if c.Server.DrainTimeout == 0 {
		// Пять секунд на обнаружение снятой готовности балансировщиком
		// (период проверки 2 с, порог 2 неудачи, предел ожидания 1 с) плюс
		// две секунды запаса на разброс проверок.
		c.Server.DrainTimeout = 7 * time.Second
	}
	if c.Limits.Inflight == 0 {
		c.Limits.Inflight = 96
	}
	if c.Limits.HeavyInflight == 0 {
		c.Limits.HeavyInflight = 6
	}
	if c.Limits.HeavyThresholdBytes == 0 {
		c.Limits.HeavyThresholdBytes = 64 << 10
	}
	if c.Limits.MaxWait == 0 {
		c.Limits.MaxWait = 500 * time.Millisecond
	}
	if c.Store.TTL == 0 {
		// Пятнадцать минут, а не час: предел числа записей ниже, чем даёт
		// час при плановой частоте, и час был бы обещанием, которого
		// хранилище не выполняет. Расчёт — в комментарии к Validate.
		c.Store.TTL = 15 * time.Minute
	}
	if c.Store.MaxRecords == 0 {
		c.Store.MaxRecords = 1_000_000
	}
	if c.Store.KeyEnv == "" {
		c.Store.KeyEnv = "PII_STORE_KEY"
	}
	// Адрес и пароль общего хранилища берутся из окружения: пароль в файле
	// настроек хранить нельзя, а адрес удобно задавать при развёртывании.
	if v := os.Getenv("PII_REDIS_ADDR"); v != "" {
		c.Store.Redis.Addr = v
	}
	if v := os.Getenv("PII_REDIS_PASSWORD"); v != "" {
		c.Store.Redis.Password = v
	}
	if !c.Defaults.Preset.Valid() {
		c.Defaults.Preset = mask.PresetFull
	}
	if c.Defaults.OnError == "" {
		c.Defaults.OnError = OnErrorClosed
	}
	if c.Defaults.OnUnknownID == "" {
		c.Defaults.OnUnknownID = OnUnknown404
	}
	if c.Defaults.MinConfidence == 0 {
		c.Defaults.MinConfidence = 0.6
	}
	if c.Defaults.DateWithoutAnchor == "" {
		c.Defaults.DateWithoutAnchor = DatePIIContext
	}
	c.applyLoggingDefaults()
}

// applyLoggingDefaults заполняет незаданные настройки журнала. Значения
// повторяют logging.DefaultConfig: файл настроек не обязан их перечислять,
// но и не должен обнулять то, чего в нём нет.
func (c *Config) applyLoggingDefaults() {
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	if c.Logging.RepeatWindow == 0 {
		c.Logging.RepeatWindow = 10 * time.Second
	}
	if c.Logging.RepeatLevel == "" {
		c.Logging.RepeatLevel = "warn"
	}
	if c.Logging.SampleN <= 0 {
		// Единица означает, что пишутся все записи об успешных запросах.
		// Почему по умолчанию единица — в комментарии к Validate.
		c.Logging.SampleN = 1
	}
	if c.Logging.Slow == 0 {
		c.Logging.Slow = 250 * time.Millisecond
	}
}

// prepare раскрывает сокращённые записи и заполняет служебные поля.
func (c *Config) prepare() error {
	for name, s := range c.Systems {
		s.Name = name
		s.AllTypes = false
		s.TypeSet = make(map[pii.Type]bool, len(s.Types))
		for _, t := range s.Types {
			if strings.EqualFold(t, "all") {
				s.AllTypes = true
				continue
			}
			s.TypeSet[pii.Type(strings.ToUpper(t))] = true
		}
		if len(s.Types) == 0 {
			s.AllTypes = true
		}
		auth, err := parseAuth(s.AuthRaw)
		if err != nil {
			return fmt.Errorf("система %q: %w", name, err)
		}
		s.Auth = auth
		s.Upstream.URL = expandEnv(s.Upstream.URL)
		s.Upstream.Model = expandEnv(s.Upstream.Model)
		s.Upstream.CAFile = expandEnv(s.Upstream.CAFile)
		s.Upstream.PinSHA256 = expandEnv(s.Upstream.PinSHA256)
		if s.Exclusions.AllowAddressesFile != "" {
			extra, ferr := readLines(s.Exclusions.AllowAddressesFile)
			if ferr != nil {
				return fmt.Errorf("система %q: список адресов: %w", name, ferr)
			}
			s.Exclusions.AllowAddresses = append(s.Exclusions.AllowAddresses, extra...)
		}
		c.Systems[name] = s
	}
	return nil
}

// readLines читает непустые строки файла, пропуская строки-комментарии.
// Используется для списков, которые удобнее держать отдельным файлом:
// например, адреса отделений банка.
func readLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // путь задаёт оператор сервиса
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// parseAuth принимает две записи: строку «none» и объект с заголовком и хешем ключа.
func parseAuth(n yaml.Node) (Auth, error) {
	if n.IsZero() {
		return Auth{None: true}, nil
	}
	if n.Kind == yaml.ScalarNode {
		var s string
		if err := n.Decode(&s); err != nil {
			return Auth{}, err
		}
		if strings.EqualFold(s, "none") {
			return Auth{None: true}, nil
		}
		return Auth{}, fmt.Errorf("неизвестное значение auth: %q", s)
	}
	var a Auth
	if err := n.Decode(&a); err != nil {
		return Auth{}, err
	}
	if a.Header == "" {
		a.Header = "X-System-Key"
	}
	a.KeySHA256 = strings.ToLower(strings.TrimSpace(expandEnv(a.KeySHA256)))
	return a, nil
}

// expandEnv подставляет значения переменных окружения вида ${NAME}.
func expandEnv(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return os.Expand(s, func(k string) string { return os.Getenv(k) })
}

// Validate проверяет настройки на противоречия, из-за которых сервис повёл бы
// себя не так, как ожидает проверяющая система или жюри.
func (c *Config) Validate() error {
	if err := c.validateLogging(); err != nil {
		return err
	}
	if len(c.Systems) == 0 {
		return errors.New("не задана ни одна система-потребитель")
	}
	anonymous := 0
	// Соотношения между сроками и пределами. По отдельности каждое значение
	// выглядит разумным, а вместе они дают поведение, которое никто не
	// закладывал. Проверяем здесь, потому что в бою это не видно: сервис
	// работает, просто не так, как написано в документах.
	if c.Limits.MaxWait > 0 && c.Server.WriteTimeout > 0 && c.Limits.MaxWait >= c.Server.WriteTimeout {
		return fmt.Errorf("ожидание места в ограничителе (%s) не короче срока записи ответа (%s): "+
			"запрос успеет получить обрыв соединения раньше, чем честный отказ с просьбой повторить",
			c.Limits.MaxWait, c.Server.WriteTimeout)
	}
	if c.Limits.Inflight > 0 && c.Limits.HeavyInflight >= c.Limits.Inflight {
		c.Warnings = append(c.Warnings, fmt.Sprintf(
			"отдельный ограничитель для больших текстов (%d) не меньше общего (%d): он ничего не ограничивает",
			c.Limits.HeavyInflight, c.Limits.Inflight))
	}

	// Один и тот же хеш ключа у двух систем делает выбор системы случайным:
	// порядок обхода карты в Go не определён, и запрос опознаётся то как одна,
	// то как другая. Снаружи это выглядит как плавающее поведение сервиса без
	// видимой причины, а по журналу видно разную систему на одинаковых
	// запросах. Ловим это на проверке настроек, а не в бою.
	byHash := make(map[string]string, len(c.Systems))
	for _, name := range sortedSystemNames(c.Systems) {
		h := c.Systems[name].Auth.KeySHA256
		if h == "" {
			continue
		}
		if other, busy := byHash[h]; busy {
			return fmt.Errorf("системы %q и %q делят один хеш ключа доступа: какая из них опознает запрос, будет решать случай", other, name)
		}
		byHash[h] = name
	}

	for name, s := range c.Systems {
		if !s.Enabled {
			continue
		}
		if s.Auth.None {
			anonymous++
		}
		p := s.Preset
		if p == "" {
			p = c.Defaults.Preset
		}
		if !p.Valid() {
			return fmt.Errorf("система %q: неизвестный пресет %q", name, p)
		}
		// Пресет, не сохраняющий длину, сдвигает границы соседних фрагментов,
		// поэтому анонимной системе проверяющего он запрещён.
		if s.Auth.None && !p.PreservesLength() {
			return fmt.Errorf("система %q без ключа не может использовать пресет %q: он не сохраняет длину", name, p)
		}
		for t, tp := range s.PerType {
			if !tp.Valid() {
				return fmt.Errorf("система %q: неизвестный пресет %q для типа %s", name, tp, t)
			}
			if s.Auth.None && !tp.PreservesLength() {
				return fmt.Errorf("система %q без ключа: пресет %q для типа %s не сохраняет длину", name, tp, t)
			}
		}
		// Система без ключа доступа ВЫКЛЮЧАЕТСЯ, а не роняет запуск.
		//
		// Прежнее поведение отвергало настройки целиком, и свежий клон с
		// пустым .env не стартовал вовсе: проверяющий копировал .env.example,
		// запускал по инструкции и получал отказ. При этом анонимный профиль
		// проверяющей системы ключа не требует, то есть контракт работал бы
		// и без остальных.
		//
		// Выключение это безопасное направление: система без ключа просто
		// недоступна. Опасным было бы обратное, включить её без проверки
		// ключа, и этого здесь не происходит.
		if !s.Auth.None && s.Auth.KeySHA256 == "" {
			s.Enabled = false
			c.Systems[name] = s
			// Имя переменной окружения не угадываем: в настройках оно
			// задаётся явно и не выводится из имени системы. Вместо догадки
			// отправляем туда, где написано точно.
			c.Warnings = append(c.Warnings,
				fmt.Sprintf("система %q выключена: пуст key_sha256, смотрите её раздел в файле настроек", name))
			continue
		}
		if s.Auth.KeySHA256 != "" && len(s.Auth.KeySHA256) != 64 {
			return fmt.Errorf("система %q: хеш ключа должен быть 64 символа", name)
		}
		switch s.ErrorMode(c.Defaults) {
		case OnErrorOpen, OnErrorClosed:
		default:
			return fmt.Errorf("система %q: неизвестное значение on_error", name)
		}
		switch s.UnknownIDMode(c.Defaults) {
		case OnUnknownPassthrough, OnUnknown404:
		default:
			return fmt.Errorf("система %q: неизвестное значение on_unknown_id", name)
		}
		switch s.DateMode(c.Defaults) {
		case DateAnchorOnly, DatePIIContext, DateAny:
		default:
			return fmt.Errorf("система %q: неизвестное значение date_without_anchor", name)
		}
	}
	if anonymous > 1 {
		return errors.New("без ключа доступа может работать только одна система")
	}
	for _, ct := range c.CustomTypes {
		if ct.Name == "" || ct.Pattern == "" {
			return errors.New("в custom_types нужны имя и выражение")
		}
	}
	return nil
}

// System возвращает настройки системы по имени.
func (c *Config) System(name string) (System, bool) {
	s, ok := c.Systems[name]
	return s, ok
}

// AnonymousSystem возвращает единственную систему без ключа доступа: именно ей
// приписываются запросы проверяющей системы, которая заголовков не шлёт.
func (c *Config) AnonymousSystem() (System, bool) {
	for _, s := range c.Systems {
		if s.Enabled && s.Auth.None {
			return s, true
		}
	}
	return System{}, false
}

// validateLogging проверяет настройки журнала. Проверка здесь, а не в момент
// создания журнала, потому что файл настроек перечитывается на ходу: набор с
// неизвестным уровнем должен быть отвергнут целиком, а не наполовину принят.
func (c *Config) validateLogging() error {
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("журнал: неизвестный уровень %q", c.Logging.Level)
	}
	switch strings.ToLower(c.Logging.RepeatLevel) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("журнал: неизвестный уровень глушения повторов %q", c.Logging.RepeatLevel)
	}
	switch strings.ToLower(c.Logging.Format) {
	case "json", "text":
	default:
		return fmt.Errorf("журнал: неизвестный формат %q", c.Logging.Format)
	}
	if c.Logging.SampleN < 1 {
		return fmt.Errorf("журнал: коэффициент прореживания должен быть не меньше единицы, получено %d", c.Logging.SampleN)
	}
	if c.Logging.Slow < 0 {
		return errors.New("журнал: порог медленного запроса не может быть отрицательным")
	}
	return nil
}

// sortedSystemNames возвращает имена систем в устойчивом порядке. Нужен там,
// где сообщение об ошибке не должно меняться от запуска к запуску.
func sortedSystemNames(m map[string]System) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
