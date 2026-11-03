package term

import (
	"image"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// hintTexts is each match as kind:text.
func hintTexts(ms []HintMatch) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Kind+":"+m.Text)
	}
	return out
}

// TestScanHints runs the scanner over recorded screens, one or more
// patterns each, and checks what it labels and what it leaves alone.
func TestScanHints(t *testing.T) {
	for _, c := range []struct {
		screen []string
		want   []string
	}{
		{[]string{"see https://github.com/quanticstudios/pitwall/pull/279."}, []string{"url:https://github.com/quanticstudios/pitwall/pull/279"}},
		{[]string{"(www.example.com/a)"}, []string{"url:www.example.com/a"}},
		{[]string{"https://en.wikipedia.org/wiki/Go_(language) ok"}, []string{"url:https://en.wikipedia.org/wiki/Go_(language)"}},
		{[]string{"  M internal/ui/term/hints.go"}, []string{"path:internal/ui/term/hints.go"}},
		{[]string{"internal/ui/app/app.go:1129:3: undefined: x"}, []string{"path:internal/ui/app/app.go:1129:3"}},
		{[]string{"--- FAIL: main_test.go:42 boom"}, []string{"path:main_test.go:42"}},
		{[]string{"cat /etc/hosts ~/notes.md ./run.sh ../up"}, []string{"path:/etc/hosts", "path:~/notes.md", "path:./run.sh", "path:../up"}},
		{[]string{"<p>x</p> 1/2 and/or"}, []string{"path:and/or"}},
		{[]string{"e5965229 Remove a committed review note (#279)"}, []string{"hash:e5965229", "number:#279"}},
		{[]string{"commit 0899f9ea2b1c4d5e6f708192a3b4c5d6e7f80912"}, []string{"hash:0899f9ea2b1c4d5e6f708192a3b4c5d6e7f80912"}},
		{[]string{"deadbeef 1234567 facade0"}, []string{"hash:facade0"}}, // a hash has a digit and a letter
		{[]string{"listening on 10.0.0.12:8080, not 300.1.1.1 or v1.2.3.4.5"}, []string{"ip:10.0.0.12:8080"}},
		{[]string{"issue#12 &#38; #7"}, []string{"number:#7"}},
		{[]string{"plain words only"}, nil},
	} {
		got := hintTexts(ScanHints(grid(c.screen...), nil))
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: %q, want %q", c.screen, got, c.want)
		}
	}
}

// TestScanHintsOverlaps checks that a match inside another gives way: a
// URL's path, hash and #fragment are the URL's, and a custom pattern wins
// over the built-in ones.
func TestScanHintsOverlaps(t *testing.T) {
	g := grid("https://github.com/o/r/commit/e5965229#12 then /srv/10.0.0.1/x")
	got := hintTexts(ScanHints(g, nil))
	want := []string{"url:https://github.com/o/r/commit/e5965229#12", "path:/srv/10.0.0.1/x"}
	if !slices.Equal(got, want) {
		t.Errorf("%q, want %q", got, want)
	}

	custom := []*regexp.Regexp{regexp.MustCompile(`ENG-\d+`), regexp.MustCompile(`branch: (\S+)`)}
	g = grid("ENG-2962 on branch: feat/hints")
	got = hintTexts(ScanHints(g, custom))
	want = []string{"custom:ENG-2962", "custom:feat/hints"}
	if !slices.Equal(got, want) {
		t.Errorf("custom: %q, want %q", got, want)
	}
}

// TestScanHintsWrapped checks a URL the terminal wrapped is one match with
// a span on each row, and that a row that only fills its width is not
// joined to the next.
func TestScanHintsWrapped(t *testing.T) {
	g := wrapped("go to https://exa\\", "mple.com/a/b now", "a/b             ")
	ms := ScanHints(g, nil)
	if got := hintTexts(ms); !slices.Equal(got, []string{"url:https://example.com/a/b", "path:a/b"}) {
		t.Fatalf("%q", got)
	}
	if want := []HintSpan{{0, 6, 17}, {1, 0, 12}}; !slices.Equal(ms[0].Spans, want) {
		t.Errorf("spans %v, want %v", ms[0].Spans, want)
	}

	g = grid("https://example.", "com")
	if got := hintTexts(ScanHints(g, nil)); !slices.Equal(got, []string{"url:https://example"}) {
		t.Errorf("not wrapped: %q", got)
	}
}

// TestScanHintsWide checks a wide character takes one place in the text
// and two columns on the screen, so the spans after it line up.
func TestScanHintsWide(t *testing.T) {
	g := &vt.Grid{Cols: 12, Rows: 1}
	g.Cells = append(g.Cells, vt.Cell{Content: "日", Width: 2}, vt.Cell{})
	for _, r := range " a/b.go   " {
		g.Cells = append(g.Cells, vt.Cell{Content: string(r), Width: 1})
	}
	ms := ScanHints(g, nil)
	if len(ms) != 1 || ms[0].Text != "a/b.go" || ms[0].Spans[0] != (HintSpan{0, 3, 9}) {
		t.Errorf("%+v", ms)
	}
}

// TestHintLabels checks labels come shortest first from the alphabet, are
// distinct, and none is the prefix of another.
func TestHintLabels(t *testing.T) {
	if got := HintLabels(3); !slices.Equal(got, []string{"a", "s", "d"}) {
		t.Errorf("3: %q", got)
	}
	for _, n := range []int{1, len(HintAlphabet), len(HintAlphabet) + 1, 100, 600} {
		ls := HintLabels(n)
		if len(ls) != n {
			t.Fatalf("%d: %d labels", n, len(ls))
		}
		for i, a := range ls {
			if i > 0 && len(a) < len(ls[i-1]) {
				t.Errorf("%d: %q after %q", n, a, ls[i-1])
			}
			for j, b := range ls {
				if i != j && strings.HasPrefix(b, a) {
					t.Fatalf("%d: %q is a prefix of %q", n, a, b)
				}
			}
			if strings.ContainsAny(a, "ilo") {
				t.Errorf("%d: %q has a letter that reads as a digit", n, a)
			}
		}
	}
	if got := HintLabels(len(HintAlphabet) + 1); got[0] != "s" || got[len(got)-2] != "aa" || got[len(got)-1] != "as" {
		t.Errorf("one past the alphabet: %q", got)
	}
}

// TestHintsLook checks hints mode dims the cells outside its matches and
// draws a label chip in the theme's primary color over a match's start.
func TestHintsLook(t *testing.T) {
	v := &View{}
	th := testTheme()
	g := grid("ab see a/b.go", "plain")
	ms := ScanHints(g, nil)
	v.SetHints(true, []Hint{{HintMatch: ms[0], Label: "a"}})
	ops := testContext(image.Pt(400, 300))
	v.Layout(ops, th, g, vt.Modes{}, true)
	v.lit = v.hintLit(v.lit[:0], 0)
	r := v.prev[v.hashRow(g.Cells[:g.Cols], -1, -1, nil, -1)]
	if r == nil {
		t.Fatal("row 0 was not drawn with its hints")
	}
	st := v.resolveStyles(nil, g.Cells[:g.Cols], -1, -1)
	dimHints(st, v.lit)
	if st[0].fg == th.TermFg || st[9].fg != th.TermFg {
		t.Errorf("dim %v, lit %v; text is %v", st[0].fg, st[9].fg, th.TermFg)
	}
	if len(v.chips) != 1 {
		t.Errorf("%d chips drawn, want 1", len(v.chips))
	}
	for k, img := range v.chips {
		if k.label != "a" || img.Size() != image.Pt(v.cell.X, v.cell.Y) {
			t.Errorf("chip %v %v", k, img.Size())
		}
	}
	v.SetHints(false, nil)
	v.Layout(ops, th, g, vt.Modes{}, true)
	if v.hintsOn {
		t.Error("still on")
	}
}
