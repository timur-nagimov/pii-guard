package engine

import (
	"strings"
	"testing"

	"pii-guard/internal/config"
	"pii-guard/internal/pii"
)

// benchRegistry собирает полный набор детекторов, как это делает сервис в
// рабочем режиме. Замер на урезанном наборе показал бы не то, что происходит
// на горячем пути.
func benchRegistry() *pii.Registry {
	reg := pii.NewRegistry()
	reg.Register(
		pii.NewNumericDetector(),
		pii.NewEmailDetector(),
		pii.NewFIODetector(),
		pii.NewDateDetector("pii_context"),
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

// benchText повторяет текст, который порождает генератор нагрузки при ключе
// payload, поэтому замер сопоставим с прогоном через сеть.
func benchText(size int) string {
	base := "Клиент Иванов Иван Иванович, паспорт 4509 123456, тел. +7 916 123-45-67, ИНН 500100732259. "
	var b strings.Builder
	for b.Len() < size {
		b.WriteString(base)
	}
	return b.String()[:size]
}

func benchSystem(tb testing.TB) (config.System, config.Defaults) {
	tb.Helper()
	cfg, err := config.Parse([]byte(`
systems:
  bench:
    enabled: true
    auth: none
    types: [all]
    demask: true
    preset: full
    min_confidence: 0.5
    exclusions:
      public_figures: true
      org_addresses: true
`))
	if err != nil {
		tb.Fatalf("настройки не разобрались: %v", err)
	}
	sys, ok := cfg.System("bench")
	if !ok {
		tb.Fatalf("система bench не найдена")
	}
	return sys, cfg.Defaults
}

// BenchmarkMask меряет полный конвейер на текстах тех размеров, на которых
// снимался потолок пропускной способности на стенде.
func BenchmarkMask(b *testing.B) {
	eng := New(benchRegistry())
	sys, defs := benchSystem(b)
	for _, size := range []int{250, 500, 2048, 8192} {
		text := benchText(size)
		b.Run(sizeName(size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = eng.Mask(text, sys, defs)
			}
		})
	}
}

// BenchmarkMaskParallel повторяет замер на всех ядрах: одиночный замер прячет
// издержки, которые появляются только при одновременной обработке.
func BenchmarkMaskParallel(b *testing.B) {
	eng := New(benchRegistry())
	sys, defs := benchSystem(b)
	text := benchText(250)
	b.SetBytes(250)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = eng.Mask(text, sys, defs)
		}
	})
}

func sizeName(size int) string {
	switch size {
	case 250:
		return "250b"
	case 500:
		return "500b"
	case 2048:
		return "2kb"
	default:
		return "8kb"
	}
}

// BenchmarkLinkSubjects меряет стоимость построения связей между фрагментами.
// Связи строятся на каждый запрос, и по заданию они не должны стоить больше
// пяти процентов от полного конвейера. Сравнение с BenchmarkMask показывает
// долю: если она выше пяти процентов, связи строятся только при включённом
// правиле сочетаний.
func BenchmarkLinkSubjects(b *testing.B) {
	eng := New(benchRegistry())
	for _, size := range []int{250, 500, 2048, 8192} {
		text := benchText(size)
		doc := pii.NewDoc(text)
		spans := eng.detectDoc(doc, newDegradation())
		b.Run(sizeName(size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = linkSubjects(doc, spans)
			}
		})
	}
}
