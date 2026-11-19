package chatin

import (
	"image"
	"image/color"
	"strconv"
	"time"

	"gioui.org/font"
	"gioui.org/io/semantic"
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

// textButton is a 28dp ghost button with a Small label.
func textButton(gtx layout.Context, th *theme.Theme, b *widget.Clickable, label string) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		h, pad := gtx.Dp(28), gtx.Dp(10)
		m := op.Record(gtx.Ops)
		d := text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, label)
		call := m.Stop()
		size := image.Pt(d.Size.X+2*pad, h)
		ghostFill(gtx, th, b, size)
		o := op.Offset(image.Pt(pad, (h-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return layout.Dimensions{Size: size}
	})
}

// iconButton is a 28dp ghost button with a 14dp icon.
func iconButton(gtx layout.Context, th *theme.Theme, b *widget.Clickable, icon, label string) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp(label).Add(gtx.Ops)
		s, is := gtx.Dp(28), gtx.Dp(14)
		ghostFill(gtx, th, b, image.Pt(s, s))
		o := op.Offset(image.Pt((s-is)/2, (s-is)/2)).Push(gtx.Ops)
		sidebar.Icon(gtx, icon, is, th.Muted)
		o.Pop()
		return layout.Dimensions{Size: image.Pt(s, s)}
	})
}

// ghostFill is a ghost control's fill: Hover under the pointer, Pressed
// held down.
func ghostFill(gtx layout.Context, th *theme.Theme, b *widget.Clickable, size image.Point) {
	fill := theme.Transparent
	switch {
	case b.Pressed():
		fill = th.Pressed
	case b.Hovered():
		fill = th.Hover
	}
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
}

// heading is a popover's section title.
func heading(th *theme.Theme, s string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: theme.SpaceS, Right: theme.SpaceS, Top: theme.SpaceS, Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, s)
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
		case b.Pressed():
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		case b.Hovered():
			paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		case on:
			paint.FillShape(gtx.Ops, th.SelectedBg, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		o := op.Offset(image.Pt(gtx.Dp(theme.SpaceS), 0)).Push(gtx.Ops)
		g := gtx
		g.Constraints.Max.X = w - gtx.Dp(36)
		row(g, 0, h, func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, name)
		})
		o.Pop()
		if on {
			is := gtx.Dp(14)
			o := op.Offset(image.Pt(w-gtx.Dp(theme.SpaceS)-is, (h-is)/2)).Push(gtx.Ops)
			sidebar.Icon(gtx, "check", is, th.Readable(th.Primary, th.SurfaceElevated))
			o.Pop()
		}
		return layout.Dimensions{Size: rect.Max}
	})
}

// tipDelay is how long the pointer rests on a control before its tooltip
// shows; within tipNear of another tooltip it shows at once.
const (
	tipDelay = 500 * time.Millisecond
	tipNear  = 300 * time.Millisecond
)

// tip shows label above b, size big at the origin, once the pointer has
// rested on it for tipDelay: centered, but never starting left of b, or
// ending at its right edge for a control at the right end of the row.
func (c *Composer) tip(gtx layout.Context, th *theme.Theme, b *widget.Clickable, size image.Point, label string, right bool) {
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
		if !c.tipSeen.IsZero() && gtx.Now.Sub(c.tipSeen) < tipNear {
			at = gtx.Now.Add(-tipDelay)
		}
		c.hover[b] = at
	}
	if gtx.Now.Sub(at) < tipDelay {
		gtx.Execute(op.InvalidateCmd{At: at.Add(tipDelay)})
		return
	}
	c.tipSeen = gtx.Now
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(480), gtx.Dp(40))}
	rec := op.Record(gtx.Ops)
	d := layout.Inset{Left: theme.SpaceS, Right: theme.SpaceS, Top: theme.SpaceXS + theme.Space2XS, Bottom: theme.SpaceXS + theme.Space2XS}.Layout(g, func(gtx layout.Context) layout.Dimensions {
		return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, label)
	})
	body := rec.Stop()
	x := max(0, (size.X-d.Size.X)/2)
	if right {
		x = size.X - d.Size.X
	}
	o := op.Offset(image.Pt(x, -d.Size.Y-gtx.Dp(6))).Push(gtx.Ops)
	kit.Surface(gtx, image.Rectangle{Max: d.Size}, gtx.Dp(theme.RadiusControl), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	body.Add(gtx.Ops)
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
