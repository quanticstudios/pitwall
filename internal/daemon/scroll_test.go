package daemon

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestScroll(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	gui := dial(t, sock, "gui")
	gui.send(proto.AddProject{Path: t.TempDir()})
	st := gui.waitState("project", func(s model.State) bool { return len(s.Workspaces) == 1 })
	gui.send(proto.OpenPane{WorkspaceID: st.Workspaces[0].ID})
	id := gui.waitState("pane", func(s model.State) bool { return len(s.Panes) == 1 }).Panes[0].ID
	p := f.pane(0)

	// output sets the fake's history and signals new output.
	output := func(hist int, pushed uint64) {
		p.mu.Lock()
		p.hist, p.pushed = hist, pushed
		p.mu.Unlock()
		p.dirty <- struct{}{}
	}
	frame := func(what string, off, max int) {
		t.Helper()
		gui.waitFor(what, func(m any) bool {
			fr, ok := m.(proto.Frame)
			return ok && fr.Pane == id && fr.ScrollOffset == off && fr.ScrollMax == max
		})
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.off != off {
			t.Fatalf("%s: SnapshotAt(%d), want %d", what, p.off, off)
		}
	}

	output(100, 100)
	frame("history", 0, 100)
	gui.send(proto.Scroll{Pane: id, Lines: 30})
	frame("scrolled", 30, 100)
	gui.send(proto.Scroll{Pane: id, Lines: 500})
	frame("clamped at max", 100, 100)
	gui.send(proto.Scroll{Pane: id, Lines: -1000})
	frame("clamped at 0", 0, 100)

	// History is full, so its length stays 100 while 5 more lines scroll off.
	gui.send(proto.Scroll{Pane: id, Lines: 40})
	frame("scrolled again", 40, 100)
	output(100, 105)
	frame("anchored", 45, 100)

	gui.send(proto.Input{Pane: id, Data: []byte("q")})
	frame("input snaps to live", 0, 100)
	output(100, 110)
	frame("live view follows output", 0, 100)

	// ED 3 empties history under a scrolled view.
	gui.send(proto.Scroll{Pane: id, Lines: 50})
	frame("scrolled before clear", 50, 100)
	output(10, 110)
	frame("clamped to the new history", 10, 10)
}
