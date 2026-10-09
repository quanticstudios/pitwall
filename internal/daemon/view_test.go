package daemon

import (
	"testing"
	"time"

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

// draw puts row on p's screen and signals output.
func (p *fakePane) draw(row string) {
	p.mu.Lock()
	p.screen = []string{row}
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
	time.Sleep(50 * time.Millisecond) // its watcher has run
	if fr := old.frameOf(hidden); row0(fr) != "while hidden" {
		t.Fatalf("older GUI's frame of %s: %q", hidden, row0(fr))
	}
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
	time.Sleep(50 * time.Millisecond)
	f.paneNamed(hidden).draw("live")
	if fr := gui.frameOf(hidden, shown); row0(fr) != "live" {
		t.Fatalf("frame of %s: %q", hidden, row0(fr))
	}
}
