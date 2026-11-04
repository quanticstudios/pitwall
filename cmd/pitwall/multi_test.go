package main

import (
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// hostState is a daemon's state with one session, tab and pane.
func hostState(session, tab, pane string) model.State {
	return model.State{
		Version:    1,
		Sessions:   []model.Session{{ID: session, Name: "main"}},
		Workspaces: []model.Workspace{{ID: tab, SessionID: session, Tabs: []model.Tab{{ID: tab + "t"}}, ActiveTab: tab + "t"}},
		Panes:      []model.Pane{{ID: pane, WorkspaceID: tab}},
	}
}

// end is a fake daemon's end of a window's connection.
type end struct {
	nc net.Conn
	c  *proto.Conn
}

// fakeDaemon is a dial func whose daemon has st; each dial hands the
// daemon's end of the connection to conns.
func fakeDaemon(st model.State, conns chan<- end) func(string, bool) (*proto.Conn, proto.StateMsg, error) {
	return func(string, bool) (*proto.Conn, proto.StateMsg, error) {
		gui, daemon := net.Pipe()
		conns <- end{daemon, proto.NewConn(daemon)}
		return proto.NewConn(gui), proto.StateMsg{State: st, Level: proto.Level}, nil
	}
}

// localBackend is a connected backend for this machine's daemon, and
// that daemon's end.
func localBackend(t *testing.T, st model.State) (*backend, end) {
	gui, daemon := net.Pipe()
	t.Cleanup(func() { gui.Close(); daemon.Close() })
	b := newBackend(proto.NewConn(gui), st.Sessions[0].ID)
	b.state, b.link.Level = st, proto.Level
	go b.recvLoop()
	go b.sendLoop()
	return b, end{daemon, proto.NewConn(daemon)}
}

// recv is the next message the daemon end c reads, nil after a second.
func recv(e end) any {
	e.nc.SetReadDeadline(time.Now().Add(time.Second))
	m, err := e.c.Recv()
	if err != nil {
		return nil
	}
	return m
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestMultiRoutes checks that a message goes to the daemon owning the id
// it names, never another, and one naming none to the host of the shown
// session.
func TestMultiRoutes(t *testing.T) {
	local, ld := localBackend(t, hostState("ls", "lw", "lp"))
	conns := make(chan end, 1)
	box := hostState("rs", "rw", "rp")
	box.Closed = []model.ClosedTab{{ID: "rc", Tab: model.Workspace{ID: "old", SessionID: "gone"}}}
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(box, conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's state", func() bool { st := m.State(); return st.Session("rs") != nil })
	if st := m.State(); len(st.Closed) != 1 || m.SessionHost("rc") != "box" {
		t.Fatalf("closed tabs %+v on %q", st.Closed, m.SessionHost("rc"))
	}
	if h := m.Hosts(); len(h) != 2 || !h[1].Up || h[1].Name != "box" || h[1].Sessions[0] != "rs" {
		t.Fatalf("hosts %+v", h)
	}
	if m.SessionHost("rs") != "box" || m.SessionHost("ls") != "" {
		t.Fatalf("session hosts %q %q", m.SessionHost("rs"), m.SessionHost("ls"))
	}

	m.Send(proto.Input{Pane: "rp", Data: []byte("x")})
	if in, ok := recv(rd).(proto.Input); !ok || in.Pane != "rp" {
		t.Fatalf("box got %#v", in)
	}
	m.Send(proto.OpenPopup{WorkspaceID: "rw", Cmd: []string{"less"}, Dir: "/remote/folder", Path: "lw"})
	if p, ok := recv(rd).(proto.OpenPopup); !ok || p.Dir != "/remote/folder" || p.Path != "lw" {
		t.Fatalf("host popup went to the wrong daemon: %#v", p)
	}
	// A fork names its source pane before user-chosen names from another host.
	m.Send(proto.ForkPane{Pane: "rp", Worktree: "lp", Name: "lw"})
	if f, ok := recv(rd).(proto.ForkPane); !ok || f.Pane != "rp" {
		t.Fatalf("box got %#v for fork", f)
	}
	m.Send(proto.CloseTab{WorkspaceID: "lw"})
	if c, ok := recv(ld).(proto.CloseTab); !ok || c.WorkspaceID != "lw" {
		t.Fatalf("local got %#v", c)
	}
	// Text that names another host's pane goes by the field before it.
	m.Send(proto.Send{Pane: "lp", Text: "rp"})
	if s, ok := recv(ld).(proto.Send); !ok || s.Pane != "lp" {
		t.Fatalf("local got %#v", s)
	}

	// A closed tab reopens on the host that recorded it.
	m.Send(proto.ReopenTab{ID: "rc"})
	if r, ok := recv(rd).(proto.ReopenTab); !ok || r.ID != "rc" {
		t.Fatalf("box got %#v", r)
	}

	m.Send(proto.SeePane{Pane: "lp"})
	recv(ld)
	m.Send(proto.SessionShow{SessionID: "rs"})
	recv(rd)
	// Repo command text must not route orphan removal to a different host.
	m.Send(proto.WorktreeQuery{Orphans: true})
	if _, ok := recv(rd).(proto.WorktreeQuery); !ok {
		t.Fatal("cleanup query did not reach the host")
	}
	m.Send(proto.DeleteWorkspace{WorkspaceID: "rw", Teardown: "lp"})
	if got, ok := recv(rd).(proto.DeleteWorkspace); !ok || got.Teardown != "lp" {
		t.Fatalf("remote teardown: %#v", got)
	}
	m.Send(proto.DeleteWorktree{Root: "/repo", Path: "/repo/.worktrees/feature", Teardown: "lp"})
	if got, ok := recv(rd).(proto.DeleteWorktree); !ok || got.Teardown != "lp" {
		t.Fatalf("remote orphan teardown: %#v", got)
	}
	m.Send(proto.SessionNew{Name: "new"}) // no id: the shown session's host
	if n, ok := recv(rd).(proto.SessionNew); !ok || n.Name != "new" {
		t.Fatalf("box got %#v for a new session", n)
	}
	m.Send(proto.SeePane{Pane: "rp"})
	if s, ok := recv(ld).(proto.SeePane); !ok || s.Pane != "" {
		t.Fatalf("local was not told its pane no longer shows: %#v", s)
	}
	if s, ok := recv(rd).(proto.SeePane); !ok || s.Pane != "rp" {
		t.Fatalf("box got %#v", s)
	}
	if got := recv(ld); got != nil {
		t.Fatalf("local got %#v as well", got)
	}
}

// TestMultiHostDown checks that a host whose dial fails, and one whose dial
// never returns, leave this machine's daemon usable at once, show as down,
// and do not cover the window.
func TestMultiHostDown(t *testing.T) {
	local, ld := localBackend(t, hostState("ls", "lw", "lp"))
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	tries := make(chan struct{}, 100)
	start := time.Now()
	m := newMulti(local, []hostDial{
		{name: "gone", dial: func(string, bool) (*proto.Conn, proto.StateMsg, error) {
			tries <- struct{}{}
			return nil, proto.StateMsg{}, errors.New("cannot reach gone over ssh: no route to host")
		}},
		{name: "stuck", dial: func(string, bool) (*proto.Conn, proto.StateMsg, error) {
			<-stall
			return nil, proto.StateMsg{}, errors.New("stopped")
		}},
	})
	m.Send(proto.Input{Pane: "lp", Data: []byte("x")})
	if in, ok := recv(ld).(proto.Input); !ok || in.Pane != "lp" {
		t.Fatalf("local got %#v", in)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("this machine waited %v for the other hosts", d)
	}
	<-tries
	<-tries // it tries again, with backoff
	waitFor(t, "gone's note", func() bool { return strings.Contains(m.Hosts()[1].Note, "no route to host") })
	h := m.Hosts()
	if h[1].Up || h[2].Up || h[2].Note != "Connecting…" || !h[0].Up {
		t.Fatalf("hosts %+v", h)
	}
	if l := m.Link(); l.State != app.LinkUp {
		t.Fatalf("link %+v covers the window", l)
	}
	if st := m.State(); len(st.Sessions) != 1 || st.Sessions[0].ID != "ls" {
		t.Fatalf("sessions %+v", st.Sessions)
	}
}

// pitwall attach run on a host focuses its tab in the window, but the
// host connecting does not pick the window's session.
func TestMultiForwardsHostFocus(t *testing.T) {
	local, _ := localBackend(t, hostState("ls", "lw", "lp"))
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(hostState("rs", "rw", "rp"), conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's state", func() bool { st := m.State(); return st.Session("rs") != nil })
	select {
	case f := <-m.Focus():
		if f.SessionID == "rs" {
			t.Fatalf("connecting took the window over: %+v", f)
		}
	case <-time.After(100 * time.Millisecond):
	}
	want := proto.FocusSession{WorkspaceID: "rw", SessionID: "rs"}
	rd.c.Send(want)
	select {
	case f := <-m.Focus():
		if f != want {
			t.Fatalf("focus %+v, want %+v", f, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("box's pitwall attach never reached the window")
	}
}

// A host's clipboard write gives the window that host's text, even while
// another host's earlier write is still in its state.
func TestMultiClipboardFollowsWriter(t *testing.T) {
	lst := hostState("ls", "lw", "lp")
	lst.Clipboard = model.Clipboard{Seq: 5, Text: "local"}
	local, _ := localBackend(t, lst)
	rst := hostState("rs", "rw", "rp")
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(rst, conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's state", func() bool { st := m.State(); return st.Session("rs") != nil })
	before := m.State().Clipboard
	if before.Text != "local" {
		t.Fatalf("clipboard %+v before box writes", before)
	}
	rst.Version, rst.Clipboard = 2, model.Clipboard{Seq: 1, Text: "remote"}
	rd.c.Send(proto.StateMsg{State: rst, Level: proto.Level})
	waitFor(t, "box's write", func() bool { return m.State().Clipboard.Seq != before.Seq })
	if c := m.State().Clipboard; c.Text != "remote" {
		t.Fatalf("clipboard %+v after box wrote", c)
	}
}

func TestWindowHosts(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	hosts, closeAll := windowHosts([]config.Host{{Name: "box", Window: true}, {Name: "gpu"}, {Name: "web", SSH: "me@web", Window: true}})
	defer closeAll()
	if runtime.GOOS == "windows" { // its ssh cannot share a connection
		if len(hosts) != 0 {
			t.Fatalf("hosts on Windows %+v", hosts)
		}
		return
	}
	if len(hosts) != 2 || hosts[0].name != "box" || hosts[1].name != "web" {
		t.Fatalf("hosts %+v", hosts)
	}
}
