// Package kit holds the drawing pieces every surface shares: the button,
// popover placement and elevation.
package kit

import (
	"image"
	"image/color"

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

// Kind is a button's emphasis.
type Kind uint8

const (
	Primary   Kind = iota // the action a dialog is for
	Secondary             // a bordered neutral button
	Ghost                 // muted text that fills on hover
	Danger                // a destructive action

	off Kind = 1 << 7
)

// Disabled is k drawn at half opacity, taking no clicks.
func (k Kind) Disabled() Kind { return k | off }

// When is k, disabled unless on.
func (k Kind) When(on bool) Kind {
	if on {
		return k
	}
	return k.Disabled()
}

// Size is a button's height.
type Size uint8

const (
	Small  Size = iota // 24dp, 12px text
	Medium             // 28dp, 13px text
	Large              // 32dp, 13px text
)

func (s Size) metrics(th *theme.Theme) (h, pad unit.Dp, ts unit.Sp) {
	switch s {
	case Small:
		return 24, 10, th.Sp(theme.Small)
	case Large:
		return 32, 14, th.Sp(theme.Body)
	}
	return 28, 10, th.Sp(theme.Body)
}

// look is how a button draws in its state this frame.
type look struct {
	fill, ring, fg color.NRGBA
	dy             int  // the label's press offset
	focus          bool // draw the focus ring
	live           bool // takes input
}

func lookOf(gtx layout.Context, th *theme.Theme, c *widget.Clickable, k Kind) look {
	l := look{live: k&off == 0}
	hovered, pressed := l.live && c.Hovered(), l.live && c.Pressed()
	l.focus = l.live && gtx.Focused(c)
	if pressed {
		l.dy = gtx.Dp(1)
	}
	tint := func(c color.NRGBA) color.NRGBA {
		switch {
		case pressed:
			return theme.Mix(c, th.Fg, 0.16)
		case hovered:
			return theme.Mix(c, th.Fg, 0.08)
		}
		return c
	}
	switch k &^ off {
	case Primary:
		l.fill, l.fg = tint(th.Primary), th.OnPrimary
		l.ring = l.fill
	case Danger:
		l.fill, l.fg = tint(th.Red), th.OnDanger
		l.ring = l.fill
	case Ghost:
		l.fg = th.Muted
		switch {
		case pressed:
			l.fill, l.ring, l.fg = th.Pressed, th.Pressed, th.Fg
		case hovered:
			l.fill, l.ring, l.fg = th.Hover, th.Hover, th.Fg
		}
	default:
		l.fill, l.fg = th.SurfaceSecondary, th.Fg
		switch {
		case pressed:
			l.fill = th.Pressed
		case hovered:
			l.fill = th.Hover
		}
		l.ring = theme.Mix(l.fill, th.Fg, 0.08)
	}
	return l
}

// Button draws a button labeled label in c and returns its size. A
// pressed one moves its label down 1dp, a disabled one draws at half
// opacity and takes no input, and a focused one has a ring 2dp outside.
func Button(gtx layout.Context, th *theme.Theme, c *widget.Clickable, k Kind, s Size, label string) layout.Dimensions {
	h, pad, ts := s.metrics(th)
	gtx.Constraints.Min = image.Point{}
	l := lookOf(gtx, th, c, k)
	m := op.Record(gtx.Ops)
	f := th.UIFont
	f.Weight = font.Medium
	td := widget.Label{MaxLines: 1, WrapPolicy: text.WrapGraphemes}.Layout(gtx, th.Shaper, f, ts, label, colorOp(gtx, l.fg))
	call := m.Stop()
	size := image.Pt(min(td.Size.X+2*gtx.Dp(pad), gtx.Constraints.Max.X), gtx.Dp(h))
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
		o := op.Offset(size.Sub(td.Size).Div(2).Add(image.Pt(0, l.dy))).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		if l.live {
			defer clip.Rect(rect).Push(gtx.Ops).Pop()
			pointer.CursorPointer.Add(gtx.Ops)
		}
		return layout.Dimensions{Size: size}
	}
	if !l.live {
		defer paint.PushOpacity(gtx.Ops, 0.5).Pop()
		return draw(gtx)
	}
	return c.Layout(gtx, draw)
}

// Row draws buttons right-aligned in the width, gap apart, the first one
// rightmost, and returns the row's size.
func Row(gtx layout.Context, gap unit.Dp, buttons ...layout.Widget) layout.Dimensions {
	x, h := gtx.Constraints.Max.X, 0
	for i, b := range buttons {
		if i > 0 {
			x -= gtx.Dp(gap)
		}
		m := op.Record(gtx.Ops)
		d := b(gtx)
		call := m.Stop()
		x -= d.Size.X
		o := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		h = max(h, d.Size.Y)
	}
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

// FocusRing outlines rect, with corner radius r, for keyboard focus: a
// 2dp ring 2dp outside it.
func FocusRing(gtx layout.Context, th *theme.Theme, rect image.Rectangle, r int) {
	g := gtx.Dp(2)
	path := clip.UniformRRect(rect.Inset(-g-g/2), r+g+g/2).Path(gtx.Ops)
	paint.FillShape(gtx.Ops, theme.Mix(th.Primary, th.Bg, 0.3), clip.Stroke{Path: path, Width: float32(g)}.Op())
}

func colorOp(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}
