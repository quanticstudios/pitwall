package settings

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Sizes follow aide's SettingsPage at its 13px body text, scaled with the
// configured UI size.
func (p *Page) sp(d float32) unit.Sp { return p.th.TextSize * unit.Sp(d/13) }

func weight(f font.Font, w font.Weight) font.Font { f.Weight = w; return f }

func colorOp(gtx gl.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// text draws one line, cut to the width, and returns its size.
func (p *Page) text(gtx gl.Context, f font.Font, size unit.Sp, c color.NRGBA, s string) gl.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return widget.Label{MaxLines: 1}.Layout(gtx, p.th.Shaper, f, size, s, colorOp(gtx, c))
}

// para draws wrapping text.
func (p *Page) para(gtx gl.Context, f font.Font, size unit.Sp, c color.NRGBA, s string) gl.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return widget.Label{}.Layout(gtx, p.th.Shaper, f, size, s, colorOp(gtx, c))
}

func rrect(gtx gl.Context, c color.NRGBA, r image.Rectangle, radius int) {
	paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, radius).Op(gtx.Ops))
}

// boxed draws w over a rounded fill with a 1px border; border may equal
// fill for none.
func boxed(gtx gl.Context, fill, border color.NRGBA, radius int, pad image.Point, w gl.Widget) gl.Dimensions {
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints.Max = g.Constraints.Max.Sub(pad.Mul(2))
	g.Constraints.Min = image.Point{X: max(0, g.Constraints.Min.X-2*pad.X), Y: max(0, g.Constraints.Min.Y-2*pad.Y)}
	d := w(g)
	call := m.Stop()
	sz := d.Size.Add(pad.Mul(2))
	rrect(gtx, border, image.Rectangle{Max: sz}, radius)
	rrect(gtx, fill, image.Rect(1, 1, sz.X-1, sz.Y-1), max(0, radius-1))
	o := op.Offset(pad).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return gl.Dimensions{Size: sz, Baseline: d.Baseline + pad.Y}
}

type btnKind = kit.Kind

const (
	ghost     = kit.Ghost
	secondary = kit.Secondary
	primary   = kit.Primary
	danger    = kit.Danger
)

// button is the medium button: 28dp, 13px medium.
func (p *Page) button(gtx gl.Context, c *widget.Clickable, kind kit.Kind, label string) gl.Dimensions {
	return kit.Button(gtx, p.th, c, kind, kit.Medium, label)
}

// keycap is aide's .keycap: 20px tall, surface gradient, border ring. Hot
// draws it as the one being recorded.
func (p *Page) keycap(gtx gl.Context, s string, hot, hover bool) gl.Dimensions {
	th := p.th
	fg := theme.Mix(th.Muted, th.Fg, 0.54)
	if hot || hover {
		fg = th.Fg
	}
	m := op.Record(gtx.Ops)
	td := p.text(gtx, th.UIFont, p.th.Sp(theme.Small), fg, s)
	call := m.Stop()
	h := gtx.Dp(22)
	sz := image.Pt(max(td.Size.X+2*gtx.Dp(7), h), h)
	r := gtx.Dp(4)
	ring := th.Border
	if hot {
		ring = th.Primary
	} else if hover {
		ring = theme.Mix(th.Border, th.Fg, 0.25)
	}
	rrect(gtx, ring, image.Rectangle{Max: sz}, r)
	in := clip.UniformRRect(image.Rect(1, 1, sz.X-1, sz.Y-1), r-1).Push(gtx.Ops)
	top := th.SurfaceElevated
	if hot {
		top = theme.Mix(th.SurfaceElevated, th.Primary, 0.18)
	}
	paint.LinearGradientOp{
		Stop1: f32.Pt(0, 0), Color1: top,
		Stop2: f32.Pt(0, float32(sz.Y)), Color2: th.Surface,
	}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	in.Pop()
	o := op.Offset(sz.Sub(td.Size).Div(2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return gl.Dimensions{Size: sz}
}

// icon draws a small stroked glyph in a size×size box.
func icon(gtx gl.Context, c color.NRGBA, size int, draw func(p *clip.Path, s float32)) gl.Dimensions {
	var path clip.Path
	path.Begin(gtx.Ops)
	s := float32(size) / 16
	draw(&path, s)
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: path.End(), Width: 1.5 * s}.Op())
	return gl.Dimensions{Size: image.Pt(size, size)}
}

// SearchIcon draws the search field's magnifier in a size×size box.
func SearchIcon(gtx gl.Context, c color.NRGBA, size int) { icon(gtx, c, size, searchGlyph) }

func searchGlyph(p *clip.Path, s float32) {
	// a circle of radius 4.5 at (7,7) and a handle to (13.5,13.5)
	c := f32.Pt(7*s, 7*s)
	p.MoveTo(c.Add(f32.Pt(4.5*s, 0)))
	p.ArcTo(c, c, 6.2832)
	p.MoveTo(f32.Pt(10.3*s, 10.3*s))
	p.LineTo(f32.Pt(13.5*s, 13.5*s))
}

func resetGlyph(p *clip.Path, s float32) {
	// a counter-clockwise arrow: three quarters of a circle and a head
	c := f32.Pt(8*s, 8.5*s)
	p.MoveTo(f32.Pt(3*s, 8.5*s))
	p.ArcTo(c, c, -4.7124)
	p.MoveTo(f32.Pt(3*s, 4*s))
	p.LineTo(f32.Pt(3*s, 8.5*s))
	p.LineTo(f32.Pt(7.5*s, 8.5*s))
}

func plusGlyph(p *clip.Path, s float32) {
	p.MoveTo(f32.Pt(8*s, 3.5*s))
	p.LineTo(f32.Pt(8*s, 12.5*s))
	p.MoveTo(f32.Pt(3.5*s, 8*s))
	p.LineTo(f32.Pt(12.5*s, 8*s))
}

func minusGlyph(p *clip.Path, s float32) {
	p.MoveTo(f32.Pt(3.5*s, 8*s))
	p.LineTo(f32.Pt(12.5*s, 8*s))
}

func chevronGlyph(p *clip.Path, s float32) {
	p.MoveTo(f32.Pt(4.5*s, 6.5*s))
	p.LineTo(f32.Pt(8*s, 10*s))
	p.LineTo(f32.Pt(11.5*s, 6.5*s))
}

func checkGlyph(p *clip.Path, s float32) {
	p.MoveTo(f32.Pt(3.5*s, 8.5*s))
	p.LineTo(f32.Pt(6.5*s, 11.5*s))
	p.LineTo(f32.Pt(12.5*s, 4.5*s))
}

// iconButton is a 28px square ghost button with a glyph.
func (p *Page) iconButton(gtx gl.Context, c *widget.Clickable, glyph func(*clip.Path, float32)) gl.Dimensions {
	th := p.th
	return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		sz := gtx.Dp(28)
		fg := th.Muted
		if c.Hovered() {
			rrect(gtx, th.SurfaceElevated, image.Rect(0, 0, sz, sz), gtx.Dp(6))
			fg = th.Fg
		}
		is := gtx.Dp(16)
		o := op.Offset(image.Pt((sz-is)/2, (sz-is)/2)).Push(gtx.Ops)
		icon(gtx, fg, is, glyph)
		o.Pop()
		defer clip.Rect{Max: image.Pt(sz, sz)}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: image.Pt(sz, sz)}
	})
}

// hstack lays ws out left to right, gap dp apart, centered vertically.
func hstack(gtx gl.Context, gap unit.Dp, ws ...gl.Widget) gl.Dimensions {
	var kids []gl.FlexChild
	for i, w := range ws {
		if i > 0 {
			kids = append(kids, gl.Rigid(gl.Spacer{Width: gap}.Layout))
		}
		kids = append(kids, gl.Rigid(w))
	}
	return gl.Flex{Alignment: gl.Middle}.Layout(gtx, kids...)
}
