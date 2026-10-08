package daemon

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestAttention(t *testing.T) {
	defer func(d time.Duration) { noticeInterval = d }(noticeInterval)
	noticeInterval = 50 * time.Millisecond
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{
			Dir: layout.Horizontal, Children: []*layout.Node{{Pane: "a"}, {Pane: "b"}}}}}, ActiveTab: "t"}},
		Panes: []model.Pane{{ID: "a", WorkspaceID: "w"}, {ID: "b", WorkspaceID: "w"}},
	}
	o := f.options()
	o.NewVT = vt.New
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	of := func(pane string) *model.Activity {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, a := range d.snapshot().Activities {
			if a.PaneID == pane {
				return &a
			}
		}
		return nil
	}
	event := func(pane, state string) {
		time.Sleep(time.Millisecond) // a strictly newer UpdatedAt
		if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: pane, Provider: model.ProviderClaude, Payload: []byte(state)}); err != nil {
			t.Fatal(err)
		}
	}
	waitDetail := func(pane, want string) {
		t.Helper()
		for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
			if a := of(pane); a != nil && a.Detail == want {
				return
			}
		}
		t.Fatalf("pane %s: activity %+v, want detail %q", pane, of(pane), want)
	}

	// An OSC 9 from the emulator becomes an unseen awaiting-input activity.
	e := d.notifyingVT("a")(10, 2, io.Discard)
	e.Write([]byte("\x1b]9;tests ✳ passed\x07"))
	waitDetail("a", "tests ✳ passed")
	if a := of("a"); a.State != model.StateAwaitingInput || !a.Unseen || a.Provider != model.ProviderTerminal {
		t.Fatalf("notice %+v", a)
	}
	d.seePane("a")
	if a := of("a"); a != nil {
		t.Fatalf("seen notice still shows: %+v", a)
	}

	// Within noticeInterval only the latest notification is kept, and it
	// shows once the interval ends.
	d.notice("a", vt.Notification{Body: "one"})
	d.notice("a", vt.Notification{Title: "Aider", Body: "two"})
	if a := of("a"); a != nil {
		t.Fatalf("held-back notice shows early: %+v", a)
	}
	waitDetail("a", "Aider: two")

	// Agent states: needs-you is unseen until seen; working never is.
	event("b", string(model.StateAwaitingInput))
	if a := of("b"); !a.Unseen {
		t.Fatalf("new question seen: %+v", a)
	}
	d.seePane("b")
	if a := of("b"); a == nil || a.Unseen {
		t.Fatalf("after SeePane: %+v", a)
	}
	event("b", string(model.StateWorking))
	if a := of("b"); a.Unseen {
		t.Fatalf("working unseen: %+v", a)
	}
	event("b", string(model.StateCompleted))
	if a := of("b"); !a.Unseen {
		t.Fatalf("finished turn seen: %+v", a)
	}

	// A newer agent state replaces a notification for good.
	time.Sleep(noticeInterval)
	d.notice("b", vt.Notification{Body: "ping"})
	waitDetail("b", "ping")
	event("b", string(model.StateWorking))
	if a := of("b"); a.State != model.StateWorking {
		t.Fatalf("notice outlived a newer state: %+v", a)
	}
	event("b", "clear")
	if a := of("b"); a != nil {
		t.Fatalf("notice came back: %+v", a)
	}
}

// TestBell checks that a BEL raises its pane's attention like an OSC
// notification, at most once per bellInterval, and stays quiet in a pane a
// GUI shows focused, in one whose agent already needs you, and when the
// config turns bells off.
func TestBell(t *testing.T) {
	defer func(b, n time.Duration) { bellInterval, noticeInterval = b, n }(bellInterval, noticeInterval)
	bellInterval, noticeInterval = time.Hour, 0
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{
			Dir: layout.Horizontal, Children: []*layout.Node{{Pane: "a"}, {Pane: "b"}}}}}, ActiveTab: "t"}},
		Panes: []model.Pane{{ID: "a", WorkspaceID: "w"}, {ID: "b", WorkspaceID: "w"}},
	}
	o := f.options()
	o.NewVT = vt.New
	var rings atomic.Int32
	var on atomic.Bool
	on.Store(true)
	o.Bell = func() bool { rings.Add(1); return on.Load() }
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	of := func(pane string) *model.Activity {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, a := range d.snapshot().Activities {
			if a.PaneID == pane {
				return &a
			}
		}
		return nil
	}

	e := d.notifyingVT("a")(10, 2, io.Discard)
	e.Write([]byte("\a\a"))
	for start := time.Now(); of("a") == nil && time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
	}
	if a := of("a"); a == nil || a.Detail != "Bell" || !a.Unseen || a.State != model.StateAwaitingInput {
		t.Fatalf("after a bell: %+v", a)
	}
	e.Write([]byte("\a"))
	if n := rings.Load(); n != 1 {
		t.Fatalf("%d bells reached the daemon within bellInterval, want 1", n)
	}

	d.seePane("a")
	gui := &client{wake: make(chan struct{}, 1), frames: map[string]proto.Frame{}, focus: "a"}
	d.mu.Lock()
	d.clients[gui] = struct{}{}
	d.mu.Unlock()
	if d.bell("a"); of("a") != nil {
		t.Fatalf("a bell in the focused pane showed: %+v", of("a"))
	}
	d.mu.Lock()
	gui.focus = "b"
	d.mu.Unlock()
	if d.bell("a"); of("a") == nil {
		t.Fatal("no attention once the GUI focused another pane")
	}

	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: "b", Provider: model.ProviderClaude, Payload: []byte(model.StateAwaitingInput)}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	gui.focus = ""
	d.mu.Unlock()
	if d.bell("b"); of("b").Detail == "Bell" {
		t.Fatalf("a bell replaced an agent that needs you: %+v", of("b"))
	}

	d.seePane("a")
	on.Store(false)
	if d.bell("a"); of("a") != nil {
		t.Fatalf("bell = off still rang: %+v", of("a"))
	}
}

// TestClipboard checks that OSC 52 writes reach the state in the order
// they arrived, and that their text leaves the state after clipKeep.
func TestClipboard(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.NewVT = vt.New
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	clip := func() model.Clipboard {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.snapshot().Clipboard
	}
	e := d.notifyingVT("a")(10, 2, io.Discard)
	e.Write([]byte("\x1b]52;c;aGk=\x07"))
	for start := time.Now(); clip().Text == "" && time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
	}
	if c := clip(); c != (model.Clipboard{Seq: 1, Text: "hi"}) {
		t.Fatalf("after OSC 52: %+v", c)
	}
	d.setClipboard(3, "three")
	d.setClipboard(2, "two") // arrived before three, landed after it
	if c := clip(); c != (model.Clipboard{Seq: 3, Text: "three"}) {
		t.Fatalf("an older write won: %+v", c)
	}
	d.mu.Lock()
	d.clipAt = d.clipAt.Add(-clipKeep - time.Second)
	d.mu.Unlock()
	if c := clip(); c != (model.Clipboard{Seq: 3}) {
		t.Fatalf("text kept past clipKeep: %+v", c)
	}
}

// TestSeePaneFocusByLevel checks the daemon takes SeePane as a GUI's focus,
// which keeps bells there quiet, only from GUIs of proto.SeeFocusLevel: an
// older GUI names a pane only when its activity is unseen, and stays on it.
func TestSeePaneFocusByLevel(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Serve(ctx, ln)
	for _, level := range []int{0, proto.SeeFocusLevel} {
		gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: level, Kind: "gui", Cwd: t.TempDir()})
		gui.request(proto.SeePane{Pane: "p"})
	}
	// request can return on a state pushed before its Sync's reply.
	var focus []string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		focus = focus[:0]
		d.mu.Lock()
		for c := range d.clients {
			focus = append(focus, c.focus)
		}
		d.mu.Unlock()
		if slices.Sort(focus); slices.Equal(focus, []string{"", "p"}) {
			return
		}
	}
	t.Fatalf("GUI focus %q, want only the new GUI's", focus)
}
