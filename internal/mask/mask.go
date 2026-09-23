// Package mask применяет к найденным фрагментам персональных данных выбранный
// вид маскирования. Вид задаётся пресетом и настраивается для каждой системы и
// каждого типа отдельно в конфигурации.
package mask

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"pii-guard/internal/pii"
)

// Preset — вид маскирования.
type Preset string

// Поддерживаемые виды маскирования. Полное описание формата с примерами по
// каждому типу лежит в docs/MASK-FORMAT.md, ссылка на него обязательна при
// сдаче: эталонных масок у организаторов нет, формат выбирает участник.
const (
	// PresetFull заменяет каждую руну фрагмента на звёздочку, сохраняя их
	// число. Вид по умолчанию: делает текст нечитаемым и невосстановимым,
	// не сдвигает границы соседних фрагментов.
	PresetFull Preset = "full"
	// PresetFullWS сохраняет пробелы внутри фрагмента, остальное заменяет.
	PresetFullWS Preset = "full_ws"
	// PresetPartial оставляет видимыми первые и последние два знака значения.
	PresetPartial Preset = "partial"
	// PresetInitials сворачивает имя до инициалов. Длину не сохраняет,
	// поэтому для профиля проверяющей системы запрещён валидатором конфигурации.
	PresetInitials Preset = "initials"
	// PresetToken подставляет типизированный плейсхолдер вида [FIO_1].
	// Единственный вид, переживающий ответ языковой модели, поэтому
	// используется в режиме прокси.
	PresetToken Preset = "token"
	// PresetSynthetic подставляет правдоподобное значение того же типа.
	PresetSynthetic Preset = "synthetic"
)

// Valid сообщает, что пресет известен.
func (p Preset) Valid() bool {
	switch p {
	case PresetFull, PresetFullWS, PresetPartial, PresetInitials, PresetToken, PresetSynthetic:
		return true
	default:
		return false
	}
}

// PreservesLength сообщает, сохраняет ли пресет число знаков во фрагменте.
// Виды, которые не сохраняют, нельзя включать в профиле проверяющей системы:
// сдвиг границ портит выравнивание соседних фрагментов.
func (p Preset) PreservesLength() bool {
	return p == PresetFull || p == PresetFullWS || p == PresetPartial
}

// Options описывает, как маскировать конкретный запрос.
type Options struct {
	// Default — пресет по умолчанию для всех типов.
	Default Preset
	// PerType переопределяет пресет для отдельных типов.
	PerType map[pii.Type]Preset
	// Shared — общее состояние нумерации плейсхолдеров для нескольких вызовов
	// Apply подряд. Нужно, когда один запрос маскируется по частям (сообщения
	// прокси): без него счётчик начинался бы с единицы в каждой части, и разные
	// значения получали бы один и тот же плейсхолдер, а обратное преобразование
	// подставило бы вместо них одно последнее значение.
	Shared *TokenState
}

// TokenState — состояние нумерации плейсхолдеров и подстановок одного прохода
// маскирования; общее состояние нескольких вызовов Apply — тот же набор карт,
// поэтому он же передаётся в Options.Shared.
type TokenState struct {
	counters   map[pii.Type]int
	valueToken map[string]string
	// synthetic — соответствие «значение → подстановка» для вида synthetic.
	// Нужно, чтобы одно и то же значение в одном запросе всегда получало одну
	// и ту же подстановку, а разные значения не сталкивались.
	synthetic map[string]string
	// syntheticUsed — уже выданные подстановки, чтобы избежать коллизий в
	// пределах запроса.
	syntheticUsed map[string]bool
}

// PresetFor возвращает пресет для типа с учётом переопределений.
func (o Options) PresetFor(t pii.Type) Preset {
	if p, ok := o.PerType[t]; ok && p.Valid() {
		return p
	}
	if o.Default.Valid() {
		return o.Default
	}
	return PresetFull
}

// Placeholder — соответствие плейсхолдера исходному значению. Нужно, чтобы
// демаскировать ответ языковой модели в режиме прокси.
type Placeholder struct {
	Token string
	Value string
	Type  pii.Type
}

// Result — итог маскирования.
type Result struct {
	// Text — текст с применёнными масками.
	Text string
	// Placeholders — подстановки для режима token в порядке появления.
	Placeholders []Placeholder
}

// Apply накладывает маски на текст. Фрагменты должны быть непересекающимися и
// отсортированными по возрастанию начала: так их отдаёт pii.Resolve.
func Apply(text string, spans []pii.Span, opts Options) Result {
	var b strings.Builder
	b.Grow(len(text))

	res := Result{}
	// Своё состояние прохода; при общем состоянии берём карты вызывающей
	// стороны — они ссылочные, поэтому applySpan пополняет их напрямую.
	state := &TokenState{
		counters:      make(map[pii.Type]int),
		valueToken:    make(map[string]string),
		synthetic:     make(map[string]string),
		syntheticUsed: make(map[string]bool),
	}
	if opts.Shared != nil {
		if opts.Shared.counters == nil {
			opts.Shared.counters = make(map[pii.Type]int)
			opts.Shared.valueToken = make(map[string]string)
			opts.Shared.synthetic = make(map[string]string)
			opts.Shared.syntheticUsed = make(map[string]bool)
		}
		state = opts.Shared
	}

	prev := 0
	for _, s := range spans {
		if !s.Valid() || s.Start < prev || s.End > len(text) {
			continue
		}
		b.WriteString(text[prev:s.Start])
		value := text[s.Start:s.End]
		b.WriteString(applySpan(s, value, opts, state, &res))
		prev = s.End
	}
	b.WriteString(text[prev:])

	res.Text = b.String()
	return res
}

// applySpan маскирует один фрагмент и возвращает текст для подстановки.
// Плейсхолдеры и подстановки synthetic добавляются в результат, чтобы их
// можно было восстановить в ответе.
func applySpan(s pii.Span, value string, opts Options, state *TokenState, res *Result) string {
	preset := opts.PresetFor(s.Type)

	switch preset {
	case PresetToken:
		key := string(s.Type) + "\x00" + value
		tok, seen := state.valueToken[key]
		if !seen {
			state.counters[s.Type]++
			tok = "[" + string(s.Type) + "_" + itoa(state.counters[s.Type]) + "]"
			state.valueToken[key] = tok
			res.Placeholders = append(res.Placeholders, Placeholder{Token: tok, Value: value, Type: s.Type})
		}
		return tok
	case PresetSynthetic:
		key := string(s.Type) + "\x00" + value
		sub, seen := state.synthetic[key]
		if !seen {
			seed := hashValue(value)
			sub = syntheticValue(value, s.Type, seed)
			// Разные значения не должны сталкиваться в пределах запроса:
			// при совпадении подстановка пересчитывается с другим сидом.
			for state.syntheticUsed[sub] {
				seed++
				sub = syntheticValue(value, s.Type, seed)
			}
			state.synthetic[key] = sub
			state.syntheticUsed[sub] = true
			res.Placeholders = append(res.Placeholders, Placeholder{Token: sub, Value: value, Type: s.Type})
		}
		return sub
	default:
		return maskValue(value, preset)
	}
}

// maskValue применяет к одному значению пресет, не сохраняющий состояния.
func maskValue(value string, preset Preset) string {
	switch preset {
	case PresetFullWS:
		return maskRunes(value, true)
	case PresetPartial:
		return maskPartial(value)
	case PresetInitials:
		return maskInitials(value)
	default:
		return maskRunes(value, false)
	}
}

// maskRunes заменяет руны на звёздочки. keepSpaces сохраняет пробельные знаки.
func maskRunes(value string, keepSpaces bool) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if keepSpaces && unicode.IsSpace(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('*')
	}
	return b.String()
}

// maskPartial оставляет видимыми два первых и два последних знака значения.
// Для значений короче шести знаков маскирует всё: иначе видимая часть
// восстанавливает исходное значение.
func maskPartial(value string) string {
	runes := []rune(value)
	significant := 0
	for _, r := range runes {
		if isSignificant(r) {
			significant++
		}
	}
	if significant < 6 {
		return maskRunes(value, false)
	}
	out := make([]rune, len(runes))
	seen := 0
	for i, r := range runes {
		if !isSignificant(r) {
			out[i] = r
			continue
		}
		seen++
		if seen <= 2 || seen > significant-2 {
			out[i] = r
		} else {
			out[i] = '*'
		}
	}
	return string(out)
}

// isSignificant отличает знаки самого значения от разделителей.
func isSignificant(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// maskInitials сворачивает многословное имя до инициалов: «Иванов Иван
// Иванович» превращается в «И. И. И.».
func maskInitials(value string) string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && r != '-'
	})
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		for _, r := range f {
			parts = append(parts, string(unicode.ToUpper(r))+".")
			break
		}
	}
	if len(parts) == 0 {
		return maskRunes(value, false)
	}
	return strings.Join(parts, " ")
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// maskShareThreshold — доля знаков маски в тексте, начиная с которой текст
// считается уже замаскированным. Порог выбран так, чтобы явно замаскированный
// текст (звёздочки, плейсхолдеры, инициалы) проходил, а обычный текст с редкой
// звёздочкой или одиночным инициалом — нет.
const maskShareThreshold = 0.5

// LooksMasked сообщает, что текст уже похож на маску данного вида: в нём
// существенная доля знаков заменена звёздочками, либо текст состоит из
// плейсхолдеров вида [FIO_1], либо из инициалов. Нужно, чтобы не маскировать
// повторно текст, который клиент прислал на восстановление после истечения
// срока хранения: звёздочки стали бы «исходником», и оригинал был бы потерян.
func LooksMasked(text string, preset Preset) bool {
	if text == "" {
		return false
	}
	switch preset {
	case PresetToken:
		return tokenShare(text) >= maskShareThreshold
	case PresetInitials:
		return initialsShare(text) >= maskShareThreshold
	default:
		return starShare(text) >= maskShareThreshold
	}
}

// starShare считает долю звёздочек среди непустых знаков текста. Пробелы не
// считаются: они остаются в маске и не говорят о маскировании.
func starShare(text string) float64 {
	total := 0
	stars := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if r == '*' {
			stars++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(stars) / float64(total)
}

// tokenShare считает долю знаков, занятых плейсхолдерами вида [FIO_1].
func tokenShare(text string) float64 {
	total := 0
	masked := 0
	for i := 0; i < len(text); {
		if text[i] == '[' {
			end := strings.IndexByte(text[i+1:], ']')
			if end >= 0 {
				tok := text[i : i+end+2]
				if isToken(tok) {
					masked += len(tok)
					total += len(tok)
					i += len(tok)
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if !unicode.IsSpace(r) {
			total += size
		}
		i += size
	}
	if total == 0 {
		return 0
	}
	return float64(masked) / float64(total)
}

// isToken проверяет, что строка имеет вид [ТИП_номер]: тип из заглавных букв и
// подчёркиваний, номер из цифр.
func isToken(s string) bool {
	if len(s) < 5 || s[0] != '[' || s[len(s)-1] != ']' {
		return false
	}
	inner := s[1 : len(s)-1]
	idx := strings.LastIndexByte(inner, '_')
	if idx <= 0 || idx == len(inner)-1 {
		return false
	}
	for _, r := range inner[:idx] {
		if !unicode.IsUpper(r) && r != '_' {
			return false
		}
	}
	for _, r := range inner[idx+1:] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// initialsShare считает долю знаков, занятых инициалами вида «И.». Инициалом
// считается буква, за которой сразу идёт точка, и сама эта точка.
func initialsShare(text string) float64 {
	total := 0
	masked := 0
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if unicode.IsLetter(r) && i+1 < len(runes) && runes[i+1] == '.' {
			masked++
		} else if r == '.' && i > 0 && unicode.IsLetter(runes[i-1]) {
			masked++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(masked) / float64(total)
}
