package kit

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestButtonStates presses and releases a button: the press moves the
// label 1dp down and the release clicks; a disabled button takes neither.
func TestButtonStates(t *testing.T) {
	th := theme.Dark()
	for _, k := range []Kind{Primary, Secondary, Ghost, Danger, Primary.Disabled(), Danger.When(false)} {
		var c widget.Clickable
		var r input.Router
		var ops op.Ops
		var last look
		clicked := false
		frame := func() {
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 2, PxPerSp: 2},
				Constraints: layout.Exact(image.Pt(400, 100)), Now: time.Now()}
			clicked = clicked || c.Clicked(gtx)
			last = lookOf(gtx, th, &c, k)
			Button(gtx, th, &c, k, Medium, "Install")
			r.Frame(&ops)
		}
		live := k&off == 0
		p := f32.Pt(10, 10)
		frame()
		r.Queue(pointer.Event{Kind: pointer.Move, Position: p, Source: pointer.Mouse})
		frame()
		r.Queue(pointer.Event{Kind: pointer.Press, Position: p, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
		frame()
		frame()
		if want := map[bool]int{true: 2, false: 0}[live]; last.dy != want {
			t.Errorf("kind %d pressed: label offset %d, want %d", k, last.dy, want)
		}
		r.Queue(pointer.Event{Kind: pointer.Release, Position: p, Source: pointer.Mouse})
		frame()
		frame()
		if clicked != live {
			t.Errorf("kind %d: clicked %v, want %v", k, clicked, live)
		}
		if last.dy != 0 {
			t.Errorf("kind %d released: label offset %d", k, last.dy)
		}
	}
}

func TestButtonColors(t *testing.T) {
	th := theme.Dark()
	var c widget.Clickable
	gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	if l := lookOf(gtx, th, &c, Danger); l.fg != th.OnDanger || l.fill != th.Red {
		t.Errorf("danger: %+v", l)
	}
	if l := lookOf(gtx, th, &c, Ghost); l.ring.A != 0 || l.fg != th.Muted {
		t.Errorf("ghost at rest: %+v", l)
	}
	if l := lookOf(gtx, th, &c, Primary.Disabled()); l.live {
		t.Error("a disabled button is live")
	}
}
