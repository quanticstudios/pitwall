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

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestChipText: a token widens to padded "Image #N" with no break in it,
// and any drawn range copies back as the typed text.
func TestChipText(t *testing.T) {
	ct := chipText("see [Image #2] here", 3, 1)
	want := "see " + strings.Repeat(nbsp, 3) + "Image" + nbsp + "#2" + nbsp + " here"
	if ct.text != want {
		t.Fatalf("text %q", ct.text)
	}
	if len(ct.chips) != 1 || ct.chips[0].name != "Image #2" || ct.chips[0].label != [2]int{7, 15} {
		t.Fatalf("chips %+v", ct.chips)
	}
	n := len([]rune(ct.text))
	for _, tc := range []struct {
		a, b int
		want string
	}{
		{0, n, "see [Image #2] here"},
		{0, 3, "see"},
		{9, 10, "[Image #2]"}, // inside the chip: the whole token
		{12, n, "[Image #2] here"},
	} {
		if got := ct.typed(tc.a, tc.b); got != tc.want {
			t.Errorf("typed(%d, %d) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
	if plain := chipText("no chips", 3, 1); plain.text != "no chips" || plain.typed(0, 2) != "no" {
		t.Fatalf("plain %+v", plain)
	}
}

// TestChipCopy: Ctrl+C on a prompt's selection copies the token as
// typed, not the chip's padded text.
func TestChipCopy(t *testing.T) {
	th := theme.Dark()
	s := &session.Snapshot{SessionID: "c", Content: []session.Item{{ID: "p", Kind: session.KindUserPrompt, Text: "like [Image #1] this"}}}
	var tr Transcript
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: time.Now()}
		tr.CopyKey(gtx)
		tr.Layout(gtx, th, s, false)
		r.Frame(&ops)
	}
	frame()
	sl := tr.sels["p"]
	if sl == nil {
		t.Fatal("no selectable for the prompt")
	}
	tr.span.a, tr.span.b = selPos{"p", 0}, selPos{"p", len([]rune(sl.Text()))}
	frame()
	r.Queue(key.Event{Name: "C", Modifiers: key.ModShortcut, State: key.Press})
	frame()
	frame()
	if _, b, ok := r.WriteClipboard(); !ok || string(b) != "like [Image #1] this" {
		t.Fatalf("copied %q %v", b, ok)
	}
}

func TestRowGap(t *testing.T) {
	rows := []trow{{b: block{kind: blockPrompt}}, {b: block{kind: blockTurn}}, {tail: space(1)}, {b: block{kind: blockPrompt}}}
	for i, want := range []unit.Dp{0, gapPrompt, gapInTurn, gapTurn} {
		if got := rowGap(rows, i); got != want {
			t.Errorf("row %d gap %v, want %v", i, got, want)
		}
	}
	turn := block{kind: blockTurn, lo: 1}
	rows = []trow{{b: block{kind: blockPrompt}}, {b: turn, seg: segHead}, {b: turn, seg: segItem}, {b: turn, seg: segItem}, {b: turn, seg: segReply}, {b: block{kind: blockPrompt, lo: 9}}}
	for i, want := range []unit.Dp{0, gapPrompt, 0, 0, theme.SpaceS, gapTurn} {
		if got := rowGap(rows, i); got != want {
			t.Errorf("work log row %d gap %v, want %v", i, got, want)
		}
	}
}

func TestMiddleCut(t *testing.T) {
	p := "internal/ui/mobile/composer.go"
	if got := middleCut(p, 1000, 7); got != p {
		t.Fatalf("fits: %q", got)
	}
	got := middleCut(p, 15*7, 7)
	if len([]rune(got)) != 15 || !strings.HasPrefix(got, "intern") || !strings.HasSuffix(got, "oser.go") || !strings.Contains(got, "…") {
		t.Fatalf("cut: %q", got)
	}
}

// TestCodePad: inline code gets a no-break space each side for its chip,
// and a copy leaves them out.
func TestCodePad(t *testing.T) {
	p := spansPara([]mdview.Span{{Text: "call "}, {Text: "f()", Mono: true}, {Text: " now"}}, nil)
	if p.text != "call "+nbsp+"f()"+nbsp+" now" || p.plain != "call f() now" || p.code[0] != [2]int{6, 9} {
		t.Fatalf("%q %q %v", p.text, p.plain, p.code)
	}
	n := len([]rune(p.text))
	if got := p.ct.typed(0, n); got != "call f() now" {
		t.Fatalf("copy %q", got)
	}
	if got := p.ct.typed(5, 10); got != "f()" {
		t.Fatalf("copy of the chip %q", got)
	}
}

func TestCodePadPunct(t *testing.T) {
	p := spansPara([]mdview.Span{{Text: "f()", Mono: true}, {Text: ": done"}}, nil)
	if p.text != nbsp+"f(): done" || p.ct.typed(0, len([]rune(p.text))) != "f(): done" {
		t.Fatalf("%q", p.text)
	}
}
