package engine

import (
	"testing"

	"pii-guard/internal/config"
	"pii-guard/internal/pii"
)

// fullRegistry собирает полный набор детекторов, как в рабочем сервисе.
func fullRegistry() *pii.Registry {
	reg := pii.NewRegistry()
	reg.Register(
		pii.NewNumericDetector(),
		pii.NewEmailDetector(),
		pii.NewFIODetector(),
		pii.NewDateDetector(config.DatePIIContext),
		pii.NewAddressDetector(),
		pii.NewBirthPlaceDetector(),
		pii.NewIssuerDetector(),
		pii.NewCitizenshipDetector(),
		pii.NewDriverLicenseLettersDetector(),
		pii.NewCardHolderDetector(),
		pii.NewExtraDocumentsDetector(),
	)
	return reg
}

// fuzzSystem возвращает систему, которая маскирует всё звёздочками.
func fuzzSystem() (config.System, config.Defaults) {
	cfg, err := config.Parse([]byte(`
systems:
  anon:
    enabled: true
    auth: none
    types: [all]
    preset: full
    min_confidence: 0.5
`))
	if err != nil {
		panic(err)
	}
	sys, ok := cfg.System("anon")
	if !ok {
		panic("система anon не найдена")
	}
	return sys, cfg.Defaults
}

// FuzzEngineMask гоняет весь конвейер: разбор, детекторы, контекстные правила,
// разрешение пересечений и маскирование. На длинных текстах включается
// разбиение на куски и параллельная обработка — именно там раньше падала
// горутина куска.
func FuzzEngineMask(f *testing.F) {
	e := New(fullRegistry())
	sys, defs := fuzzSystem()
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 4<<20 {
			t.Skip()
		}
		res := e.Mask(text, sys, defs)
		// Фрагменты обязаны лежать в границах текста.
		for _, s := range res.Spans {
			if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
				t.Fatalf("фрагмент %s:%d-%d вне границ текста длиной %d", s.Type, s.Start, s.End, len(text))
			}
		}
	})
}

// FuzzSplitBounds гоняет разбиение длинного текста на куски. Границы кусков
// обязаны попадать на начало руны, иначе кусок окажется битым и срез по нему
// упадёт.
func FuzzSplitBounds(f *testing.F) {
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 4<<20 {
			t.Skip()
		}
		bounds := splitBounds(text)
		for _, b := range bounds {
			if b[0] < 0 || b[1] > len(text) || b[0] > b[1] {
				t.Fatalf("кусок %d:%d вне границ текста длиной %d", b[0], b[1], len(text))
			}
			// Граница обязана попадать на начало руны. Нулевая позиция всегда
			// граница: alignRune не сдвигает её, и она не может быть битой.
			if b[0] > 0 && b[0] < len(text) && !isRuneStart(text[b[0]]) {
				t.Fatalf("начало куска %d не на границе руны", b[0])
			}
			if b[1] > 0 && b[1] < len(text) && !isRuneStart(text[b[1]]) {
				t.Fatalf("конец куска %d не на границе руны", b[1])
			}
		}
	})
}

// isRuneStart сообщает, что байт начинает руну.
func isRuneStart(b byte) bool {
	return b&0xC0 != 0x80
}
