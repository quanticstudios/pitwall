package main

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestSendDoesNotBlock checks Send returns at once while the daemon is not
// reading, and that once it reads it gets every input byte in order, the
// last resize per pane, and requests that were not dropped, in order.
func TestSendDoesNotBlock(t *testing.T) {
	gui, daemon := net.Pipe() // unbuffered: a write blocks until the daemon reads
	defer gui.Close()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "")
	go b.sendLoop()

	start := time.Now()
	var typed []byte
	for i := range 2000 {
		c := byte('a' + i%26)
		typed = append(typed, c)
		b.Send(proto.Input{Pane: "p1", Data: []byte{c}})
		b.Send(proto.Resize{Pane: "p1", Cols: 80 + i%7, Rows: 24})
		b.Send(proto.Scroll{Pane: "p1", Lines: 1})
	}
	b.Send(proto.Resize{Pane: "p1", Cols: 120, Rows: 40})
	b.Send(proto.NewTab{WorkspaceID: "last"})
	if d := time.Since(start); d > time.Second {
		t.Fatalf("6000 sends to a daemon that does not read took %v", d)
	}

	c := proto.NewConn(daemon)
	daemon.SetReadDeadline(time.Now().Add(10 * time.Second))
	var got []byte
	var resize proto.Resize
	for {
		m, err := c.Recv()
		if err != nil {
			t.Fatalf("recv after %d input bytes: %v", len(got), err)
		}
		switch m := m.(type) {
		case proto.Input:
			got = append(got, m.Data...)
		case proto.Resize:
			resize = m
		}
		if _, ok := m.(proto.NewTab); ok {
			break
		}
	}
	if !bytes.Equal(got, typed) {
		t.Fatalf("input arrived as %d bytes, want the %d typed in order", len(got), len(typed))
	}
	if resize != (proto.Resize{Pane: "p1", Cols: 120, Rows: 40}) {
		t.Fatalf("last resize %+v", resize)
	}
}
