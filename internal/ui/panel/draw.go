package panel

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// pal is the mock's palette in the theme's colors.
type pal struct {
	bg, fg, muted, quiet, soft, border, border2 color.NRGBA
	card, card2, graph, node                    color.NRGBA
	blue, green, yellow, red, purple, primary   color.NRGBA
}

func palette(th *theme.Theme) pal {
	return pal{
		bg: th.Bg, fg: th.Fg, muted: th.Muted,
		quiet:   theme.Mix(th.Bg, th.Muted, 0.72),
		soft:    theme.Mix(th.Bg, th.Fg, 0.06),
		border:  th.Border,
		border2: theme.Mix(th.Border, th.Fg, 0.07),
		card:    theme.Mix(th.Bg, th.Surface, 0.4),
		card2:   theme.Mix(th.Bg, th.Surface, 0.7),
		graph:   theme.Mix(th.Bg, th.Surface, 0.2),
		node:    theme.Mix(th.Bg, th.Surface, 0.75),
		blue:    th.Blue, green: th.Green, yellow: th.Yellow, red: th.Red, purple: th.Purple, primary: th.Primary,
	}
}

func semibold(f font.Font) font.Font { f.Weight = font.SemiBold; return f }
func medium(f font.Font) font.Font   { f.Weight = font.Medium; return f }

func material(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// label draws txt in at most lines lines, 0 for any, cut with an ellipsis.
func label(gtx layout.Context, th *theme.Theme, f font.Font, size unit.Sp, c color.NRGBA, txt string, lines int) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	l := widget.Label{MaxLines: lines}
	if lines == 1 {
		l.WrapPolicy = text.WrapGraphemes
	}
	if lines != 1 {
		l.LineHeightScale = 1.35
	}
	if lines == 2 {
		l.LineHeightScale = 1.12 // two-line titles
	}
	return l.Layout(gtx, th.Shaper, f, size, txt, material(gtx, c))
}

// rrect fills a rounded rectangle with a 1dp border of bc; bc's zero value
// draws none.
func rrect(gtx layout.Context, r image.Rectangle, rad int, fill, bc color.NRGBA) {
	if bc.A != 0 {
		paint.FillShape(gtx.Ops, bc, clip.UniformRRect(r, rad).Op(gtx.Ops))
		r = r.Inset(gtx.Dp(1))
		rad = max(0, rad-gtx.Dp(1))
	}
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(r, rad).Op(gtx.Ops))
}

// dashedBorder draws a 1dp dashed outline of a rounded rectangle: solid
// corners, 4dp dashes along the sides.
func dashedBorder(gtx layout.Context, r image.Rectangle, rad int, c color.NRGBA) {
	w := float32(gtx.Dp(1))
	inner := clip.UniformRRect(r, rad)
	defer clip.Stroke{Path: inner.Path(gtx.Ops), Width: w * 2}.Op().Push(gtx.Ops).Pop()
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	var p clip.Path
	p.Begin(gtx.Ops)
	box := func(x0, y0, x1, y1 int) {
		p.MoveTo(f32.Pt(float32(x0), float32(y0)))
		p.LineTo(f32.Pt(float32(x1), float32(y0)))
		p.LineTo(f32.Pt(float32(x1), float32(y1)))
		p.LineTo(f32.Pt(float32(x0), float32(y1)))
		p.Close()
	}
	box(r.Min.X, r.Min.Y, r.Min.X+rad, r.Min.Y+rad)
	box(r.Max.X-rad, r.Min.Y, r.Max.X, r.Min.Y+rad)
	box(r.Min.X, r.Max.Y-rad, r.Min.X+rad, r.Max.Y)
	box(r.Max.X-rad, r.Max.Y-rad, r.Max.X, r.Max.Y)
	dash, gap := gtx.Dp(4), gtx.Dp(4)
	for x := r.Min.X + rad + gap/2; x < r.Max.X-rad; x += dash + gap {
		e := min(x+dash, r.Max.X-rad)
		box(x, r.Min.Y, e, r.Min.Y+rad)
		box(x, r.Max.Y-rad, e, r.Max.Y)
	}
	for y := r.Min.Y + rad + gap/2; y < r.Max.Y-rad; y += dash + gap {
		e := min(y+dash, r.Max.Y-rad)
		box(r.Min.X, y, r.Min.X+rad, e)
		box(r.Max.X-rad, y, r.Max.X, e)
	}
	paint.FillShape(gtx.Ops, c, clip.Outline{Path: p.End()}.Op())
}

// stroke draws a polyline.
func stroke(gtx layout.Context, w float32, c color.NRGBA, pts ...f32.Point) {
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(pts[0])
	for _, q := range pts[1:] {
		p.LineTo(q)
	}
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: w}.Op())
}

// bezier draws the graph's edge from a to b, a vertical S-curve, dashed
// when not reached.
func bezier(gtx layout.Context, a, b f32.Point, w float32, c color.NRGBA, dashed bool) {
	my := (a.Y + b.Y) / 2
	at := func(t float32) f32.Point {
		u := 1 - t
		c1, c2 := f32.Pt(a.X, my), f32.Pt(b.X, my)
		return a.Mul(u * u * u).Add(c1.Mul(3 * u * u * t)).Add(c2.Mul(3 * u * t * t)).Add(b.Mul(t * t * t))
	}
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(a)
	if !dashed {
		p.CubeTo(f32.Pt(a.X, my), f32.Pt(b.X, my), b)
	} else {
		const n = 64
		dash := float32(gtx.Dp(4))
		run, on, prev := float32(0), true, a
		for i := 1; i <= n; i++ {
			q := at(float32(i) / n)
			d := q.Sub(prev)
			run += float32(math.Hypot(float64(d.X), float64(d.Y)))
			if on {
				p.LineTo(q)
			} else {
				p.MoveTo(q)
			}
			if run >= dash {
				run, on = 0, !on
			}
			prev = q
		}
	}
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: w}.Op())
}

// circle fills a circle of diameter d at off.
func circle(gtx layout.Context, off image.Point, d int, c color.NRGBA) {
	paint.FillShape(gtx.Ops, c, clip.Ellipse{Min: off, Max: off.Add(image.Pt(d, d))}.Op(gtx.Ops))
}

// ring strokes a circle of diameter d at off.
func ring(gtx layout.Context, off image.Point, d int, w float32, c color.NRGBA) {
	in := int(w / 2)
	e := clip.Ellipse{Min: off.Add(image.Pt(in, in)), Max: off.Add(image.Pt(d-in, d-in))}
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: e.Path(gtx.Ops), Width: w}.Op())
}

// spinner is the mock's 0.8s spinning ring, a lit quarter on a faint one.
func spinner(gtx layout.Context, d int, bg, c color.NRGBA, t float64) layout.Dimensions {
	w := float32(gtx.Dp(1.5))
	ring(gtx, image.Point{}, d, w, theme.Mix(bg, c, 0.25))
	r := (float32(d) - w) / 2
	ctr := f32.Pt(float32(d)/2, float32(d)/2)
	_, frac := math.Modf(t / 0.8)
	a := float32(frac * 2 * math.Pi)
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(ctr.Add(f32.Pt(r*float32(math.Cos(float64(a))), r*float32(math.Sin(float64(a))))))
	p.ArcTo(ctr, ctr, math.Pi/2)
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: w}.Op())
	return layout.Dimensions{Size: image.Pt(d, d)}
}

// check draws a check mark in a d box.
func check(gtx layout.Context, d int, w float32, c color.NRGBA) layout.Dimensions {
	s := float32(d)
	stroke(gtx, w, c, f32.Pt(0.18*s, 0.52*s), f32.Pt(0.42*s, 0.74*s), f32.Pt(0.84*s, 0.28*s))
	return layout.Dimensions{Size: image.Pt(d, d)}
}

// pulse is the mock's 1.4s opacity pulse, 1 to 0.35 and back.
func pulse(t float64) float32 {
	return float32(0.675 + 0.325*math.Cos(t*2*math.Pi/1.4))
}

// avatar is a subagent's mark, four petals in its hue around a dark eye.
func avatar(gtx layout.Context, h float64, d int, bg color.NRGBA) layout.Dimensions {
	a, b := hsl(h, 0.70, 0.62), hsl(h, 0.65, 0.45)
	u := float32(d) / 24
	dot := func(cx, cy, r float32, c color.NRGBA) {
		paint.FillShape(gtx.Ops, c, clip.Ellipse{
			Min: image.Pt(int(math.Round(float64((cx-r)*u))), int(math.Round(float64((cy-r)*u)))),
			Max: image.Pt(int(math.Round(float64((cx+r)*u))), int(math.Round(float64((cy+r)*u)))),
		}.Op(gtx.Ops))
	}
	dot(8, 8, 5.5, a)
	dot(16, 8, 5.5, b)
	dot(8, 16, 5.5, b)
	dot(16, 16, 5.5, a)
	dot(12, 12, 3, bg)
	return layout.Dimensions{Size: image.Pt(d, d)}
}

// hsl converts CSS hsl() to a color; h in degrees, s and l in 0..1.
func hsl(h, s, l float64) color.NRGBA {
	f := func(n float64) uint8 {
		k := math.Mod(n+h/30, 12)
		a := s * min(l, 1-l)
		return uint8(math.Round(255 * (l - a*max(-1, min(k-3, 9-k, 1)))))
	}
	return color.NRGBA{R: f(0), G: f(8), B: f(4), A: 0xff}
}

// clickable wraps c with a pointer cursor.
func clickable(gtx layout.Context, c *widget.Clickable, w layout.Widget) layout.Dimensions {
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		d := w(gtx)
		defer clip.Rect{Max: d.Size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return d
	})
}

// part is one child of row; flex marks the one that takes the width left.
type part struct {
	w    layout.Widget
	flex bool
	gap  int // space before it
}

// row lays parts left to right, top-aligned, the flex part filling what
// the others leave; it is as wide as the constraints.
func row(gtx layout.Context, parts ...part) layout.Dimensions {
	maxW := gtx.Constraints.Max.X
	calls := make([]op.CallOp, len(parts))
	dims := make([]layout.Dimensions, len(parts))
	measure := func(i, avail int) {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(avail, 0), gtx.Constraints.Max.Y)}
		dims[i] = parts[i].w(g)
		calls[i] = m.Stop()
	}
	used := 0
	for i, p := range parts {
		used += p.gap
		if !p.flex {
			measure(i, maxW-used)
			used += dims[i].Size.X
		}
	}
	for i, p := range parts {
		if p.flex {
			measure(i, maxW-used)
			dims[i].Size.X = max(dims[i].Size.X, maxW-used)
		}
	}
	x, h := 0, 0
	for i, p := range parts {
		x += p.gap
		off := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		calls[i].Add(gtx.Ops)
		off.Pop()
		x += dims[i].Size.X
		h = max(h, dims[i].Size.Y)
	}
	return layout.Dimensions{Size: image.Pt(maxW, h)}
}

// fixed is w in a box exactly size wide, any height.
func fixed(width int, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = width
		d := w(gtx)
		d.Size.X = width
		return d
	}
}

// pad insets w.
func pad(top, right, bottom, left unit.Dp, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: top, Right: right, Bottom: bottom, Left: left}.Layout(gtx, w)
	}
}

// boxed draws w with a background drawn by bg once w's size is known.
func boxed(gtx layout.Context, in layout.Inset, w layout.Widget, bg func(size image.Point)) layout.Dimensions {
	m := op.Record(gtx.Ops)
	d := in.Layout(gtx, w)
	call := m.Stop()
	bg(d.Size)
	call.Add(gtx.Ops)
	return d
}

// space is an empty widget h dp tall.
func space(h unit.Dp) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(h))}
	}
}
