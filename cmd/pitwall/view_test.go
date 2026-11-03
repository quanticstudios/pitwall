package main

import (
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
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
	b.Show([]string{"p1"}, nil) // what it shows already: nothing to send
	shows("p2", "b", "p1", "p2")
	b.Show([]string{"p2"}, nil)
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
	if g, _, _ := b.Frame("p2"); g.At(0, 0).Content != "b" {
		t.Fatalf("an older daemon's frame: %q", g.At(0, 0).Content)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.view) != 0 || len(b.waits) != 0 {
		t.Fatalf("asked an older daemon for frames: view %v", b.view)
	}
}

// TestShowLive checks Show names picture in picture's pane in View.Live,
// and sends again only when the view or the live panes change.
func TestShowLive(t *testing.T) {
	gui, daemon := net.Pipe()
	defer gui.Close()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "s")
	b.link.Level = proto.Level
	b.view = map[string]bool{"p1": true, "p2": true}
	go b.sendLoop()
	d := proto.NewConn(daemon)
	daemon.SetDeadline(time.Now().Add(10 * time.Second))
	b.Show([]string{"p1", "p2"}, []string{"p2", "gone"})
	if m, err := d.Recv(); err != nil || !reflect.DeepEqual(m, proto.View{Panes: []string{"p1", "p2"}, Live: []string{"p2"}}) {
		t.Fatalf("Show sent %#v, %v; want p2 live", m, err)
	}
	b.Show([]string{"p1", "p2"}, []string{"p2"}) // no change: nothing to send
	b.Show([]string{"p1", "p2"}, nil)
	if m, err := d.Recv(); err != nil || !reflect.DeepEqual(m, proto.View{Panes: []string{"p1", "p2"}}) {
		t.Fatalf("Show sent %#v, %v; want none live", m, err)
	}
}

// TestViewedPaneRedraws checks a frame of a pane the window draws wakes it
// even when the pane's tab is not one the session shows, as picture in
// picture draws a pane of another session.
func TestViewedPaneRedraws(t *testing.T) {
	gui, daemon := net.Pipe()
	defer gui.Close()
	defer daemon.Close()
	b := newBackend(proto.NewConn(gui), "s")
	b.session = "s"
	b.state = model.State{
		Panes:      []model.Pane{{ID: "p9", WorkspaceID: "w9"}},
		Workspaces: []model.Workspace{{ID: "w9", SessionID: "other", ActiveTab: "t9", Tabs: []model.Tab{{ID: "t9", Layout: layout.Leaf("p9")}}}},
	}
	b.link.Level = proto.Level
	b.view = map[string]bool{"p9": true}
	go b.recvLoop()
	d := proto.NewConn(daemon)
	daemon.SetDeadline(time.Now().Add(10 * time.Second))
	select {
	case <-b.Changed():
	default:
	}
	if err := d.Send(proto.Frame{Pane: "p9", Grid: vt.Grid{Cols: 1, Rows: 1, Cells: []vt.Cell{{Content: "x", Width: 1}}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Changed():
	case <-time.After(2 * time.Second):
		t.Fatal("a frame of a pane in the view did not redraw the window")
	}
}
