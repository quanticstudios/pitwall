package app

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// chatTab adds chat tab wc with chat c1 in state to b and returns the ui
// showing it.
func chatTab(t *testing.T, state string) (*chatBackend, *ui) {
	t.Helper()
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	b := &chatBackend{FakeBackend: NewFakeBackend()}
	b.mu.Lock()
	b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Fix it", Path: t.TempDir(), Branch: "fix/login", Chat: "c1"})
	s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
	s1.Order = append(s1.Order, "wc")
	b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: model.ProviderClaude, State: state, Title: "Fix it"}}
	b.mu.Unlock()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	return b, u
}

// TestChatTerminalToggle: flipping to the terminal asks first while a turn
// runs, goes at once when idle, and flips back from the terminal view.
func TestChatTerminalToggle(t *testing.T) {
	b, u := chatTab(t, session.StateRunning)
	st := b.State()
	ws := st.Workspace("wc")
	n := len(b.Sent())
	u.toggleChatTerminal(&st, ws)
	if u.modal.kind != modalChatTerminal || u.modal.then != (proto.ChatTerminal{Chat: "c1", On: true}) || len(b.Sent()) != n {
		t.Fatalf("mid-turn: modal %d then %#v, sent %v", u.modal.kind, u.modal.then, b.Sent()[n:])
	}
	u.confirmModal(&st)
	if sent := b.Sent(); sent[len(sent)-1] != (proto.ChatTerminal{Chat: "c1", On: true}) || u.modal.kind != modalNone {
		t.Fatalf("confirmed: sent %#v", sent[len(sent)-1])
	}

	st.Chats[0].State = session.StateReady
	u.toggleChatTerminal(&st, ws)
	if sent := b.Sent(); sent[len(sent)-1] != (proto.ChatTerminal{Chat: "c1", On: true}) || u.modal.kind != modalNone {
		t.Fatalf("idle: sent %#v, modal %d", sent[len(sent)-1], u.modal.kind)
	}
	st.Chats[0].State, st.Chats[0].Terminal = session.StateRunning, "tp"
	u.toggleChatTerminal(&st, ws)
	if sent := b.Sent(); sent[len(sent)-1] != (proto.ChatTerminal{Chat: "c1"}) || u.modal.kind != modalNone {
		t.Fatalf("from the terminal: sent %#v", sent[len(sent)-1])
	}
}

// TestChatTerminalKey: the window takes chat_terminal only on a chat tab;
// elsewhere Alt+T reaches the pane.
func TestChatTerminalKey(t *testing.T) {
	b, u := chatTab(t, session.StateReady)
	st := b.State()
	altT := func() bool {
		return slices.ContainsFunc(u.nav.keyFilters(&st), func(f key.Filter) bool { return f.Name == "T" && f.Required == key.ModAlt })
	}
	u.nav.workspace = "wc"
	if !altT() {
		t.Fatal("chat tab: Alt+T not taken")
	}
	if u.nav.globalOp(&st, "chat_terminal"); !u.nav.chatTerminal {
		t.Fatal("chat_terminal did not ask for the toggle")
	}
	u.nav.workspace = "w6"
	if altT() {
		t.Fatal("pane tab: Alt+T taken from the pane")
	}
}

// TestChatHeadViews draws a chat tab's header, then its terminal view with
// the bar over the tab's pane.
func TestChatHeadViews(t *testing.T) {
	b, u := chatTab(t, session.StateReady)
	var ops op.Ops
	now := time.Now()
	frame := func() {
		ops.Reset()
		now = now.Add(time.Second)
		u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1600, 800)), Now: now})
	}
	frame()
	u.nav.workspace = "wc"
	frame()
	b.mu.Lock()
	w := &b.st.Workspaces[len(b.st.Workspaces)-1]
	w.Tabs = []model.Tab{{ID: "tt", Layout: &layout.Node{Pane: "tp"}}}
	w.ActiveTab = "tt"
	b.st.Panes = append(b.st.Panes, model.Pane{ID: "tp", WorkspaceID: "wc", Cwd: w.Path, Cmd: []string{"/opt/claude", "--resume", "7f3c2a10-0000-4000-8000-000000000001"}})
	b.st.Chats[0].Terminal = "tp"
	b.mu.Unlock()
	frame()
	frame()
	if u.panes["tp"] == nil {
		t.Fatal("terminal view drew no pane")
	}
	if got := terminalLine([]string{"/opt/claude", "--resume", "7f3c2a10-0000", "--model", "opus"}); got != "claude --resume 7f3c… --model opus" {
		t.Fatalf("bar says %q", got)
	}
}

// TestChatSeen: looking at a chat tab marks its finished turn seen, so its
// sidebar pill settles as a pane agent's does.
func TestChatSeen(t *testing.T) {
	b, u := chatTab(t, session.StateReady)
	b.mu.Lock()
	b.st.Activities = append(b.st.Activities, model.Activity{PaneID: "c1", WorkspaceID: "wc", Provider: model.ProviderClaude, State: model.StateCompleted, Unseen: true, UpdatedAt: time.Now()})
	b.mu.Unlock()
	st := b.State()
	u.winFocused, u.nav.workspace = true, "wc"
	u.markSeen(&st)
	if !slices.Contains(b.Sent(), any(proto.SeePane{Pane: "c1"})) {
		t.Fatalf("sent %v, want SeePane for the chat", b.Sent())
	}
}

// TestTipReady: the first tooltip waits, a neighbor's right after shows at once.
func TestTipReady(t *testing.T) {
	t0 := time.Now()
	if tipReady(t0.Add(400*time.Millisecond), t0, time.Time{}) || !tipReady(t0.Add(500*time.Millisecond), t0, time.Time{}) {
		t.Fatal("first tooltip not after 500ms")
	}
	if !tipReady(t0.Add(time.Millisecond), t0, t0.Add(-200*time.Millisecond)) || tipReady(t0.Add(time.Millisecond), t0, t0.Add(-time.Second)) {
		t.Fatal("neighbor within 300ms not at once")
	}
}
