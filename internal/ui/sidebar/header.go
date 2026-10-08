package sidebar

import (
	"image"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// switchFlash is how long the header's session name glows after the
// window switched sessions.
const switchFlash = 700 * time.Millisecond

// header is aide's brand bar: h-14, border-b, px-3, a 24px mark, then the
// session's name, which opens the session switcher.
func (s *Sidebar) header(gtx layout.Context, th *theme.Theme, session string) layout.Dimensions {
	h := gtx.Dp(56)
	w := gtx.Constraints.Max.X
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, h-1), Max: image.Pt(w, h)}.Op())
	gtx.Constraints = layout.Exact(image.Pt(w-gtx.Dp(18*2), h-1))
	off := op.Offset(image.Pt(gtx.Dp(18), 0)).Push(gtx.Ops)
	if session == "" {
		session = "pitwall"
	}
	flash := float32(0)
	if since := gtx.Now.Sub(s.switchedAt); !s.switchedAt.IsZero() && since < switchFlash {
		flash = 1 - float32(since)/float32(switchFlash)
		flash *= flash
		gtx.Execute(op.InvalidateCmd{})
	}
	items := []item{
		{w: func(gtx layout.Context) layout.Dimensions {
			return drawLogo(gtx, th, gtx.Dp(22))
		}},
		{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return s.sessionButton(gtx, th, session, flash)
		}},
	}
	if s.Host != "" {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			return hostTag(gtx, th, s.Host)
		}})
	}
	hrow(gtx, h-1, gtx.Dp(6), append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
		return iconButton(gtx, th, &s.newTab, icPlus, gtx.Dp(28), gtx.Dp(16), true)
	}})...)
	off.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// hostTag is the header's ssh host: muted text on a filled tag, at most
// 112dp wide, after the session's name.
func hostTag(gtx layout.Context, th *theme.Theme, host string) layout.Dimensions {
	h, px := gtx.Dp(20), gtx.Dp(6)
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(min(gtx.Constraints.Max.X, gtx.Dp(112))-2*px, h)}
	d := label(g, th, th.UIFont, 12, th.Muted, host)
	call := m.Stop()
	size := image.Pt(d.Size.X+2*px, h)
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(4)).Op(gtx.Ops))
	o := op.Offset(image.Pt(px, (h-d.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: size}
}

// sessionButton is the header's session name with a chevron: hover fills
// it, a click opens the switcher, and it glows as flash fades from 1 to 0.
func (s *Sidebar) sessionButton(gtx layout.Context, th *theme.Theme, name string, flash float32) layout.Dimensions {
	bh, px := gtx.Dp(28), gtx.Dp(6)
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Constraints.Max.X-2*px, bh)}
	d := hrowFit(g, bh, gtx.Dp(6),
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), 14, th.Fg, name)
		}},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icChevronsUpDown, gtx.Dp(14), th.Muted, 0)
		}},
	)
	call := m.Stop()
	size := image.Pt(d.Size.X+2*px, bh)
	gtx.Constraints = layout.Exact(size)
	return clickable(gtx, &s.sessions, func(gtx layout.Context) layout.Dimensions {
		rr := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(6))
		switch {
		case flash > 0:
			paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Primary, 0.28*flash), rr.Op(gtx.Ops))
		case s.sessions.Hovered():
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, rr.Op(gtx.Ops))
		}
		o := op.Offset(image.Pt(px, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return layout.Dimensions{Size: size}
	})
}
