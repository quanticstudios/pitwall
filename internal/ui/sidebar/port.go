package sidebar

import (
	"image"
	"image/color"
	"strconv"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// PortChip draws a tab's first listening port on base, ":3010" on a blue
// pill shaped like PRChip's.
func PortChip(gtx layout.Context, th *theme.Theme, port int, base color.NRGBA) layout.Dimensions {
	bg := theme.Mix(base, th.Blue, 0.14)
	h := gtx.Dp(16.5)
	m := op.Record(gtx.Ops)
	gtx.Constraints.Min = image.Point{}
	gtx.Constraints.Max.Y = h
	off := op.Offset(image.Pt(gtx.Dp(6), 0)).Push(gtx.Ops)
	d := hrowFit(gtx, h, 0, item{w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), th.Readable(th.Blue, bg), ":"+strconv.Itoa(port))
	}})
	off.Pop()
	call := m.Stop()
	size := image.Pt(d.Size.X+gtx.Dp(12), h)
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: size}, h/2).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: size}
}
