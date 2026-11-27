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

// Border paints a w px band in c just inside rect's edge, corners r, and
// leaves what is inside the band as it was.
func Border(gtx layout.Context, rect image.Rectangle, r, w int, c color.NRGBA) {
	// why: a stroke centred on the edge straddles two pixels at half strength and a parent clip cuts its outer half; a band inside stays on whole pixels.
	w = max(w, 1)
	var p clip.Path
	p.Begin(gtx.Ops)
	rrPath(&p, rect, r, false)
	if in := rect.Inset(w); !in.Empty() {
		rrPath(&p, in, max(r-w, 0), true)
	}
	paint.FillShape(gtx.Ops, c, clip.Outline{Path: p.End()}.Op())
}

// rrPath adds the outline of a rounded rectangle to p, clockwise, or
// counterclockwise when rev, which cuts a hole under the non-zero rule.
func rrPath(p *clip.Path, b image.Rectangle, r int, rev bool) {
	r = max(0, min(r, b.Dx()/2, b.Dy()/2))
	x0, y0, x1, y1 := float32(b.Min.X), float32(b.Min.Y), float32(b.Max.X), float32(b.Max.Y)
	rf := float32(r)
	q := rf * 0.44771525 // 1 - 4(√2-1)/3, the cubic's handle for a quarter circle, as clip.RRect
	pt := func(x, y float32) f32.Point {
		if rev {
			x = x0 + x1 - x // the mirror image runs the other way round
		}
		return f32.Pt(x, y)
	}
	p.MoveTo(pt(x0+rf, y0))
	p.LineTo(pt(x1-rf, y0))
	p.CubeTo(pt(x1-q, y0), pt(x1, y0+q), pt(x1, y0+rf))
	p.LineTo(pt(x1, y1-rf))
	p.CubeTo(pt(x1, y1-q), pt(x1-q, y1), pt(x1-rf, y1))
	p.LineTo(pt(x0+rf, y1))
	p.CubeTo(pt(x0+q, y1), pt(x0, y1-q), pt(x0, y1-rf))
	p.LineTo(pt(x0, y0+rf))
	p.CubeTo(pt(x0, y0+q), pt(x0+q, y0), pt(x0+rf, y0))
	p.Close()
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
