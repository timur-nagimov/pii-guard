// Package mask применяет к найденным фрагментам персональных данных выбранный
// вид маскирования. Вид задаётся пресетом и настраивается для каждой системы и
// каждого типа отдельно в конфигурации.
package mask

import (
	"strings"
	"unicode"

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

// TokenState — общее состояние нумерации плейсхолдеров между вызовами Apply.
type TokenState struct {
	counters   map[pii.Type]int
	valueToken map[string]string
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
	counters := make(map[pii.Type]int)
	valueToken := make(map[string]string)
	if opts.Shared != nil {
		if opts.Shared.counters == nil {
			opts.Shared.counters = make(map[pii.Type]int)
			opts.Shared.valueToken = make(map[string]string)
		}
		counters = opts.Shared.counters
		valueToken = opts.Shared.valueToken
	}

	prev := 0
	for _, s := range spans {
		if !s.Valid() || s.Start < prev || s.End > len(text) {
			continue
		}
		b.WriteString(text[prev:s.Start])
		value := text[s.Start:s.End]
		preset := opts.PresetFor(s.Type)

		switch preset {
		case PresetToken:
			tok, fresh := tokenFor(s.Type, value, counters, valueToken)
			if fresh {
				res.Placeholders = append(res.Placeholders, Placeholder{Token: tok, Value: value, Type: s.Type})
			}
			b.WriteString(tok)
		default:
			b.WriteString(maskValue(value, preset))
		}
		prev = s.End
	}
	b.WriteString(text[prev:])

	res.Text = b.String()
	return res
}

// tokenFor выдаёт метку значению: одно и то же значение одного типа получает
// одну и ту же метку, иначе связь между упоминаниями в тексте потерялась бы.
// Второй результат сообщает, что метка выдана впервые и её надо занести в
// список подстановок — повторную запись список не переживёт.
func tokenFor(t pii.Type, value string, counters map[pii.Type]int, valueToken map[string]string) (string, bool) {
	key := string(t) + "\x00" + value
	if tok, seen := valueToken[key]; seen {
		return tok, false
	}
	counters[t]++
	tok := "[" + string(t) + "_" + itoa(counters[t]) + "]"
	valueToken[key] = tok
	return tok, true
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
	case PresetSynthetic:
		// Правдоподобная подстановка — отдельный пресет со своим словарём.
		// Пока он не подключён, значение маскируется полностью: это никогда
		// не приводит к утечке.
		return maskRunes(value, false)
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
