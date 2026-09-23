package pii

import "testing"

func TestDebugLiniya(t *testing.T) {
	txt := "Адрес регистрации: индекс 643810, пос. Новосёлово, линия Гагарина, д. 44, к. 135"
	d := NewDoc(txt)
	spans := NewAddressDetector().Detect(d)
	for _, s := range spans {
		t.Logf("span: %q [%d:%d]", txt[s.Start:s.End], s.Start, s.End)
	}
	ws := addrWords(d)
	comps := addrScan(d, ws)
	for _, c := range comps {
		t.Logf("comp: %q kinds=%v solo=%v", d.Text[c.start:c.end], c.kinds, c.solo)
	}
}