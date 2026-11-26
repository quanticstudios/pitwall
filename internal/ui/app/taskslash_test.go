package app

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unicode/utf8"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// slashSkill writes a user skill named name under Claude Code's config
// dir conf.
func slashSkill(t *testing.T, conf, name string) {
	t.Helper()
	dir := filepath.Join(conf, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Review the UI\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The New task prompt has the chat composer's / menu, with Claude Code's
// commands for the picked folder: every one at the prompt's start, only
// skills and custom commands mid-draft. Enter and Tab pick rather than
// add a line, a pick replaces only the /word, Esc closes the menu and
// leaves the dialog open, and an agent with no / menu opens none.
func TestTaskSlashMenu(t *testing.T) {
	bin := t.TempDir()
	for _, a := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, exeName(a)), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	conf := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", conf)
	slashSkill(t, conf, "ux-review")

	b, u, frame, queue := formWindow(t)
	st := b.State()
	u.gui.TaskAgent = "claude"
	u.runAction(&st, config.Action{Name: "new_task", Group: "Agents"})
	u.openRequested(&st, time.Now())
	frame()
	frame()
	d := &u.task
	if u.modal.kind != modalTask {
		t.Fatal("the New task dialog did not open")
	}
	typ := func(s string) {
		n := utf8.RuneCountInString(d.prompt.Text())
		e := n + utf8.RuneCountInString(s)
		queue(key.EditEvent{Range: key.Range{Start: n, End: n}, Text: s}, key.SelectionEvent{Start: e, End: e})
	}
	rows := func() []string {
		var out []string
		for _, c := range d.slash.Rows() {
			out = append(out, c.Name)
		}
		return out
	}

	typ("/")
	if got, want := len(rows()), len(claude.Commands(conf, d.projects[d.project].dir)); got == 0 || got != want {
		t.Fatalf("/ lists %d commands, want all %d", got, want)
	}
	typ("ux")
	if got := rows(); !slices.Equal(got, []string{"ux-review"}) {
		t.Fatalf("/ux lists %q", got)
	}
	if queue(press(key.NameReturn, 0)); d.prompt.Text() != "/ux-review " || d.slash.Shown() || u.modal.kind != modalTask {
		t.Fatalf("Enter in the menu: prompt %q, menu %v, dialog %v", d.prompt.Text(), d.slash.Shown(), u.modal.kind)
	}

	d.prompt.SetText("")
	typ("look at it with /co")
	if got := rows(); len(got) != 0 {
		t.Fatalf("mid-draft /co lists built-ins %q", got)
	}
	d.prompt.SetText("")
	typ("look at it with /u")
	if got := rows(); !slices.Equal(got, []string{"ux-review"}) {
		t.Fatalf("mid-draft /u lists %q, want the skill alone", got)
	}
	d.prompt.SetText("look at /u and then")
	d.prompt.SetCaret(10, 10)
	frame()
	if queue(press(key.NameTab, 0)); d.prompt.Text() != "look at /ux-review and then" {
		t.Fatalf("a pick mid-draft: %q", d.prompt.Text())
	}

	d.prompt.SetText("")
	typ("/ux")
	if queue(press(key.NameEscape, 0)); d.slash.Shown() || u.modal.kind != modalTask {
		t.Fatalf("Esc: menu %v, dialog %v", d.slash.Shown(), u.modal.kind)
	}
	if frame(); d.slash.Shown() {
		t.Fatal("the menu came back on the text Esc closed it on")
	}
	if queue(press(key.NameEscape, 0)); u.modal.kind != modalNone {
		t.Fatal("Esc with the menu closed left the dialog open")
	}

	u.runAction(&st, config.Action{Name: "new_task", Group: "Agents"})
	u.openRequested(&st, time.Now())
	frame()
	d.pickAgent(slices.IndexFunc(d.agents, func(a foundAgent) bool { return a.cmd == "codex" }), false)
	typ("/")
	if d.slash.Shown() {
		t.Fatalf("Codex's prompt opened the menu: %q", rows())
	}
}

// The dialog reads the / menu again when the picked folder or agent
// changes, and not on every frame; a host's folder, which this window
// cannot read, gets none.
func TestTaskSlashCommands(t *testing.T) {
	conf := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", conf)
	slashSkill(t, conf, "ux-review")
	a, b := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(b, ".claude", "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, ".claude", "commands", "ship.md"), []byte("Ship it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &taskDialog{
		projects: []taskProject{{name: "a", dir: a}, {name: "b", dir: b}, {name: "far", dir: b, host: "box"}},
		agents:   []foundAgent{{cmd: "claude", name: "Claude Code"}, {cmd: "codex", name: "Codex"}, {cmd: "agy", name: "Antigravity"}},
		picked:   []bool{true, false, false},
	}
	has := func(cmds []session.Command, name string) bool {
		return slices.ContainsFunc(cmds, func(c session.Command) bool { return c.Name == name })
	}
	cmds, p := d.slashCommands(config.Settings{})
	if p != model.ProviderClaude || !has(cmds, "ux-review") || has(cmds, "ship") {
		t.Fatalf("in a: %v %v", p, cmds)
	}
	slashSkill(t, conf, "later")
	if cmds, _ = d.slashCommands(config.Settings{}); has(cmds, "later") {
		t.Fatal("the same folder and agent read the files again")
	}
	d.project = 1
	if cmds, _ = d.slashCommands(config.Settings{}); !has(cmds, "ship") || !has(cmds, "later") {
		t.Fatalf("in b: %v", cmds)
	}
	d.picked = []bool{false, true, false}
	if cmds, p = d.slashCommands(config.Settings{}); cmds != nil || p != "" {
		t.Fatalf("Codex: %v %v", p, cmds)
	}
	d.picked = []bool{false, false, true}
	if _, p = d.slashCommands(config.Settings{}); p != model.ProviderAntigravity {
		t.Fatalf("agy: %v", p)
	}
	d.picked, d.project = []bool{true, false, false}, 2
	if cmds, _ = d.slashCommands(config.Settings{}); cmds != nil {
		t.Fatalf("a host's folder: %v", cmds)
	}
}

// The reply sheet's / menu lists the commands of the pane chat of the
// agent the reply goes to, the tab's agent for no pane; none for a pane
// that runs no agent chat.
func TestReplyCommands(t *testing.T) {
	cmds := []session.Command{{Name: "ux-review", Source: "user"}, {Name: "compact"}}
	b := agyQueueBackend{NewFakeBackend(), session.Snapshot{Commands: cmds}}
	i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
	b.st.Panes[i].Provider, b.st.Panes[i].Transcript = model.ProviderClaude, "/tmp/transcript.jsonl"
	u := &ui{b: b}
	st := b.State()
	ws := b.st.Panes[i].WorkspaceID
	if got, p := u.replyCommands(&st, ws, "h"); !slices.EqualFunc(got, cmds, session.Command.Equal) || p != model.ProviderClaude {
		t.Fatalf("pane h: %v %v", p, got)
	}
	b.st.Panes[i].Provider = ""
	st = b.State()
	if got, _ := u.replyCommands(&st, ws, "h"); got != nil {
		t.Fatalf("a shell pane: %v", got)
	}
}

// TestTaskSlashShot renders a headless PNG of the New task dialog's /
// menu, mid-draft, into $PITWALL_SLASH_SHOTS; it skips when unset.
func TestTaskSlashShot(t *testing.T) {
	dir := os.Getenv("PITWALL_SLASH_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_SLASH_SHOTS is not set")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, exeName("claude")), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	conf := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", conf)
	for _, s := range []string{"ux-review", "release-notes", "deploy-check"} {
		slashSkill(t, conf, s)
	}
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.cfg.Font.UISize = 13
	size := image.Pt(1100, 900)
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(size), Now: time.Now()})
		r.Frame(&ops)
	}
	frame()
	st := b.State()
	u.gui.TaskAgent = "claude"
	u.runAction(&st, config.Action{Name: "new_task", Group: "Agents"})
	u.openRequested(&st, time.Now())
	frame()
	frame()
	s := "Tighten the login page copy, then run /"
	n := utf8.RuneCountInString(s)
	r.Queue(key.EditEvent{Text: s}, key.SelectionEvent{Start: n, End: n})
	for range 4 {
		frame()
		time.Sleep(100 * time.Millisecond) // the dialog's entrance
	}
	if !u.task.slash.Shown() {
		t.Fatal("the menu is not open")
	}
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	out := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "task-slash.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		t.Fatal(err)
	}
}
