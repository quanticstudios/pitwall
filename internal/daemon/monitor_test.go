package daemon

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/sector/procs"
	"github.com/quanticstudios/sector/sample"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// fakeMonitor counts readings; each reports its number as the host name.
func fakeMonitor(n *atomic.Int64) func() (sample.Snapshot, procs.Table, error) {
	return func() (sample.Snapshot, procs.Table, error) {
		i := n.Add(1)
		return sample.Snapshot{Host: sample.Host{Name: "box", Threads: int(i)}}, procs.Table{Procs: int(i)}, nil
	}
}

// The daemon samples only while a window watches a Monitor tab: the first
// watch starts it, Interval 0 and a disconnect stop it, and a window below
// MonitorLevel never gets a sample.
func TestMonitorWatch(t *testing.T) {
	var n atomic.Int64
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := runWith(t, f, func(o *Options) { o.Monitor = fakeMonitor(&n) })
	defer stop()

	old := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.MonitorLevel - 1, Kind: "gui"})
	old.waitState("old window", func(model.State) bool { return true })
	old.send(proto.MonitorWatch{WorkspaceID: "w", Interval: time.Second})
	old.send(proto.Sync{})
	old.waitState("sync", func(model.State) bool { return true })
	time.Sleep(100 * time.Millisecond)
	if got := n.Load(); got != 0 {
		t.Fatalf("%d readings with no watcher of MonitorLevel", got)
	}

	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui"})
	gui.waitState("state", func(model.State) bool { return true })
	gui.send(proto.MonitorWatch{WorkspaceID: "w", Interval: time.Second})
	s := gui.waitFor("a sample", func(m any) bool { _, ok := m.(proto.MonitorSample); return ok }).(proto.MonitorSample)
	if s.Snapshot.Host.Name != "box" || s.Err != "" {
		t.Fatalf("sample = %+v", s)
	}
	gui.send(proto.MonitorWatch{WorkspaceID: "w"})
	waitUntil(t, "the loop to stop", func() bool { d := n.Load(); time.Sleep(1200 * time.Millisecond); return n.Load() == d })

	gui.send(proto.MonitorWatch{WorkspaceID: "w", Interval: time.Second})
	gui.waitFor("a sample again", func(m any) bool { _, ok := m.(proto.MonitorSample); return ok })
	gui.conn.Close()
	waitUntil(t, "a disconnect to stop it", func() bool { d := n.Load(); time.Sleep(1200 * time.Millisecond); return n.Load() == d })
	select {
	case m, ok := <-old.in:
		if _, isSample := m.(proto.MonitorSample); ok && isSample {
			t.Fatal("a window below MonitorLevel got a sample")
		}
	default:
	}
}

// The shortest interval any watcher asks for wins, at the floor at least.
func TestMonitorShortestInterval(t *testing.T) {
	var n atomic.Int64
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := runWith(t, f, func(o *Options) { o.Monitor = fakeMonitor(&n) })
	defer stop()
	a := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui"})
	b := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui"})
	a.waitState("a", func(model.State) bool { return true })
	b.waitState("b", func(model.State) bool { return true })
	a.send(proto.MonitorWatch{WorkspaceID: "w", Interval: 5 * time.Second})
	a.waitFor("a's sample", func(m any) bool { _, ok := m.(proto.MonitorSample); return ok })
	b.send(proto.MonitorWatch{WorkspaceID: "w", Interval: time.Millisecond}) // clamped to monitorFloor
	start := n.Load()
	time.Sleep(3*monitorFloor + monitorFloor/2)
	if got := n.Load() - start; got < 3 {
		t.Fatalf("%d readings in 3.5 floors; want the shortest interval", got)
	}
}

// A slow window holds one unsent sample, the newest, and nothing queues.
// An error reaches each window once.
func TestMonitorSlot(t *testing.T) {
	d := fromState(Options{}, model.State{})
	c := &client{wake: make(chan struct{}, 1)}
	d.mon.watchers, d.mon.errSent = map[watchKey]time.Duration{{c, "w"}: time.Second}, map[*client]string{}
	for i := range 3 {
		d.sendMonitor(proto.MonitorSample{Table: procs.Table{Procs: i}})
	}
	if c.sample == nil || c.sample.Table.Procs != 2 || len(c.msgs) != 0 {
		t.Fatalf("slot = %+v, %d queued; want the third sample alone", c.sample, len(c.msgs))
	}
	c.sample = nil
	d.sendMonitor(proto.MonitorSample{Err: "unsupported"})
	if c.sample == nil || c.sample.Err != "unsupported" {
		t.Fatalf("slot = %+v; want the error", c.sample)
	}
	c.sample = nil
	d.sendMonitor(proto.MonitorSample{Err: "unsupported"})
	if c.sample != nil {
		t.Fatal("the same error was sent twice")
	}
}

// readMonitor maps an agent group to the pane whose session its agent is
// in, and leaves an agent outside pitwall and other groups out.
func TestMonitorTabs(t *testing.T) {
	old := sessionOf
	sessionOf = func(pid int) int { return map[int]int{10: 100, 20: 999}[pid] }
	t.Cleanup(func() { sessionOf = old })
	table := procs.Table{Groups: []procs.Group{
		{Key: "claude 10", Kind: procs.Agent, PID: 10},
		{Key: "codex 20", Kind: procs.Agent, PID: 20}, // started outside pitwall
		{Key: "app", Kind: procs.App},
	}}
	d := fromState(Options{Monitor: func() (sample.Snapshot, procs.Table, error) { return sample.Snapshot{}, table, nil }}, model.State{})
	d.live.det = map[string]*detected{"p1": {sid: 100}, "p2": {sid: 200}}
	s := d.readMonitor()
	if len(s.Tabs) != 1 || s.Tabs["claude 10"] != "p1" {
		t.Fatalf("Tabs = %v; want claude 10 in p1 alone", s.Tabs)
	}

	d.o.Monitor = func() (sample.Snapshot, procs.Table, error) {
		return sample.Snapshot{}, procs.Table{}, errors.ErrUnsupported
	}
	if s := d.readMonitor(); s.Err == "" || s.Tabs != nil {
		t.Fatalf("a failed reading = %+v; want Err", s)
	}
}

// A session gets one Monitor tab, with no pane; asking again shows it. A
// window below MonitorTabLevel never sees it (forLevel), and it survives a
// restart.
func TestMonitorTab(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui"})
	st := gui.waitState("first tab", func(s model.State) bool { return len(s.Workspaces) == 1 })
	session := st.Workspaces[0].SessionID
	gui.send(proto.SessionShow{SessionID: session})

	gui.send(proto.NewSession{SessionID: session, Monitor: true})
	st = gui.waitState("a Monitor tab", func(s model.State) bool { return len(s.Workspaces) == 2 })
	mon := st.Workspaces[1]
	if !mon.Monitor || mon.Label != "Monitor" || len(mon.Tabs) != 0 || len(st.Panes) != 1 {
		t.Fatalf("Monitor tab %+v with %d panes", mon, len(st.Panes))
	}
	gui.send(proto.NewSession{SessionID: session, Monitor: true})
	fs := gui.waitFor("focus on it", func(m any) bool { _, ok := m.(proto.FocusSession); return ok }).(proto.FocusSession)
	if fs.WorkspaceID != mon.ID {
		t.Fatalf("focus %+v; want the Monitor tab %s", fs, mon.ID)
	}
	gui.send(proto.Sync{})
	if st = gui.waitState("sync", func(model.State) bool { return true }); len(st.Workspaces) != 2 {
		t.Fatalf("%d tabs after asking twice; want one Monitor tab", len(st.Workspaces))
	}
	gui.send(proto.OpenPane{WorkspaceID: mon.ID})
	for m := range gui.in {
		if e, ok := m.(proto.Error); ok {
			if e.Message != "a Monitor tab has no panes" {
				t.Fatalf("OpenPane: %q", e.Message)
			}
			break
		}
	}

	ost := forLevel(st, proto.MonitorTabLevel-1)
	for _, w := range ost.Workspaces {
		if w.Monitor || w.ID == mon.ID {
			t.Fatalf("a window below MonitorTabLevel sees the Monitor tab: %+v", w)
		}
	}
	for _, s := range ost.Sessions {
		if slices.Contains(s.Order, mon.ID) {
			t.Fatalf("a window below MonitorTabLevel sees the Monitor tab in %v", s.Order)
		}
	}
	stop()

	d, sock, stop := runDaemon(t, f, func(*Options) {})
	defer stop()
	gui = dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui"})
	st = gui.waitState("restored", func(s model.State) bool { return len(s.Workspaces) == 2 })
	if w := st.Workspaces[1]; !w.Monitor || w.ID != mon.ID {
		t.Fatalf("after a restart: %+v; want the Monitor tab back", w)
	}
	if ws := (chatHub{d}).Workspaces(); slices.ContainsFunc(ws, func(w model.Workspace) bool { return w.Monitor }) {
		t.Fatal("the web protocol lists the Monitor tab")
	}
	// Closing the tab stops its watches: the window may not reach the
	// daemon to say so.
	c := &client{wake: make(chan struct{}, 1)}
	d.watchMonitor(context.Background(), c, mon.ID, time.Hour)
	gui.send(proto.KillSession{WorkspaceID: mon.ID})
	gui.waitState("closed", func(s model.State) bool { return len(s.Workspaces) == 1 })
	d.mon.mu.Lock()
	n := len(d.mon.watchers)
	d.mon.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d watches left after the Monitor tab closed", n)
	}
}
