package store

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// newTestCodec готовит шифрование на заданном ключе.
func newTestCodec(t *testing.T, fill byte) *codec {
	t.Helper()
	c, err := newCodec(testKey(fill))
	if err != nil {
		t.Fatalf("не удалось создать шифрование: %v", err)
	}
	return c
}

// TestCodecRoundTrip проверяет, что запись переживает сериализацию и
// расшифровку без потерь.
func TestCodecRoundTrip(t *testing.T) {
	c := newTestCodec(t, 5)
	original := "Иванов Иван Иванович, паспорт 4509 123456, тел +7 999 000-11-22"
	spans := []SpanMeta{
		{Type: "FIO", Start: 0, End: 20},
		{Type: "PASSPORT", Start: 30, End: 41},
		{Type: "PHONE", Start: 47, End: 63},
	}
	expires := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	e, err := c.newEntry(original, "маска ***", "alfasonar", spans, expires)
	if err != nil {
		t.Fatalf("запись не собралась: %v", err)
	}
	blob, err := c.encode(e)
	if err != nil {
		t.Fatalf("сериализация не удалась: %v", err)
	}
	got, err := c.decode(blob)
	if err != nil {
		t.Fatalf("разбор не удался: %v", err)
	}
	if got.Mask != e.Mask || got.System != e.System {
		t.Fatalf("поля записи разошлись: %+v", got)
	}
	if got.OrigHash != e.OrigHash || got.MaskHash != e.MaskHash {
		t.Fatal("хеши разошлись")
	}
	if !got.ExpiresAt.Equal(expires) {
		t.Fatalf("срок истечения %v, ожидался %v", got.ExpiresAt, expires)
	}
	if len(got.Spans) != len(spans) {
		t.Fatalf("фрагментов %d, ожидалось %d", len(got.Spans), len(spans))
	}
	for i, sp := range got.Spans {
		if sp != spans[i] {
			t.Fatalf("фрагмент %d прочитан как %+v, ожидался %+v", i, sp, spans[i])
		}
	}
	back, err := c.original(got)
	if err != nil || back != original {
		t.Fatalf("исходный текст прочитан как %q, ошибка %v", back, err)
	}
}

// TestCodecEmptyFields проверяет запись без маски, системы и фрагментов.
func TestCodecEmptyFields(t *testing.T) {
	c := newTestCodec(t, 6)
	e, err := c.newEntry("", "", "", nil, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := c.encode(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.decode(blob)
	if err != nil {
		t.Fatalf("пустая запись не разобралась: %v", err)
	}
	if got.Mask != "" || got.System != "" || len(got.Spans) != 0 {
		t.Fatalf("пустая запись прочитана как %+v", got)
	}
}

// TestCodecValueIsEncrypted проверяет, что сериализованное значение не
// содержит ни исходного текста, ни маски, ни типов фрагментов.
func TestCodecValueIsEncrypted(t *testing.T) {
	c := newTestCodec(t, 7)
	original := "Петров Пётр, карта 4111 1111 1111 1111"
	e, err := c.newEntry(original, "маска", "sonar", []SpanMeta{{Type: "CARD", Start: 1, End: 2}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := c.encode(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{original, "Петров", "4111", "маска", "sonar", "CARD"} {
		if bytes.Contains(blob, []byte(part)) {
			t.Fatalf("кусок %q лежит в значении открытым", part)
		}
	}
	if len(blob) <= len(original) {
		t.Fatal("значение короче исходного текста, метка целостности отсутствует")
	}
}

// TestCodecCompact проверяет компактность: служебная часть значения невелика
// по сравнению с самим текстом.
func TestCodecCompact(t *testing.T) {
	c := newTestCodec(t, 8)
	original := strings.Repeat("текст запроса ", 40)
	e, err := c.newEntry(original, original, "sys", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := c.encode(e)
	if err != nil {
		t.Fatal(err)
	}
	overhead := len(blob) - 2*len(original)
	if overhead > 128 {
		t.Fatalf("служебная часть значения %d байт, ожидалось не больше 128", overhead)
	}
}

// TestCodecForeignKey проверяет, что значение, записанное одним ключом, не
// читается другим.
func TestCodecForeignKey(t *testing.T) {
	first := newTestCodec(t, 9)
	second := newTestCodec(t, 210)
	e, err := first.newEntry("секрет", "маска", "sys", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := first.encode(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.decode(blob); !errors.Is(err, ErrCorrupted) {
		t.Fatalf("получена ошибка %v, ожидалась %v", err, ErrCorrupted)
	}
}

// TestCodecBrokenValue проверяет разбор испорченных значений: ошибка вместо
// мусора и без паники.
func TestCodecBrokenValue(t *testing.T) {
	c := newTestCodec(t, 10)
	e, err := c.newEntry("текст", "маска", "sys", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := c.encode(e)
	if err != nil {
		t.Fatal(err)
	}

	short := blob[:4]
	if _, err := c.decode(short); !errors.Is(err, ErrNotFound) {
		t.Errorf("для короткого значения получена ошибка %v, ожидалась %v", err, ErrNotFound)
	}
	broken := bytes.Clone(blob)
	broken[len(broken)-1] ^= 0xFF
	if _, err := c.decode(broken); !errors.Is(err, ErrCorrupted) {
		t.Errorf("для испорченного значения получена ошибка %v, ожидалась %v", err, ErrCorrupted)
	}
	if _, err := c.decode(nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("для пустого значения получена ошибка %v, ожидалась %v", err, ErrNotFound)
	}
}

// TestCodecBadRecord проверяет разбор значения с верной меткой целостности, но
// неизвестным или обрезанным содержимым.
func TestCodecBadRecord(t *testing.T) {
	c := newTestCodec(t, 12)
	cases := []struct {
		name string
		body []byte
	}{
		{"пустое содержимое", nil},
		{"чужая версия формата", []byte{9, 0, 0}},
		{"обрезано на маске", []byte{recordVersion, 10, 'a'}},
		{"нет числа фрагментов", []byte{recordVersion, 0, 0, 1}},
		{"фрагментов больше, чем байт", []byte{recordVersion, 0, 0, 1, 200}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob, err := c.seal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.decode(blob); !errors.Is(err, ErrBadRecord) {
				t.Fatalf("получена ошибка %v, ожидалась %v", err, ErrBadRecord)
			}
		})
	}
}

// TestCodecKeySize проверяет требования к ключу шифрования.
func TestCodecKeySize(t *testing.T) {
	if _, err := newCodec(make([]byte, 16)); !errors.Is(err, ErrKeySize) {
		t.Errorf("короткий ключ дал ошибку %v, ожидалась %v", err, ErrKeySize)
	}
	if _, err := newCodec(make([]byte, 64)); !errors.Is(err, ErrKeySize) {
		t.Errorf("длинный ключ дал ошибку %v, ожидалась %v", err, ErrKeySize)
	}
	c, err := newCodec(nil)
	if err != nil {
		t.Fatalf("пустой ключ не заменён случайным: %v", err)
	}
	if _, err := c.newEntry("текст", "маска", "sys", nil, time.Now()); err != nil {
		t.Fatalf("на случайном ключе запись не собралась: %v", err)
	}
}

// TestCodecNonceDiffers проверяет, что два одинаковых текста дают разные
// значения: по длине и байтам значения нельзя понять, что текст повторился.
func TestCodecNonceDiffers(t *testing.T) {
	c := newTestCodec(t, 14)
	first, err := c.seal([]byte("один и тот же текст"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.seal([]byte("один и тот же текст"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("одинаковый текст зашифрован одинаково")
	}
}
