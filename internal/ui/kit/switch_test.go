package kit

import (
	"image"
	"testing"

	"gioui.org/io/input"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestSwitchTouchAreaAndSemantics(t *testing.T) {
	for _, on := range []bool{false, true} {
		var c widget.Clickable
		var r input.Router
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Min: image.Pt(0, 32), Max: image.Pt(100, 100)}}
		d := Switch(gtx, theme.Dark(), &c, on)
		if d.Size != image.Pt(32, 32) {
			t.Fatalf("touch area: %v", d.Size)
		}
		r.Frame(&ops)
		found := false
		for _, n := range r.AppendSemantics(nil) {
			if n.Desc.Class == semantic.Switch {
				found = true
				if n.Desc.Selected != on {
					t.Fatal("switch misreports its state")
				}
			}
		}
		if !found {
			t.Fatal("switch has no Switch semantic role")
		}
	}
}
