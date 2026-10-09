package app

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestPaletteRows: goto_tab_1..9 are one row until the query has a digit,
// each mode action titled as a window action is folded into it with its
// keys, and recent actions lead an empty query.
func TestPaletteRows(t *testing.T) {
	titles := func(es []paletteEntry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.action.Title())
		}
		return out
	}
	es := paletteRows(aide, "", nil)
	for _, title := range []string{"Go to tab 1–9", "Split the pane below", "Split the pane to the right", "Next pane", "Show the session switcher"} {
		if n := strings.Count(strings.Join(titles(es), "\n")+"\n", title+"\n"); n != 1 {
			t.Errorf("%q listed %d times", title, n)
		}
	}
	if slices.ContainsFunc(es, func(e paletteEntry) bool { return e.action.Name != gotoAny && strings.HasPrefix(e.action.Name, "goto_") }) {
		t.Error("a goto row is listed beside Go to tab 1–9")
	}
	i := slices.IndexFunc(es, func(e paletteEntry) bool { return e.action.Name == gotoAny })
	if i < 0 || !slices.Equal(es[i].keys, []string{"Alt+1…9", "Ctrl+T 1…9"}) {
		t.Fatalf("Go to tab 1–9 has keys %v", es[i].keys)
	}
	i = slices.IndexFunc(es, func(e paletteEntry) bool { return e.action.Name == "split_down" })
	if e := es[i]; e.action.Pane || !slices.Equal(e.keys, []string{"Alt+Shift+N", "Ctrl+P D"}) {
		t.Errorf("split_down: pane %v, keys %v", e.action.Pane, e.keys)
	}

	// A digit brings the nine back, and picks one.
	if got := paletteRows(aide, "tab 4", nil); len(got) == 0 || got[0].action.Name != "goto_tab_4" {
		t.Errorf("tab 4: first is %v", names(got))
	}

	// Recent ones first, newest first; one no longer listed is skipped.
	recent := []string{"pane.fullscreen", "gone", "toggle_sidebar"}
	es = paletteRows(aide, "", recent)
	if got := []string{es[0].id(), es[1].id()}; !slices.Equal(got, []string{"pane.fullscreen", "toggle_sidebar"}) {
		t.Errorf("recent first: %v", got)
	}
	if len(es) != len(paletteRows(aide, "", nil)) {
		t.Errorf("recent ones are listed twice: %d rows", len(es))
	}
	if got := paletteRows(aide, "split", recent); got[0].action.Name == "fullscreen" {
		t.Error("a query still puts recent ones first")
	}

	r := pushRecent([]string{"a", "b", "c", "d", "e"}, "c")
	r = pushRecent(r, "f")
	if !slices.Equal(r, []string{"f", "c", "a", "b", "d"}) {
		t.Errorf("pushRecent: %v", r)
	}
}

// TestPaletteGotoPick: Enter on Go to tab 1–9 asks for the digit and keeps
// the palette open; the digit's row runs.
func TestPaletteGotoPick(t *testing.T) {
	var p palette
	p.openAt(time.Now())
	rows := paletteRows(aide, "", nil)
	i := slices.IndexFunc(rows, func(e paletteEntry) bool { return e.action.Name == gotoAny })
	if r := p.pick(rows, i); r != nil || !p.open || p.query != "go to tab " {
		t.Fatalf("pick: %v, open %v, query %q", r, p.open, p.query)
	}
	p.query += "3"
	rows = paletteRows(aide, p.query, nil)
	if r := p.pick(rows, 0); r == nil || r.action.Name != "goto_tab_3" || p.open {
		t.Fatalf("pick after 3: %v, open %v", r, p.open)
	}
}

// TestHitSpans: the characters each word matched are hits, as fuzzy
// aligned them, and a word matching only the group or keys marks none.
func TestHitSpans(t *testing.T) {
	show := func(ss []hitSpan) string {
		var b strings.Builder
		for _, s := range ss {
			if s.hit {
				b.WriteString("[" + s.s + "]")
			} else {
				b.WriteString(s.s)
			}
		}
		return b.String()
	}
	for _, c := range []struct{ title, query, want string }{
		{"Next tab", "tab", "Next [tab]"},
		{"New tab", "nt", "[N]ew [t]ab"},
		{"Split the pane below", "sp bel", "[Sp]lit the pane [bel]ow"},
		{"Split the pane below", "panes", "Split the pane below"}, // "panes" is the group
		{"Go to tab 1–9", "go tab", "[Go] to [tab] 1–9"},
		{"Paste", "", "Paste"},
	} {
		if got := show(hitSpans(c.title, c.query)); got != c.want {
			t.Errorf("%q in %q: %s, want %s", c.query, c.title, got, c.want)
		}
	}
}

// TestPaletteHeight: the card fits its rows up to the most, and keeps one
// row's room for the empty state.
func TestPaletteHeight(t *testing.T) {
	for _, c := range []struct{ n, want int }{{0, 136}, {1, 136}, {5, 280}, {9, 420}, {68, 420}} {
		if got := paletteHeight(c.n, 36, 100, 420); got != c.want {
			t.Errorf("%d rows: %d, want %d", c.n, got, c.want)
		}
	}
}
