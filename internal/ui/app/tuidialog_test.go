package app

import (
	"image"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// TestTUICommand: the commands that draw a screen of their own route to
// the dialog, with their arguments; the chips' and cards' commands, a
// prompt, and a longer name that starts like one do not.
func TestTUICommand(t *testing.T) {
	for text, want := range map[string]string{
		"/config":            "/config",
		"  /mcp ":            "/mcp",
		"/resume abc":        "/resume abc",
		"/login":             "/login",
		"/permissions":       "/permissions",
		"/model opus":        "",
		"/effort high":       "",
		"/usage":             "",
		"/context":           "",
		"/configure the app": "",
		"config":             "",
		"fix /config":        "",
		"":                   "",
	} {
		if got := tuiCommand(text); got != want {
			t.Errorf("tuiCommand(%q) = %q, want %q", text, got, want)
		}
	}
}

// TestTUIDialogStep: a pane chat's dialog shows once the TUI's input box
// leaves the screen and goes when it is back; a command whose screen never
// comes goes after tuiWait unseen; a chat tab's shows while its TUI starts.
func TestTUIDialogStep(t *testing.T) {
	now := time.Now()
	d := &tuiDialog{typed: true, since: now}
	for i, tc := range []struct {
		ready      bool
		show, keep bool
	}{{true, false, true}, {false, true, true}, {false, true, true}, {true, false, false}} {
		if show, keep := d.step(tc.ready, now); show != tc.show || keep != tc.keep {
			t.Fatalf("step %d: show %v keep %v, want %v %v", i, show, keep, tc.show, tc.keep)
		}
	}
	d = &tuiDialog{typed: true, since: now}
	if _, keep := d.step(true, now.Add(tuiWait+time.Second)); keep {
		t.Fatal("a screen that never came kept the dialog")
	}
	d = &tuiDialog{tab: true, since: now}
	if show, keep := d.step(true, now); !show || !keep {
		t.Fatal("a chat tab's dialog hid while its TUI starts")
	}
}

// screenBackend is chatBackend with pane h's screen set by the test.
type screenBackend struct {
	*chatBackend
	smu    sync.Mutex
	screen []string
}

func (b *screenBackend) Frame(pane string) (vt.Grid, vt.Modes, bool) {
	b.smu.Lock()
	lines := b.screen
	b.smu.Unlock()
	if pane != "h" || lines == nil {
		return b.chatBackend.Frame(pane)
	}
	e := vt.New(100, 30, nil)
	e.Write([]byte(strings.Join(lines, "\r\n")))
	return e.Snapshot(), vt.Modes{}, true
}

func (b *screenBackend) show(lines ...string) {
	b.smu.Lock()
	b.screen = lines
	b.smu.Unlock()
}

var (
	tuiBox    = []string{"● Done.", "", strings.Repeat("─", 60), "❯ ", strings.Repeat("─", 60), "  ⏸ manual mode on · ? for shortcuts"}
	tuiConfig = []string{"  Settings  Status   Config   Usage", "    Auto-scroll       on", "  ↑/↓ to navigate · Space to toggle · Esc to close"}
)

// TestTUIDialog: /config sent from a pane chat opens the dialog over the
// chat once its screen is up, which lays the pane out at the dialog's
// size and leaves the composer the keys no more; the TUI's prompt coming
// back closes it, the close button sends Esc and closes it, and Alt+T's
// flip drops it for the full TUI.
func TestTUIDialog(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	b := &screenBackend{chatBackend: &chatBackend{FakeBackend: NewFakeBackend()}}
	b.mu.Lock()
	i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
	b.st.Panes[i].Provider, b.st.Panes[i].Transcript = model.ProviderClaude, "/tmp/s.jsonl"
	b.mu.Unlock()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.cfg.Chat.Claude = true
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "w6", "")
	var ops op.Ops
	now := time.Now()
	frame := func() {
		ops.Reset()
		u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1600, 800)), Now: now})
	}
	resizes := func() []proto.Resize {
		var out []proto.Resize
		for _, m := range b.Sent() {
			if r, ok := m.(proto.Resize); ok && r.Pane == "h" {
				out = append(out, r)
			}
		}
		return out
	}

	b.show(tuiBox...)
	frame()
	full := resizes()
	if len(full) == 0 {
		t.Fatal("the pane chat sent no size")
	}
	if !u.openTUI(&st, "h", "/config", now) || u.tui["h"].tab {
		t.Fatal("a pane chat's command opened no pane dialog")
	}
	frame() // typed, the screen not up yet: no dialog
	if d := u.tui["h"]; d == nil || d.up {
		t.Fatal("the dialog closed or showed before the command's screen")
	}
	b.show(tuiConfig...)
	frame()
	if d := u.tui["h"]; d == nil || !d.up {
		t.Fatal("the command's screen did not open the dialog")
	}
	if r := resizes(); r[len(r)-1].Cols >= full[len(full)-1].Cols || r[len(r)-1].Rows >= full[len(full)-1].Rows {
		t.Fatalf("the dialog did not size the pane to it: %+v after %+v", r[len(r)-1], full[len(full)-1])
	}
	b.show(tuiBox...)
	frame()
	if u.tui["h"] != nil {
		t.Fatal("the TUI's prompt coming back left the dialog open")
	}

	u.openTUI(&st, "h", "/mcp", now)
	b.show(tuiConfig...)
	frame()
	u.tui["h"].close.Click()
	n := len(b.Sent())
	frame()
	if u.tui["h"] != nil || !slices.ContainsFunc(b.Sent()[n:], func(m any) bool {
		in, ok := m.(proto.Input)
		return ok && in.Pane == "h" && string(in.Data) == "\x1b"
	}) {
		t.Fatal("close did not press Esc in the TUI and close the dialog")
	}

	u.openTUI(&st, "h", "/hooks", now)
	frame()
	u.flipPaneChat("h")
	if u.tui["h"] != nil {
		t.Fatal("the flip to the full TUI kept the dialog")
	}
}

// TestTUIDialogChatTab: a chat tab's command opens its terminal view on
// the same session and not the driver, types the command once the TUI is
// up, then Enter, and closing goes back to the chat; a turn running, or
// arguments the driver takes, as /config key=value, keep it a message.
func TestTUIDialogChatTab(t *testing.T) {
	b := &screenBackend{chatBackend: &chatBackend{FakeBackend: NewFakeBackend()}}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}}
	st := &model.State{Chats: []model.Chat{{ID: "c1", Provider: model.ProviderClaude, State: session.StateRunning}}}
	now := time.Now()
	if u.openTUI(st, "c1", "/config", now) {
		t.Fatal("a running chat opened the dialog")
	}
	st.Chats[0].State = session.StateReady
	if u.openTUI(st, "c1", "/config theme=dark", now) {
		t.Fatal("a command the driver runs headless opened the dialog")
	}
	if !u.openTUI(st, "c1", "/config", now) || !slices.Contains(b.Sent(), any(proto.ChatTerminal{Chat: "c1", On: true})) {
		t.Fatal("no terminal view for the dialog")
	}
	if d := u.tuiShown(st, "c1", now); d == nil || d.pane != "" {
		t.Fatal("the dialog hid while the terminal view starts")
	}
	b.mu.Lock()
	st.Panes = append(st.Panes, b.st.Panes[slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })])
	b.mu.Unlock()
	st.Chats[0].Terminal = "h"
	b.show(tuiBox...)
	u.tuiShown(st, "c1", now)
	u.tuiShown(st, "c1", now.Add(tuiEnterAfter))
	var typed []string
	for _, m := range b.Sent() {
		if in, ok := m.(proto.Input); ok && in.Pane == "h" {
			typed = append(typed, string(in.Data))
		}
	}
	if !slices.Equal(typed, []string{"/config", "\r"}) {
		t.Fatalf("typed %q", typed)
	}
	b.show(tuiConfig...)
	if d := u.tuiShown(st, "c1", now.Add(time.Second)); d == nil || !d.up {
		t.Fatal("the command's screen did not show")
	}
	b.show(tuiBox...)
	if u.tuiShown(st, "c1", now.Add(2*time.Second)) != nil || !slices.Contains(b.Sent(), any(proto.ChatTerminal{Chat: "c1"})) {
		t.Fatal("closing did not go back to the chat")
	}
}
