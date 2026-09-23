package engine

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// unmask восстанавливает исходный текст из замаскированного. Фрагменты задают
// границы в координатах исходного текста. Не-маскированные части обязаны
// стоять в замаскированном тексте без изменений; маскированные занимают в нём
// столько же рун, сколько исходное значение, и заменяются им. Если
// замаскированный текст не согласуется с фрагментами, возвращается строка,
// заведомо не равная исходной.
func unmask(original string, res Result) string {
	var b strings.Builder
	origPos := 0
	maskPos := 0
	for _, s := range res.Spans {
		plain := original[origPos:s.Start]
		if !strings.HasPrefix(res.Text[maskPos:], plain) {
			return "\x00unmask: не-маскированная часть не совпала"
		}
		b.WriteString(plain)
		maskPos += len(plain)

		value := original[s.Start:s.End]
		nRunes := utf8.RuneCountInString(value)
		segLen := advanceRunes(res.Text[maskPos:], nRunes)
		if nRunes > 0 && segLen == 0 {
			return "\x00unmask: маскированная часть выходит за границы"
		}
		b.WriteString(value)
		maskPos += segLen
		origPos = s.End
	}
	tail := original[origPos:]
	if res.Text[maskPos:] != tail {
		return "\x00unmask: хвост не совпал"
	}
	b.WriteString(tail)
	return b.String()
}

// advanceRunes возвращает байтовую длину первых n рун строки. Битые байты
// считаются по одному, как их считает range и utf8.RuneCountInString.
func advanceRunes(s string, n int) int {
	off := 0
	for i := 0; i < n && off < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[off:])
		off += size
	}
	return off
}

// invariantMaskRoundTrip проверяет свойство: для любого входного текста
// маскирование с последующим обратным преобразованием даёт исходный текст
// знак в знак.
func invariantMaskRoundTrip(t *testing.T, text string) {
	t.Helper()
	sys, defs := allTypesSystem(t)
	res := newTestEngine().Mask(text, sys, defs)
	if got := unmask(text, res); got != text {
		t.Fatalf("маскирование необратимо:\n  вход: %q\n  маска: %q\n  обратно: %q\n  фрагменты: %+v",
			text, res.Text, got, res.Spans)
	}
}

// TestMaskRoundTripEdgeCases проверяет свойство на граничных входных текстах:
// пустой строке, одном пробеле, тексте из одних цифр, на границе порога
// разбиения в 64 килобайта и сразу за ней, ломаном UTF-8, всех видах перевода
// строки, тексте из одних эмодзи и тексте в четыре мегабайта.
func TestMaskRoundTripEdgeCases(t *testing.T) {
	cases := []string{
		"",
		" ",
		"1234567890",
		strings.Repeat("a", chunkThreshold),
		strings.Repeat("a", chunkThreshold+1),
		string([]byte{0xff, 0xfe, 0xfd}),
		"a\nb\r\nc\rd\n",
		"😀😃😄😁😆😅😂🤣",
		strings.Repeat("😀", 4<<20/4),
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			invariantMaskRoundTrip(t, c)
		})
	}
}

// FuzzMaskRoundTrip проверяет свойство обратимости маскирования на
// произвольных входных текстах. Найденные фаззером входы складываются в
// testdata и проверяются обычным прогоном тестов.
func FuzzMaskRoundTrip(f *testing.F) {
	f.Add("")
	f.Add(" ")
	f.Add("1234567890")
	f.Add(strings.Repeat("a", chunkThreshold))
	f.Add(strings.Repeat("a", chunkThreshold+1))
	f.Add(string([]byte{0xff, 0xfe, 0xfd}))
	f.Add("a\nb\r\nc\rd\n")
	f.Add("😀😃😄😁😆😅😂🤣")
	f.Add(strings.Repeat("😀", 4<<20/4))

	f.Fuzz(func(t *testing.T, text string) {
		invariantMaskRoundTrip(t, text)
	})
}
