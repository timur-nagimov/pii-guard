package pii

import "testing"

// TestEmailTrailingDotNoPanic закрывает ошибку, найденную на живом запросе:
// адрес в конце предложения приводил к перевёрнутым границам среза и сбою
// детектора. Проверяем и отсутствие сбоя, и правильные границы.
func TestEmailTrailingDotNoPanic(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"точка в конце предложения", "почта ivan.petrov@mail.ru.", "ivan.petrov@mail.ru"},
		{"две точки в конце", "пишите на a@b.ru..", "a@b.ru"},
		{"точка и пробел", "адрес a@b.ru. Далее текст", "a@b.ru"},
		{"домен без зоны", "битый адрес a@b.", ""},
		{"только собака с точкой", "@.", ""},
		{"полное предложение", "Контакты: телефон +7 (916) 123-45-67, электронная почта ivan.petrov@mail.ru.", "ivan.petrov@mail.ru"},
	}
	det := NewEmailDetector()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := NewDoc(c.text)
			spans := det.Detect(d)
			if c.want == "" {
				if len(spans) != 0 {
					t.Fatalf("ожидалось отсутствие находок, получено %d: %q", len(spans), c.text[spans[0].Start:spans[0].End])
				}
				return
			}
			if len(spans) != 1 {
				t.Fatalf("ожидалась ровно одна находка, получено %d", len(spans))
			}
			got := c.text[spans[0].Start:spans[0].End]
			if got != c.want {
				t.Fatalf("границы неверны: получено %q, ожидалось %q", got, c.want)
			}
		})
	}
}

// TestRegistryRecoversDetectorPanic проверяет, что сбой одного детектора не
// роняет обработку и не мешает остальным типам найтись.
func TestRegistryRecoversDetectorPanic(t *testing.T) {
	reg := NewRegistry()
	var gotPanic bool
	reg.OnPanic(func(_ []Type, _ any) { gotPanic = true })
	reg.Register(panicDetector{}, NewEmailDetector())

	spans := reg.Detect(NewDoc("адрес a@b.ru в тексте"))
	if !gotPanic {
		t.Fatal("обработчик сбоя не вызван")
	}
	if len(spans) != 1 || spans[0].Type != TypeEmail {
		t.Fatalf("исправный детектор должен был отработать, получено %#v", spans)
	}
}

type panicDetector struct{}

func (panicDetector) Types() []Type { return []Type{TypeFIO} }

func (panicDetector) Detect(_ *Doc) []Span { panic("проверка перехвата") }
