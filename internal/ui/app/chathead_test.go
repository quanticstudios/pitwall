package app

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
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

// TestAgentChatTerminalToggle: Codex and agy chats flip to their TUI on a
// daemon that runs theirs, the dialog naming the agent mid-turn; on an
// older daemon, and for other agents, nothing goes.
func TestAgentChatTerminalToggle(t *testing.T) {
	b, u := chatTab(t, session.StateRunning)
	st := b.State()
	ws := st.Workspace("wc")
	st.Chats[0].Provider = model.ProviderCodex
	u.toggleChatTerminal(&st, ws)
	if u.modal.kind != modalChatTerminal || u.modal.then != (proto.ChatTerminal{Chat: "c1", On: true}) || !slices.Equal(u.modal.agents, []string{"Codex"}) {
		t.Fatalf("codex mid-turn: modal %d then %#v agents %q", u.modal.kind, u.modal.then, u.modal.agents)
	}
	u.modal.close()
	st.Chats[0].Provider, st.Chats[0].State = model.ProviderAntigravity, session.StateReady
	n := len(b.Sent())
	u.toggleChatTerminal(&st, ws)
	if sent := b.Sent(); len(sent) != n+1 || sent[n] != (proto.ChatTerminal{Chat: "c1", On: true}) {
		t.Fatalf("agy idle: sent %#v", sent[n:])
	}

	old := &levelBackend{FakeBackend: b.FakeBackend, level: proto.ChatTerminalAgentsLevel - 1}
	u.b = old
	for _, p := range []model.Provider{model.ProviderCodex, model.ProviderAntigravity, "acp:grok"} {
		st.Chats[0].Provider = p
		n := len(b.Sent())
		if u.toggleChatTerminal(&st, ws); len(b.Sent()) != n || u.modal.kind != modalNone {
			t.Fatalf("%s on an older daemon: sent %#v, modal %d", p, b.Sent()[n:], u.modal.kind)
		}
	}
	st.Chats[0].Provider = model.ProviderClaude
	if u.toggleChatTerminal(&st, ws); len(b.Sent()) != n+2 {
		t.Fatal("claude on an older daemon: nothing sent")
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
	for cmd, want := range map[string]string{
		"/opt/claude --resume 7f3c2a10-0000 --model opus":           "claude --resume 7f3c… --model opus",
		"/opt/codex resume 01a2092d-8bc0 --no-daemon -a on-request": "codex resume 01a2… --no-daemon -a on-request",
		"/opt/agy --conversation 71b21408-ec1c --mode plan":         "agy --conversation 71b2… --mode plan",
	} {
		if got := terminalLine(strings.Fields(cmd)); got != want {
			t.Errorf("bar says %q, want %q", got, want)
		}
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

// TestChatHeadLongTitleDoesNotClipButtons verifies that a very long title and
// branch name truncate inside the Flexed(1) left cluster so right-side toolbar
// buttons stay inside the header width and clickable.
func TestChatHeadLongTitleDoesNotClipButtons(t *testing.T) {
	b, u := chatTab(t, session.StateReady)
	b.mu.Lock()
	w := &b.st.Workspaces[len(b.st.Workspaces)-1]
	w.Label = strings.Repeat("Very Long Chat Workspace Title ", 8)
	w.Branch = "feature/" + strings.Repeat("extremely-long-branch-name-", 8)
	b.mu.Unlock()

	st := b.State()
	ws := st.Workspace("wc")
	var r input.Router
	var ops op.Ops
	const width = 500
	frame := func() {
		ops.Reset()
		gtx := gl.Context{
			Ops:         &ops,
			Source:      r.Source(),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(width, 200)),
			Now:         time.Now(),
		}
		u.layoutChatHead(gtx, &st, ws)
		r.Frame(&ops)
	}
	frame()
	// The rightmost control (panel button) sits at [width-SpaceL-headControl, width-SpaceL].
	// Clicking inside it must toggle u.nav.panelOpen rather than being clipped off-screen.
	pos := f32.Pt(float32(width-16-14), float32(22))
	r.Queue(
		pointer.Event{Kind: pointer.Move, Position: pos, Source: pointer.Mouse},
		pointer.Event{Kind: pointer.Press, Position: pos, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary},
		pointer.Event{Kind: pointer.Release, Position: pos, Source: pointer.Mouse},
	)
	frame()
	frame()
	if !u.nav.panelOpen {
		t.Fatal("rightmost Agent panel button was clipped or unclickable with long title+branch")
	}
}
