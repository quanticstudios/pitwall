package app

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestFindSteps(t *testing.T) {
	var f findBar
	f.sent = "x"
	f.take(proto.SearchResult{Query: "x", Matches: []vt.Match{{Line: 1}, {Line: 5}, {Line: 9}}})
	walk := []struct {
		d     int
		line  uint64
		label string
	}{
		{0, 9, "1 of 3"}, // the newest first
		{1, 5, "2 of 3"}, // then up into history
		{1, 1, "3 of 3"},
		{1, 9, "1 of 3"}, // wrapping around the oldest
		{-1, 1, "3 of 3"},
		{-1, 5, "2 of 3"},
	}
	for i, w := range walk {
		f.step(w.d)
		if m, _ := f.current(); m.Line != w.line || f.label() != w.label {
			t.Fatalf("step %d (%+d): line %d %q, want %d %q", i, w.d, m.Line, f.label(), w.line, w.label)
		}
	}

	// A new reply starts at the newest match at or above the current one,
	// line 5.
	f.take(proto.SearchResult{Matches: []vt.Match{{Line: 2}, {Line: 5, Col: 3}, {Line: 7}}})
	if m, _ := f.current(); m.Line != 2 || f.label() != "3 of 3" {
		t.Fatalf("after a new reply: line %d %q", m.Line, f.label())
	}
	f.take(proto.SearchResult{Matches: []vt.Match{{Line: 4}}, More: true})
	if f.label() != "1 of 1+" {
		t.Fatalf("more: %q", f.label())
	}
	f.take(proto.SearchResult{})
	if _, ok := f.current(); ok || f.label() != "No matches" {
		t.Fatalf("none: %q", f.label())
	}
	f.step(1) // nothing to step to
}

func TestReveal(t *testing.T) {
	for _, tc := range []struct {
		line, top       uint64
		off, most, rows int
		want            int
	}{
		{105, 100, 0, 500, 24, 0},    // on screen
		{100, 100, 0, 500, 24, 0},    // the top row
		{124, 100, 0, 500, 24, 0},    // below the live view: nowhere to go
		{90, 100, 0, 500, 24, 22},    // 10 above: to mid-view
		{10, 100, 30, 40, 24, 10},    // clamped to the oldest line
		{180, 100, 50, 500, 24, -50}, // below a scrolled view: back toward live
	} {
		if got := reveal(tc.line, tc.top, tc.off, tc.most, tc.rows); got != tc.want {
			t.Errorf("reveal(%d, top %d, off %d, most %d, rows %d) = %d, want %d", tc.line, tc.top, tc.off, tc.most, tc.rows, got, tc.want)
		}
	}
}

// TestFindBar drives the bar through Gio's router: the find key opens it on
// the focused pane, typing searches instead of reaching the pane, and
// Escape closes it, back to the live screen and the pane's keys.
func TestFindBar(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	typed := func() (s string, last any) {
		for _, m := range b.Sent() {
			if m, ok := m.(proto.Input); ok {
				s += string(m.Data)
			}
			last = m
		}
		return s, last
	}
	frame()
	frame()
	pane := u.nav.focused()
	r.Queue(press("F", key.ModCtrl|key.ModShift))
	frame()
	frame() // the bar takes key focus
	if u.find.pane != pane {
		t.Fatalf("find bar on %q, want %q", u.find.pane, pane)
	}
	r.Queue(key.EditEvent{Text: "release"})
	frame()
	frame()
	if s, last := typed(); s != "" || last != (proto.Search{Pane: pane, Query: "release"}) {
		t.Fatalf("typing: pane got %q, last message %#v", s, last)
	}
	if got := u.find.label(); got != "1 of 1" {
		t.Fatalf("label %q", got)
	}
	r.Queue(press(key.NameEscape, 0))
	frame()
	if _, last := typed(); u.find.pane != "" || last != (proto.Scroll{Pane: pane, Lines: -1 << 30}) {
		t.Fatalf("Escape: bar on %q, last message %#v", u.find.pane, last)
	}
	frame()
	r.Queue(press("Q", 0), key.EditEvent{Text: "q"})
	frame()
	if s, _ := typed(); s != "q" {
		t.Fatalf("after Escape the pane got %q", s)
	}
}

// TestFindBarButtons: the up button steps as Enter does, down as
// Shift+Enter, and close as Escape, after which the pane has its keys.
func TestFindBarButtons(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	frame()
	frame()
	pane := u.nav.focused()
	r.Queue(press("F", key.ModCtrl|key.ModShift))
	frame()
	frame()
	r.Queue(key.EditEvent{Text: "e"})
	frame()
	frame()
	n := len(u.find.res.Matches)
	if n < 2 || u.find.label() != fmt.Sprintf("1 of %d", n) {
		t.Fatalf("e: %q with %d matches", u.find.label(), n)
	}
	for _, s := range []struct {
		c    *widget.Clickable
		want int
	}{{&u.find.up, 2}, {&u.find.up, 3}, {&u.find.down, 2}, {&u.find.down, 1}, {&u.find.down, n}} {
		s.c.Click()
		frame()
		if want := fmt.Sprintf("%d of %d", s.want, n); u.find.label() != want {
			t.Fatalf("after a click: %q, want %q", u.find.label(), want)
		}
	}
	u.find.shut.Click()
	frame()
	if u.find.pane != "" {
		t.Fatal("close left the bar open")
	}
	if last := b.Sent()[len(b.Sent())-1]; last != (proto.Scroll{Pane: pane, Lines: -1 << 30}) {
		t.Fatalf("close sent %#v last", last)
	}
	frame()
	r.Queue(press("Q", 0), key.EditEvent{Text: "q"})
	frame()
	var s string
	for _, m := range b.Sent() {
		if m, ok := m.(proto.Input); ok {
			s += string(m.Data)
		}
	}
	if s != "q" {
		t.Fatalf("after close the pane got %q", s)
	}
}

// level0 is a FakeBackend whose daemon predates proto.Level 1, as the
// daemon of v0.1.0-alpha.22 does.
type level0 struct{ *FakeBackend }

func (level0) Link() Link { return Link{} }
func (level0) Reconnect() {}
func (level0) Restart()   {}

// TestFindBarOldDaemon checks the find key still opens the bar against a
// daemon that does not know proto.Search, which it would drop the
// connection on, and that typing sends no Search and reaches no pane.
func TestFindBarOldDaemon(t *testing.T) {
	b := level0{NewFakeBackend()}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	frame()
	frame()
	pane := u.nav.focused()
	r.Queue(press("F", key.ModCtrl|key.ModShift))
	frame()
	frame()
	r.Queue(key.EditEvent{Text: "release"})
	frame()
	frame()
	if u.find.pane != pane || !u.find.ed.ReadOnly || u.find.ed.Text() != "" {
		t.Fatalf("bar on %q (want %q), read-only %v, text %q", u.find.pane, pane, u.find.ed.ReadOnly, u.find.ed.Text())
	}
	for _, m := range b.Sent() {
		switch m.(type) {
		case proto.Search, proto.Input:
			t.Fatalf("sent %#v to a daemon at level 0", m)
		}
	}
}
