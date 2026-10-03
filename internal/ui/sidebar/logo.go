package sidebar

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// drawLogo is the P of packaging/pitwall.svg without its tile: a bowl
// stroke over three status pills, scaled into a size-square box. The
// pills' separator stroke takes the sidebar's color so it reads as a gap,
// and the bowl's white gradient follows the theme's text on light themes.
func drawLogo(gtx layout.Context, th *theme.Theme, size int) layout.Dimensions {
	// The P spans x 73.5-189, y 53.5-202.5 of the 256 viewBox, strokes
	// included.
	s := float32(size) / 149
	defer op.Affine(f32.AffineId().Offset(f32.Pt(-73.5, -53.5)).Scale(f32.Point{}, f32.Pt(s, s)).
		Offset(f32.Pt((float32(size)-115.5*s)/2, 0))).Push(gtx.Ops).Pop()

	// Bowl: M91 70h50 a34 34 0 0 1 0 68 h-50, width 28. Its round caps sit
	// under the pills.
	const k = 34 * 0.5523 // cubic handle for a quarter circle
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(91, 70))
	p.LineTo(f32.Pt(141, 70))
	p.CubeTo(f32.Pt(141+k, 70), f32.Pt(175, 104-k), f32.Pt(175, 104))
	p.CubeTo(f32.Pt(175, 104+k), f32.Pt(141+k, 138), f32.Pt(141, 138))
	p.LineTo(f32.Pt(91, 138))
	from, to := theme.Hex("#ffffff"), theme.Hex("#b9c6da")
	if int(th.Fg.R)+int(th.Fg.G)+int(th.Fg.B) < 384 {
		from, to = th.Fg, theme.Mix(th.Fg, th.Primary, 0.35)
	}
	bowl := clip.Stroke{Path: p.End(), Width: 28}.Op().Push(gtx.Ops)
	paint.LinearGradientOp{Stop1: f32.Pt(91, 70), Color1: from, Stop2: f32.Pt(175, 138), Color2: to}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	bowl.Pop()

	for i, c := range []color.NRGBA{th.Blue, th.Yellow, th.Green} {
		r := image.Rect(76, 56+50*i, 106, 100+50*i)
		rr := clip.UniformRRect(r, 15)
		paint.FillShape(gtx.Ops, c, rr.Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.Sidebar, clip.Stroke{Path: rr.Path(gtx.Ops), Width: 5}.Op())
	}
	return layout.Dimensions{Size: image.Pt(size, size)}
}
