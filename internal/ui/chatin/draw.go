package chatin

import (
	"image"
	"image/color"
	"strconv"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func itoa(n int) string { return strconv.Itoa(n) }

func colorOp(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

func medium(f font.Font) font.Font {
	f.Weight = font.Medium
	return f
}

func withAlpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

// text is one line of s, cut at the width.
func text(gtx layout.Context, th *theme.Theme, f font.Font, size unit.Sp, col color.NRGBA, s string) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return widget.Label{MaxLines: 1}.Layout(gtx, th.Shaper, f, size, s, colorOp(gtx, col))
}

// row lays ws out left to right, gap apart, each centered in height h.
func row(gtx layout.Context, gap, h int, ws ...layout.Widget) layout.Dimensions {
	x := 0
	for i, w := range ws {
		if i > 0 {
			x += gap
		}
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(max(0, gtx.Constraints.Max.X-x), h)}
		m := op.Record(gtx.Ops)
		d := w(g)
		call := m.Stop()
		o := op.Offset(image.Pt(x, (h-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		x += d.Size.X
	}
	return layout.Dimensions{Size: image.Pt(x, h)}
}

// heading is a popover's section title.
func heading(th *theme.Theme, s string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: 8, Right: 8, Top: 6, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, s)
		})
	}
}

// menuRow is a popover row: a name and a check on the current one.
func menuRow(gtx layout.Context, th *theme.Theme, b *widget.Clickable, name string, on bool) layout.Dimensions {
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(28)
		w := gtx.Constraints.Max.X
		rect := image.Rectangle{Max: image.Pt(w, h)}
		switch {
		case b.Hovered():
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		case on:
			paint.FillShape(gtx.Ops, th.SelectedBg, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		o := op.Offset(image.Pt(gtx.Dp(8), 0)).Push(gtx.Ops)
		g := gtx
		g.Constraints.Max.X = w - gtx.Dp(36)
		row(g, 0, h, func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, th.UIFont, th.Sp(theme.Body)*12.5/13, th.Fg, name)
		})
		o.Pop()
		if on {
			is := gtx.Dp(14)
			o := op.Offset(image.Pt(w-gtx.Dp(8)-is, (h-is)/2)).Push(gtx.Ops)
			sidebar.Icon(gtx, "check", is, th.Readable(th.Primary, th.SurfaceElevated))
			o.Pop()
		}
		return layout.Dimensions{Size: rect.Max}
	})
}

// segment is one effort in the popover's row of efforts.
func segment(gtx layout.Context, th *theme.Theme, b *widget.Clickable, name string, on bool) layout.Dimensions {
	return layout.Inset{Right: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			h := gtx.Dp(26)
			w := gtx.Constraints.Max.X
			rect := image.Rectangle{Max: image.Pt(w, h)}
			fill, fg := th.SurfaceSecondary, th.Fg
			switch {
			case on:
				fill, fg = th.SelectedBg, th.Readable(th.Primary, th.SelectedBg)
			case b.Hovered():
				fill = th.Pressed
			}
			paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
			m := op.Record(gtx.Ops)
			d := text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), fg, name)
			call := m.Stop()
			o := op.Offset(image.Pt((w-d.Size.X)/2, (h-d.Size.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
			return layout.Dimensions{Size: rect.Max}
		})
	})
}

// tipDelay is how long the pointer rests on a control before its tooltip
// shows.
const tipDelay = 500 * time.Millisecond

// tip shows label above b, size big at the origin, once the pointer has
// rested on it for tipDelay.
func (c *Composer) tip(gtx layout.Context, th *theme.Theme, b *widget.Clickable, size image.Point, label string) {
	if c.hover == nil {
		c.hover = map[*widget.Clickable]time.Time{}
	}
	if !b.Hovered() || c.menu {
		delete(c.hover, b)
		return
	}
	at, ok := c.hover[b]
	if !ok {
		at = gtx.Now
		c.hover[b] = at
	}
	if gtx.Now.Sub(at) < tipDelay {
		gtx.Execute(op.InvalidateCmd{At: at.Add(tipDelay)})
		return
	}
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(320), gtx.Dp(40))}
	rec := op.Record(gtx.Ops)
	d := layout.Inset{Left: 8, Right: 8, Top: 5, Bottom: 5}.Layout(g, func(gtx layout.Context) layout.Dimensions {
		return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, label)
	})
	body := rec.Stop()
	o := op.Offset(image.Pt((size.X-d.Size.X)/2, -d.Size.Y-gtx.Dp(6))).Push(gtx.Ops)
	kit.Surface(gtx, image.Rectangle{Max: d.Size}, gtx.Dp(theme.RadiusControl), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	body.Add(gtx.Ops)
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
