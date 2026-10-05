package main

import (
	"net"
	"reflect"
	"testing"
	"time"

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
