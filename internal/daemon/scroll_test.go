package daemon

import (
	"fmt"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestSearchReply(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	gui := dial(t, sock, "gui")
	id := gui.waitState("pane", func(s model.State) bool { return len(s.Panes) == 1 }).Panes[0].ID
	p := f.pane(0)
	p.mu.Lock()
	p.hist, p.pushed = 100, 250
	p.mu.Unlock()
	p.dirty <- struct{}{}
	gui.waitFor("frame with pushed", func(m any) bool {
		fr, ok := m.(proto.Frame)
		return ok && fr.Pane == id && fr.ScrollPushed == 250
	})

	gui.send(proto.Search{Pane: id, Query: "err"})
	r := gui.waitFor("result", func(m any) bool { _, ok := m.(proto.SearchResult); return ok }).(proto.SearchResult)
	if r.Pane != id || r.Query != "err" || len(r.Matches) != 1 || r.Matches[0] != (vt.Match{Line: 250, Cols: 3}) || r.More {
		t.Fatalf("result %+v", r)
	}
	if e := gui.request(proto.Search{Pane: "nope", Query: "x"}); e == "" {
		t.Error("no error for a missing pane")
	}
}

// TestTextReply checks a Text request is answered to its client with the
// pane's text of the selection.
func TestTextReply(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	gui := dial(t, sock, "gui")
	id := gui.waitState("pane", func(s model.State) bool { return len(s.Panes) == 1 }).Panes[0].ID
	sel := vt.Selection{A: vt.Pos{Line: 3}, B: vt.Pos{Line: 9000, Col: 2}}
	gui.send(proto.Text{Pane: id, Sel: sel})
	r := gui.waitFor("result", func(m any) bool { _, ok := m.(proto.TextResult); return ok }).(proto.TextResult)
	if r.Pane != id || r.Sel != sel || r.Text != "lines 3-9000" {
		t.Fatalf("result %+v", r)
	}
	if e := gui.request(proto.Text{Pane: "nope"}); e == "" {
		t.Error("no error for a missing pane")
	}
}

// TestScrollbackSetting checks [terminal] scrollback reaches new panes'
// emulators.
func TestScrollbackSetting(t *testing.T) {
	d := &Daemon{o: Options{NewVT: vt.New, Scrollback: func() int { return 5 }}}
	e := d.notifyingVT("p")(8, 3, nil)
	for i := range 20 {
		fmt.Fprintf(e, "line %d\r\n", i)
	}
	if n := e.ScrollbackLen(); n != 5 {
		t.Errorf("ScrollbackLen %d, want 5", n)
	}
}

func TestScroll(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	gui := dial(t, sock, "gui")
	// The first session's shell is the pane under test.
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
	gui.send(proto.Scroll{Pane: id, Lines: 10, Prompts: 2})
	frame("two prompts back from 10 lines up", 24, 100)
	gui.send(proto.Scroll{Pane: id, Prompts: -9})
	frame("prompts forward stop at the live screen", 0, 100)

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
