package daemon

import (
	"fmt"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// paneNamed is the fake behind pane id.
func (f *fakes) paneNamed(id string) *fakePane {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.panes {
		if p.cfg.ID == id {
			return p
		}
	}
	return nil
}

// draw puts rows on p's screen and signals output.
func (p *fakePane) draw(rows ...string) {
	p.mu.Lock()
	p.screen = rows
	p.mu.Unlock()
	select {
	case p.dirty <- struct{}{}:
	default:
	}
}

// frameOf waits for a Frame of pane and returns it, failing on a Frame of
// any pane in not on the way.
func (c *testClient) frameOf(pane string, not ...string) proto.Frame {
	c.t.Helper()
	return c.waitFor("frame of "+pane, func(m any) bool {
		f, ok := m.(proto.Frame)
		for _, n := range not {
			if ok && f.Pane == n {
				c.t.Fatalf("got a frame of %s, which the GUI does not show", n)
			}
		}
		return ok && f.Pane == pane
	}).(proto.Frame)
}

// rowOf waits for a Frame of pane whose first row is text.
func (c *testClient) rowOf(pane, text string) {
	c.t.Helper()
	c.waitFor(pane+" showing "+text, func(m any) bool {
		f, ok := m.(proto.Frame)
		return ok && f.Pane == pane && row0(f) == text
	})
}

func row0(f proto.Frame) string {
	s := ""
	for x := range f.Grid.Cols {
		s += f.Grid.At(x, 0).Content
	}
	return s
}

// A GUI that sends View gets frames of the panes it draws alone, and a
// current frame of a pane as soon as it draws it again; an older GUI gets
// every pane's.
func TestViewFrames(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}, saved: fanoutState(8)}
	sock, stop := run(t, f)
	defer stop()
	shown, hidden := "p00-0", "p01-0"
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Session: "bench"})
	old := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Since(proto.View{}) - 1, Kind: "gui", Session: "bench"})
	gui.waitState("state", func(model.State) bool { return true })
	old.frameOf(hidden)

	gui.send(proto.View{Panes: []string{shown}})
	if fr := gui.frameOf(shown, hidden); fr.Grid.Cols == 0 {
		t.Fatalf("first frame of a shown pane is empty: %+v", fr)
	}
	f.paneNamed(hidden).draw("while hidden")
	// The older GUI shows every pane: once it has this frame, the daemon
	// has pushed it to every GUI that shows the pane.
	old.rowOf(hidden, "while hidden")
	f.paneNamed(shown).draw("shown")
	if fr := gui.frameOf(shown, hidden); row0(fr) != "shown" {
		t.Fatalf("frame of %s: %q", shown, row0(fr))
	}

	// The hidden pane changed while not drawn; drawing it brings it current
	// with no output of its own.
	gui.send(proto.View{Panes: []string{hidden}})
	if fr := gui.frameOf(hidden); row0(fr) != "while hidden" {
		t.Fatalf("frame of %s once shown: %q", hidden, row0(fr))
	}
	f.paneNamed(shown).draw("now hidden")
	old.rowOf(shown, "now hidden")
	f.paneNamed(hidden).draw("live")
	if fr := gui.frameOf(hidden, shown); row0(fr) != "live" {
		t.Fatalf("frame of %s: %q", hidden, row0(fr))
	}
}

// A GUI of FrameRows' Level gets a whole frame first and on a resize or a
// scroll, and the changed rows otherwise; applied, they give the screen.
func TestFrameRows(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}, saved: fanoutState(4)}
	sock, stop := run(t, f)
	defer stop()
	id := "p00-0"
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Session: "bench"})
	gui.waitState("state", func(model.State) bool { return true })
	p := f.paneNamed(id)
	p.draw("aaa", "bbb", "ccc", "ddd")
	gui.send(proto.View{Panes: []string{id}})
	var held proto.Frame
	// next waits for id's next Frame or FrameRows, of type want, applies it
	// to held and checks held's first rows.
	next := func(want string, rows ...string) {
		t.Helper()
		m := gui.waitFor("frame of "+id, func(m any) bool {
			f, isFrame := m.(proto.Frame)
			r, isRows := m.(proto.FrameRows)
			return isFrame && f.Pane == id || isRows && r.Pane == id
		})
		switch m := m.(type) {
		case proto.Frame:
			held = m
		case proto.FrameRows:
			var ok bool
			if held, ok = m.Apply(held); !ok {
				t.Fatalf("rows %v do not fit the frame", m.Rows)
			}
		}
		if got := fmt.Sprintf("%T", m); got != want {
			t.Fatalf("got a %s, want a %s", got, want)
		}
		for y, r := range rows {
			s := ""
			for x := range len(r) {
				s += held.Grid.At(x, y).Content
			}
			if s != r {
				t.Fatalf("row %d is %q, want %q", y, s, r)
			}
		}
	}
	next("proto.Frame", "aaa", "bbb", "ccc", "ddd")
	p.draw("aaa", "bXb", "ccc", "ddd")
	next("proto.FrameRows", "aaa", "bXb", "ccc", "ddd")
	p.draw("aaa", "bXb", "ccc", "ddd", "eee") // the pane grew a row
	next("proto.Frame", "aaa", "bXb", "ccc", "ddd", "eee")
	p.draw("aaa", "bXb", "ccc", "ddY", "eee")
	next("proto.FrameRows", "aaa", "bXb", "ccc", "ddY", "eee")
	// Shown again after a change unseen, it comes whole: the backend waits
	// for a Frame.
	gui.send(proto.View{})
	p.draw("aaa", "bXb", "Zcc", "ddY", "eee") // on the screen now, its frame or not
	gui.send(proto.View{Panes: []string{id}})
	next("proto.Frame", "aaa", "bXb", "Zcc", "ddY", "eee")
	p.mu.Lock()
	p.hist = 10
	p.mu.Unlock()
	gui.send(proto.Scroll{Pane: id, Lines: 3})
	next("proto.Frame")
	if held.ScrollOffset != 3 {
		t.Fatalf("scrolled frame at offset %d", held.ScrollOffset)
	}
}
