package settings

import (
	"image"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func chord(s string) config.Chord {
	c, err := config.ParseChord(s)
	if err != nil {
		panic(err)
	}
	return c
}

func strs(cs []config.Chord) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.String())
	}
	return out
}

func TestRecord(t *testing.T) {
	b := config.Preset("aide")
	// A free chord replaces the first of next_tab's two.
	es := record(b, slot{"next_tab", false, 0}, chord("Ctrl+J"), false)
	if len(es) != 1 || !reflect.DeepEqual(strs(es[0].chords), []string{"Ctrl+J", "Alt+Down"}) {
		t.Fatalf("replace: %+v", es)
	}
	// new_tab's chord recorded on split_right: a conflict; swapping gives
	// new_tab split_right's old chord.
	s := slot{"split_right", false, 0}
	if o := owner(b, s, chord("Alt+Shift+T")); o != "new_tab" {
		t.Fatalf("owner = %q", o)
	}
	es = record(b, s, chord("Alt+Shift+T"), true)
	if len(es) != 2 || es[1].action != "new_tab" ||
		!reflect.DeepEqual(strs(es[0].chords), []string{"Alt+Shift+T"}) || !reflect.DeepEqual(strs(es[1].chords), []string{"Alt+N"}) {
		t.Fatalf("swap: %+v", es)
	}
	// Adding the chord takes it from the other action.
	es = record(b, slot{"split_right", false, -1}, chord("Alt+Shift+T"), true)
	if !reflect.DeepEqual(strs(es[0].chords), []string{"Alt+N", "Alt+Shift+T"}) || len(es[1].chords) != 0 {
		t.Fatalf("add+swap: %+v", es)
	}
	if v := es[1].value("aide"); v == nil || *v != "[]" {
		t.Errorf("emptied action should write [] , got %v", v)
	}
	// Tab-mode keys only clash with tab-mode keys.
	if o := owner(b, slot{"new", true, 0}, chord("X")); o != "close" {
		t.Errorf("tab owner = %q", o)
	}
	if o := owner(b, slot{"next_tab", false, 0}, chord("X")); o != "" {
		t.Errorf("X is not a global chord: %q", o)
	}
	// Back to the preset's chords removes the key.
	if v := (edit{"next_tab", false, b.Global["next_tab"]}).value("aide"); v != nil {
		t.Errorf("preset value written: %s", *v)
	}
	// Backspace on the only chord unbinds.
	if got := replaceChord([]config.Chord{chord("Alt+N")}, 0, nil); got == nil || len(got) != 0 {
		t.Errorf("remove: %v", got)
	}
}

func TestMatches(t *testing.T) {
	if !matches("tab NEW", "New tab", "new_tab") || matches("tab xyz", "New tab") || !matches("", "x") {
		t.Error("matches")
	}
	if got := firstSentence("Show or hide the sidebar. aide's Ctrl+B..."); got != "Show or hide the sidebar" {
		t.Errorf("firstSentence = %q", got)
	}
}

func TestHookStatus(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"model":"x","hooks":`+string(agent.ClaudeHooks("/opt/bin/pitwall"))+`}`), 0o644)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "hooks.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"'/home/me/my pitwall/pitwall' hook codex"}]}]}}`), 0o644)
	hs := hookStatus(home)
	if hs[0].Have == 0 || hs[0].Have != hs[0].Want || hs[0].Err != "" {
		t.Errorf("claude: %+v", hs[0])
	}
	if hs[1].Have != 1 || hs[1].Want < 2 {
		t.Errorf("codex: %+v", hs[1])
	}
	if hs := hookStatus(t.TempDir()); hs[0].Have != 0 || hs[0].Err != "" {
		t.Errorf("missing file: %+v", hs[0])
	}
}

func TestVersion(t *testing.T) {
	if v := versionFrom([]debug.BuildSetting{{Key: "-ldflags", Value: `"-X main.version=v0.0.5"`}, {Key: "vcs.revision", Value: "abc"}}); v != "v0.0.5" {
		t.Errorf("ldflags: %q", v)
	}
	if v := versionFrom([]debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}, {Key: "vcs.modified", Value: "true"}}); v != "dev-0123456789ab-dirty" {
		t.Errorf("vcs: %q", v)
	}
	if v := versionFrom(nil); v != "dev" {
		t.Errorf("none: %q", v)
	}
}

// TestPageRecord drives the page through a router as the window does: the
// page's keys first, then a window shortcut filter, then the layout.
func TestPageRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[keys]\npreset = \"aide\" # mine\n"), 0o644)
	load := func() config.Settings { s, _ := config.LoadFile(path); return s }
	s := load()
	var p Page
	p.Show(path)
	var r input.Router
	var ops op.Ops
	stolen := false
	frame := func() Result {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		res := p.Keys(gtx)
		for {
			ev, ok := gtx.Event(key.Filter{Name: "T", Required: key.ModAlt | key.ModShift})
			if !ok {
				break
			}
			stolen = stolen || ev.(key.Event).State == key.Press // the window ignores releases
		}
		if l := p.Layout(gtx, theme.Dark(), s, nil); l != None {
			res = l
		}
		r.Frame(&ops)
		if res == Saved {
			s = load()
		}
		return res
	}
	press := func(n key.Name, m key.Modifiers) Result {
		r.Queue(key.Event{Name: n, Modifiers: m, State: key.Press})
		res := frame()
		r.Queue(key.Event{Name: n, Modifiers: m, State: key.Release})
		frame()
		return res
	}
	frame()
	p.startRecord(slot{"split_right", false, 0})
	frame()
	frame()
	if press(key.NameShift, key.ModShift); p.rec.action == "" {
		t.Fatal("a modifier alone ended the recording")
	}
	press("T", key.ModAlt|key.ModShift) // new_tab's chord
	if stolen || p.conflict == nil || p.conflict.other != "new_tab" {
		t.Fatalf("conflict = %+v, window got it: %v", p.conflict, stolen)
	}
	p.write(record(s.Keys, p.conflict.slot, p.conflict.chord, true)...) // Swap
	s = load()
	if got := strs(s.Keys.Global["split_right"]); !reflect.DeepEqual(got, []string{"Alt+Shift+T"}) {
		t.Fatalf("split_right = %v", got)
	}
	if got := strs(s.Keys.Global["new_tab"]); !reflect.DeepEqual(got, []string{"Alt+N"}) {
		t.Fatalf("new_tab = %v", got)
	}

	p.startRecord(slot{"close_pane", false, -1})
	frame()
	frame()
	if press("Q", key.ModCtrl) != Saved {
		t.Fatal("recording a free chord did not save")
	}
	if got := strs(s.Keys.Global["close_pane"]); !reflect.DeepEqual(got, []string{"Alt+Shift+W", "Ctrl+Q"}) {
		t.Fatalf("close_pane = %v", got)
	}
	if data, _ := os.ReadFile(path); !strings.HasPrefix(string(data), "[keys]\npreset = \"aide\" # mine\n") {
		t.Errorf("file lost its first lines:\n%s", data)
	}
	if press(key.NameEscape, 0) != Closed {
		t.Error("Escape did not close the page")
	}
}
