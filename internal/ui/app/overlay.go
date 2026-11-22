package app

import (
	"image"
	"image/color"
	"math"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The centred overlays, the session switcher and the command palette,
// share these pieces: a dimmed backdrop, a card that scales in, a filter
// field, a scrolled list with an easing highlight and a row of key hints.

// backdrop dims the window by t and reports whether it was pressed, which
// closes the overlay.
func (u *ui) backdrop(gtx gl.Context, t float32, tag *int) bool {
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, scrim(u.th, t), clip.Rect{Max: size}.Op())
	pressed := false
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			pressed = true
		}
	}
	bg := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	bg.Pop()
	return pressed
}

// overlayCard draws a w by h card centred in the window, faded and scaled
// in by t, and takes presses on it so they stop short of the backdrop. The
// caller draws the contents from the card's top left, then calls end.
func (u *ui) overlayCard(gtx gl.Context, t float32, w, h int, tag *int) (end func()) {
	return u.overlayCardAt(gtx, t, w, h, (gtx.Constraints.Max.Y-h)/2, tag)
}

// overlayCardAt is overlayCard with the card's top at top, so a card that
// changes height keeps its top still.
func (u *ui) overlayCardAt(gtx gl.Context, t float32, w, h, top int, tag *int) (end func()) {
	th := u.th
	card := image.Rectangle{Max: image.Pt(w, h)}
	at := image.Pt((gtx.Constraints.Max.X-w)/2, top)
	pop := kit.PopIn(gtx, t, card.Add(at), 10)
	move := op.Offset(at).Push(gtx.Ops)
	kit.Surface(gtx, card, gtx.Dp(theme.RadiusCard), kit.Modal, th.BorderSubtle, th.Surface)
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press}); !ok {
			break
		}
	}
	area := clip.Rect(card).Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	area.Pop()
	return func() { move.Pop(); pop() }
}

// scrim is the backdrop under a dialog or overlay, faded in by t.
func scrim(th *theme.Theme, t float32) color.NRGBA {
	c := th.Scrim
	c.A = uint8(float32(c.A) * t)
	return c
}

// listScroll scrolls a list of equal rows so the highlighted one shows,
// and eases the highlight toward it with a 60ms time constant.
type listScroll struct {
	scroll   int
	selY     float32   // the highlight's drawn top; < 0 jumps to the row
	lastAt   time.Time // the frame selY was last eased in
	lastSel  int
	hasSel   bool
	scrolled bool // wheel scrolled this frame: keep scroll and snap selY
	tag      int
}

// handleScroll applies mouse-wheel scrolling over rect to l.scroll.
func (l *listScroll) handleScroll(gtx gl.Context, rect image.Rectangle, n, rowH, gap int) {
	l.scrolled = false
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target:  &l.tag,
			Kinds:   pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20},
		})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok && pe.Kind == pointer.Scroll && pe.Scroll.Y != 0 {
			l.scroll += int(math.Round(float64(pe.Scroll.Y)))
			l.scrolled = true
		}
	}
	l.scroll = max(0, min(l.scroll, max(0, n*(rowH+gap)-gap-rect.Dy())))
}

// scrollArea registers l's wheel scroll target over rect. Call inside the list's
// clip and before row clickables so clicks still hit rows.
func (l *listScroll) scrollArea(gtx gl.Context, rect image.Rectangle) {
	a := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &l.tag)
	a.Pop()
}

// update scrolls row sel of n into a view viewH tall that starts at top,
// and returns the highlight's top now. sel < 0 is no highlight.
func (l *listScroll) update(gtx gl.Context, sel, n, rowH, gap, top, viewH int) int {
	reset := !l.hasSel || (l.selY == -1 && l.scroll == 0 && !l.scrolled)
	if sel >= 0 && (reset || sel != l.lastSel) {
		y := sel * (rowH + gap)
		l.scroll = min(l.scroll, y)
		l.scroll = max(l.scroll, y+rowH-viewH)
	}
	l.lastSel, l.hasSel = sel, true
	l.scroll = max(0, min(l.scroll, max(0, n*(rowH+gap)-gap-viewH)))
	if sel >= 0 {
		target := float32(top + sel*(rowH+gap) - l.scroll)
		if anim.Reduced() || l.scrolled || reset || l.lastAt.IsZero() {
			l.selY = target
		} else if dt := gtx.Now.Sub(l.lastAt).Seconds(); dt > 0 {
			k := float32(1 - math.Exp(-dt/0.06))
			l.selY += (target - l.selY) * k
			if diff := target - l.selY; diff > 0.5 || diff < -0.5 {
				gtx.Execute(op.InvalidateCmd{})
			} else {
				l.selY = target
			}
		}
	}
	l.scrolled = false
	l.lastAt = gtx.Now
	return int(l.selY + 0.5)
}

// highlight fills the highlighted row's rect; ring draws its outline over
// the rows, so it shows on a tinted one too.
func (u *ui) highlight(gtx gl.Context, r image.Rectangle, ring bool) {
	th := u.th
	if !ring {
		paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(r, gtx.Dp(8)).Op(gtx.Ops))
		return
	}
	// The keyboard's focus: kit.FocusRing's color, inside the row.
	w := gtx.Dp(2)
	p := clip.UniformRRect(r.Inset(w/2), gtx.Dp(8)-w/2).Path(gtx.Ops)
	paint.FillShape(gtx.Ops, theme.Mix(th.Primary, th.Bg, 0.3), clip.Stroke{Path: p, Width: float32(w)}.Op())
}

// filterField draws a filter holding text, with a caret blinking since
// since while it is active; count shows on the right once text is typed,
// hint while it is empty.
func (u *ui) filterField(gtx gl.Context, rect image.Rectangle, text, hint, count string, active bool, since time.Time) {
	th := u.th
	rr := gtx.Dp(8)
	border := th.BorderSubtle
	if active {
		border = theme.Mix(th.SurfaceSecondary, th.Primary, 0.6)
	}
	paint.FillShape(gtx.Ops, border, clip.UniformRRect(rect, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), rr-1).Op(gtx.Ops))
	right := rect.Max.X - gtx.Dp(12)
	if active && text != "" && count != "" {
		c, csz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, count)
		cx := right - csz.X
		o := op.Offset(image.Pt(cx, rect.Min.Y+(rect.Dy()-csz.Y)/2)).Push(gtx.Ops)
		c.Add(gtx.Ops)
		o.Pop()
		right = cx - gtx.Dp(8)
	}
	x := rect.Min.X + gtx.Dp(12)
	shown, col := text, th.Fg
	if text == "" {
		shown, col = hint, theme.Mix(th.SurfaceSecondary, th.Muted, 0.75)
	}
	call, sz := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), col, shown)
	ty := rect.Min.Y + (rect.Dy()-sz.Y)/2
	cl := clip.Rect(image.Rect(x, rect.Min.Y+1, max(x, right), rect.Max.Y-1)).Push(gtx.Ops)
	o := op.Offset(image.Pt(x, ty)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	if active {
		cx := x
		if text != "" {
			cx += sz.X + 1
		}
		u.caret(gtx, image.Rect(cx, ty+gtx.Dp(1), cx+gtx.Dp(2), ty+sz.Y-gtx.Dp(1)), since)
	}
	cl.Pop()
}

// filterEditor draws a single-line widget.Editor in the filter field style:
// paste, Unicode/IME, cursor navigation and selection work natively, and
// query text is clipped before count on the right.
func (u *ui) filterEditor(gtx gl.Context, rect image.Rectangle, ed *widget.Editor, hint, count string, active bool) {
	th := u.th
	ed.SingleLine = true
	ed.ReadOnly = !active
	rr := gtx.Dp(8)
	border := th.BorderSubtle
	if active {
		border = theme.Mix(th.SurfaceSecondary, th.Primary, 0.6)
	}
	paint.FillShape(gtx.Ops, border, clip.UniformRRect(rect, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), rr-1).Op(gtx.Ops))
	right := rect.Max.X - gtx.Dp(12)
	if active && ed.Len() > 0 && count != "" {
		c, csz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, count)
		cx := right - csz.X
		o := op.Offset(image.Pt(cx, rect.Min.Y+(rect.Dy()-csz.Y)/2)).Push(gtx.Ops)
		c.Add(gtx.Ops)
		o.Pop()
		right = cx - gtx.Dp(8)
	}
	x := rect.Min.X + gtx.Dp(12)
	w := max(0, right-x)
	cl := clip.Rect(image.Rect(x, rect.Min.Y+1, x+w, rect.Max.Y-1)).Push(gtx.Ops)
	eg := gtx
	eg.Constraints = gl.Exact(image.Pt(w, rect.Dy()))
	o := op.Offset(image.Pt(x, rect.Min.Y)).Push(gtx.Ops)
	if ed.Len() == 0 {
		gl.W.Layout(eg, func(gtx gl.Context) gl.Dimensions {
			call, sz := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), theme.Mix(th.SurfaceSecondary, th.Muted, 0.75), hint)
			call.Add(gtx.Ops)
			return gl.Dimensions{Size: sz}
		})
	}
	gl.W.Layout(eg, func(gtx gl.Context) gl.Dimensions {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
		return ed.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Body), colorCall(gtx, th.Fg), colorCall(gtx, theme.Mix(th.SurfaceSecondary, th.Primary, 0.35)))
	})
	o.Pop()
	cl.Pop()
}

// caret draws a text caret that blinks once a second from since, or stays
// steady when reduced motion is on.
func (u *ui) caret(gtx gl.Context, r image.Rectangle, since time.Time) {
	if anim.Reduced() {
		paint.FillShape(gtx.Ops, u.th.Primary, clip.Rect(r).Op())
		return
	}
	phase := gtx.Now.Sub(since) % time.Second
	if phase < 600*time.Millisecond {
		paint.FillShape(gtx.Ops, u.th.Primary, clip.Rect(r).Op())
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(600*time.Millisecond - phase)})
	} else {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second - phase)})
	}
}

// drawHints draws keys and what they do, as keycaps along a strip h tall.
func (u *ui) drawHints(gtx gl.Context, h int, hints [][2]string) {
	th := u.th
	x := 0
	for _, k := range hints {
		kc, ks := keycap(gtx, th, k[0])
		o := op.Offset(image.Pt(x, (h-ks.Y)/2)).Push(gtx.Ops)
		kc.Add(gtx.Ops)
		o.Pop()
		x += ks.X + gtx.Dp(6)
		tc, tsz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, k[1])
		o = op.Offset(image.Pt(x, (h-tsz.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		x += tsz.X + gtx.Dp(16)
	}
}
