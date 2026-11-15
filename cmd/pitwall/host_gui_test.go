//go:build unix && !nogui

package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// TestRemoteReconnects runs pitwall --host's dial through a fake ssh whose
// "host" is this machine: remote-start runs there and reports a daemon
// socket, which the shared connection forwards. When the link drops, the
// window reconnects through a new forward to the same daemon.
func TestRemoteReconnects(t *testing.T) {
	dir := fakeHost(t)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("PITWALL_PANE", "")
	// remote-start, run on the "host", reports this socket; the window
	// reaches it only through the forward.
	sock := filepath.Join(dir, "host.sock")
	t.Setenv("PITWALL_SOCKET", sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Cleanup(func() { killRelay(dir) })

	accept := func() *proto.Conn {
		t.Helper()
		for {
			ln.(*net.UnixListener).SetDeadline(time.Now().Add(10 * time.Second))
			nc, err := ln.Accept()
			if err != nil {
				t.Fatalf("no dial: %v", err)
			}
			nc.SetDeadline(time.Now().Add(10 * time.Second))
			c := proto.NewConn(nc)
			m, err := c.Recv()
			if err != nil {
				nc.Close() // remote-start checking for a daemon
				continue
			}
			if h, ok := m.(proto.Hello); !ok || h.Kind != "gui" || h.Session != "work" {
				t.Fatalf("hello: %#v", m)
			}
			t.Cleanup(func() { nc.Close() })
			return c
		}
	}
	r, err := newSSHHost("box", []config.Host{{Name: "box", SSH: "me@box"}})
	if err != nil {
		t.Fatal(err)
	}
	type dialed struct {
		c   *proto.Conn
		st  proto.StateMsg
		err error
	}
	first := make(chan dialed, 1)
	go func() { c, st, err := r.dial("work", false); first <- dialed{c, st, err} }()
	d := accept()
	st := model.State{Version: 1, Sessions: []model.Session{{ID: "s1", Name: "work"}}}
	d.Send(proto.StateMsg{State: st})
	got := <-first
	if got.err != nil {
		t.Fatal(got.err)
	}
	b := newBackend(got.c, "s1")
	// Once the test is over, the backend's next try ends its dial loop.
	var over atomic.Bool
	defer over.Store(true)
	b.state, b.name = got.st.State, "work"
	b.redial = func(session string, restart bool) (*proto.Conn, proto.StateMsg, error) {
		if over.Load() {
			return nil, proto.StateMsg{}, incompatible{proto.Version - 1}
		}
		return r.dial(session, restart)
	}
	go b.recvLoop()
	go b.sendLoop()

	killRelay(dir) // the link drops, and the forward with it
	d = accept()
	st.Version = 2
	d.Send(proto.StateMsg{State: st})
	for deadline := time.Now().Add(10 * time.Second); b.Link() != (app.Link{Epoch: 2}) || b.State().Version != 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no reconnect: link %+v, state version %d", b.Link(), b.State().Version)
		}
	}
	b.Send(proto.Input{Pane: "p", Data: []byte("x")})
	if m, err := d.Recv(); err != nil {
		t.Fatal(err)
	} else if in, ok := m.(proto.Input); !ok || in.Pane != "p" {
		t.Fatalf("%#v through the new forward", m)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "ssh.log"))
	log := string(data)
	// A slow try can time out and go again; each one starts and forwards.
	starts := strings.Count(log, "-- me@box "+startCommand(false))
	forwards := strings.Count(log, "-O forward -L "+r.local+":"+sock+" -- me@box")
	if starts < 2 || forwards != starts {
		t.Errorf("%d starts and %d forwards, want 2 or more of each:\n%s", starts, forwards, log)
	}

	over.Store(true)
	r.close()
	data, _ = os.ReadFile(filepath.Join(dir, "ssh.log"))
	if !strings.HasSuffix(string(data), "-O cancel -L "+r.local+":"+sock+" -- me@box\n") {
		t.Errorf("no cancel on close:\n%s", data)
	}
	if _, err := os.Stat(r.local); !os.IsNotExist(err) {
		t.Errorf("forwarded socket left behind: %v", err)
	}
}
