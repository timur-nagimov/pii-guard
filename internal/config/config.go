// Package config читает и проверяет настройки сервиса. Настройки задаются
// одним файлом YAML и применяются без перезапуска: по сигналу SIGHUP или при
// изменении файла на диске.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"pii-guard/internal/mask"
	"pii-guard/internal/pii"
)

// Config — полный набор настроек сервиса.
type Config struct {
	Server      Server            `yaml:"server"`
	Limits      Limits            `yaml:"limits"`
	Store       Store             `yaml:"store"`
	Defaults    Defaults          `yaml:"defaults"`
	Systems     map[string]System `yaml:"systems"`
	CustomTypes []CustomType      `yaml:"custom_types"`
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
}

// Defaults — значения, действующие для всех систем, если те их не переопределили.
type Defaults struct {
	Preset              mask.Preset `yaml:"preset"`
	OnError             string      `yaml:"on_error"`
	OnUnknownID         string      `yaml:"on_unknown_id"`
	MinConfidence       float64     `yaml:"min_confidence"`
	ContextRulesEnabled bool        `yaml:"context_rules_enabled"`
	DateWithoutAnchor   string      `yaml:"date_without_anchor"`
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
		c.Store.TTL = time.Hour
	}
	if c.Store.MaxRecords == 0 {
		c.Store.MaxRecords = 1_000_000
	}
	if c.Store.KeyEnv == "" {
		c.Store.KeyEnv = "PII_STORE_KEY"
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
	if len(c.Systems) == 0 {
		return errors.New("не задана ни одна система-потребитель")
	}
	anonymous := 0
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
		if !s.Auth.None && s.Auth.KeySHA256 == "" {
			return fmt.Errorf("система %q: не задан хеш ключа доступа", name)
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
