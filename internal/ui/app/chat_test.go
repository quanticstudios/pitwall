package app

import (
	"image"
	"slices"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
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
	// why: each frame is a second later, so the panel's slide finishes. On
	// Windows, time.Now() can return the same instant for all three frames.
	now := time.Now()
	frame := func() {
		ops.Reset()
		now = now.Add(time.Second)
		u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1600, 800)), Now: now})
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
	// The composer's draft survives a switch to another tab and back.
	cmp := u.composers["c1"]
	if cmp == nil {
		t.Fatal("the chat tab drew no composer")
	}
	cmp.SetText("half a thought")
	u.nav.workspace = "w1"
	frame()
	u.nav.workspace = "wc"
	frame()
	if u.composers["c1"] != cmp || cmp.Text() != "half a thought" {
		t.Fatalf("the draft is %q after a tab switch", u.composers["c1"].Text())
	}
	st := b.State()
	if p := sidebar.AgentOf(&st, st.Workspaces[len(st.Workspaces)-1]); p != model.ProviderClaude {
		t.Fatalf("sidebar mark %q", p)
	}
}

// TestChatTranscripts: each chat keeps its own transcript across tab
// switches, and a chat that is gone loses it.
func TestChatTranscripts(t *testing.T) {
	var u ui
	st := &model.State{Chats: []model.Chat{{ID: "a"}, {ID: "b"}}}
	a, b := u.chatTranscript(st, "a"), u.chatTranscript(st, "b")
	if a == b || u.chatTranscript(st, "a") != a {
		t.Fatal("chats share a transcript, or a tab switch made a new one")
	}
	st.Chats = st.Chats[:1]
	u.chatTranscript(st, "a")
	if _, ok := u.transcripts["b"]; ok || len(u.transcripts) != 1 {
		t.Fatalf("a closed chat keeps its transcript: %d", len(u.transcripts))
	}
}

// TestChatComposerKeys: 2 typed into the empty composer answers the
// chat's newest approval through its transcript, and leaves no draft.
func TestChatComposerKeys(t *testing.T) {
	old := findAide
	findAide = func() []string { return nil }
	defer func() { findAide = old }()
	b := &chatBackend{FakeBackend: NewFakeBackend()}
	b.mu.Lock()
	b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Fix it", Path: t.TempDir(), Chat: "c1"})
	s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
	s1.Order = append(s1.Order, "wc")
	b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: model.ProviderClaude, State: session.StateWaiting, Title: "Fix it"}}
	b.mu.Unlock()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	var r input.Router
	var ops op.Ops
	now := time.Now()
	frame := func() {
		ops.Reset()
		now = now.Add(time.Second)
		u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1600, 800)), Now: now})
		r.Frame(&ops)
	}
	frame()
	u.nav.workspace = "wc"
	for range 3 {
		frame()
	}
	r.Queue(key.EditEvent{Text: "2"})
	frame()
	frame()
	want := proto.ChatApprove{Chat: "c1", Request: "r1", Decision: "allow-session"}
	if !slices.Contains(b.Sent(), any(want)) {
		t.Fatalf("sent %+v, want %+v", b.Sent(), want)
	}
	if c := u.composers["c1"]; c == nil || c.Text() != "" {
		t.Fatal("the key stayed in the composer as a draft")
	}
}
