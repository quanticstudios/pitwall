package main

import (
	"errors"
	"net"
	"runtime"
	"slices"
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
	localState := hostState("ls", "lw", "lp")
	localState.DevProxyPort = 7778
	local, ld := localBackend(t, localState)
	conns := make(chan end, 1)
	box := hostState("rs", "rw", "rp")
	box.DevProxyPort = 8888
	box.Closed = []model.ClosedTab{{ID: "rc", Tab: model.Workspace{ID: "old", SessionID: "gone"}}}
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(box, conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's state", func() bool { st := m.State(); return st.Session("rs") != nil })
	if st := m.State(); len(st.Closed) != 1 || m.SessionHost("rc") != "box" {
		t.Fatalf("closed tabs %+v on %q", st.Closed, m.SessionHost("rc"))
	}
	if got := m.State().DevProxyPort; got != localState.DevProxyPort {
		t.Fatalf("local proxy port %d, want %d", got, localState.DevProxyPort)
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
	if h, ok := m.ClipboardHost(before.Seq); before.Text != "local" || h != "" || !ok {
		t.Fatalf("clipboard %+v before box writes", before)
	}
	rst.Version, rst.Clipboard = 2, model.Clipboard{Seq: 1, Text: "remote"}
	rd.c.Send(proto.StateMsg{State: rst, Level: proto.Level})
	waitFor(t, "box's write", func() bool { return m.State().Clipboard.Seq != before.Seq })
	c := m.State().Clipboard
	if h, ok := m.ClipboardHost(c.Seq); c.Text != "remote" || h != "box" || !ok {
		t.Fatalf("clipboard %+v from %q after box wrote", c, h)
	}
	if _, ok := m.ClipboardHost(before.Seq); ok {
		t.Fatal("the replaced write still has a host")
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

// next is the next message e reads that is not a latency ping.
func next(e end) any {
	for {
		m := recv(e)
		if _, ping := m.(proto.Sync); !ping {
			return m
		}
	}
}

// TestMultiShared checks a shared session: box's part folds into the
// session here, in the order this machine keeps; new tabs open on the
// host asked for; renames and moves reach every part; a host with no part
// gets one, which joins once it shows up; and a tab closed while its host
// is down is forgotten here, not sent.
func TestMultiShared(t *testing.T) {
	ls := hostState("ls", "lw", "lp")
	ls.Sessions[0].Remotes = []model.RemoteSession{{Host: "box", Session: "rs"}}
	ls.Sessions[0].Order = []string{"rw", "lw"}
	ls.Sessions = append(ls.Sessions, model.Session{ID: "ls2", Name: "solo"})
	ls.Workspaces = append(ls.Workspaces, model.Workspace{ID: "lw2", SessionID: "ls2"})
	local, ld := localBackend(t, ls)
	box := hostState("rs", "rw", "rp")
	box.Sessions = append(box.Sessions, model.Session{ID: "os", Name: "other"})
	box.Workspaces = append(box.Workspaces, model.Workspace{ID: "ow", SessionID: "os"})
	conns := make(chan end, 1)
	dial, dials := fakeDaemon(box, conns), 0
	m := newMulti(local, []hostDial{{name: "box", ssh: "me@box", dial: func(s string, r bool) (*proto.Conn, proto.StateMsg, error) {
		if dials++; dials > 1 {
			return nil, proto.StateMsg{}, errors.New("cannot reach box over ssh: connection timed out")
		}
		return dial(s, r)
	}}})
	rd := <-conns
	waitFor(t, "box's state", func() bool { return m.HostOf("rw") == "box" })

	st := m.State()
	if st.Session("rs") != nil || st.Session("os") == nil || st.Workspace("rw").SessionID != "ls" {
		t.Fatalf("not folded: sessions %+v", st.Sessions)
	}
	if got := st.TopOrder("ls"); !slices.Equal(got, []string{"rw", "lw"}) {
		t.Fatalf("order %v", got)
	}
	if h := m.Hosts(); !slices.Equal(h[1].Sessions, []string{"os"}) || h[1].SSH != "me@box" {
		t.Fatalf("hosts %+v", h)
	}
	if m.HostOf("rp") != "box" || m.HostOf("lw") != "" {
		t.Fatal("hosts of tabs and panes")
	}
	// Each host numbers its own tabs, as its pitwall ls does.
	if m.TabNumber("rw") != "box:1" || m.TabNumber("lw") != "1" || m.TabNumber("ow") != "box:1" {
		t.Fatalf("numbers %q %q %q", m.TabNumber("rw"), m.TabNumber("lw"), m.TabNumber("ow"))
	}

	m.Send(app.NewTabOn{Host: "box", Tab: proto.NewSession{SessionID: "ls", FromPane: "lp"}})
	if n, ok := next(rd).(proto.NewSession); !ok || n.SessionID != "rs" || n.FromPane != "" {
		t.Fatalf("box got %#v", n)
	}
	m.Send(app.NewTabOn{Tab: proto.NewSession{SessionID: "ls"}})
	if n, ok := next(ld).(proto.NewSession); !ok || n.SessionID != "ls" {
		t.Fatalf("local got %#v", n)
	}
	m.Send(proto.MoveSession{WorkspaceID: "lw", Before: "rw"})
	if o, ok := next(ld).(proto.SessionOrder); !ok || !slices.Equal(o.Order, []string{"lw", "rw"}) {
		t.Fatalf("local got %#v", o)
	}
	if err := m.Send(proto.NewGroup{WorkspaceIDs: []string{"lw", "rw"}}); err == nil {
		t.Fatal("grouped tabs of two machines")
	}
	m.Send(proto.SessionRename{SessionID: "ls", Name: "work"})
	if r, ok := next(rd).(proto.SessionRename); !ok || r.SessionID != "rs" || r.Name != "work" {
		t.Fatalf("box got %#v", r)
	}
	if r, ok := next(ld).(proto.SessionRename); !ok || r.SessionID != "ls" {
		t.Fatalf("local got %#v", r)
	}

	// solo has no part on box: box makes a session named solo, which
	// joins. That first shell cannot take an account profile, so a tab
	// that names one is refused.
	if err := m.Send(app.NewTabOn{Host: "box", Tab: proto.NewSession{SessionID: "ls2", Profile: "work"}}); err == nil {
		t.Fatal("a new part's first tab took a profile it would drop")
	}
	m.Send(app.NewTabOn{Host: "box", Tab: proto.NewSession{SessionID: "ls2"}})
	if n, ok := next(rd).(proto.SessionNew); !ok || n.Name != "solo" {
		t.Fatalf("box got %#v", n)
	}
	box.Sessions = append(box.Sessions, model.Session{ID: "ss", Name: "solo"})
	box.Workspaces = append(box.Workspaces, model.Workspace{ID: "sw", SessionID: "ss"})
	rd.c.Send(proto.StateMsg{State: box, Level: proto.Level})
	if l, ok := next(ld).(proto.SessionLink); !ok || l.SessionID != "ls2" || l.Host != "box" || l.Remote != "ss" {
		t.Fatalf("local got %#v", l)
	}

	// box goes down: its tab stays, and closing it is kept here.
	rd.nc.Close()
	waitFor(t, "box down", func() bool { return !m.Hosts()[1].Up })
	if st := m.State(); st.Workspace("rw") == nil {
		t.Fatal("box's tab left with box")
	}
	m.Send(proto.CloseTab{WorkspaceID: "rw"})
	if l, ok := next(ld).(proto.SessionLink); !ok || l.Remote != "rs" || !slices.Equal(l.Forget, []string{"rw"}) {
		t.Fatalf("local got %#v", l)
	}
}

// A shared session's order gains another host's tabs as they show up, so
// they keep their places whatever order that host lists them in.
func TestMultiSharedOrderSticks(t *testing.T) {
	ls := hostState("ls", "lw", "lp")
	ls.Sessions[0].Remotes = []model.RemoteSession{{Host: "box", Session: "rs"}}
	ls.Sessions[0].Order = []string{"lw"}
	local, ld := localBackend(t, ls)
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(hostState("rs", "rw", "rp"), conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's tab", func() bool { return m.HostOf("rw") == "box" })
	if o, ok := next(ld).(proto.SessionOrder); !ok || o.SessionID != "ls" || !slices.Equal(o.Order, []string{"lw", "rw"}) {
		t.Fatalf("local got %#v", o)
	}
}

// A tab closed while its host was down stays forgotten here until the
// host's state shows it gone: the close, after an unpin for a pinned tab,
// goes to the host on reconnect, and Forget drops it only then.
func TestMultiForgetKeptUntilGone(t *testing.T) {
	ls := hostState("ls", "lw", "lp")
	ls.Sessions[0].Remotes = []model.RemoteSession{{Host: "box", Session: "rs", Forget: []string{"rw"}}}
	ls.Sessions[0].Order = []string{"lw"}
	local, ld := localBackend(t, ls)
	box := hostState("rs", "rw", "rp")
	box.Workspaces[0].Pinned, box.Workspaces[0].Color = true, "teal"
	box.Workspaces = append(box.Workspaces, model.Workspace{ID: "rw2", SessionID: "rs"})
	conns := make(chan end, 1)
	newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(box, conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	if o, ok := next(rd).(proto.SetTabOptions); !ok || o.WorkspaceID != "rw" || o.Pinned || o.Color != "teal" {
		t.Fatalf("box got %#v, want the unpin", o)
	}
	if c, ok := next(rd).(proto.CloseTab); !ok || c.WorkspaceID != "rw" {
		t.Fatalf("box got %#v, want the close", c)
	}
	for m := recv(ld); m != nil; m = recv(ld) {
		if l, ok := m.(proto.SessionLink); ok {
			t.Fatalf("Forget changed before box closed the tab: %#v", l)
		}
	}
	box.Workspaces = box.Workspaces[1:]
	box.Panes = nil
	rd.c.Send(proto.StateMsg{State: box, Level: proto.Level})
	for {
		m := recv(ld)
		if m == nil {
			t.Fatal("Forget never dropped the closed tab")
		}
		if l, ok := m.(proto.SessionLink); ok {
			if l.Remote != "rs" || len(l.Forget) != 0 || !slices.Equal(l.Unforget, []string{"rw"}) {
				t.Fatalf("local got %#v", l)
			}
			break
		}
	}
}

// sharedDown is a window whose session ls is shared with box, which it
// never reaches, and this machine's daemon end.
func sharedDown(t *testing.T) (*multi, end) {
	ls := hostState("ls", "lw", "lp")
	ls.Sessions[0].Remotes = []model.RemoteSession{{Host: "box", Session: "rs"}}
	ls.Sessions[0].Order = []string{"rw", "lw", "lw2"}
	ls.Workspaces = append(ls.Workspaces, model.Workspace{ID: "lw2", SessionID: "ls"})
	local, ld := localBackend(t, ls)
	m := newMulti(local, []hostDial{{name: "box", dial: func(string, bool) (*proto.Conn, proto.StateMsg, error) {
		return nil, proto.StateMsg{}, errors.New("cannot reach box over ssh: connection timed out")
	}}})
	return m, ld
}

// A shared session is not killed while a part's host is down: its tabs
// there would keep running with nothing left to stop them.
func TestMultiKillWaitsForHosts(t *testing.T) {
	m, ld := sharedDown(t)
	err := m.Send(proto.SessionKill{SessionID: "ls"})
	if err == nil || !strings.Contains(err.Error(), "box is offline") {
		t.Fatalf("kill with box down: %v", err)
	}
	if !errors.As(err, new(app.Refusal)) {
		t.Fatalf("kill with box down is no refusal the window shows: %#v", err)
	}
	if got := recv(ld); got != nil {
		t.Fatalf("local got %#v", got)
	}
}

// Reordering while a host has sent no state yet, as after the window
// restarted with box down, keeps box's tabs' saved places.
func TestMultiOrderKeepsUnseenHost(t *testing.T) {
	m, ld := sharedDown(t)
	if err := m.Send(proto.MoveSession{WorkspaceID: "lw2", Before: "lw"}); err != nil {
		t.Fatal(err)
	}
	if o, ok := recv(ld).(proto.SessionOrder); !ok || !slices.Equal(o.Order, []string{"rw", "lw2", "lw"}) {
		t.Fatalf("local got %#v", o)
	}
}

// Showing a shared session tells each part's host the window shows its
// part, so pitwall attach there focuses this window.
func TestMultiShowTellsParts(t *testing.T) {
	ls := hostState("ls", "lw", "lp")
	ls.Sessions[0].Remotes = []model.RemoteSession{{Host: "box", Session: "rs"}}
	local, ld := localBackend(t, ls)
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(hostState("rs", "rw", "rp"), conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box up", func() bool { return m.Hosts()[1].Up })
	m.Send(proto.SessionShow{SessionID: "ls"})
	if s, ok := next(rd).(proto.SessionShow); !ok || s.SessionID != "rs" {
		t.Fatalf("box got %#v", s)
	}
	for {
		m := next(ld)
		if m == nil {
			t.Fatal("local never heard of the shown session")
		}
		if s, ok := m.(proto.SessionShow); ok {
			if s.SessionID != "ls" {
				t.Fatalf("local got %#v", s)
			}
			break
		}
	}
}

// A daemon here too old to keep a shared session gets no part made on a
// host, which it could not link.
func TestMultiShareNeedsLevel(t *testing.T) {
	local, _ := localBackend(t, hostState("ls", "lw", "lp"))
	local.link.Level = proto.SharedLevel - 1
	box := hostState("rs", "rw", "rp")
	box.Sessions[0].Name = "other" // so box would make a part
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(box, conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box up", func() bool { return m.Hosts()[1].Up })
	if err := m.Send(app.NewTabOn{Host: "box", Tab: proto.NewSession{SessionID: "ls"}}); err == nil {
		t.Fatal("a part made for an old daemon here")
	}
	if got := next(rd); got != nil {
		t.Fatalf("box got %#v", got)
	}
}

// A tab here while the window shows a session of box's, with no session
// here of its name, would make one here whose first shell cannot take an
// account profile, so a tab that names one is refused and nothing is sent.
func TestMultiShareHereProfile(t *testing.T) {
	local, ld := localBackend(t, hostState("ls", "lw", "lp"))
	box := hostState("rs", "rw", "rp")
	box.Sessions[0].Name = "other"
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(box, conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's tab", func() bool { return m.HostOf("rw") == "box" })
	err := m.Send(app.NewTabOn{Host: "", Tab: proto.NewSession{SessionID: "rs", Profile: "work"}})
	if r := (app.Refusal{}); !errors.As(err, &r) {
		t.Fatalf("a new session here took a profile it would drop: %v", err)
	}
	if got := next(ld); got != nil {
		t.Fatalf("local got %#v", got)
	}
}

// Killing a shared session ends its tabs here only once each part's
// host shows its part gone: a kill lost on a dropping link is sent again,
// and the tabs here stay until then.
func TestMultiKillWaitsForParts(t *testing.T) {
	ls := hostState("ls", "lw", "lp")
	ls.Sessions[0].Remotes = []model.RemoteSession{{Host: "box", Session: "rs"}}
	local, ld := localBackend(t, ls)
	box := hostState("rs", "rw", "rp")
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(box, conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box up", func() bool { return m.Hosts()[1].Up })
	if err := m.Send(proto.SessionKill{SessionID: "ls"}); err != nil {
		t.Fatal(err)
	}
	for got := next(rd); ; got = next(rd) {
		if k, ok := got.(proto.SessionKill); ok && k.SessionID == "rs" {
			break
		} else if got == nil {
			t.Fatal("box never got the kill")
		}
	}
	kept := false
	for got := recv(ld); got != nil; got = recv(ld) {
		if _, ok := got.(proto.SessionKill); ok {
			t.Fatal("killed here before box ended its part")
		}
		if l, ok := got.(proto.SessionLink); ok && l.SessionID == "ls" && l.Kill {
			kept = true
		}
	}
	if !kept {
		t.Fatal("the kill was not kept here")
	}
	// box's state shows the part gone: the part leaves, then the session
	// ends here.
	rd.c.Send(proto.StateMsg{State: model.State{Version: 2}, Level: proto.Level})
	for got := next(ld); ; got = next(ld) {
		if l, ok := got.(proto.SessionLink); ok && l.Remote == "" {
			break
		} else if got == nil {
			t.Fatal("the gone part never left")
		}
	}
	ls.Sessions = slices.Clone(ls.Sessions) // the backend holds the old one
	ls.Sessions[0].Remotes, ls.Sessions[0].Killing, ls.Version = nil, true, 2
	ld.c.Send(proto.StateMsg{State: ls, Level: proto.Level})
	for got := next(ld); ; got = next(ld) {
		if k, ok := got.(proto.SessionKill); ok && k.SessionID == "ls" {
			break
		} else if got == nil {
			t.Fatal("never killed here")
		}
	}
}

// A kill a closed window left pending, kept by this machine's daemon, is
// what the next window sends to a part still running.
func TestMultiKillOutlivesWindow(t *testing.T) {
	ls := hostState("ls", "lw", "lp")
	ls.Sessions[0].Remotes = []model.RemoteSession{{Host: "box", Session: "rs"}}
	ls.Sessions[0].Killing = true
	local, _ := localBackend(t, ls)
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(hostState("rs", "rw", "rp"), conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box up", func() bool { return m.Hosts()[1].Up })
	for got := next(rd); ; got = next(rd) {
		if k, ok := got.(proto.SessionKill); ok && k.SessionID == "rs" {
			break
		} else if got == nil {
			t.Fatal("box never got the kill")
		}
	}
}

// A part that joins the session the window shows, as pitwall new --host
// makes one, hears that the window shows it.
func TestMultiShowTellsNewPart(t *testing.T) {
	ls := hostState("ls", "lw", "lp")
	local, ld := localBackend(t, ls)
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(hostState("rs", "rw", "rp"), conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box up", func() bool { return m.Hosts()[1].Up })
	m.Send(proto.SessionShow{SessionID: "ls"})
	ls.Sessions = slices.Clone(ls.Sessions) // the backend holds the old one
	ls.Sessions[0].Remotes, ls.Version = []model.RemoteSession{{Host: "box", Session: "rs"}}, 2
	ld.c.Send(proto.StateMsg{State: ls, Level: proto.Level})
	for got := next(rd); ; got = next(rd) {
		if s, ok := got.(proto.SessionShow); ok && s.SessionID == "rs" {
			break
		} else if got == nil {
			t.Fatal("box never heard the window shows its new part")
		}
	}
}

// A task in a box folder of a shared session starts in box's part, not
// in the session here, even from a tab here.
func TestMultiTaskOnHost(t *testing.T) {
	ls := hostState("ls", "lw", "lp")
	ls.Sessions[0].Remotes = []model.RemoteSession{{Host: "box", Session: "rs"}}
	local, ld := localBackend(t, ls)
	conns := make(chan end, 1)
	box := hostState("rs", "rw", "rp")
	box.Projects = []model.Project{{ID: "rg", SessionID: "rs"}}
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(box, conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's group", func() bool { return m.HostOf("rg") == "box" })
	task := proto.NewTask{FromPane: "lp", Task: model.Task{SessionID: "ls", GroupID: "rg", Dir: "/src/app", Cmd: []string{"codex", "go"}}}
	if err := m.Send(app.NewTaskOn{Host: "box", Task: task}); err != nil {
		t.Fatal(err)
	}
	if n, ok := next(rd).(proto.NewTask); !ok || n.Task.SessionID != "rs" || n.Task.GroupID != "rg" || n.FromPane != "" {
		t.Fatalf("box got %#v", n)
	}
	if err := m.Send(app.NewTaskOn{Task: task}); err != nil {
		t.Fatal(err)
	}
	for got := next(ld); ; got = next(ld) {
		if n, ok := got.(proto.NewTask); ok {
			if n.Task.SessionID != "ls" || n.Task.GroupID != "" || n.FromPane != "lp" {
				t.Fatalf("local got %#v", n)
			}
			break
		} else if got == nil {
			t.Fatal("local never got the task")
		}
	}
}

// A task with a schedule or an account profile is refused for a box
// whose daemon is too old to keep them, instead of starting without.
func TestMultiTaskOnOldHost(t *testing.T) {
	ls := hostState("ls", "lw", "lp")
	ls.Sessions[0].Remotes = []model.RemoteSession{{Host: "box", Session: "rs"}}
	local, _ := localBackend(t, ls)
	for _, tc := range []struct {
		level int
		task  model.Task
		want  string
	}{
		{proto.ScheduleLevel - 1, model.Task{NextStart: time.Now().Add(time.Hour)}, "task schedules"},
		{proto.ScheduleLevel - 1, model.Task{Repeat: time.Hour}, "task schedules"},
		{proto.ProfileLevel - 1, model.Task{Profile: "work"}, "account profiles"},
		{proto.TaskWorktreeLevel - 1, model.Task{FreshWorktree: true, Worktree: "triage"}, "fresh task worktrees"},
	} {
		conns := make(chan end, 1)
		dial := func(string, bool) (*proto.Conn, proto.StateMsg, error) {
			gui, daemon := net.Pipe()
			conns <- end{daemon, proto.NewConn(daemon)}
			return proto.NewConn(gui), proto.StateMsg{State: hostState("rs", "rw", "rp"), Level: tc.level}, nil
		}
		m := newMulti(local, []hostDial{{name: "box", dial: dial}})
		rd := <-conns
		waitFor(t, "box up", func() bool { return m.Hosts()[1].Up })
		tc.task.SessionID, tc.task.Dir, tc.task.Cmd = "ls", "/src/app", []string{"codex"}
		err := m.Send(app.NewTaskOn{Host: "box", Task: proto.NewTask{Task: tc.task}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("level %d, %+v: %v", tc.level, tc.task, err)
		}
		rd.nc.Close()
	}
}

// A host's MonitorSample answers for that host's tabs alone, and a
// MonitorWatch goes to the daemon of the tab it names.
func TestMultiMonitor(t *testing.T) {
	local, ld := localBackend(t, hostState("ls", "lw", "lp"))
	conns := make(chan end, 1)
	m := newMulti(local, []hostDial{{name: "box", dial: fakeDaemon(hostState("rs", "rw", "rp"), conns)}})
	rd := <-conns
	t.Cleanup(func() { rd.nc.Close() })
	waitFor(t, "box's state", func() bool { st := m.State(); return st.Session("rs") != nil })

	m.Send(proto.MonitorWatch{WorkspaceID: "rw", Interval: time.Second})
	if w, ok := recv(rd).(proto.MonitorWatch); !ok || w.WorkspaceID != "rw" {
		t.Fatalf("box got %#v", w)
	}
	if got := recv(ld); got != nil {
		t.Fatalf("local got %#v", got)
	}
	if err := rd.c.Send(proto.MonitorSample{Tabs: map[string]string{"claude 7": "rp"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "box's sample", func() bool { _, _, ok := m.Monitor("rw"); return ok })
	if s, _, _ := m.Monitor("rw"); s.Tabs["claude 7"] != "rp" {
		t.Fatalf("box's sample %+v", s)
	}
	if _, _, ok := m.Monitor("lw"); ok {
		t.Fatal("this machine's tab shows box's sample")
	}
}
