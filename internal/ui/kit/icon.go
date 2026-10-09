package kit

import (
	"image"
	"image/color"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// IconButton draws a square button of size s in c, filled and pressed as
// Button is, with icon drawing its glyph in a 14dp box in the color given.
// Ghost suits a toolbar of them.
func IconButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, k Kind, s Size, icon func(gtx layout.Context, col color.NRGBA, size int)) layout.Dimensions {
	h, _, _ := s.metrics(th)
	l := lookOf(gtx, th, c, k)
	side := gtx.Dp(h)
	size := image.Pt(side, side)
	draw := func(gtx layout.Context) layout.Dimensions {
		r := gtx.Dp(theme.RadiusControl)
		rect := image.Rectangle{Max: size}
		if l.focus {
			FocusRing(gtx, th, rect, r)
		}
		if l.ring.A > 0 {
			paint.FillShape(gtx.Ops, l.ring, clip.UniformRRect(rect, r).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, l.fill, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
		}
		is := gtx.Dp(14)
		o := op.Offset(image.Pt((side-is)/2, (side-is)/2+l.dy)).Push(gtx.Ops)
		icon(gtx, l.fg, is)
		o.Pop()
		if l.live {
			defer clip.Rect(rect).Push(gtx.Ops).Pop()
			pointer.CursorPointer.Add(gtx.Ops)
		}
		return layout.Dimensions{Size: size}
	}
	gtx.Constraints.Min = image.Point{}
	if !l.live {
		defer paint.PushOpacity(gtx.Ops, 0.5).Pop()
		return draw(gtx)
	}
	return c.Layout(gtx, draw)
}
