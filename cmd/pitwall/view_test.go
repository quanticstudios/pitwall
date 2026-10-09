package main

import (
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// TestFrameOnShow checks the window never draws a pane it starts showing,
// a tab switched to say, empty or from a stale frame: Frame names the pane
// in a View and waits for its current frame, and Show drops the panes the
// window stopped drawing.
func TestFrameOnShow(t *testing.T) {
	gui, daemon := net.Pipe()
	defer gui.Close()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "s")
	b.state = model.State{Panes: []model.Pane{{ID: "p1"}, {ID: "p2"}}}
	b.link.Level = proto.Level
	go b.recvLoop()
	go b.sendLoop()
	d := proto.NewConn(daemon)
	daemon.SetDeadline(time.Now().Add(10 * time.Second))

	frame := func(pane, text string) proto.Frame {
		return proto.Frame{Pane: pane, Grid: vt.Grid{Cols: 1, Rows: 1, Cells: []vt.Cell{{Content: text, Width: 1}}}}
	}
	// shows draws pane while the daemon answers its View with text.
	shows := func(pane, text string, view ...string) {
		t.Helper()
		got := make(chan string)
		go func() {
			g, _, _ := b.Frame(pane)
			got <- g.At(0, 0).Content
		}()
		if m, err := d.Recv(); err != nil || !reflect.DeepEqual(m, proto.View{Panes: view}) {
			t.Fatalf("drawing %s sent %#v, %v; want a View of %v", pane, m, err, view)
		}
		d.Send(frame(pane, text))
		if s := <-got; s != text {
			t.Fatalf("drew %s as %q, want %q", pane, s, text)
		}
	}
	shows("p1", "a", "p1")
	if g, _, _ := b.Frame("p1"); g.At(0, 0).Content != "a" {
		t.Fatal("p1's frame went")
	}
	b.Show([]string{"p1"}) // what it shows already: nothing to send
	shows("p2", "b", "p1", "p2")
	b.Show([]string{"p2"})
	if m, err := d.Recv(); err != nil || !reflect.DeepEqual(m, proto.View{Panes: []string{"p2"}}) {
		t.Fatalf("Show sent %#v, %v", m, err)
	}
	// p1's frame is stale now: drawing it again waits for a current one.
	shows("p1", "c", "p1", "p2")
	// Rows change the frame held.
	d.Send(proto.FrameRows{Pane: "p1", Rows: []int{0}, Cells: []vt.Cell{{Content: "d", Width: 1}}})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if g, _, _ := b.Frame("p1"); g.At(0, 0).Content == "d" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("p1 after FrameRows: %q", g.At(0, 0).Content)
		}
	}

	// A daemon below View's Level sends every frame, unasked.
	b.mu.Lock()
	b.link.Level = proto.Since(proto.View{}) - 1
	b.view = map[string]bool{}
	b.mu.Unlock()
	start := time.Now()
	if g, _, _ := b.Frame("p2"); g.At(0, 0).Content != "b" || time.Since(start) >= frameWait {
		t.Fatalf("an older daemon's frame: %q after %v", g.At(0, 0).Content, time.Since(start))
	}
}
