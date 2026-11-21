package app

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// suggestBackend is a chat backend whose chat c1 finished turn t1, and
// that answers ChatSuggest and ServerStop as a daemon would.
type suggestBackend struct {
	*FakeBackend
	mu      sync.Mutex
	turns   []session.TurnSummary
	state   string
	answers map[string]proto.ChatSuggestResult
	stops   []proto.ServerStopResult
	level   int
}

func (b *suggestBackend) Chat(id string) (session.Snapshot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return session.Snapshot{SessionID: id, Provider: model.ProviderClaude, SessionState: b.state, Turns: slices.Clone(b.turns),
		Content: []session.Item{{ID: "p", Kind: session.KindUserPrompt, Text: "Fix it"}, {ID: "a", Kind: session.KindAssistantText, Text: "Fixed."}}}, true
}

func (b *suggestBackend) ChatSuggestion(id string) (proto.ChatSuggestResult, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.answers[id]
	return r, ok
}

func (b *suggestBackend) ServerStops(ws string) []proto.ServerStopResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.stops
	b.stops = nil
	return out
}

func (b *suggestBackend) Link() Link { b.mu.Lock(); defer b.mu.Unlock(); return Link{Level: b.level} }
func (b *suggestBackend) Reconnect() {}
func (b *suggestBackend) Restart()   {}

func suggestUI(t *testing.T, edit func(*ui, *suggestBackend)) (*ui, *suggestBackend, func()) {
	t.Helper()
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	done := "2026-11-21T10:00:00Z"
	b := &suggestBackend{FakeBackend: NewFakeBackend(), state: session.StateReady, level: proto.Level,
		turns: []session.TurnSummary{{ID: "t1", State: session.TurnCompleted, CompletedAt: &done}}}
	b.FakeBackend.mu.Lock()
	b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Fix it", Path: t.TempDir(), Chat: "c1"})
	s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
	s1.Order = append(s1.Order, "wc")
	b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: model.ProviderClaude, State: session.StateReady, Title: "Fix it"}}
	b.st.Servers = map[string][]model.Server{"wc": {{PID: 42, Name: "node", Ports: []int{3000}, Agent: true}}}
	b.FakeBackend.mu.Unlock()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	u.cfg.Chat.SuggestNext = true
	if edit != nil {
		edit(u, b)
	}
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
	return u, b, frame
}

// TestSuggestAsks: a finished turn and an empty composer ask the daemon
// once; its answer is the composer's suggestion; a draft, the setting
// off or an older daemon ask nothing.
func TestSuggestAsks(t *testing.T) {
	u, b, frame := suggestUI(t, nil)
	frame()
	frame()
	if got := sentOf[proto.ChatSuggest](b.FakeBackend); !slices.Equal(got, []proto.ChatSuggest{{Chat: "c1", Turn: "t1"}}) {
		t.Fatalf("asked %+v, want once for t1", got)
	}
	st := b.State()
	s, _ := b.Chat("c1")
	if got := u.suggestion(&st, "c1", &s, u.composers["c1"]); got != "" {
		t.Fatalf("a suggestion before the answer: %q", got)
	}
	b.mu.Lock()
	b.answers = map[string]proto.ChatSuggestResult{"c1": {Chat: "c1", Turn: "t1", Text: "Run the tests"}}
	b.mu.Unlock()
	if got := u.suggestion(&st, "c1", &s, u.composers["c1"]); got != "Run the tests" {
		t.Fatalf("suggestion %q", got)
	}
	// The next turn runs: no suggestion, and the old answer is stale.
	b.mu.Lock()
	b.state = session.StateRunning
	b.turns = append(b.turns, session.TurnSummary{ID: "t2", State: session.TurnRunning})
	b.mu.Unlock()
	s, _ = b.Chat("c1")
	if got := u.suggestion(&st, "c1", &s, u.composers["c1"]); got != "" {
		t.Fatalf("a suggestion while running: %q", got)
	}
	// It ends while a draft is in the box: nothing is asked for it.
	u.composers["c1"].SetText("my own next step")
	b.mu.Lock()
	b.state = session.StateReady
	b.turns[1].State = session.TurnCompleted
	b.mu.Unlock()
	frame()
	if got := sentOf[proto.ChatSuggest](b.FakeBackend); len(got) != 1 {
		t.Fatalf("asked over a draft: %+v", got)
	}
	// Cleared: it asks for t2, once.
	u.composers["c1"].SetText("")
	frame()
	frame()
	if got := sentOf[proto.ChatSuggest](b.FakeBackend); len(got) != 2 || got[1].Turn != "t2" {
		t.Fatalf("asked %+v", got)
	}
	// A failed turn gets none.
	s.Turns[len(s.Turns)-1].State = session.TurnFailed
	if finishedTurn(&s) != "" {
		t.Fatal("a failed turn has a suggestion")
	}
}

func TestSuggestGates(t *testing.T) {
	_, b, frame := suggestUI(t, func(u *ui, _ *suggestBackend) { u.cfg.Chat.SuggestNext = false })
	frame()
	if got := sentOf[proto.ChatSuggest](b.FakeBackend); len(got) != 0 {
		t.Fatalf("asked with the setting off: %+v", got)
	}
	_, b, frame = suggestUI(t, func(_ *ui, b *suggestBackend) { b.level = proto.SuggestLevel - 1 })
	frame()
	if got := sentOf[proto.ChatSuggest](b.FakeBackend); len(got) != 0 {
		t.Fatalf("asked with the setting off or an older daemon: %+v", got)
	}
}

// TestServersWindow: the tab's servers reach the chat's Servers view, its
// Stop goes to the daemon as ServerStop, and the answer comes back to it.
func TestServersWindow(t *testing.T) {
	u, b, frame := suggestUI(t, nil)
	tr := u.transcripts["c1"]
	if tr == nil || len(tr.Servers.List) != 1 || tr.Servers.Note != "" {
		t.Fatalf("transcript servers %+v", tr)
	}
	tr.ShowServers()
	frame()
	if !tr.ServersOpen() {
		t.Fatal("the Servers view closed")
	}
	if tip := serversTip(b.State().Servers["wc"]); tip != "Servers: 3000" {
		t.Fatalf("tip %q", tip)
	}
	// An older daemon: a note in place of the list.
	b.mu.Lock()
	b.level = proto.ServersLevel - 1
	b.mu.Unlock()
	frame()
	if tr.Servers.Note == "" {
		t.Fatal("no note for an older daemon")
	}
	b.mu.Lock()
	b.level = proto.Level
	b.stops = []proto.ServerStopResult{{Workspace: "wc", PID: 42, Port: 3000, Running: true}}
	b.mu.Unlock()
	frame()
	frame()
}

func TestServersTip(t *testing.T) {
	ss := []model.Server{{Ports: []int{3000, 3001}}, {Ports: []int{5173, 8000, 9000}}}
	if got := serversTip(ss); got != "Servers: 3000, 3001, 5173, 8000 …" {
		t.Fatalf("tip %q", got)
	}
}

// TestServersShots writes the window with a chat whose turn ended, its
// suggestion in the composer, the header's Servers button and the
// sidebar's port, then with the Servers view open, to $PITWALL_CHAT_SHOTS.
func TestServersShots(t *testing.T) {
	dir := os.Getenv("PITWALL_CHAT_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_CHAT_SHOTS is not set")
	}
	u, _, _ := suggestUI(t, func(_ *ui, b *suggestBackend) {
		b.answers = map[string]proto.ChatSuggestResult{"c1": {Chat: "c1", Turn: "t1", Text: "Now run the e2e suite against the dev server"}}
		b.st.Servers["wc"] = append(b.st.Servers["wc"], model.Server{PID: 4310, Name: "vite", Ports: []int{5173}})
	})
	shot := func(name string) {
		size := image.Pt(1600, 900)
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		defer win.Release()
		var r input.Router
		var ops op.Ops
		for range 4 {
			ops.Reset()
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(size), Now: time.Now()})
			r.Frame(&ops)
			time.Sleep(150 * time.Millisecond)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
	}
	shot("window-suggest.png")
	u.transcripts["c1"].ShowServers()
	shot("window-servers.png")
}

// TestSuggestRetry: a refusal because the daemon still sees the agent
// working is asked again, a few times; a real answer, even an empty one,
// is not.
func TestSuggestRetry(t *testing.T) {
	u, b, frame := suggestUI(t, nil)
	b.mu.Lock()
	b.answers = map[string]proto.ChatSuggestResult{"c1": {Chat: "c1", Turn: "t1", Code: "busy", Err: "the agent is working"}}
	b.mu.Unlock()
	for range 10 {
		a := u.suggested["c1"]
		a.at = a.at.Add(-suggestRetry)
		u.suggested["c1"] = a
		frame()
	}
	if got := sentOf[proto.ChatSuggest](b.FakeBackend); len(got) != suggestTries {
		t.Fatalf("asked %d times, want %d", len(got), suggestTries)
	}
	u2, b2, frame2 := suggestUI(t, func(_ *ui, b *suggestBackend) {
		b.answers = map[string]proto.ChatSuggestResult{"c1": {Chat: "c1", Turn: "t1"}}
	})
	for range 5 {
		a := u2.suggested["c1"]
		a.at = a.at.Add(-suggestRetry)
		u2.suggested["c1"] = a
		frame2()
	}
	if got := sentOf[proto.ChatSuggest](b2.FakeBackend); len(got) != 1 {
		t.Fatalf("an answered turn asked %d times", len(got))
	}
}
