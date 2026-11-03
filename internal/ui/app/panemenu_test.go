package app

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestEditScrollbackAction(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	n := nav{keys: aide}
	n.sync(&st)
	got := n.globalOp(&st, "edit_scrollback")
	if got != (proto.EditScrollback{Pane: "a"}) || n.openingWS != "w1" {
		t.Fatalf("action %+v, workspace %q", got, n.openingWS)
	}
}

func TestPaneMenu(t *testing.T) {
	for _, action := range []string{"click", "enter", "escape", "outside"} {
		t.Run(action, func(t *testing.T) {
			b := NewFakeBackend()
			st := b.State()
			u := &ui{b: b, th: theme.Dark(), paneMenu: paneMenu{pane: "a", at: image.Pt(100, 100)}}
			u.nav.sync(&st)
			var r input.Router
			var ops op.Ops
			frame := func() {
				ops.Reset()
				gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(800, 600)), Now: time.Now()}
				u.layoutPaneMenu(gtx, &st)
				r.Frame(&ops)
			}
			frame()
			frame()
			switch action {
			case "enter", "escape":
				name := key.NameReturn
				if action == "escape" {
					name = key.NameEscape
				}
				r.Queue(key.Event{Name: name, State: key.Press})
			default:
				at := f32.Pt(150, 120)
				if action == "outside" {
					at = f32.Pt(20, 20)
				}
				r.Queue(pointer.Event{Kind: pointer.Move, Position: at, Source: pointer.Mouse})
				r.Queue(pointer.Event{Kind: pointer.Press, Position: at, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary})
				r.Queue(pointer.Event{Kind: pointer.Release, Position: at, Source: pointer.Mouse})
			}
			frame()
			frame()
			sent := slices.Contains(b.Sent(), any(proto.EditScrollback{Pane: "a"}))
			if want := action == "click" || action == "enter"; sent != want || u.paneMenu.pane != "" {
				t.Fatalf("sent %v, menu %q", b.Sent(), u.paneMenu.pane)
			}
		})
	}
}

func TestPaneMenuYieldsFocus(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	u := &ui{b: b, th: theme.Dark(), paneMenu: paneMenu{pane: "a", at: image.Pt(100, 100)}}
	u.nav.sync(&st)
	var r input.Router
	var ops op.Ops
	var other int
	frame := func(take bool) {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(800, 600)), Now: time.Now()}
		gtx.Event(key.FocusFilter{Target: &other})
		event.Op(gtx.Ops, &other)
		if take {
			gtx.Execute(key.FocusCmd{Tag: &other})
		}
		u.layoutPaneMenu(gtx, &st)
		r.Frame(&ops)
	}
	frame(false)
	frame(false)
	frame(false)
	frame(true)
	frame(false)
	frame(false)
	if u.paneMenu.pane != "" || !r.Source().Focused(&other) {
		t.Fatal("pane menu kept focus from another editor")
	}
}

func TestPaneMenuHiddenViews(t *testing.T) {
	for _, view := range []string{"settings", "review"} {
		t.Run(view, func(t *testing.T) {
			u, keys := keyWindow(t, conventional)
			u.paneMenu.pane = "a"
			if view == "settings" {
				u.openSettings()
			} else {
				u.review.on = true
				u.review.ws = u.nav.workspace
			}
			keys(press(key.NameCtrl, 0))
			if u.paneMenu.pane != "" {
				t.Fatal("pane menu survives a hidden pane view")
			}
		})
	}
}

func TestPaneMenuAndHints(t *testing.T) {
	for _, first := range []string{"hints", "menu"} {
		t.Run(first, func(t *testing.T) {
			b, u, frame, queue := formWindow(t)
			pane := u.nav.focused()
			if first == "hints" {
				u.openHints(false)
				frame()
				at := u.panes[pane].rect.Min.Add(image.Pt(80, 80))
				pos := f32.Pt(float32(at.X), float32(at.Y))
				queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: pos})
				queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Position: pos, Buttons: pointer.ButtonSecondary})
				queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pos})
				if u.hints.on || u.paneMenu.pane != pane {
					t.Fatalf("right click: hints %v, menu %q", u.hints.on, u.paneMenu.pane)
				}
				queue(key.Event{Name: key.NameReturn, State: key.Press})
				if got := sentOf[proto.EditScrollback](b); len(got) != 1 || got[0].Pane != pane {
					t.Fatalf("menu Enter sent %+v", got)
				}
			} else {
				u.paneMenu.pane = pane
				frame()
				frame()
				queue(key.Event{Name: "G", Modifiers: key.ModCtrl | key.ModShift, State: key.Press})
				if !u.hints.on || u.paneMenu.pane != "" {
					t.Fatalf("hints chord: hints %v, menu %q", u.hints.on, u.paneMenu.pane)
				}
				queue(key.Event{Name: key.NameEscape, State: key.Press})
				if u.hints.on {
					t.Fatal("Escape kept hints open")
				}
			}
			if got := sentOf[proto.Input](b); len(got) != 0 {
				t.Fatalf("pane received mode keys: %+v", got)
			}
		})
	}
}
