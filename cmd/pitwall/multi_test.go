package main

import (
	"errors"
	"net"
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
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(hostState("rs", "rw", "rp"), conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's state", func() bool { st := m.State(); return st.Session("rs") != nil })
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
	m.Send(proto.CloseTab{WorkspaceID: "lw"})
	if c, ok := recv(ld).(proto.CloseTab); !ok || c.WorkspaceID != "lw" {
		t.Fatalf("local got %#v", c)
	}
	// Text that names another host's pane goes by the field before it.
	m.Send(proto.Send{Pane: "lp", Text: "rp"})
	if s, ok := recv(ld).(proto.Send); !ok || s.Pane != "lp" {
		t.Fatalf("local got %#v", s)
	}

	m.Send(proto.SeePane{Pane: "lp"})
	recv(ld)
	m.Send(proto.SessionShow{SessionID: "rs"})
	recv(rd)
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

func TestWindowHosts(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	hosts, closeAll := windowHosts([]config.Host{{Name: "box", Window: true}, {Name: "gpu"}, {Name: "web", SSH: "me@web", Window: true}})
	defer closeAll()
	if len(hosts) != 2 || hosts[0].name != "box" || hosts[1].name != "web" {
		t.Fatalf("hosts %+v", hosts)
	}
}
