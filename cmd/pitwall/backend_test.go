package main

import (
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
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
// recvLoop ends and Changed closes, and later Sends return the error.
func TestBackendWriteFailure(t *testing.T) {
	gui, daemon := net.Pipe()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "")
	exited := make(chan struct{})
	go func() { b.sendLoop(); close(exited) }()
	go b.recvLoop()
	gui.SetWriteDeadline(time.Now()) // the next write fails at once
	b.Send(proto.Input{Pane: "p1", Data: []byte("x")})
	b.Send(proto.Scroll{Pane: "p1", Lines: 1})
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer did not exit after a write failed")
	}
	select {
	case _, ok := <-b.Changed():
		for ok {
			_, ok = <-b.Changed()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Changed did not close after a write failed")
	}
	if err := b.Send(proto.Input{Pane: "p1", Data: []byte("y")}); err == nil {
		t.Fatal("Send after a write failure returned nil")
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
