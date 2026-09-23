package pii

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// CustomRule — описание типа персональных данных, заданного настройкой.
// Оператор добавляет новый тип записью в раздел custom_types конфигурации,
// и ядро движка при этом не меняется.
type CustomRule struct {
	// Name — имя типа, оно же попадает в ответ и в метрики.
	Name Type
	// Pattern — выражение в синтаксисе Go, без опережающих и ретроспективных
	// проверок. Применяется к тексту в нижнем регистре и без учёта регистра,
	// поэтому писать его достаточно строчными буквами.
	Pattern string
	// Group — номер группы выражения, по которой берутся границы фрагмента.
	// Ноль означает всё совпадение целиком.
	Group int
	// Validator — имя контрольной суммы: luhn, inn, snils, none либо пусто.
	Validator string
	// Anchors — слова рядом со значением, повышающие уверенность.
	Anchors []string
	// RequireAnchor требует наличия якоря: без него фрагмент не возвращается.
	// Нужно для коротких неспецифичных форм вроде пяти цифр подряд.
	RequireAnchor bool
	// AnchorWindow — окно поиска якоря в рунах. Ноль означает окно по умолчанию.
	AnchorWindow int
}

// Допустимые значения поля Validator. Пустая строка равнозначна
// CustomValidatorNone и означает, что контрольной суммы у типа нет.
const (
	CustomValidatorNone  = "none"
	CustomValidatorLuhn  = "luhn"
	CustomValidatorINN   = "inn"
	CustomValidatorSNILS = "snils"
)

// defaultCustomAnchorWindow — окно поиска якоря по умолчанию, в рунах.
// Значение совпадает с окном встроенных детекторов, чтобы поведение типа из
// настройки не отличалось от поведения типа, зашитого в код.
const defaultCustomAnchorWindow = 40

// customSpanLimit — предел числа фрагментов на один документ. Ошибочное
// выражение вроде одиночной цифры иначе выдаст миллионы совпадений и съест
// всю память сервиса.
const customSpanLimit = 10000

// customIgnoreCase — флаг регистронезависимого поиска. Движок работает по
// копии текста в нижнем регистре, а флаг дополнительно спасает выражения,
// записанные оператором заглавными буквами.
const customIgnoreCase = "(?i)"

// unsupportedConstructs перечисляет конструкции Perl, которых нет в выражениях
// Go. Их ловим до компиляции, чтобы выдать понятную подсказку вместо
// технического сообщения разборщика.
var unsupportedConstructs = []string{"(?=", "(?!", "(?<=", "(?<!", "(?>"}

// compiledCustomRule — правило, готовое к применению: выражение скомпилировано,
// якоря приведены к нижнему регистру, валидатор выбран.
type compiledCustomRule struct {
	name          Type
	re            *regexp.Regexp
	group         int
	anchors       []string
	requireAnchor bool
	window        int
	validate      func(string) bool
	reasonPrefix  string
}

// customDetector находит типы, описанные в настройках. После создания поля
// только читаются, поэтому один экземпляр безопасно обслуживает параллельные
// запросы.
type customDetector struct {
	rules []compiledCustomRule
	types []Type
}

// NewCustomDetector собирает детектор по правилам из настроек.
//
// Выражения компилируются один раз при создании, а не при обработке запроса:
// неверная настройка должна отвергаться на проверке конфигурации, до того как
// сервис начнёт принимать трафик.
func NewCustomDetector(rules []CustomRule) (Detector, error) {
	det := customDetector{}
	seen := make(map[Type]bool, len(rules))
	// Правило берётся по указателю: описание типа занимает почти сотню байт,
	// и копировать его на каждом шаге незачем — разбор только читает поля.
	for i := range rules {
		rule := &rules[i]
		compiled, err := compileCustomRule(rule)
		if err != nil {
			return nil, err
		}
		det.rules = append(det.rules, compiled)
		if !seen[rule.Name] {
			seen[rule.Name] = true
			det.types = append(det.types, rule.Name)
		}
	}
	return det, nil
}

// compileCustomRule проверяет одно правило и готовит его к работе.
func compileCustomRule(rule *CustomRule) (compiledCustomRule, error) {
	if rule.Name == "" {
		return compiledCustomRule{}, errors.New("в правиле custom_types не задано имя типа")
	}
	if strings.TrimSpace(rule.Pattern) == "" {
		return compiledCustomRule{}, fmt.Errorf("правило %q: не задано выражение", rule.Name)
	}
	if bad, found := findUnsupportedConstruct(rule.Pattern); found {
		return compiledCustomRule{}, fmt.Errorf(
			"правило %q: конструкция %q не поддерживается синтаксисом выражений Go, опережающие и ретроспективные проверки и обратные ссылки использовать нельзя",
			rule.Name, bad)
	}
	if rule.Group < 0 {
		return compiledCustomRule{}, fmt.Errorf("правило %q: номер группы %d отрицательный", rule.Name, rule.Group)
	}
	re, err := regexp.Compile(customIgnoreCase + rule.Pattern)
	if err != nil {
		return compiledCustomRule{}, fmt.Errorf("правило %q: не удалось разобрать выражение %q: %w", rule.Name, rule.Pattern, err)
	}
	if re.MatchString("") {
		return compiledCustomRule{}, fmt.Errorf(
			"правило %q: выражение %q совпадает с пустой строкой и пометило бы весь документ целиком",
			rule.Name, rule.Pattern)
	}
	if rule.Group > re.NumSubexp() {
		return compiledCustomRule{}, fmt.Errorf(
			"правило %q: запрошена группа %d, а в выражении %q групп всего %d",
			rule.Name, rule.Group, rule.Pattern, re.NumSubexp())
	}
	validate, err := customValidator(rule.Name, rule.Validator)
	if err != nil {
		return compiledCustomRule{}, err
	}
	window := rule.AnchorWindow
	if window <= 0 {
		window = defaultCustomAnchorWindow
	}
	return compiledCustomRule{
		name:          rule.Name,
		re:            re,
		group:         rule.Group,
		anchors:       lowerAnchors(rule.Anchors),
		requireAnchor: rule.RequireAnchor,
		window:        window,
		validate:      validate,
		reasonPrefix:  "custom:" + string(rule.Name) + ":",
	}, nil
}

// customValidator выбирает функцию контрольной суммы по её имени.
// Неизвестное имя — ошибка настройки: молча игнорировать его нельзя, иначе
// оператор будет уверен в проверке, которой на самом деле нет.
func customValidator(name Type, validator string) (func(string) bool, error) {
	switch strings.ToLower(strings.TrimSpace(validator)) {
	case "", CustomValidatorNone:
		return nil, nil
	case CustomValidatorLuhn:
		return Luhn, nil
	case CustomValidatorINN:
		return INNValid, nil
	case CustomValidatorSNILS:
		return SNILSValid, nil
	default:
		return nil, fmt.Errorf(
			"правило %q: неизвестный валидатор %q, допустимы luhn, inn, snils и none",
			name, validator)
	}
}

// lowerAnchors приводит якоря к нижнему регистру и выбрасывает пустые:
// поиск идёт по копии текста в нижнем регистре.
func lowerAnchors(anchors []string) []string {
	out := make([]string, 0, len(anchors))
	for _, a := range anchors {
		if a == "" {
			continue
		}
		out = append(out, strings.ToLower(a))
	}
	return out
}

// findUnsupportedConstruct ищет в выражении конструкции, которых нет в Go.
func findUnsupportedConstruct(pattern string) (string, bool) {
	for _, c := range unsupportedConstructs {
		if strings.Contains(pattern, c) {
			return c, true
		}
	}
	return findBackreference(pattern)
}

// findBackreference ищет обратную ссылку вида \1. Экранированный символ
// пропускается целиком, иначе запись \\1 была бы принята за ссылку.
func findBackreference(pattern string) (string, bool) {
	for i := 0; i+1 < len(pattern); i++ {
		if pattern[i] != '\\' {
			continue
		}
		if pattern[i+1] >= '1' && pattern[i+1] <= '9' {
			return pattern[i : i+2], true
		}
		i++
	}
	return "", false
}

// Types перечисляет типы, описанные в настройках, без повторов.
func (c customDetector) Types() []Type {
	return append([]Type(nil), c.types...)
}

// Detect применяет правила настройки к документу.
//
// Результат упорядочен и не зависит от порядка правил в файле настроек:
// пересечения всё равно снимает Resolve, но стабильный порядок делает ответ
// воспроизводимым при одинаковом входе.
func (c customDetector) Detect(d *Doc) []Span {
	if len(c.rules) == 0 {
		return nil
	}
	var out []Span
	for i := range c.rules {
		left := customSpanLimit - len(out)
		if left <= 0 {
			break
		}
		out = append(out, c.rules[i].collect(d, left)...)
	}
	sortCustomSpans(out)
	return out
}

// collect применяет одно правило, но не больше limit совпадений.
func (r *compiledCustomRule) collect(d *Doc, limit int) []Span {
	matches := r.re.FindAllStringSubmatchIndex(d.Lower, limit)
	out := make([]Span, 0, len(matches))
	for _, m := range matches {
		if s, ok := r.spanFor(d, m); ok {
			out = append(out, s)
		}
	}
	return out
}

// spanFor превращает одно совпадение в фрагмент, проверяя якорь и контрольную
// сумму. Второй результат ложный, если совпадение отбраковано.
func (r *compiledCustomRule) spanFor(d *Doc, m []int) (Span, bool) {
	start, end, ok := r.bounds(m)
	if !ok {
		return Span{}, false
	}
	// Фрагмент персональных данных не пересекает перевод строки: выражение с
	// жадным пробельным классом иначе склеит две соседние записи.
	if _, hi := d.LineBounds(start); end > hi {
		return Span{}, false
	}
	start, end = NormalizeSpan(d.Text, start, end)
	if start >= end {
		return Span{}, false
	}
	hasAnchor := false
	if len(r.anchors) > 0 {
		_, hasAnchor = d.FindAnchor(start, end, r.anchors, r.window, r.window)
	}
	if r.requireAnchor && !hasAnchor {
		return Span{}, false
	}
	passed := false
	if r.validate != nil {
		passed = r.validate(DigitsOnly(d.Text[start:end]))
	}
	conf, reason := customConfidence(r.validate != nil, passed, hasAnchor)
	return Span{Start: start, End: end, Type: r.name, Conf: conf, Reason: r.reasonPrefix + reason}, true
}

// bounds возвращает границы совпадения с учётом заданной группы.
func (r *compiledCustomRule) bounds(m []int) (start, end int, ok bool) {
	start, end = m[0], m[1]
	if r.group > 0 {
		idx := 2 * r.group
		if idx+1 >= len(m) || m[idx] < 0 {
			return 0, 0, false
		}
		start, end = m[idx], m[idx+1]
	}
	return start, end, start < end
}

// customConfidence назначает уверенность по двум усилителям: контрольной
// сумме и якорю.
//
// Контрольная сумма только повышает уверенность и не является условием:
// в проверочных текстах номера выдуманные и сумму не проходят, а пропуск
// такого фрагмента стоит дороже лишнего срабатывания.
func customConfidence(hasValidator, passed, hasAnchor bool) (conf float64, reason string) {
	switch {
	case hasValidator && passed && hasAnchor:
		return ConfHigh, "checksum+anchor"
	case hasValidator && passed:
		return ConfAnchored, "checksum"
	case hasAnchor:
		return ConfAnchored, "anchor"
	default:
		return ConfMedium, "shape"
	}
}

// sortCustomSpans упорядочивает фрагменты так, чтобы результат не зависел от
// порядка правил в настройке: сначала по началу, затем по убыванию длины и
// уверенности, и в последнюю очередь по имени типа.
func sortCustomSpans(spans []Span) {
	sort.Slice(spans, func(i, j int) bool {
		a, b := spans[i], spans[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.Len() != b.Len() {
			return a.Len() > b.Len()
		}
		if a.Conf != b.Conf {
			return a.Conf > b.Conf
		}
		return a.Type < b.Type
	})
}
