package app

import (
	"image"
	"slices"
	"sync"
	"testing"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// chatBackend is the fake with a Chatter that records the chats asked for.
type chatBackend struct {
	*FakeBackend
	mu    sync.Mutex
	asked []string
}

func (c *chatBackend) Chat(id string) (session.Snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, id)
	return session.Snapshot{SessionID: id, Provider: model.ProviderClaude, SessionState: session.StateWaiting,
		Content:          []session.Item{{ID: "p", Kind: session.KindUserPrompt, Text: "Fix it"}},
		PendingApprovals: []session.Approval{{ID: "r1", RequestType: session.RequestCommand}}}, true
}

// TestChatTab: a chat tab draws its transcript, with no Open in aide
// where aide is not installed, and the panel follows the chat in its
// folder; the sidebar marks it with its provider.
func TestChatTab(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	defer func() { findAide = old }()
	dir := t.TempDir()
	b := &chatBackend{FakeBackend: NewFakeBackend()}
	b.mu.Lock()
	b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Fix it", Path: dir, Chat: "c1"})
	s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
	s1.Order = append(s1.Order, "wc")
	b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: model.ProviderClaude, State: session.StateRunning, Title: "Fix it"}}
	b.mu.Unlock()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	var ops op.Ops
	frame := func() {
		ops.Reset()
		u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1600, 800)), Now: time.Now()})
	}
	frame()
	u.nav.workspace, u.nav.panelOpen = "wc", true
	frame()
	frame()
	if u.nav.workspace != "wc" {
		t.Fatalf("on %q, want the chat tab", u.nav.workspace)
	}
	b.mu.Lock()
	asked := slices.Clone(b.asked)
	b.mu.Unlock()
	if n := len(asked); n < 2 || slices.ContainsFunc(asked, func(id string) bool { return id != "c1" }) {
		t.Fatalf("asked for %v, want c1 by the transcript and the panel", asked)
	}
	u.panel.mu.Lock()
	listed := u.panel.filesDir
	u.panel.mu.Unlock()
	if listed != dir {
		t.Fatalf("panel lists %q, want the chat's folder %q", listed, dir)
	}
	st := b.State()
	if p := sidebar.AgentOf(&st, st.Workspaces[len(st.Workspaces)-1]); p != model.ProviderClaude {
		t.Fatalf("sidebar mark %q", p)
	}
}
