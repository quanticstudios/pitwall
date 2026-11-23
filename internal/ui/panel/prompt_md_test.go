package panel

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestPromptMarkdown draws the user's markdown message as blocks, a plain
// one as typed, and copies them: a selection of a whole markdown message
// and its Copy give back its text as typed, a part of it what it shows.
func TestPromptMarkdown(t *testing.T) {
	th := theme.Dark()
	s := promptMDSnap()
	s.Content[4].Text = "plain words\n\nand /home/u/foo_bar_baz.go:12, [Image #1]"
	for _, lay := range []string{LayoutLedger, LayoutColumn} {
		tr := Transcript{ChatLayout: lay}
		var r input.Router
		var ops op.Ops
		frame := func() {
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 3000)), Now: time.Now()}
			tr.CopyKey(gtx)
			tr.Layout(gtx, th, s, false)
			r.Frame(&ops)
		}
		copySel := func(a, b selPos) string {
			t.Helper()
			tr.span.a, tr.span.b = a, b
			frame()
			r.Queue(key.Event{Name: "C", Modifiers: key.ModShortcut, State: key.Press})
			frame()
			frame()
			_, got, ok := r.WriteClipboard()
			if !ok {
				t.Fatalf("%s: nothing copied", lay)
			}
			return string(got)
		}
		frame()

		// The markdown message: blocks, not one text, with no markers left.
		if d := tr.docs["p1"]; d == nil || d.flat || tr.sels["p1"] != nil {
			t.Fatalf("%s: p1 drew as typed", lay)
		}
		first := tr.sels["p1/0"]
		if first == nil || strings.ContainsAny(first.Text(), "*`") || !strings.Contains(first.Text(), "resize") {
			t.Fatalf("%s: p1's first block %v", lay, first)
		}
		last, end := "", -1
		for k := range tr.sels {
			if itemOf(k) == "p1" && tr.span.ord[k] > end {
				last, end = k, tr.span.ord[k]
			}
		}
		n := len([]rune(tr.sels[last].Text()))
		if got := copySel(selPos{"p1/0", 0}, selPos{last, n}); got != promptMDText {
			t.Errorf("%s: whole p1 copied %q, want it as typed", lay, got)
		}
		if got := copySel(selPos{"p1/0", 0}, selPos{"p1/0", len([]rune(first.Text()))}); got != "Fix the resize in image.go before [Image #1] lands:" {
			t.Errorf("%s: p1's first block copied %q", lay, got)
		}
		// Copy and Edit take the source.
		if got := promptText(&s.Content[0]); !strings.Contains(got, "**resize**") || !strings.Contains(got, "```go") {
			t.Errorf("%s: Copy takes %q", lay, got)
		}

		// Another agent's message: its sender line apart, its text markdown,
		// all of it copied with the header as typed.
		if tr.docs["p2"] == nil || tr.docs["p2"].flat {
			t.Fatalf("%s: p2 drew as typed", lay)
		}
		last, end = "", -1
		for k := range tr.sels {
			if itemOf(k) == "p2" && tr.span.ord[k] > end {
				last, end = k, tr.span.ord[k]
			}
		}
		if got := copySel(selPos{"p2/0", 0}, selPos{last, len([]rune(tr.sels[last].Text()))}); got != s.Content[2].Text {
			t.Errorf("%s: whole p2 copied %q", lay, got)
		}

		// The plain message draws as typed, as before, its chip and all.
		p := tr.sels["p3"]
		if d := tr.docs["p3"]; d == nil || !d.flat || p == nil || tr.sels["p3/0"] != nil {
			t.Fatalf("%s: p3 did not draw as typed", lay)
		}
		if got := copySel(selPos{"p3", 0}, selPos{"p3", len([]rune(p.Text()))}); got != s.Content[4].Text {
			t.Errorf("%s: plain p3 copied %q", lay, got)
		}
	}
}

// TestSpansChips: an image token in a markdown paragraph widens for its
// chip, styled runs move with it, and a copy gives back the token.
func TestSpansChips(t *testing.T) {
	spans := []mdview.Span{{Text: "see "}, {Text: "[Image #2] now", Bold: true}, {Text: "x", Mono: true}}
	p := spansChips(spans, nil, [2]int{2, 1})
	want := "see " + nbsp + nbsp + "Image" + nbsp + "#2" + nbsp + " now" + nbsp + "x" + nbsp
	if p.text != want {
		t.Fatalf("text %q", p.text)
	}
	if len(p.ct.chips) != 1 || p.ct.chips[0].name != "Image #2" || p.ct.chips[0].label != [2]int{6, 14} {
		t.Fatalf("chips %+v", p.ct.chips)
	}
	if len(p.runs) != 2 || p.runs[0].lo != 4 || p.runs[0].hi != 19 || p.code[0] != [2]int{20, 21} {
		t.Fatalf("runs %+v code %v", p.runs, p.code)
	}
	if got := p.ct.typed(0, len([]rune(p.text))); got != "see [Image #2] nowx" {
		t.Fatalf("copy %q", got)
	}
	if r := spansPara(spans, nil); len(r.ct.chips) != 0 || !strings.Contains(r.text, "[Image #2]") {
		t.Fatalf("a reply chipped its token: %q", r.text)
	}
}
