package settings

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/model"
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
	es := record(b, slot{"next_tab", "keys", 0}, chord("Ctrl+J"), false)
	if len(es) != 1 || !reflect.DeepEqual(strs(es[0].chords), []string{"Ctrl+J", "Alt+Down"}) {
		t.Fatalf("replace: %+v", es)
	}
	// new_tab's chord recorded on split_right: a conflict; swapping gives
	// new_tab split_right's old chord.
	s := slot{"split_right", "keys", 0}
	if o := owner(b, s, chord("Alt+Shift+T")); o != "new_tab" {
		t.Fatalf("owner = %q", o)
	}
	es = record(b, s, chord("Alt+Shift+T"), true)
	if len(es) != 2 || es[1].action != "new_tab" ||
		!reflect.DeepEqual(strs(es[0].chords), []string{"Alt+Shift+T"}) || !reflect.DeepEqual(strs(es[1].chords), []string{"Alt+N"}) {
		t.Fatalf("swap: %+v", es)
	}
	// Adding the chord takes it from the other action.
	es = record(b, slot{"split_right", "keys", -1}, chord("Alt+Shift+T"), true)
	if !reflect.DeepEqual(strs(es[0].chords), []string{"Alt+N", "Alt+Shift+T"}) || len(es[1].chords) != 0 {
		t.Fatalf("add+swap: %+v", es)
	}
	if v := es[1].value("aide"); v == nil || *v != "[]" {
		t.Errorf("emptied action should write [] , got %v", v)
	}
	// Tab-mode keys only clash with tab-mode keys.
	if o := owner(b, slot{"new", "keys.tab", 0}, chord("X")); o != "close" {
		t.Errorf("tab owner = %q", o)
	}
	if o := owner(b, slot{"next_tab", "keys", 0}, chord("X")); o != "" {
		t.Errorf("X is not a global chord: %q", o)
	}
	// Back to the preset's chords removes the key.
	if v := (edit{"next_tab", "keys", b.Global["next_tab"]}).value("aide"); v != nil {
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
	var click *slot
	frame := func() Result {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		res := p.Keys(gtx)
		if click != nil { // as a click on a keycap does: after Keys, before the layout
			p.startRecord(*click)
			click = nil
		}
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
	p.startRecord(slot{"split_right", "keys", 0})
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

	click = &slot{"close_pane", "keys", -1}
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

// TestPaneModeSection: pane-mode keys get their own section after tab
// mode, clash only among themselves, and say how to turn the mode on.
func TestPaneModeSection(t *testing.T) {
	for _, tc := range []struct{ preset, desc string }{
		{"conventional", "Off: give pane_prefix a shortcut above to use these."},
		{"aide", "After Ctrl+P, these keys act on panes until Esc or Enter."},
	} {
		var p Page
		p.s.Keys = config.Preset(tc.preset)
		ss := p.shortcuts()
		last := ss[len(ss)-1]
		if last.title != "Pane mode" || last.desc != tc.desc || len(last.rows) != 10 {
			t.Fatalf("%s: last section %q %q with %d rows", tc.preset, last.title, last.desc, len(last.rows))
		}
		if r := last.rows[0]; r.label != "Pane mode: new pane, split along the focused pane's longer side" || r.desc != "new" {
			t.Errorf("%s: first row %q / %q", tc.preset, r.label, r.desc)
		}
	}
	b := config.Preset("aide")
	if o := owner(b, slot{"new", "keys.pane", 0}, chord("X")); o != "close" {
		t.Errorf("pane owner = %q", o)
	}
	if o := owner(b, slot{"new", "keys.pane", 0}, chord("R")); o != "split_right" {
		t.Errorf("R in pane mode is split_right, not tab rename: %q", o)
	}
	if v := (edit{"next", "keys.pane", b.Pane["next"]}).value("aide"); v != nil {
		t.Errorf("preset pane value written: %s", *v)
	}
}

// TestDecisionsConnect: the Decisions page saves a pasted key to a 0600
// credentials file, never to config.toml, turns Jev on, tests it, and
// disconnecting removes both.
func TestDecisionsConnect(t *testing.T) {
	const key = "ts_live_abcdefghijklmnopqrstuvwxyz012345"
	t.Setenv(decide.KeyEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	defer func(f func(context.Context, decide.Provider, time.Duration, ...string) (time.Duration, error)) {
		ping = f
	}(ping)
	pinged := make(chan string, 1)
	ping = func(_ context.Context, p decide.Provider, _ time.Duration, secrets ...string) (time.Duration, error) {
		pinged <- string(p.(*decide.Jev).Key)
		return 120 * time.Millisecond, nil
	}
	var p Page
	p.Show(path)
	p.s, _ = config.LoadFile(path)
	p.th = theme.Dark()
	if p.dp.keySrc != "" {
		t.Fatal("a key before connecting")
	}
	p.dp.key.SetText("  " + key + "\n")
	p.connect()
	if got := <-pinged; got != key {
		t.Errorf("tested %q", got)
	}
	cred := filepath.Join(dir, "credentials")
	if fi, err := os.Stat(cred); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("credentials %v %v", fi, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), key) {
		t.Fatal("the key went into config.toml")
	}
	p.s, _ = config.LoadFile(path)
	if p.s.Decisions.Provider != "jev" || p.dp.keySrc != decide.FromFile || p.dp.key.Text() != "" {
		t.Fatalf("after connect: provider %q, source %q, field %q", p.s.Decisions.Provider, p.dp.keySrc, p.dp.key.Text())
	}
	for start := time.Now(); ; time.Sleep(time.Millisecond) {
		p.dp.mu.Lock()
		res, testing := p.dp.result, p.dp.testing
		p.dp.mu.Unlock()
		if !testing && res != "" {
			if res != "Connection ok · 120 ms" {
				t.Errorf("result %q", res)
			}
			break
		}
		if time.Since(start) > time.Second {
			t.Fatal("test never finished")
		}
	}
	p.disconnect()
	p.s, _ = config.LoadFile(path)
	if _, err := os.Stat(cred); !os.IsNotExist(err) || p.s.Decisions.Provider != "" || p.dp.keySrc != "" {
		t.Errorf("after disconnect: %v, provider %q, source %q", err, p.s.Decisions.Provider, p.dp.keySrc)
	}
	p.dp.key.SetText("short")
	p.connect()
	if p.dp.keyErr == "" {
		t.Error("a bad key connected")
	}
}

// TestDecisionsStates: the Connect card picks the right row for each
// setup, and every state lays out.
func TestDecisionsStates(t *testing.T) {
	t.Setenv(decide.KeyEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	var p Page
	p.Show(path)
	p.th = theme.Dark()
	var ops op.Ops
	draw := func() {
		t.Helper()
		ops.Reset()
		p.cat = catDecisions
		gtx := gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		p.Layout(gtx, theme.Dark(), p.s, nil)
	}
	conn := func() string { return p.decisions()[0].rows[0].label }
	setup := func(cfg string) {
		os.WriteFile(path, []byte(cfg), 0o644)
		p.s, _ = config.LoadFile(path)
		p.readKey()
	}
	cred := decide.CredentialsPath(dir)
	setup("")
	if got := conn(); got != "Connect Jev" || p.decisions()[0].rows[0].wide != true {
		t.Errorf("disconnected: %q", got)
	}
	draw()
	decide.SaveKey(cred, "ts_live_abcdefghijklmnopqrstuvwxyz012345")
	setup("")
	if got := p.decisions()[0].rows[0]; got.label != "Connect Jev" || !strings.Contains(got.desc, "decisions are off") {
		t.Errorf("key saved, provider off: %+v", got.label)
	}
	draw()
	setup("[decisions]\nprovider = \"jev\"\n")
	if got := conn(); got != "Jev is connected" {
		t.Errorf("connected: %q", got)
	}
	draw()
	t.Setenv(decide.KeyEnv, "env_key_1234567890")
	setup("[decisions]\nprovider = \"jev\"\n")
	if got := p.decisions()[0].rows[0]; !strings.Contains(got.desc, "TYPESAFE_API_KEY") {
		t.Errorf("env key: %q", got.desc)
	}
	draw()
	t.Setenv(decide.KeyEnv, "")
	setup("[decisions]\nprovider = \"command\"\ncommand = [\"my-classifier\"]\n")
	if got := conn(); got != "Your command" {
		t.Errorf("command provider: %q", got)
	}
	draw()
	setup("[decisions]\nprovider = \"jev\"\n[decisions.approvals]\nmode = \"auto\"\n")
	p.SetDecisions(model.DecideInfo{Provider: "jev", Auto: true, Counts: []model.DecideCount{{Feature: decide.FeatureApprovals, Calls: 3, Errors: 1}},
		Audit: []model.AutoDecision{{Tab: "fix", Tool: "Bash", Input: "go test", Verdict: "allow", Allow: 0.97}, {Tab: "fix", Tool: "Bash", Input: "x", Verdict: "deny", Deny: 0.97, Rules: []string{"sudo"}}}})
	secs := p.decisions()
	if len(secs) != 3 || len(secs[1].rows) != 6 || !strings.Contains(secs[1].rows[0].desc, "3 calls, 1 failed") {
		t.Errorf("auto mode: %d sections, %d feature rows", len(secs), len(secs[1].rows))
	}
	draw()
	for _, st := range []struct {
		testing bool
		result  string
		ok      bool
		keyErr  string
	}{{true, "", false, ""}, {false, "Connection ok · 90 ms", true, ""}, {false, "jev: HTTP 401, the API key was refused", false, ""}, {false, "", false, "the key has spaces or quotes in it"}} {
		p.dp.mu.Lock()
		p.dp.testing, p.dp.result, p.dp.ok = st.testing, st.result, st.ok
		p.dp.mu.Unlock()
		p.dp.keyErr = st.keyErr
		draw()
	}
}
