package kit

import (
	"image"
	"image/color"

	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Switch draws an on/off control with the theme's track and knob.
func Switch(gtx layout.Context, th *theme.Theme, c *widget.Clickable, on bool) layout.Dimensions {
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		sz := image.Pt(gtx.Dp(32), gtx.Dp(18))
		hit := image.Pt(sz.X, max(sz.Y, gtx.Constraints.Min.Y))
		semantic.Switch.Add(gtx.Ops)
		semantic.SelectedOp(on).Add(gtx.Ops)
		semantic.EnabledOp(gtx.Enabled()).Add(gtx.Ops)
		defer op.Offset(image.Pt(0, (hit.Y-sz.Y)/2)).Push(gtx.Ops).Pop()
		knob, in := gtx.Dp(14), gtx.Dp(2)
		track := theme.Mix(th.SurfaceElevated, th.Fg, 0.12)
		ring, x := track, in
		if on {
			track, ring, x = th.Primary, th.Primary, sz.X-in-knob
		}
		if c.Hovered() {
			track = theme.Mix(track, th.Fg, 0.08)
		}
		paint.FillShape(gtx.Ops, ring, clip.UniformRRect(image.Rectangle{Max: sz}, sz.Y/2).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, track, clip.UniformRRect(image.Rect(1, 1, sz.X-1, sz.Y-1), sz.Y/2-1).Op(gtx.Ops))
		fg := th.Muted
		if on {
			fg = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
		}
		paint.FillShape(gtx.Ops, fg, clip.UniformRRect(image.Rect(x, in, x+knob, in+knob), knob/2).Op(gtx.Ops))
		if gtx.Focused(c) {
			FocusRing(gtx, th, image.Rectangle{Max: sz}, sz.Y/2)
		}
		defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: hit}
	})
}
