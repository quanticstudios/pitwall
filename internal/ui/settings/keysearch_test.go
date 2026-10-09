package settings

import (
	"image"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// shown is the config names of the rows secs show, by section title.
func shown(secs []section) map[string][]string {
	out := map[string][]string{}
	for _, sec := range secs {
		for _, r := range sec.rows {
			out[sec.title] = append(out[sec.title], r.desc)
		}
	}
	return out
}

// TestShortcutSearch: keys typed as a chord find that chord only, in any
// modifier order and spelling, while words still match names, and the
// page's own search keeps the shortcut sections.
func TestShortcutSearch(t *testing.T) {
	for _, tc := range []struct {
		preset, q string
		want      map[string][]string
	}{
		// Not next_prompt's Ctrl+Shift+Down, nor every row with a d.
		{"conventional", "ctrl+shift+d", map[string][]string{"Shortcuts": {"deny_prompt"}}},
		{"conventional", "shift ctrl d", map[string][]string{"Shortcuts": {"deny_prompt"}}},
		{"conventional", "ctrl d", map[string][]string{}},
		{"conventional", "alt 1", map[string][]string{"Shortcuts": {"goto_tab_1"}}},
		{"conventional", "ctrl shift ]", map[string][]string{"Sessions": {"session_next"}}},
		{"mac", "cmd+shift+r", map[string][]string{"Shortcuts": {"view_diff"}}},
		{"mac", "⌘⇧R", map[string][]string{"Shortcuts": {"view_diff"}}},
		{"conventional", "deny", map[string][]string{"Shortcuts": {"deny_prompt"}}},
		{"conventional", "review", map[string][]string{"Shortcuts": {"view_diff"}}},
		{"conventional", "copy", map[string][]string{"Shortcuts": {"copy", "copy_mode"}}},
	} {
		var p Page
		p.s.Keys = config.Preset(tc.preset)
		p.keySearch.SetText(tc.q)
		secs, _ := p.keyResults()
		if got := shown(secs); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %q: %v, want %v", tc.preset, tc.q, got, tc.want)
		}
	}
	var p Page
	p.s.Keys = config.Preset("conventional")
	p.keySearch.SetText("ctrl+shift+k")
	if secs, msg := p.keyResults(); len(secs) != 0 || msg != "Nothing uses Ctrl+Shift+K." {
		t.Errorf("ctrl+shift+k: %v, %q", shown(secs), msg)
	}
	// The settings search finds the chord too, among every category.
	if n := p.counts("ctrl+shift+d"); n[catKeys] != 1 || n[catTerminal] != 0 {
		t.Errorf("counts = %v", n)
	}
}

// TestRecordSearch drives Record through a router as the window does:
// the chord pressed shows every action bound to it, in every table, a
// free one says so, a modifier alone does nothing, and Escape stops
// recording without closing the page.
func TestRecordSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[keys]\npreset = \"aide\"\n"), 0o644)
	s, _ := config.LoadFile(path)
	var p Page
	p.cat = catKeys
	p.Show(path)
	var r input.Router
	var ops op.Ops
	stolen := false
	click := false
	frame := func() Result {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		res := p.Keys(gtx)
		if click { // as a click on Record does: after Keys, before the layout
			p.toggleKeyRec()
			click = false
		}
		for {
			ev, ok := gtx.Event(key.Filter{Name: "D", Required: key.ModCtrl | key.ModShift})
			if !ok {
				break
			}
			stolen = stolen || ev.(key.Event).State == key.Press
		}
		if l := p.Layout(gtx, theme.Dark(), s, nil); l != None {
			res = l
		}
		r.Frame(&ops)
		return res
	}
	press := func(n key.Name, m key.Modifiers) Result {
		r.Queue(key.Event{Name: n, Modifiers: m, State: key.Press})
		res := frame()
		r.Queue(key.Event{Name: n, Modifiers: m, State: key.Release})
		frame()
		return res
	}
	results := func() map[string][]string { secs, _ := p.keyResults(); return shown(secs) }
	frame()
	click = true
	frame()
	frame()
	if !p.keyRec {
		t.Fatal("Record did not start")
	}
	if press(key.NameCtrl, key.ModCtrl); p.keySearch.Text() != "" || !p.keyRec {
		t.Fatalf("a modifier alone searched %q", p.keySearch.Text())
	}
	press("D", key.ModCtrl|key.ModShift)
	if want := map[string][]string{"Shortcuts": {"deny_prompt"}}; stolen || !reflect.DeepEqual(results(), want) {
		t.Fatalf("Ctrl+Shift+D: %v, window got it: %v", results(), stolen)
	}
	// N with no modifier: tab mode's and pane mode's new, as a typed "N"
	// would not find.
	press("N", 0)
	if want := map[string][]string{"Tab mode": {"new"}, "Pane mode": {"new"}}; !reflect.DeepEqual(results(), want) {
		t.Fatalf("N: %v", results())
	}
	press("K", key.ModCtrl|key.ModShift)
	if secs, msg := p.keyResults(); len(secs) != 0 || msg != "Nothing uses Ctrl+Shift+K." {
		t.Fatalf("Ctrl+Shift+K: %v, %q", shown(secs), msg)
	}
	if press(key.NameEscape, 0) == Closed || p.keyRec {
		t.Fatalf("Escape closed the page or kept recording: %v", p.keyRec)
	}
	if p.keySearch.Text() != "Ctrl+Shift+K" {
		t.Errorf("stopping lost the search: %q", p.keySearch.Text())
	}
	// Escape then clears the search, and only then closes the page.
	if press(key.NameEscape, 0) == Closed || p.keySearch.Text() != "" {
		t.Fatalf("second Escape: search %q", p.keySearch.Text())
	}
	if press(key.NameEscape, 0) != Closed {
		t.Error("third Escape did not close the page")
	}
}
