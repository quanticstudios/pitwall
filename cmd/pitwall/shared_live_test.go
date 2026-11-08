//go:build unix

package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// TestSharedSessionLive runs a shared session on two real daemons: this
// machine's, and box's, which the window reaches through pitwall's own
// ssh code and the fake ssh of the remote tests. It opens tabs on both
// hosts in one session, types into each, takes box down and checks the
// down state, closes a box tab meanwhile, then brings box back and checks
// that its tabs return, the closed one closes there, and typing works.
func TestSharedSessionLive(t *testing.T) {
	dir := fakeHost(t)
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	setHome(t, home)
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("PITWALL_ATTACH", "")
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", filepath.Join(dir, "bin")+":/usr/bin:/bin") // no real agent
	sockR, sockL := sockPath(t, "box.sock"), sockPath(t, "here.sock")
	runR, runL := filepath.Dir(sockR), filepath.Dir(sockL)
	envOf := func(name, run string) {
		t.Setenv("XDG_STATE_HOME", filepath.Join(dir, name, "state"))
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, name, "config"))
		t.Setenv("XDG_RUNTIME_DIR", run)
	}
	serve := func(sock string) (stop func()) {
		d, err := daemon.New()
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		served := make(chan error, 1)
		go func() { served <- d.Serve(ctx, ln) }()
		return func() {
			cancel()
			if err := <-served; err != nil {
				t.Error(err)
			}
		}
	}
	envOf("box", runR)
	stopBox := serve(sockR)
	boxUp := true
	t.Cleanup(func() {
		if boxUp {
			stopBox()
		}
	})
	envOf("here", runL)
	t.Cleanup(serve(sockL))
	// remote-start, run on "box" through the fake ssh, finds box's daemon.
	t.Setenv("PITWALL_SOCKET", sockR)
	t.Cleanup(func() { killRelay(dir) })

	nc, err := net.Dial("unix", sockL)
	if err != nil {
		t.Fatal(err)
	}
	conn, initial, err := guiHandshake(nc, home, "")
	if err != nil {
		t.Fatal(err)
	}
	ls := initial.State.Sessions[0]
	local := newBackend(conn, ls.ID)
	local.state, local.link.Level = initial.State, initial.Level
	go local.recvLoop()
	go local.sendLoop()
	r, err := newSSHHost("box", []config.Host{{Name: "box", SSH: "me@box", Window: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.close)
	prev := pingEvery
	pingEvery = 50 * time.Millisecond
	t.Cleanup(func() { pingEvery = prev })
	// why: the window outlives the test. Its reconnects after box's daemon
	// stops would start a daemon there that outlives the test too, and
	// read globals other tests set.
	var over atomic.Bool
	t.Cleanup(func() { over.Store(true) })
	dial := func(session string, restart bool) (*proto.Conn, proto.StateMsg, error) {
		if over.Load() {
			return nil, proto.StateMsg{}, errors.New("the test is over")
		}
		return r.dial(session, restart)
	}
	m := newMulti(local, []hostDial{{name: "box", ssh: "me@box", dial: dial}})
	m.Send(proto.SessionShow{SessionID: ls.ID})
	waitFor(t, "box up", func() bool { return m.Hosts()[1].Up })

	boxTabs := func() []model.Workspace {
		st := m.State()
		return slices.DeleteFunc(slices.Clone(st.Workspaces), func(w model.Workspace) bool {
			return w.SessionID != ls.ID || m.HostOf(w.ID) != "box"
		})
	}
	// The first box tab makes box's part of the session, named after it.
	if err := m.Send(app.NewTabOn{Host: "box", Tab: proto.NewSession{SessionID: ls.ID}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a box tab in the session", func() bool { return len(boxTabs()) == 1 })
	waitFor(t, "box's part recorded here", func() bool {
		st := local.State()
		s := st.Session(ls.ID)
		return s != nil && s.Remote("box") != nil
	})
	if err := m.Send(app.NewTabOn{Host: "box", Tab: proto.NewSession{SessionID: ls.ID}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two box tabs", func() bool { return len(boxTabs()) == 2 })
	rw1, rw2 := boxTabs()[0].ID, boxTabs()[1].ID
	paneOf := func(tab string) string {
		st := m.State()
		for _, p := range st.Panes {
			if p.WorkspaceID == tab {
				return p.ID
			}
		}
		return ""
	}
	st := m.State()
	lw := slices.IndexFunc(st.Workspaces, func(w model.Workspace) bool { return w.SessionID == ls.ID && m.HostOf(w.ID) == "" })
	if lw < 0 {
		t.Fatal("no local tab in the session")
	}
	lp, rp := paneOf(st.Workspaces[lw].ID), paneOf(rw1)
	if m.TabNumber(rw1) != "box:1" && m.TabNumber(rw1) != "box:2" {
		t.Fatalf("box tab number %q", m.TabNumber(rw1))
	}

	screen := func(sock, pane string) string {
		nc, err := net.Dial("unix", sock)
		if err != nil {
			return ""
		}
		c, err := helloCLI(nc)
		if err != nil {
			return ""
		}
		defer c.Close()
		text, _ := readPane(c, proto.Read{Pane: pane, Screen: true})
		return text
	}
	typed := func(sock, pane, cmd, want string) {
		t.Helper()
		if err := m.Send(proto.Input{Pane: pane, Data: []byte(cmd + "\r")}); err != nil {
			t.Fatalf("typing into %s: %v", pane, err)
		}
		waitFor(t, want+" on "+pane, func() bool { return strings.Contains(screen(sock, pane), want) })
	}
	typed(sockL, lp, "echo here-$((6*7))", "here-42")
	typed(sockR, rp, "echo box-$((6*7))", "box-42")
	waitFor(t, "box's latency", func() bool { return m.Hosts()[1].Latency > 0 })

	// box goes down: unreachable, and its daemon gone.
	if err := os.WriteFile(filepath.Join(dir, "down"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stopBox()
	boxUp = false
	waitFor(t, "box down", func() bool { h := m.Hosts()[1]; return !h.Up && strings.Contains(h.Note, "timed out") })
	if h := m.Hosts()[1]; h.Since.IsZero() {
		t.Fatalf("down host %+v has no time it went down", h)
	}
	if len(boxTabs()) != 2 {
		t.Fatalf("box's tabs left the session: %v", boxTabs())
	}
	if err := m.Send(proto.Input{Pane: rp, Data: []byte("x")}); err == nil {
		t.Fatal("typing into a down host's pane was accepted")
	}
	typed(sockL, lp, "echo still-$((1+1))", "still-2") // this machine's tabs are never affected
	// Closing a box tab while box is down forgets it here.
	if err := m.Send(proto.CloseTab{WorkspaceID: rw2}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the closed tab forgotten", func() bool {
		st := local.State()
		r := st.Session(ls.ID).Remote("box")
		return r != nil && slices.Equal(r.Forget, []string{rw2}) && len(boxTabs()) == 1
	})

	// box comes back.
	envOf("box", runR)
	stopBox = serve(sockR)
	boxUp = true
	envOf("here", runL)
	os.Remove(filepath.Join(dir, "down"))
	m.Reconnect()
	waitFor(t, "box up again", func() bool { return m.Hosts()[1].Up })
	defer func() {
		if t.Failed() {
			st := local.State()
			bs := m.hosts[1].b.State()
			t.Logf("remote %+v; box tabs %v; box sessions %+v; box workspaces %d; rw1 %s rw2 %s", st.Session(ls.ID).Remotes, boxTabs(), bs.Sessions, len(bs.Workspaces), rw1, rw2)
		}
	}()
	waitFor(t, "the forgotten tab closed on box", func() bool {
		st := local.State()
		r := st.Session(ls.ID).Remote("box")
		return r != nil && len(r.Forget) == 0 && len(boxTabs()) == 1 && boxTabs()[0].ID == rw1
	})
	waitFor(t, "a pane in box's tab", func() bool { return paneOf(rw1) != "" })
	typed(sockR, paneOf(rw1), "echo back-$((2+3))", "back-5")

	// pitwall new --host box, run here, opens a tab in box's part.
	prevDial := dialHost
	t.Cleanup(func() { dialHost = prevDial })
	dialHost = func(string) (*cliConn, func(), error) {
		nc, err := net.Dial("unix", sockR)
		if err != nil {
			return nil, nil, err
		}
		c, err := helloCLI(nc)
		if err != nil {
			return nil, nil, err
		}
		return c, func() { c.Close() }, nil
	}
	t.Setenv("PITWALL_SOCKET", sockL)
	code, out, errOut := cliOutput("new", "-s", ls.Name, "--host", "box", "--", "sh", "-c", "echo cli-$((3*3)); sleep 60")
	if code != 0 || !strings.HasPrefix(out, "#box:") {
		t.Fatalf("pitwall new --host box: %d %q %q", code, out, errOut)
	}
	waitFor(t, "the CLI's tab in the session", func() bool { return len(boxTabs()) == 2 })
	cli := slices.DeleteFunc(boxTabs(), func(w model.Workspace) bool { return w.ID == rw1 })[0]
	waitFor(t, "its command's output", func() bool { return strings.Contains(screen(sockR, paneOf(cli.ID)), "cli-9") })

	// pitwall session kill here cannot reach box, so it leaves a shared
	// session alone.
	if code, _, errOut := cliOutput("session", "kill", "-f", ls.Name); code == 0 || !strings.Contains(errOut, "has tabs on box") {
		t.Fatalf("session kill of a shared session: %d %q", code, errOut)
	}
	if len(boxTabs()) != 2 {
		t.Fatalf("box's tabs after a refused kill: %v", boxTabs())
	}
}
