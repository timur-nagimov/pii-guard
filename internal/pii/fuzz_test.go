package pii

import (
	"testing"
)

// allDetectors собирает полный набор детекторов, как в рабочем сервисе.
// Фаззер гоняет их по произвольному пользовательскому тексту: именно такой
// вход детекторы получают в проде, и именно для него фаззинг придуман.
func allDetectors() []Detector {
	return []Detector{
		NewNumericDetector(),
		NewEmailDetector(),
		NewFIODetector(),
		NewDateDetector(DateModePIIContext),
		NewAddressDetector(),
		NewBirthPlaceDetector(),
		NewIssuerDetector(),
		NewCitizenshipDetector(),
		NewDriverLicenseLettersDetector(),
		NewCardHolderDetector(),
		NewExtraDocumentsDetector(),
	}
}

// FuzzDetect прогоняет все детекторы по произвольному тексту. Цель — ни одной
// паники, ни одного выхода за границы среза, ни одного зацикливания. Вход
// детектора это произвольный текст от пользователя, поэтому фаззер мутирует
// настоящие тексты набора, а не выдуманные строки.
func FuzzDetect(f *testing.F) {
	reg := NewRegistry()
	reg.Register(allDetectors()...)
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 4<<20 {
			t.Skip()
		}
		doc := NewDoc(text)
		spans := reg.Detect(doc)
		// Фрагменты обязаны лежать в границах текста и быть непустыми.
		for _, s := range spans {
			if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
				t.Fatalf("фрагмент %s:%d-%d вне границ текста длиной %d", s.Type, s.Start, s.End, len(text))
			}
		}
		// Контекстные правила тоже обязаны пережить любой текст.
		filter := NewContextFilter(ContextOptions{PublicFigures: true, OrgAddresses: true})
		filter.Apply(doc, spans)
	})
}

// FuzzDigitRuns гоняет разбор числовых последовательностей. Именно на этом
// разборе стоят все форматные детекторы, и именно здесь раньше ломались
// границы среза.
func FuzzDigitRuns(f *testing.F) {
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 4<<20 {
			t.Skip()
		}
		doc := NewDoc(text)
		runs := doc.NumRuns()
		for _, r := range runs {
			if r.Start < 0 || r.End > len(text) || r.Start >= r.End {
				t.Fatalf("последовательность %d:%d вне границ текста длиной %d", r.Start, r.End, len(text))
			}
			if r.Digits != DigitsOnly(text[r.Start:r.End]) {
				t.Fatalf("цифры %q не совпали с текстом %q", r.Digits, text[r.Start:r.End])
			}
		}
	})
}

// FuzzNormalizeSpan гоняет обрезку границ фрагмента. Здесь раньше ломались
// границы среза на многобайтовых рунах.
func FuzzNormalizeSpan(f *testing.F) {
	f.Fuzz(func(t *testing.T, text string, start, end int) {
		if len(text) > 4<<20 {
			t.Skip()
		}
		s, e := NormalizeSpan(text, start, end)
		// Границы обязаны лежать в пределах текста. Перевёрнутый вход
		// (start > end) может дать перевёрнутый выход — это не паника, и
		// производственный код такие границы отбрасывает проверкой start >= end.
		if s < 0 || e > len(text) {
			t.Fatalf("NormalizeSpan(%q, %d, %d) = %d:%d вне границ", text, start, end, s, e)
		}
	})
}

// FuzzWindowRunes гоняет перевод байтовых смещений в рунные и обратно. Окна
// считаются в рунах, и ошибка перевода даёт выход за границы среза.
func FuzzWindowRunes(f *testing.F) {
	f.Fuzz(func(t *testing.T, text string, start, end, before, after int) {
		if len(text) > 4<<20 {
			t.Skip()
		}
		doc := NewDoc(text)
		lo, hi := doc.WindowRunes(start, end, before, after)
		if lo < 0 || hi > len(text) || lo > hi {
			t.Fatalf("WindowRunes(%d, %d, %d, %d) = %d:%d вне границ", start, end, before, after, lo, hi)
		}
		// LowerWindow обязан вернуть участок той же длины.
		w := doc.LowerWindow(start, end, before, after)
		if len(w) != hi-lo {
			t.Fatalf("LowerWindow вернул %d байт, ожидалось %d", len(w), hi-lo)
		}
	})
}

// FuzzContextFilter гоняет контекстные правила по произвольному тексту и
// произвольному набору фрагментов. Правила снимают фрагменты, и ошибка в
// границах здесь роняет обработчик.
func FuzzContextFilter(f *testing.F) {
	filter := NewContextFilter(ContextOptions{PublicFigures: true, OrgAddresses: true})
	f.Fuzz(func(t *testing.T, text string, a, b, c, d int) {
		if len(text) > 4<<20 {
			t.Skip()
		}
		doc := NewDoc(text)
		spans := []Span{
			{Start: a, End: b, Type: TypeFIO, Conf: ConfHigh},
			{Start: c, End: d, Type: TypeAddress, Conf: ConfHigh},
		}
		filter.Apply(doc, spans)
	})
}