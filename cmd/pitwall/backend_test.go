package main

import (
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/testenv"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// TestSendDoesNotBlock checks Send returns at once while the daemon is not
// reading, and that once it reads it gets every message in the order sent,
// except that a newer Resize for a pane replaces the queued one in place.
func TestSendDoesNotBlock(t *testing.T) {
	gui, daemon := net.Pipe() // unbuffered: a write blocks until the daemon reads
	defer gui.Close()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "")
	go b.sendLoop()

	var want []any
	send := func(m any) {
		if err := b.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	for i := range 1000 {
		in := proto.Input{Pane: "p1", Data: []byte{byte('a' + i%26)}}
		send(in)
		want = append(want, in)
		if i == 0 {
			send(proto.Resize{Pane: "p1", Cols: 80, Rows: 24})
			send(proto.Resize{Pane: "p2", Cols: 10, Rows: 5})
			want = append(want, proto.Resize{Pane: "p1", Cols: 120, Rows: 40}, proto.Resize{Pane: "p2", Cols: 10, Rows: 5})
		}
		send(proto.Scroll{Pane: "p1", Lines: i})
		want = append(want, proto.Scroll{Pane: "p1", Lines: i})
	}
	send(proto.Resize{Pane: "p1", Cols: 120, Rows: 40}) // replaces the first one, in its place
	send(proto.NewTab{WorkspaceID: "last"})
	want = append(want, proto.NewTab{WorkspaceID: "last"})
	if d := time.Since(start); d > time.Second {
		t.Fatalf("sends to a daemon that does not read took %v", d)
	}

	c := proto.NewConn(daemon)
	daemon.SetReadDeadline(time.Now().Add(10 * time.Second))
	for i, w := range want {
		m, err := c.Recv()
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if !reflect.DeepEqual(m, w) {
			t.Fatalf("message %d is %#v, want %#v", i, m, w)
		}
	}
}

// TestBackendEndsWithConnection checks an idle writer exits when the daemon
// closes the connection, and that Send then returns the error.
func TestBackendEndsWithConnection(t *testing.T) {
	gui, daemon := net.Pipe()
	defer gui.Close()
	b := newBackend(proto.NewConn(gui), "")
	exited := make(chan struct{})
	go func() { b.sendLoop(); close(exited) }()
	go b.recvLoop()
	daemon.Close()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer did not exit after the daemon closed")
	}
	if err := b.Send(proto.Input{Pane: "p1", Data: []byte("x")}); err == nil {
		t.Fatal("Send on a closed connection returned nil")
	}
}

// TestBackendWriteFailure checks a failed write closes the connection, so
// recvLoop ends and the link is down, and later Sends return the error.
func TestBackendWriteFailure(t *testing.T) {
	gui, daemon := net.Pipe()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "")
	exited, read := make(chan struct{}), make(chan struct{})
	go func() { b.sendLoop(); close(exited) }()
	go func() { b.recvLoop(); close(read) }()
	gui.SetWriteDeadline(time.Now()) // the next write fails at once
	b.Send(proto.Input{Pane: "p1", Data: []byte("x")})
	b.Send(proto.Scroll{Pane: "p1", Lines: 1})
	for _, c := range []chan struct{}{exited, read} {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			t.Fatal("a loop did not exit after a write failed")
		}
	}
	if l := b.Link(); l.State != app.LinkDown {
		t.Fatalf("link %+v after a write failed", l)
	}
	if err := b.Send(proto.Input{Pane: "p1", Data: []byte("y")}); err == nil {
		t.Fatal("Send after a write failure returned nil")
	}
}

// TestSendsByDaemonLevel checks a message type newer than the daemon's
// proto.Level, which a daemon of v0.1.0-alpha.22 (Level 0) would drop the
// connection on, is refused and never sent, and goes out once a daemon of
// that Level answers.
func TestSendsByDaemonLevel(t *testing.T) {
	gui, daemon := net.Pipe()
	defer gui.Close()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "")
	go b.recvLoop()
	go b.sendLoop()
	c := proto.NewConn(daemon)
	daemon.SetDeadline(time.Now().Add(5 * time.Second))
	newer := proto.Search{Pane: "p", Query: "q"}
	for _, level := range []int{0, proto.Since(newer)} {
		if err := c.Send(proto.StateMsg{State: model.State{Version: uint64(1 + level)}, Level: level}); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(5 * time.Second); b.State().Version != uint64(1+level); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("level %d: no state", level)
			}
		}
		err := b.Send(newer)
		if (err == nil) != (level >= proto.Since(newer)) {
			t.Fatalf("level %d: Send(%T) = %v", level, newer, err)
		}
		b.Send(proto.Sync{})
		want := []any{proto.Sync{}}
		if err == nil {
			want = []any{newer, proto.Sync{}}
		}
		for _, w := range want {
			if m, err := c.Recv(); err != nil || m != w {
				t.Fatalf("level %d: got %#v, %v, want %#v", level, m, err, w)
			}
		}
	}
}

// TestFocusNeverBlocksState: FocusSession requests nobody has taken yet,
// as when the window is busy raising itself, must not stop the state after
// them from reaching the window; the newest request wins.
func TestFocusNeverBlocksState(t *testing.T) {
	gui, daemon := net.Pipe()
	defer gui.Close()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "first")
	go b.recvLoop()
	c := proto.NewConn(daemon)
	daemon.SetWriteDeadline(time.Now().Add(5 * time.Second))
	for _, id := range []string{"a", "b", "c"} {
		if err := c.Send(proto.FocusSession{SessionID: id}); err != nil {
			t.Fatalf("send focus %s: %v", id, err)
		}
	}
	if err := c.Send(proto.StateMsg{State: model.State{Version: 7}}); err != nil {
		t.Fatalf("send state: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); b.State().Version != 7; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the state behind unread focus requests never arrived")
		}
	}
	if f := <-b.Focus(); f.SessionID != "c" {
		t.Fatalf("focus %+v, want the newest, c", f)
	}
}

// TestBackendReconnects drops the daemon connection and checks the backend
// comes back through dialOrStart: it keeps the last state meanwhile, waits
// after a failed try until Reconnect, and says Hello for the session the
// window shows. It skips a message type from a newer daemon of its
// Version. A daemon of another Version ends the retries: an older one asks
// to restart it, a newer one reopens the window.
func TestBackendReconnects(t *testing.T) {
	path := sockPath(t, "d.sock")
	t.Setenv("PITWALL_SOCKET", path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	prev := retryMin
	retryMin = time.Hour // only Reconnect ends the wait
	t.Cleanup(func() { retryMin = prev })
	accept := func() *proto.Conn {
		t.Helper()
		ln.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second))
		nc, err := ln.Accept()
		if err != nil {
			t.Fatalf("no dial: %v", err)
		}
		t.Cleanup(func() { nc.Close() })
		nc.SetDeadline(time.Now().Add(5 * time.Second))
		c := proto.NewConn(testenv.FromFuture(nc))
		if m, err := c.Recv(); err != nil {
			t.Fatal(err)
		} else if h, ok := m.(proto.Hello); !ok || h.Session != "work" || h.Level != proto.Level {
			t.Fatalf("hello: %#v", m)
		}
		return c
	}
	var b *backend
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: link %+v, state version %d", what, b.Link(), b.State().Version)
			}
		}
	}
	st := model.State{Version: 1, Sessions: []model.Session{{ID: "s1", Name: "work"}}}

	type dialed struct {
		c   *proto.Conn
		st  proto.StateMsg
		err error
	}
	first := make(chan dialed, 1)
	go func() { c, st, err := dialOrStart("work", false); first <- dialed{c, st, err} }()
	d := accept()
	d.Send(proto.StateMsg{State: st})
	got := <-first
	if got.err != nil {
		t.Fatal(got.err)
	}
	b = newBackend(got.c, "s1")
	b.state, b.name, b.redial = got.st.State, "work", dialOrStart
	go b.recvLoop()
	go b.sendLoop()
	b.Send(proto.SessionShow{SessionID: "s1"})
	if m, err := d.Recv(); err != nil || m != (proto.SessionShow{SessionID: "s1"}) {
		t.Fatalf("%#v, %v", m, err)
	}

	d.Close() // the daemon goes away
	accept().Close()
	waitFor("a failed try", func() bool { return strings.Contains(b.Link().Note, "Trying again") })
	if b.Link().State != app.LinkDown || b.State().Version != 1 {
		t.Fatalf("while down: %+v, state version %d", b.Link(), b.State().Version)
	}
	b.Reconnect()
	d = accept()
	st.Version = 2
	d.Send(proto.StateMsg{State: st, Level: proto.Level})
	d.Send(testenv.Future{}) // a newer daemon's message, which the backend skips
	st.Version = 3
	d.Send(proto.StateMsg{State: st, Level: proto.Level})
	waitFor("reconnect", func() bool { return b.Link() == app.Link{Epoch: 2, Level: proto.Level} && b.State().Version == 3 })
	b.Send(proto.Input{Pane: "p", Data: []byte("x")})
	if m, err := d.Recv(); err != nil {
		t.Fatal(err)
	} else if in, ok := m.(proto.Input); !ok || in.Pane != "p" {
		t.Fatalf("%#v after reconnecting", m)
	}

	for _, c := range []struct {
		version int
		want    app.LinkState
	}{{proto.Version - 1, app.LinkRestart}, {proto.Version + 1, app.LinkStale}} {
		if b.Link().State == app.LinkUp {
			d.Close()
		} else {
			b.startDialing(false)
		}
		d = accept()
		d.Send(proto.Error{Message: fmt.Sprintf("daemon speaks protocol version %d; send Hello{Version: %d} first", c.version, c.version)})
		waitFor(fmt.Sprint("refused by ", c.version), func() bool { return b.Link().State == c.want })
	}
}
