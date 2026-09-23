package pii

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

type zzRec struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Text     string `json:"text"`
	Spans    []struct {
		Start int    `json:"start"`
		End   int    `json:"end"`
		Type  string `json:"type"`
	} `json:"spans"`
}

func TestZZCitizenshipAudit(t *testing.T) {
	files := []string{
		"../../corpus/informal_negative.jsonl",
		"../../corpus/wiki_negative.jsonl",
		"../../corpus/dataset.jsonl",
		"../../corpus/holdout.jsonl",
		"../../corpus/all.jsonl",
		"../../corpus/informal_injected.jsonl",
		"../../corpus/wiki_injected.jsonl",
	}
	det := NewCitizenshipDetector()
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Logf("SKIP %s: %v", f, err)
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		var total, tp, fp, fn, partial int
		shown, shownP := 0, 0
		for sc.Scan() {
			var r zzRec
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			total++
			gold := map[[2]int]bool{}
			for _, g := range r.Spans {
				if g.Type == "CITIZENSHIP" {
					gold[[2]int{g.Start, g.End}] = true
				}
			}
			got := det.Detect(NewDoc(r.Text))
			seen := map[[2]int]bool{}
			for _, s := range got {
				k := [2]int{s.Start, s.End}
				seen[k] = true
				switch {
				case gold[k]:
					tp++
				default:
					over := false
					for g := range gold {
						if g[0] < k[1] && k[0] < g[1] {
							over = true
						}
					}
					if over {
						partial++
						if shownP < 12 {
							shownP++
							t.Logf("PARTIAL %s %s: got=%q ctx=%q", f, r.ID, r.Text[s.Start:s.End], zzCtx(r.Text, s.Start, s.End))
						}
					} else {
						fp++
						if shown < 8 {
							shown++
							t.Logf("FP %s %s: %q  <<in>>  %q", f, r.ID, r.Text[s.Start:s.End], zzCtx(r.Text, s.Start, s.End))
						}
					}
				}
			}
			for g := range gold {
				if !seen[g] {
					fn++
				}
			}
		}
		fh.Close()
		t.Logf("RESULT %s: lines=%d tp=%d fp=%d partial=%d fn=%d", f, total, tp, fp, partial, fn)
	}
}

func zzCtx(s string, a, b int) string {
	lo, hi := a-40, b+40
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	for lo > 0 && lo < len(s) && s[lo]&0xC0 == 0x80 {
		lo--
	}
	for hi < len(s) && s[hi]&0xC0 == 0x80 {
		hi++
	}
	return s[lo:hi]
}
