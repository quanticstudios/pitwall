package sidebar

import (
	"image"
	"image/color"
	"strconv"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

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
	for s.boardBt.Clicked(gtx) {
		s.events = append(s.events, ToggleBoard{})
	}
	for s.monitorBt.Clicked(gtx) {
		s.events = append(s.events, ToggleMonitor{})
	}
	hrow(gtx, h-1, gtx.Dp(6), append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
		return s.viewButton(gtx, th)
	}}, item{w: func(gtx layout.Context) layout.Dimensions {
		return s.boardButton(gtx, th)
	}}, item{w: func(gtx layout.Context) layout.Dimensions {
		return toggleButton(gtx, th, &s.monitorBt, s.MonitorOn, icCPU)
	}}, item{w: func(gtx layout.Context) layout.Dimensions {
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
	d := label(g, th, th.UIFont, th.Sp(theme.Small), th.Muted, host)
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
			return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, name)
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

// viewButton switches between the tree and the inbox: an inbox glyph,
// primary on a tinted fill while the inbox shows.
func (s *Sidebar) viewButton(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	return toggleButton(gtx, th, &s.viewBtn, s.Inbox, icInbox)
}

// toggleButton is a header button for a view that is on or off: the glyph,
// primary on a tinted fill while on, filled on hover.
func toggleButton(gtx layout.Context, th *theme.Theme, bt *widget.Clickable, on bool, icon string) layout.Dimensions {
	size := gtx.Dp(28)
	gtx.Constraints = layout.Exact(image.Pt(size, size))
	glyph := gtx.Dp(16)
	box := iconBox(size, glyph)
	return clickable(gtx, bt, func(gtx layout.Context) layout.Dimensions {
		rr := clip.UniformRRect(image.Rect(0, 0, box, box), gtx.Dp(6))
		col := th.Muted
		switch {
		case on:
			fill := theme.Mix(th.Sidebar, th.Primary, 0.14)
			if bt.Hovered() {
				fill = theme.Mix(th.Sidebar, th.Primary, 0.22)
			}
			paint.FillShape(gtx.Ops, fill, rr.Op(gtx.Ops))
			col = th.Primary
		case bt.Hovered():
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, rr.Op(gtx.Ops))
			col = th.Fg
		}
		drawCentered(gtx, box, func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icon, glyph, col, 0)
		})
		return layout.Dimensions{Size: image.Pt(size, size)}
	})
}

// boardButton opens and closes the board: a board glyph, primary on a
// tinted fill while the board shows, with the count of cards that wait on
// the user in a badge.
func (s *Sidebar) boardButton(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	size := gtx.Dp(28)
	gtx.Constraints = layout.Exact(image.Pt(size, size))
	glyph := gtx.Dp(16)
	box := iconBox(size, glyph)
	return clickable(gtx, &s.boardBt, func(gtx layout.Context) layout.Dimensions {
		rr := clip.UniformRRect(image.Rect(0, 0, box, box), gtx.Dp(6))
		col := th.Muted
		switch {
		case s.BoardOn:
			fill := theme.Mix(th.Sidebar, th.Primary, 0.14)
			if s.boardBt.Hovered() {
				fill = theme.Mix(th.Sidebar, th.Primary, 0.22)
			}
			paint.FillShape(gtx.Ops, fill, rr.Op(gtx.Ops))
			col = th.Primary
		case s.boardBt.Hovered():
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, rr.Op(gtx.Ops))
			col = th.Fg
		}
		drawCentered(gtx, box, func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icBoard, glyph, col, 0)
		})
		d := layout.Dimensions{Size: image.Pt(size, size)}
		if s.NeedYou > 0 {
			bg, fg := badgeColors(th)
			n := strconv.Itoa(s.NeedYou)
			m := op.Record(gtx.Ops)
			ld := label(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption)*0.85, fg, n)
			call := m.Stop()
			bh := gtx.Dp(14)
			bw := max(bh, ld.Size.X+gtx.Dp(6))
			at := image.Pt(size-bw, 0) // why: inside the button, whose clip would cut a badge over its corner
			paint.FillShape(gtx.Ops, th.Sidebar, clip.UniformRRect(image.Rectangle{Min: at, Max: at.Add(image.Pt(bw, bh))}.Inset(-gtx.Dp(1)), bh/2+gtx.Dp(1)).Op(gtx.Ops))
			rr := clip.UniformRRect(image.Rectangle{Min: at, Max: at.Add(image.Pt(bw, bh))}, bh/2)
			paint.FillShape(gtx.Ops, bg, rr.Op(gtx.Ops))
			paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Yellow, 0.6), clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
			o := op.Offset(at.Add(image.Pt((bw-ld.Size.X)/2, (bh-ld.Size.Y)/2))).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
		}
		return d
	})
}

// badgeColors are the board button badge's fill and number: a yellow chip
// over the sidebar, as a pill draws one, so the number reads at 4.5:1.
func badgeColors(th *theme.Theme) (bg, fg color.NRGBA) { return th.Chip(th.Yellow, th.Sidebar) }

// icCPU is lucide cpu: the monitor's button.
const icCPU = "M6 4h12a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2zM9 8h6a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1zM12 2v2M12 20v2M17 2v2M17 20v2M7 2v2M7 20v2M2 12h2M2 17h2M2 7h2M20 12h2M20 17h2M20 7h2"

// icBoard is lucide square-kanban.
const icBoard = "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2zM8 7v7M12 7v4M16 7v9"
