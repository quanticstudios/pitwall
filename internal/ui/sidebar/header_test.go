package sidebar

import (
	"image"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestHeaderButtonsFitWithHost: at the default 288dp width, an ssh host
// tag leaves the new tab button inside the header's 18dp padding.
func TestHeaderButtonsFitWithHost(t *testing.T) {
	for _, host := range []string{"", "box", "build-server.example.com"} {
		var s Sidebar
		s.Host = host
		var r input.Router
		var ops op.Ops
		frame := func() {
			ops.Reset()
			gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(288, 56))}
			s.header(gtx, theme.Dark(), "swift-otter")
			r.Frame(&ops)
		}
		frame()
		r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(288-18-14, 28)}) // the middle of the last 28dp button
		frame()
		if !s.newTab.Hovered() {
			t.Errorf("host %q: the new tab button is not at the header's right edge", host)
		}
	}
}
