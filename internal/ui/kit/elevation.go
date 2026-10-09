package kit

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// Elevation levels: a hover card, a menu or popover, a dialog or overlay.
const (
	Raised   = 1
	Floating = 2
	Modal    = 3
)

type shadowLayer struct {
	spread unit.Dp
	alpha  uint8
}

var shadows = [...]struct {
	drop   unit.Dp
	layers []shadowLayer
}{
	Raised:   {3, []shadowLayer{{2, 0x30}, {6, 0x18}}},
	Floating: {4, []shadowLayer{{2, 0x48}, {8, 0x1c}}},
	Modal:    {8, []shadowLayer{{6, 0x22}, {12, 0x1a}, {18, 0x12}}},
}

// Shadow draws the shadow of a surface at rect with corner radius r at
// elevation level, under where the surface goes.
func Shadow(gtx layout.Context, rect image.Rectangle, r, level int) {
	s := shadows[min(max(level, Raised), Modal)]
	at := rect.Add(image.Pt(0, gtx.Dp(s.drop)))
	for _, l := range s.layers {
		g := gtx.Dp(l.spread)
		paint.FillShape(gtx.Ops, color.NRGBA{A: l.alpha}, clip.UniformRRect(at.Inset(-g), r+g).Op(gtx.Ops))
	}
}

// Surface draws a floating surface at rect: its shadow, a 1px ring and the
// fill.
func Surface(gtx layout.Context, rect image.Rectangle, r, level int, ring, fill color.NRGBA) {
	Shadow(gtx, rect, r, level)
	paint.FillShape(gtx.Ops, ring, clip.UniformRRect(rect, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect.Inset(1), max(r-1, 0)).Op(gtx.Ops))
}

// PopIn fades and scales in what is drawn at r, by t from 0 to 1: from
// 98.5% and rise lower than its place. It returns the call that ends it.
func PopIn(gtx layout.Context, t float32, r image.Rectangle, rise unit.Dp) func() {
	fade := paint.PushOpacity(gtx.Ops, t)
	scale := 0.985 + 0.015*t
	center := f32.Pt(float32(r.Min.X+r.Max.X)/2, float32(r.Min.Y+r.Max.Y)/2)
	move := op.Affine(f32.AffineId().Scale(center, f32.Pt(scale, scale)).Offset(f32.Pt(0, float32(gtx.Dp(rise))*(1-t)))).Push(gtx.Ops)
	return func() { move.Pop(); fade.Pop() }
}
