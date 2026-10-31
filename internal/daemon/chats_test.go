package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/fake"
)

// stubProvider starts stubDrivers. Each Send starts a turn and emits
// turn.started from inside the call, as a driver's reader may; the test
// emits the rest.
type stubProvider struct {
	mu      sync.Mutex
	drivers []*stubDriver
}

func (p *stubProvider) Start(_ context.Context, cfg session.Config, _ session.Origin, emit func(session.Event)) (session.Driver, error) {
	d := &stubDriver{emit: emit, steer: session.ErrUnsupported, cfg: cfg}
	p.mu.Lock()
	p.drivers = append(p.drivers, d)
	p.mu.Unlock()
	return d, nil
}

func (p *stubProvider) driver(t *testing.T) *stubDriver {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.drivers) != 1 {
		t.Fatalf("%d drivers started, want 1", len(p.drivers))
	}
	return p.drivers[0]
}

type stubDriver struct {
	mu     sync.Mutex
	cfg    session.Config // what Start got
	emit   func(session.Event)
	steer  error
	turn   string
	turns  int
	calls  []string
	msgs   []session.Message // what Send got
	closed bool
}

func (s *stubDriver) call(c string) {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
}

func (s *stubDriver) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *stubDriver) event(e session.Event) {
	e.CreatedAt = session.Stamp(time.Now())
	s.emit(e)
}

func (s *stubDriver) Send(_ context.Context, m session.Message) (string, error) {
	s.mu.Lock()
	if s.turn != "" {
		s.mu.Unlock()
		return "", session.ErrBusy
	}
	s.turns++
	s.turn = fmt.Sprint("turn-", s.turns)
	turn := s.turn
	s.calls = append(s.calls, "send "+m.Text)
	s.msgs = append(s.msgs, m)
	s.mu.Unlock()
	s.event(session.Event{Type: session.EventTurnStarted, TurnID: turn})
	return turn, nil
}

// end finishes the running turn in state.
func (s *stubDriver) end(state string) {
	s.mu.Lock()
	turn := s.turn
	s.turn = ""
	s.mu.Unlock()
	s.event(session.Event{Type: session.EventTurnCompleted, TurnID: turn, State: state})
}

func (s *stubDriver) Steer(context.Context, session.Message) error { return s.steer }
func (s *stubDriver) Interrupt(context.Context) error {
	s.call("interrupt")
	s.end(session.TurnInterrupted)
	return nil
}
func (s *stubDriver) Approve(_ context.Context, id string, d session.Decision) error {
	s.call(fmt.Sprintf("approve %s %v", id, d.Allow))
	return nil
}
func (s *stubDriver) Answer(_ context.Context, id string, a map[string][]string) error {
	s.call(fmt.Sprintf("answer %s %v", id, a))
	return nil
}
func (s *stubDriver) Set(_ context.Context, x session.Settings) error {
	s.call("set " + x.Model + x.Mode)
	return nil
}
func (s *stubDriver) Close(context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

// chatCall sends m and returns the reply to it.
func chatCall[T any](c *testClient, m any) T {
	c.t.Helper()
	c.send(m)
	return c.waitFor(fmt.Sprintf("reply to %T", m), func(r any) bool { _, ok := r.(T); return ok }).(T)
}

func chatOf(s model.State) model.Chat {
	if len(s.Chats) != 1 {
		return model.Chat{}
	}
	return s.Chats[0]
}

// TestChatFlow runs a chat from ChatNew to ChatArchive against a stub
// driver, with a GUI of this Level and one from before chats connected.
func TestChatFlow(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	prov := &stubProvider{}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Providers = map[model.Provider]session.Provider{model.ProviderCodex: prov}
	})
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	old := dialIn(t, sock, "gui", t.TempDir()) // Level 0
	gui.waitState("first tab", func(s model.State) bool { return len(s.Workspaces) == 1 })

	if r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir()}); r.Code != "unsupported" {
		t.Fatalf("claude without a provider: %+v", r)
	}
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderCodex, Dir: t.TempDir(), Prompt: "Fix the build\nplease", Model: "m1"})
	if r.Code != "" || r.Chat == "" || r.Turn != "turn-1" {
		t.Fatalf("new: %+v", r)
	}
	id, drv := r.Chat, prov.driver(t)
	st := waitChat(gui, "chat running", func(s model.State) bool { return chatOf(s).State == session.StateRunning })
	ch := chatOf(st)
	ws := st.Workspaces[slices.IndexFunc(st.Workspaces, func(w model.Workspace) bool { return w.Chat == id })]
	if ch.WorkspaceID != ws.ID || ch.Title != "Fix the build" || ws.Label != "Fix the build" || ch.Model != "m1" || len(ws.Tabs) != 0 {
		t.Fatalf("chat %+v, tab %+v", ch, ws)
	}
	if !slices.Contains(st.Session(ws.SessionID).Order, ws.ID) {
		t.Fatalf("chat tab not in its session's order: %v", st.Sessions)
	}
	if r := chatCall[proto.ChatResult](gui, proto.ChatSend{Chat: id, Text: "and the tests"}); r.Code != "" || r.Turn != "" || r.Message != "queued" {
		t.Fatalf("send while running: %+v", r)
	}

	drv.event(session.Event{Type: session.EventContent, ItemID: "a1", Kind: session.KindAssistantText, Text: "On it."})
	drv.event(session.Event{Type: session.EventRequestOpened, RequestID: "r1", RequestType: session.RequestCommand, Detail: "make"})
	waitChat(gui, "approval pending", func(s model.State) bool { c := chatOf(s); return c.Pending == 1 && c.Queued == 1 })
	if r := chatCall[proto.ChatResult](gui, proto.ChatApprove{Chat: id, Decision: "allow"}); r.Code != "" {
		t.Fatalf("approve: %+v", r)
	}
	if r := chatCall[proto.ChatResult](gui, proto.ChatApprove{Chat: id, Request: "r1", Decision: "deny"}); r.Code != "conflict" {
		t.Fatalf("second answer: %+v", r)
	}

	drv.end(session.TurnCompleted) // the queued message starts the next turn
	waitChat(gui, "queued turn", func(s model.State) bool { c := chatOf(s); return c.Turns == 2 && c.Queued == 0 })
	if got, want := drv.got(), []string{"send Fix the build\nplease", "approve r1 true", "send and the tests"}; !slices.Equal(got, want) {
		t.Fatalf("driver calls %q, want %q", got, want)
	}

	// Interrupt holds the queue until the next send.
	chatCall[proto.ChatResult](gui, proto.ChatSend{Chat: id, Text: "later", How: "steer"})
	if r := chatCall[proto.ChatResult](gui, proto.ChatInterrupt{Chat: id}); r.Code != "" {
		t.Fatalf("interrupt: %+v", r)
	}
	st = waitChat(gui, "interrupted", func(s model.State) bool { return chatOf(s).State == session.StateError })
	if c := chatOf(st); c.Queued != 1 || c.Turns != 2 {
		t.Fatalf("after interrupt: %+v", c)
	}

	if r := chatCall[proto.ChatResult](gui, proto.ChatSet{Chat: id, Model: "m2"}); r.Code != "" {
		t.Fatalf("set: %+v", r)
	}
	waitChat(gui, "model set", func(s model.State) bool { return chatOf(s).Model == "m2" })

	full := chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: id})
	var snap session.Snapshot
	if err := json.Unmarshal(full.Snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, it := range snap.Content {
		texts = append(texts, string(it.Kind)+": "+it.Text)
	}
	if want := []string{"user-prompt: Fix the build\nplease", "assistant-text: On it.", "user-prompt: and the tests"}; !slices.Equal(texts, want) || snap.Model == nil || *snap.Model != "m2" {
		t.Fatalf("transcript %q, model %v", texts, snap.Model)
	}
	part := chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: id, After: full.Seq - 2})
	if part.Snapshot != nil || len(part.Deltas) != 2 || part.Seq != full.Seq-2 {
		t.Fatalf("read after %d: %+v", full.Seq-2, part)
	}
	var last session.Delta
	if err := json.Unmarshal(part.Deltas[1], &last); err != nil || last.Seq != full.Seq {
		t.Fatalf("last delta %s: %v", part.Deltas[1], err)
	}

	// The old GUI never saw the chat tab.
	old.send(proto.Sync{})
	ost := old.waitState("old sync", func(model.State) bool { return true })
	for _, w := range ost.Workspaces {
		if w.Chat != "" || w.ID == ws.ID {
			t.Fatalf("old GUI got the chat tab %+v", w)
		}
	}
	for _, s := range ost.Sessions {
		if slices.Contains(s.Order, ws.ID) {
			t.Fatalf("old GUI got the chat tab in its order: %v", s.Order)
		}
	}

	gui.send(proto.OpenPane{WorkspaceID: ws.ID})
	if e := nextError(t, gui); !strings.Contains(e, "chat") {
		t.Fatalf("open a pane in a chat tab: %q", e)
	}

	gui.send(proto.SetTabOptions{WorkspaceID: ws.ID, Pinned: true})
	waitChat(gui, "pinned", func(s model.State) bool {
		return slices.ContainsFunc(s.Workspaces, func(w model.Workspace) bool { return w.Pinned })
	})
	if r := chatCall[proto.ChatResult](gui, proto.ChatArchive{Chat: id}); r.Code != "conflict" {
		t.Fatalf("archive a pinned tab: %+v", r)
	}
	gui.send(proto.SetTabOptions{WorkspaceID: ws.ID, Pinned: false})
	if r := chatCall[proto.ChatResult](gui, proto.ChatArchive{Chat: id}); r.Code != "" {
		t.Fatalf("archive: %+v", r)
	}
	waitChat(gui, "archived", func(s model.State) bool {
		return len(s.Chats) == 0 && !slices.ContainsFunc(s.Workspaces, func(w model.Workspace) bool { return w.ID == ws.ID })
	})
	waitUntil(t, "driver closed", func() bool { drv.mu.Lock(); defer drv.mu.Unlock(); return drv.closed })
	if r := chatCall[proto.ChatResult](gui, proto.ChatSend{Chat: id, Text: "hi"}); r.Code != "not-found" {
		t.Fatalf("send to an archived chat: %+v", r)
	}
}

// TestChatPlan: approving a plan leaves plan mode, refining it does not,
// and a request the provider withdraws says so in the transcript.
func TestChatPlan(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	prov := &stubProvider{}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Providers = map[model.Provider]session.Provider{model.ProviderCodex: prov}
	})
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	gui.waitState("first tab", func(s model.State) bool { return len(s.Workspaces) == 1 })
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderCodex, Dir: t.TempDir(), Prompt: "Plan it", Mode: "plan"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	id, drv := r.Chat, prov.driver(t)

	drv.event(session.Event{Type: session.EventRequestOpened, RequestID: "p1", RequestType: session.RequestPlan, Detail: "1. Do it"})
	waitChat(gui, "plan pending", func(s model.State) bool { return chatOf(s).Pending == 1 })
	chatCall[proto.ChatResult](gui, proto.ChatApprove{Chat: id, Decision: "deny", Reason: "Smaller"})
	drv.event(session.Event{Type: session.EventRequestOpened, RequestID: "w1", RequestType: session.RequestCommand, Detail: "make"})
	drv.event(session.Event{Type: session.EventRequestResolved, RequestID: "w1"})
	drv.event(session.Event{Type: session.EventRequestOpened, RequestID: "p2", RequestType: session.RequestPlan, Detail: "1. Do less"})
	waitChat(gui, "second plan pending", func(s model.State) bool { return chatOf(s).Pending == 1 })
	if r := chatCall[proto.ChatResult](gui, proto.ChatApprove{Chat: id, Decision: "allow"}); r.Code != "" {
		t.Fatalf("implement: %+v", r)
	}
	waitChat(gui, "out of plan mode", func(s model.State) bool { return chatOf(s).Mode == "default" })
	if got, want := drv.got(), []string{"send Plan it", "approve p1 false", "approve p2 true", "set default"}; !slices.Equal(got, want) {
		t.Fatalf("driver calls %q, want %q", got, want)
	}
	var snap session.Snapshot
	json.Unmarshal(chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: id}).Snapshot, &snap)
	if snap.PermissionMode != "default" || !slices.ContainsFunc(snap.Content, func(it session.Item) bool {
		return it.Kind == session.KindSystem && it.Text == "Codex withdrew this request"
	}) {
		t.Fatalf("mode %q, transcript %+v", snap.PermissionMode, snap.Content)
	}
}

// waitChat syncs until c's state passes ok: a push that came before a
// reply was read past.
func waitChat(c *testClient, what string, ok func(model.State) bool) model.State {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.send(proto.Sync{})
		if s := c.waitState(what, func(model.State) bool { return true }); ok(s) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.t.Fatalf("timed out waiting for %s", what)
	return model.State{}
}

// nextError is the next Error c gets.
func nextError(t *testing.T, c *testClient) string {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m := <-c.in:
			if e, ok := m.(proto.Error); ok {
				return e.Message
			}
		case <-timeout:
			t.Fatal("no error")
		}
	}
}

func TestForLevel(t *testing.T) {
	s := model.State{
		Sessions:   []model.Session{{ID: "s", Order: []string{"w", "c"}}},
		Workspaces: []model.Workspace{{ID: "w"}, {ID: "c", Chat: "chat"}},
		Activities: []model.Activity{{PaneID: "p"}, {PaneID: "chat"}},
		Chats:      []model.Chat{{ID: "chat"}},
	}
	if got := forLevel(s, proto.ChatLevel); len(got.Workspaces) != 2 || len(got.Activities) != 2 {
		t.Fatalf("at ChatLevel: %+v", got)
	}
	got := forLevel(s, proto.ChatLevel-1)
	if len(got.Workspaces) != 1 || len(got.Activities) != 1 || !slices.Equal(got.Sessions[0].Order, []string{"w"}) {
		t.Fatalf("below ChatLevel: %+v", got)
	}
	if len(s.Workspaces) != 2 || len(s.Sessions[0].Order) != 2 {
		t.Fatalf("forLevel changed its argument: %+v", s)
	}
}

func TestPendingID(t *testing.T) {
	for _, c := range []struct {
		ids       []string
		want, got string
		code      string
	}{
		{[]string{"a"}, "", "a", ""},
		{[]string{"a", "b"}, "b", "b", ""},
		{[]string{"a", "b"}, "", "", "invalid"},
		{nil, "", "", "conflict"},
		{[]string{"a"}, "x", "", "conflict"},
	} {
		got, err := pendingID(c.ids, c.want, "approval")
		if got != c.got || chatCode(err) != c.code {
			t.Errorf("pendingID(%v, %q) = %q, %v", c.ids, c.want, got, err)
		}
	}
}

// TestChatFollow follows a fake Claude's chat as the GUI does, reading
// the deltas after the last seq it has until the turn ends, and ends with
// the snapshot a fresh read gives.
func TestChatFollow(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: fake.New(fake.Options{})}
	})
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir(), Prompt: "tools"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	var snap *session.Snapshot
	var seq int64
	reads := 0
	for deadline := time.Now().Add(10 * time.Second); ; {
		d := chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: r.Chat, After: seq})
		reads++
		if d.Snapshot != nil {
			if seq != 0 {
				t.Fatalf("read after %d: a snapshot, want deltas", seq)
			}
			snap = new(session.Snapshot)
			if err := json.Unmarshal(d.Snapshot, snap); err != nil {
				t.Fatal(err)
			}
		}
		seq = d.Seq
		for _, raw := range d.Deltas {
			var dl session.Delta
			if err := json.Unmarshal(raw, &dl); err != nil {
				t.Fatal(err)
			}
			n, err := session.Apply(snap, seq, dl)
			if err != nil {
				t.Fatalf("delta %d after %d: %v", dl.Seq, seq, err)
			}
			snap, seq = &n, dl.Seq
		}
		if len(snap.Turns) == 1 && snap.Turns[0].State == session.TurnCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn never finished: %s running %v %+v", snap.SessionState, snap.Running, snap.Turns)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reads < 2 {
		t.Fatalf("the turn ended in %d read; nothing was followed", reads)
	}
	tools := 0
	for _, it := range snap.Content {
		if it.Kind == session.KindToolCall {
			tools++
		}
	}
	if tools < 3 {
		t.Fatalf("%d tool calls followed, want the script's 3", tools)
	}
	fresh := chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: r.Chat})
	got, _ := json.Marshal(snap)
	if string(got) != string(fresh.Snapshot) || fresh.Seq != seq {
		t.Fatalf("followed (seq %d):\n%s\nfresh read (seq %d):\n%s", seq, got, fresh.Seq, fresh.Snapshot)
	}
}

// TestChatApproveFake answers a fake Claude's approval with the message the
// GUI's Allow button sends, and the turn finishes.
func TestChatApproveFake(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: fake.New(fake.Options{})}
	})
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: model.ProviderClaude, Dir: t.TempDir(), Prompt: "approval"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	read := func(what string, ok func(session.Snapshot) bool) session.Snapshot {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			var s session.Snapshot
			if err := json.Unmarshal(chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: r.Chat}).Snapshot, &s); err != nil {
				t.Fatal(err)
			}
			if ok(s) {
				return s
			}
		}
		t.Fatalf("timed out waiting for %s", what)
		return session.Snapshot{}
	}
	s := read("the approval", func(s session.Snapshot) bool { return len(s.PendingApprovals) == 1 })
	a := proto.ChatApprove{Chat: r.Chat, Request: s.PendingApprovals[0].ID, Decision: "allow", By: "gui"}
	if res := chatCall[proto.ChatResult](gui, a); res.Code != "" {
		t.Fatalf("approve: %+v", res)
	}
	read("the turn to end", func(s session.Snapshot) bool {
		return len(s.PendingApprovals) == 0 && len(s.Turns) == 1 && s.Turns[0].State == session.TurnCompleted
	})
}

func TestChatQueueRefusedSteerPreservesQueue(t *testing.T) {
	c := &chat{
		snap: session.Snapshot{Turns: []session.TurnSummary{{State: session.TurnRunning}}},
		cfg:  session.Config{Held: true, Queue: []session.QueuedMessage{{ID: "q1", Text: "later"}, {ID: "q2", Text: "last"}}},
		drv:  &stubDriver{steer: session.ErrClosed},
	}
	before := slices.Clone(c.cfg.Queue)
	_, _, err := (&Daemon{}).chatQueue(context.Background(), c, proto.ChatQueue{Action: "steer", ID: "q1"})
	if err != session.ErrClosed || !reflect.DeepEqual(c.cfg.Queue, before) || !c.cfg.Held {
		t.Fatalf("refused steer: %v queue %+v held %v", err, c.cfg.Queue, c.cfg.Held)
	}
}
