package engine

import (
	"strings"
	"testing"

	"pii-guard/internal/pii"
)

// linkTestEngine собирает конвейер с детекторами, нужными для проверки связей:
// ФИО, числа (телефон, паспорт, карта, пин) и почта.
func linkTestEngine() *Engine {
	reg := pii.NewRegistry()
	reg.Register(pii.NewNumericDetector(), pii.NewEmailDetector(), pii.NewFIODetector())
	return New(reg)
}

// TestLinkBySentence проверяет, что фрагменты в одном предложении связываются
// основанием «соседство», а фрагменты из разных предложений — нет.
func TestLinkBySentence(t *testing.T) {
	text := "Иванов Иван Иванович, тел +7 916 123-45-67. Петров Пётр, тел +7 916 000-00-00."
	doc := pii.NewDoc(text)
	spans := linkTestEngine().detect(text)

	links := linkSubjects(doc, spans)
	if len(links.Subjects) != 2 {
		t.Fatalf("ожидались два субъекта, получено %d: %+v", len(links.Subjects), links.Subjects)
	}
	// В каждом субъекте должны быть ФИО и телефон одного человека.
	for _, sub := range links.Subjects {
		if !sub.HasType(pii.TypeFIO) || !sub.HasType(pii.TypePhone) {
			t.Fatalf("субъект %+v не содержит и ФИО, и телефон", sub)
		}
	}
	// Рёбра должны быть основанием «соседство».
	for _, e := range links.Edges {
		if e.Basis != BasisSentence {
			t.Fatalf("ребро %+v имеет основание %q, ожидалось sentence", e, e.Basis)
		}
	}
}

// TestLinkByAnchor проверяет, что фрагменты в одной анкете связываются
// основанием «общий якорь», даже если они в разных строках.
func TestLinkByAnchor(t *testing.T) {
	text := "ФИО: Иванов Иван Иванович\nТелефон: +7 916 123-45-67\nПаспорт: 4509 123456"
	doc := pii.NewDoc(text)
	spans := linkTestEngine().detect(text)

	links := linkSubjects(doc, spans)
	if len(links.Subjects) != 1 {
		t.Fatalf("ожидался один субъект, получено %d: %+v", len(links.Subjects), links.Subjects)
	}
	sub := links.Subjects[0]
	for _, want := range []pii.Type{pii.TypeFIO, pii.TypePhone, pii.TypePassport} {
		if !sub.HasType(want) {
			t.Fatalf("субъект %+v не содержит тип %s", sub, want)
		}
	}
	// Хотя бы одно ребро должно быть основанием «общий якорь».
	hasAnchor := false
	for _, e := range links.Edges {
		if e.Basis == BasisAnchor {
			hasAnchor = true
			break
		}
	}
	if !hasAnchor {
		t.Fatalf("нет ни одного ребра с основанием anchor: %+v", links.Edges)
	}
}

// TestLinkByRepeat проверяет, что повтор значения связывает фрагменты
// основанием «повтор», даже если они в разных предложениях.
func TestLinkByRepeat(t *testing.T) {
	text := "Иванов Иван Иванович. Позже: Иванов Иван Иванович."
	doc := pii.NewDoc(text)
	spans := linkTestEngine().detect(text)

	links := linkSubjects(doc, spans)
	if len(links.Subjects) != 1 {
		t.Fatalf("ожидался один субъект, получено %d: %+v", len(links.Subjects), links.Subjects)
	}
	hasRepeat := false
	for _, e := range links.Edges {
		if e.Basis == BasisRepeat {
			hasRepeat = true
			break
		}
	}
	if !hasRepeat {
		t.Fatalf("нет ни одного ребра с основанием repeat: %+v", links.Edges)
	}
}

// TestLinkDoesNotExpandMasking проверяет главную осторожность: рёбра сами по
// себе ничего не маскируют. Связь — основание для правила, а не правило.
func TestLinkDoesNotExpandMasking(t *testing.T) {
	sys, defs := parseSystem(t, "ctx", contextRulesYAML)
	// Два субъекта: у первого только пин, у второго только карта. Рёбра между
	// ними нет, поэтому пин не маскируется, хотя карта в тексте есть.
	text := "Пин 1234. Карта 4111 1111 1111 1111."
	res := newTestEngine().Mask(text, sys, defs)

	if res.Counts[pii.TypePIN] != 0 {
		t.Fatalf("пин замаскирован без карты у того же субъекта: %q", res.Text)
	}
	if res.Counts[pii.TypeCard] != 1 {
		t.Fatalf("карта не замаскирована: %q", res.Text)
	}
	if !strings.Contains(res.Text, "1234") {
		t.Fatalf("пин без карты у субъекта не должен маскироваться: %q", res.Text)
	}
}

// TestContextRulePerSubject проверяет, что правило сочетаний выражается через
// связи: пин маскируется, только если у того же субъекта есть номер карты.
func TestContextRulePerSubject(t *testing.T) {
	sys, defs := parseSystem(t, "ctx", contextRulesYAML)
	// Один субъект: пин и карта в одном предложении. Пин маскируется.
	text := "Карта 4111 1111 1111 1111, пин 1234"
	res := newTestEngine().Mask(text, sys, defs)

	if res.Counts[pii.TypePIN] != 1 {
		t.Fatalf("пин не замаскирован рядом с картой того же субъекта: %q", res.Text)
	}
	if strings.Contains(res.Text, "1234") {
		t.Fatalf("пин остался в тексте: %q", res.Text)
	}
}

// TestContextRuleSeparateSubjects проверяет разницу между глобальным и
// посубъектным правилом: пин в одном субъекте не маскируется из-за карты в
// другом субъекте.
func TestContextRuleSeparateSubjects(t *testing.T) {
	sys, defs := parseSystem(t, "ctx", contextRulesYAML)
	// Пин и карта в разных предложениях — разные субъекты. Пин не маскируется.
	text := "Пин 1234. Карта 4111 1111 1111 1111."
	res := newTestEngine().Mask(text, sys, defs)

	if res.Counts[pii.TypePIN] != 0 {
		t.Fatalf("пин замаскирован из-за карты в другом субъекте: %q", res.Text)
	}
	if !strings.Contains(res.Text, "1234") {
		t.Fatalf("пин без карты у субъекта не должен маскироваться: %q", res.Text)
	}
	if res.Counts[pii.TypeCard] != 1 {
		t.Fatalf("карта не замаскирована: %q", res.Text)
	}
}

// TestSubjectsCount проверяет, что по связям видно, сколько людей в тексте.
func TestSubjectsCount(t *testing.T) {
	text := "Иванов Иван Иванович, тел +7 916 123-45-67. Петров Пётр Петрович, тел +7 916 000-00-00."
	sys, defs := parseSystem(t, "ctx", contextRulesYAML)
	res := linkTestEngine().Mask(text, sys, defs)

	if len(res.Subjects) != 2 {
		t.Fatalf("ожидались два субъекта, получено %d: %+v", len(res.Subjects), res.Subjects)
	}
	// Индексы субъектов ссылаются на фрагменты в res.Spans.
	for _, sub := range res.Subjects {
		for _, fi := range sub.Fragments {
			if fi < 0 || fi >= len(res.Spans) {
				t.Fatalf("индекс фрагмента %d вне диапазона %d", fi, len(res.Spans))
			}
		}
	}
}
