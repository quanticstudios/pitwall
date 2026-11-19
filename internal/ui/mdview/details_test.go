package mdview

import "testing"

// TestDetails: a <details> block's summary and the blocks up to its
// </details> share a fold number, a nested one names its parent, an open
// one says so, and the text after both is in no fold.
func TestDetails(t *testing.T) {
	src := "<details>\n<summary>Outer</summary>\n\nbody\n\n<details open><summary>Inner</summary>\n\ndeep\n\n</details>\n\nback\n\n</details>\n\nafter\n"
	b := Parse([]byte(src))
	type want struct {
		text          string
		fold, parent  int
		summary, open bool
	}
	ws := []want{{"Outer", 1, 0, true, false}, {"body", 1, 0, false, false}, {"Inner", 2, 1, true, true}, {"deep", 2, 0, false, false}, {"back", 1, 0, false, false}, {"after", 0, 0, false, false}}
	if len(b) != len(ws) {
		t.Fatalf("%d blocks: %+v", len(b), b)
	}
	for i, w := range ws {
		g := b[i]
		if plain(g.Spans) != w.text || g.Fold != w.fold || g.Parent != w.parent || g.Summary != w.summary || g.Open != w.open {
			t.Errorf("block %d: %q fold %d parent %d summary %v open %v, want %+v", i, plain(g.Spans), g.Fold, g.Parent, g.Summary, g.Open, w)
		}
	}
}
