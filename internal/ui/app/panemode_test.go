package app

import (
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestPaneMode walks the pane-mode keys against the fake's first tab, a
// left pane a and a right column of b over c. Pane mode stays on after
// each key it knows and ends on anything else.
func TestPaneMode(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	n := nav{keys: aide}
	n.sync(&st)
	ctrlP := press("P", key.ModCtrl)
	if n.key(&st, ctrlP) != nil || !n.paneMode {
		t.Fatal("Ctrl+P did not enter pane mode quietly")
	}
	do := func(e key.Event) any {
		t.Helper()
		msg := n.key(&st, e)
		n.key(&st, key.Event{Name: e.Name, Modifiers: e.Modifiers, State: key.Release})
		if msg != nil {
			if err := b.Send(msg); err != nil {
				t.Fatal(err)
			}
			st = b.State()
		}
		n.sync(&st)
		return msg
	}
	walk := func(name key.Name, want string) {
		t.Helper()
		if msg := do(press(name, 0)); msg != nil {
			t.Fatalf("%s sent %#v", name, msg)
		}
		if n.focused() != want || !n.paneMode {
			t.Fatalf("%s: focus %s, mode %v; want %s and still in pane mode", name, n.focused(), n.paneMode, want)
		}
	}
	walk("L", "b")
	walk("J", "c")
	walk("J", "b")            // the same key again is not swallowed, and wraps
	n.key(&st, press("K", 0)) // a held key repeats its press before any release
	n.key(&st, press("K", 0))
	if n.focused() != "b" { // b, c, b: both presses moved
		t.Fatalf("K held: focus %s, want b", n.focused())
	}
	walk("K", "c")
	walk("H", "a")
	walk(key.NameRightArrow, "b")
	walk(key.NameTab, "c")
	walk("P", "a")

	// f zooms the focused pane until f again, a focus move, or its close.
	walk("F", "a")
	if n.zoomed() != "a" {
		t.Fatalf("f: zoomed %q", n.zoomed())
	}
	walk("F", "a")
	if n.zoomed() != "" {
		t.Fatalf("f twice: zoomed %q", n.zoomed())
	}
	walk("F", "a")
	walk("L", "b")
	if n.zoomed() != "" || n.zoom != "" {
		t.Fatalf("focus moved, still zoomed %q", n.zoom)
	}
	walk("F", "b")
	n.setFocus("c") // a click, or Alt+L
	n.sync(&st)
	if n.zoomed() != "" {
		t.Fatalf("focus moved by click, still zoomed %q", n.zoom)
	}
	n.setFocus("b")
	walk("F", "b")
	if msg := do(press("X", 0)); msg != (proto.ClosePane{Pane: "b"}) || !n.paneMode {
		t.Fatalf("x: %#v, mode %v", msg, n.paneMode)
	}
	if n.focused() == "b" || n.zoomed() != "" || n.zoom != "" {
		t.Fatalf("closed b: focus %s, zoom %q", n.focused(), n.zoom)
	}

	// d and r split down and right; n splits the longer side of the pane.
	at := n.focused()
	if msg := do(press("D", 0)); !reflect.DeepEqual(msg, proto.OpenPane{WorkspaceID: "w1", TabID: "t1", Target: at, Dir: layout.Vertical}) || !n.paneMode {
		t.Fatalf("d: %#v", msg)
	}
	at = n.focused()
	if msg := do(press("R", key.ModShift)); !reflect.DeepEqual(msg, proto.OpenPane{WorkspaceID: "w1", TabID: "t1", Target: at, Dir: layout.Horizontal}) {
		t.Fatalf("shift+r: %#v", msg)
	}
	n.setFocus("a")
	n.area = layout.Rect{W: 600, H: 900} // a is narrow and tall
	if msg := do(press("N", 0)); msg.(proto.OpenPane).Dir != layout.Vertical {
		t.Fatalf("n on a tall pane: %#v", msg)
	}
	n.setFocus("a")
	n.area = layout.Rect{W: 6000, H: 900}
	if msg := do(press("N", 0)); msg.(proto.OpenPane).Dir != layout.Horizontal {
		t.Fatalf("n on a wide pane: %#v", msg)
	}

	// Escape, Enter and unknown keys leave without doing anything.
	for _, e := range []key.Event{press(key.NameEscape, 0), press(key.NameReturn, 0), press("Q", 0), press("C", key.ModCtrl)} {
		n.key(&st, ctrlP)
		focus := n.focused()
		if msg := do(e); msg != nil || n.paneMode || n.focused() != focus {
			t.Fatalf("%v: %#v, mode %v", e, msg, n.paneMode)
		}
	}

	// Ctrl+P twice sends one literal Ctrl+P to the focused pane.
	n.key(&st, ctrlP)
	if msg := n.key(&st, ctrlP); !reflect.DeepEqual(msg, proto.Input{Pane: n.focused(), Data: []byte{0x10}}) || n.paneMode {
		t.Fatalf("Ctrl+P Ctrl+P: %#v", msg)
	}

	// The two modes replace each other.
	n.key(&st, ctrlP)
	n.key(&st, press(tabPrefix, key.ModCtrl))
	if n.paneMode || !n.tabMode {
		t.Fatalf("Ctrl+P Ctrl+T: pane %v tab %v", n.paneMode, n.tabMode)
	}
	n.key(&st, ctrlP)
	if !n.paneMode || n.tabMode {
		t.Fatalf("Ctrl+T Ctrl+P: pane %v tab %v", n.paneMode, n.tabMode)
	}

	// A bound chord leaves pane mode and does what it always does.
	n.key(&st, press("J", key.ModAlt))
	if n.paneMode || n.workspace == "w1" {
		t.Fatalf("Alt+J in pane mode: mode %v at %s", n.paneMode, n.workspace)
	}
}

// TestPaneModeNoLeak drives the window through Gio's router: keys typed in
// pane mode never reach a pane, fullscreen gives the pane the whole area,
// and typing goes back to the pane after Escape.
func TestPaneModeNoLeak(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	sent := func() (typed string, cols map[string]int) {
		cols = map[string]int{}
		for _, m := range b.Sent() {
			switch m := m.(type) {
			case proto.Input:
				typed += string(m.Data)
			case proto.Resize:
				cols[m.Pane] = m.Cols
			}
		}
		return typed, cols
	}
	keys := func(es ...key.Event) {
		for _, e := range es {
			r.Queue(e)
			if e.State == key.Press && len(e.Name) == 1 && e.Modifiers&^key.ModShift == 0 {
				r.Queue(key.EditEvent{Text: string(e.Name[0] | 0x20)})
			}
			frame()
			if e.State == key.Press {
				r.Queue(key.Event{Name: e.Name, Modifiers: e.Modifiers, State: key.Release})
				frame()
			}
		}
	}
	frame()
	frame()
	_, before := sent()
	keys(press("P", key.ModCtrl), press("L", 0), press("F", 0))
	frame()
	if u.nav.zoomed() != "b" {
		t.Fatalf("Ctrl+P l f: zoomed %q", u.nav.zoomed())
	}
	if typed, cols := sent(); typed != "" || cols["b"] <= before["b"] || cols["b"] <= before["a"] {
		t.Fatalf("typed %q; b's columns %d before, %d zoomed (a had %d)", typed, before["b"], cols["b"], before["a"])
	}
	keys(press(key.NameEscape, 0), press("B", 0))
	if typed, _ := sent(); typed != "b" {
		t.Fatalf("after Escape the pane got %q, want b", typed)
	}
	if u.nav.zoomed() != "b" {
		t.Fatal("leaving pane mode dropped fullscreen")
	}
}
