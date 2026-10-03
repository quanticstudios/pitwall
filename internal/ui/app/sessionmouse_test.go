package app

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The switcher by mouse: Keep and Kill on the inline confirm, a click on a
// row switches, a click outside the card closes it.
func TestSessionSwitcherMouse(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
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
	st := b.State()
	u.sw.openAt(&st, "s1", "pick", time.Now().Add(-time.Second))
	u.sw.setSel("s2", time.Now())
	u.sw.mode = modeKill
	frame()
	frame()
	click := func(x, y float32) {
		p := f32.Pt(x, y)
		r.Queue(pointer.Event{Kind: pointer.Move, Position: p, Source: pointer.Mouse})
		frame()
		r.Queue(pointer.Event{Kind: pointer.Press, Position: p, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
		frame()
		r.Queue(pointer.Event{Kind: pointer.Release, Position: p, Source: pointer.Mouse})
		frame()
		frame()
	}
	click(605, 312)
	for _, m := range b.Sent() {
		if _, ok := m.(proto.SessionKill); ok {
			t.Fatal("killed")
		}
	}
	if u.sw.mode != modePick || !u.sw.open {
		t.Fatalf("Keep: mode %v open %v", u.sw.mode, u.sw.open)
	}
	u.sw.mode = modeKill
	frame()
	click(656, 312)
	if got := b.Sent()[len(b.Sent())-1]; got != (proto.SessionKill{SessionID: "s2"}) {
		t.Fatalf("Kill sent %#v", got)
	}
	click(480, 301) // brave-lynx moved up into the killed session's row
	if u.nav.session != "s3" || u.sw.open {
		t.Fatalf("row click: at %s, open %v", u.nav.session, u.sw.open)
	}
	u.sw.openAt(&st, "s3", "pick", time.Now().Add(-time.Second))
	frame()
	click(357, 603) // New session
	if !u.sw.open || u.sw.mode != modeNew {
		t.Fatalf("New session: open %v mode %v", u.sw.open, u.sw.mode)
	}
	u.sw.mode = modePick
	frame()
	click(100, 700)
	if u.sw.open {
		t.Fatal("a click outside the card left it open")
	}
}
