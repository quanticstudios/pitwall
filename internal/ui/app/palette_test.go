package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestFuzzy: the best alignment counts, word starts and runs beat
// scattered letters, and letters out of order do not match.
func TestFuzzy(t *testing.T) {
	// The word "tab" scores 9+7+7; nexT then tAB would score 1-4+1+7=5.
	if got := fuzzy("tab", "next tab"); got != 23 {
		t.Errorf("tab in next tab scores %d, want 23", got)
	}
	// Two word starts with a gap of three: 9-5+9.
	if got := fuzzy("nt", "new tab"); got != 13 {
		t.Errorf("nt in new tab scores %d, want 13", got)
	}
	if a, b := fuzzy("sp", "split the pane"), fuzzy("sp", "close the pane"); a <= b || b < 0 {
		t.Errorf("sp: split %d, close %d", a, b)
	}
	if fuzzy("bat", "tab") != -1 || fuzzy("x", "") != -1 || fuzzy("", "x") != 0 {
		t.Error("fuzzy matched letters out of order, or an empty string")
	}
}

// TestPaletteRank: the action a query names comes first, every word must
// match, and an empty query keeps every action grouped.
func TestPaletteRank(t *testing.T) {
	es := paletteEntries(conventional)
	if len(rank(es, "")) != len(config.Actions()) {
		t.Fatalf("empty query lists %d of %d actions", len(rank(es, "")), len(config.Actions()))
	}
	for q, want := range map[string]string{
		"paste":        "paste",
		"sidebar":      "toggle_sidebar",
		"pal":          "command_palette",
		"settings":     "open_settings",
		"split below":  "split_down",
		"sess switch":  "session_switcher",
		"attention":    "jump_attention",
		"toggle_panel": "toggle_panel",
		"pane":         "next_pane",
	} {
		if got := rank(es, q); len(got) == 0 || got[0].action.Name != want || got[0].action.Tab || got[0].action.Pane {
			t.Errorf("%q: first is %v, want %s", q, names(got), want)
		}
	}
	if got := rank(es, "split zzz"); len(got) > 0 {
		t.Errorf("split zzz matched %v", names(got))
	}
	// Grouped: once a group ends it does not come back.
	seen := map[string]bool{}
	last := ""
	for _, e := range es {
		if g := e.action.Group; g != last {
			if seen[g] {
				t.Fatalf("group %s comes back after %s", g, last)
			}
			seen[g], last = true, g
		}
	}
}

func names(es []paletteEntry) []string {
	var out []string
	for _, e := range es[:min(len(es), 4)] {
		out = append(out, e.action.Group+"/"+e.action.Name)
	}
	return out
}

// TestPaletteChords: every action is listed once with the chords its
// preset binds, tab- and pane-mode keys after their prefix; an unbound
// prefix leaves them listed without keys.
func TestPaletteChords(t *testing.T) {
	for _, b := range []*config.Bindings{aide, conventional} {
		es := paletteEntries(b)
		if len(es) != len(config.Actions()) {
			t.Fatalf("%s: %d entries for %d actions", b.Preset, len(es), len(config.Actions()))
		}
		for _, e := range es {
			a := e.action
			var want []string
			cs, prefix := b.Global[a.Name], ""
			if a.Tab {
				cs, prefix = b.Tab[a.Name], firstChord(b.Global["tab_prefix"])
			} else if a.Pane {
				cs, prefix = b.Pane[a.Name], firstChord(b.Global["pane_prefix"])
			}
			for _, c := range cs {
				if !a.Tab && !a.Pane {
					want = append(want, c.String())
				} else if prefix != "" {
					want = append(want, prefix+" "+c.String())
				}
			}
			if !slices.Equal(e.keys, want) {
				t.Errorf("%s %s: keys %v, want %v", b.Preset, a.Name, e.keys, want)
			}
		}
	}
	find := func(b *config.Bindings, name string, tab bool) []string {
		for _, e := range paletteEntries(b) {
			if e.action.Name == name && e.action.Tab == tab && !e.action.Pane {
				return e.keys
			}
		}
		t.Fatalf("%s: no %s", b.Preset, name)
		return nil
	}
	for _, c := range []struct {
		b    *config.Bindings
		name string
		tab  bool
		want string
	}{
		{conventional, "new_tab", false, "Ctrl+Shift+T"},
		{conventional, "command_palette", false, "Ctrl+Shift+P"},
		{aide, "command_palette", false, "Ctrl+Shift+P"},
		{aide, "next_tab", false, "Alt+J Alt+Down"},
		{aide, "new", true, "Ctrl+T N"},
		{conventional, "new", true, ""},
		{conventional, "session_rename", false, ""},
	} {
		if got := strings.Join(find(c.b, c.name, c.tab), " "); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.b.Preset, c.name, got, c.want)
		}
	}
}

// TestPaletteRebind: the palette reads the bindings in effect, so a
// rebound action shows its new chord and a bound prefix gives its mode's
// keys theirs.
func TestPaletteRebind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[keys]\nnew_tab = \"Super+T\"\ntab_prefix = \"Ctrl+G\"\ncommand_palette = []\n"), 0o644)
	s, probs := config.LoadFile(path)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	got := map[string]string{}
	for _, e := range paletteEntries(s.Keys) {
		if !e.action.Pane {
			got[map[bool]string{true: "tab."}[e.action.Tab]+e.action.Name] = strings.Join(e.keys, " ")
		}
	}
	for name, want := range map[string]string{
		"new_tab": "Super+T", "tab.new": "Ctrl+G N", "tab.prev": "Ctrl+G H Ctrl+G Left", "command_palette": "",
	} {
		if got[name] != want {
			t.Errorf("%s: %q, want %q", name, got[name], want)
		}
	}
}

// TestPaletteWindow: Ctrl+Shift+P opens the palette in the real
// window; typed keys filter it and never reach the pane; Enter runs the
// highlighted action as its key would, Escape and the palette's key close
// it, and once closed the pane gets its keys again.
func TestPaletteWindow(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	sent := func() []any { return u.b.(*FakeBackend).Sent() }
	typed := func(s string) []key.Event {
		var es []key.Event
		for _, r := range strings.ToUpper(s) {
			n := key.Name(r)
			if r == ' ' {
				n = key.NameSpace
			}
			es = append(es, press(n, 0))
		}
		return es
	}
	cs := key.ModCtrl | key.ModShift
	if got := keys(press("P", cs)); got != "" || !u.pal.open {
		t.Fatalf("Ctrl+Shift+P: open %v, pane got %q", u.pal.open, got)
	}
	keys(typed("split right")...)
	if u.pal.query != "split right" {
		t.Fatalf("query %q", u.pal.query)
	}
	if got := keys(press(key.NameReturn, 0)); got != "" || u.pal.open {
		t.Fatalf("Enter: open %v, pane got %q", u.pal.open, got)
	}
	var open *proto.OpenPane
	for _, m := range sent() {
		if o, ok := m.(proto.OpenPane); ok {
			open = &o
		}
	}
	if open == nil || open.Dir != layout.Horizontal || open.WorkspaceID != "w1" {
		t.Fatalf("split right sent %+v", open)
	}

	// It opens again; the arrows move; Escape closes.
	keys(press("P", key.ModCtrl|key.ModShift))
	keys(typed("sidebar")...)
	keys(press(key.NameDownArrow, 0), press("K", key.ModCtrl))
	if u.pal.sel != 0 {
		t.Fatalf("down then Ctrl+K at row %d", u.pal.sel)
	}
	keys(press(key.NameEscape, 0))
	if u.pal.open || u.nav.sidebarHidden {
		t.Fatalf("Escape: open %v, sidebar hidden %v", u.pal.open, u.nav.sidebarHidden)
	}
	keys(press("P", key.ModCtrl|key.ModShift))
	keys(typed("sidebar")...)
	keys(press(key.NameReturn, 0))
	if !u.nav.sidebarHidden {
		t.Fatal("running toggle_sidebar left the sidebar shown")
	}

	// A pane action goes through the pane's view: Scroll back a page.
	keys(press("P", cs))
	keys(typed("scroll back")...)
	keys(press(key.NameReturn, 0))
	if !slices.ContainsFunc(sent(), func(m any) bool { s, ok := m.(proto.Scroll); return ok && s.Lines > 0 }) {
		t.Fatal("scroll back sent no Scroll")
	}

	// The palette's key closes it, and the pane has its keys back.
	keys(press("P", cs))
	keys(press("P", cs))
	if u.pal.open {
		t.Fatal("Ctrl+Shift+P did not close the palette")
	}
	for _, m := range sent() {
		if in, ok := m.(proto.Input); ok {
			t.Fatalf("the pane got %q", in.Data)
		}
	}
	if got := keys(press("T", key.ModCtrl)); got != "\x14" {
		t.Fatalf("after the palette Ctrl+T sent %q", got)
	}

	// Tab mode is unbound here, and the palette still runs its actions.
	keys(press("P", cs))
	keys(typed("rename tab")...)
	if got := rank(paletteEntries(conventional), u.pal.query); len(got) == 0 || got[0].action.Name != "rename" || !got[0].action.Tab {
		t.Fatalf("rename tab ranks %v first", names(got))
	}
	keys(press(key.NameReturn, 0))
	if !u.sidebar.Editing() {
		t.Fatal("tab mode's rename did not start a rename")
	}
}
