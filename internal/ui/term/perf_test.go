package term

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// TestTermReduceMotionNoBlink verifies a focused visible cursor does not
// schedule blink invalidations when reduce_motion is enabled.
func TestTermReduceMotionNoBlink(t *testing.T) {
	prev := anim.Reduced()
	anim.SetReduced(true)
	t.Cleanup(func() { anim.SetReduced(prev) })

	var v View
	th := testTheme()
	g := grid("hello")
	g.Cursor = vt.Cursor{X: 2, Y: 0, Visible: true}
	var r input.Router
	var ops op.Ops
	now := time.Unix(1e9, 0)
	gtx := layout.Context{
		Ops:         &ops,
		Source:      r.Source(),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(400, 200)),
		Now:         now,
	}
	v.Layout(gtx, th, g, vt.Modes{}, true)
	r.Frame(&ops)
	if wake, ok := r.WakeupTime(); ok {
		t.Fatalf("reduce_motion scheduled cursor blink wakeup at %v", wake)
	}
}
