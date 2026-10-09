package app

import (
	"image"
	"image/color"
	"math"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The centred overlays, the session switcher and the command palette,
// share these pieces: a dimmed backdrop, a card that scales in, a filter
// field, a scrolled list with an easing highlight and a row of key hints.

// backdrop dims the window by t and reports whether it was pressed, which
// closes the overlay.
func backdrop(gtx gl.Context, t float32, tag *int) bool {
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, color.NRGBA{A: uint8(0xa6 * t)}, clip.Rect{Max: size}.Op())
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
	th := u.th
	card := image.Rectangle{Max: image.Pt(w, h)}
	at := gtx.Constraints.Max.Sub(card.Size()).Div(2)
	fade := paint.PushOpacity(gtx.Ops, t)
	scale := 0.985 + 0.015*t
	center := f32.Pt(float32(at.X)+float32(w)/2, float32(at.Y)+float32(h)/2)
	move := op.Affine(f32.AffineId().Scale(center, f32.Pt(scale, scale)).Offset(f32.Pt(float32(at.X), float32(at.Y)+float32(gtx.Dp(10))*(1-t)))).Push(gtx.Ops)

	r := gtx.Dp(14)
	for i, a := range []uint8{0x22, 0x1a, 0x12} {
		g := gtx.Dp(unit.Dp(6 * (i + 1)))
		paint.FillShape(gtx.Ops, color.NRGBA{A: a}, clip.UniformRRect(card.Add(image.Pt(0, gtx.Dp(8))).Inset(-g), r+g).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.1), clip.UniformRRect(card, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(card.Inset(1), r-1).Op(gtx.Ops))
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press}); !ok {
			break
		}
	}
	area := clip.Rect(card).Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	area.Pop()
	return func() { move.Pop(); fade.Pop() }
}

// listScroll scrolls a list of equal rows so the highlighted one shows,
// and eases the highlight toward it with a 60ms time constant.
type listScroll struct {
	scroll int
	selY   float32   // the highlight's drawn top; < 0 jumps to the row
	lastAt time.Time // the frame selY was last eased in
}

// update scrolls row sel of n into a view viewH tall that starts at top,
// and returns the highlight's top now. sel < 0 is no highlight.
func (l *listScroll) update(gtx gl.Context, sel, n, rowH, gap, top, viewH int) int {
	if sel >= 0 {
		y := sel * (rowH + gap)
		l.scroll = min(l.scroll, y)
		l.scroll = max(l.scroll, y+rowH-viewH)
	}
	l.scroll = max(0, min(l.scroll, n*(rowH+gap)-gap-viewH))
	if sel >= 0 {
		target := float32(top + sel*(rowH+gap) - l.scroll)
		if l.selY < 0 || l.lastAt.IsZero() {
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
	l.lastAt = gtx.Now
	return int(l.selY + 0.5)
}

// highlight fills the highlighted row's rect; ring draws its outline over
// the rows, so it shows on a tinted one too.
func (u *ui) highlight(gtx gl.Context, r image.Rectangle, ring bool) {
	th := u.th
	if !ring {
		paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.065), clip.UniformRRect(r, gtx.Dp(8)).Op(gtx.Ops))
		return
	}
	p := clip.UniformRRect(r, gtx.Dp(8)).Path(gtx.Ops)
	paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.22), clip.Stroke{Path: p, Width: float32(gtx.Dp(1))}.Op())
}

// filterField draws a filter holding text, with a caret blinking since
// since while it is active; count shows on the right once text is typed,
// hint while it is empty.
func (u *ui) filterField(gtx gl.Context, rect image.Rectangle, text, hint, count string, active bool, since time.Time) {
	th := u.th
	rr := gtx.Dp(8)
	border := theme.Mix(th.SurfaceSecondary, th.Fg, 0.07)
	if active {
		border = theme.Mix(th.SurfaceSecondary, th.Primary, 0.6)
	}
	paint.FillShape(gtx.Ops, border, clip.UniformRRect(rect, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), rr-1).Op(gtx.Ops))
	x := rect.Min.X + gtx.Dp(12)
	shown, col := text, th.Fg
	if text == "" {
		shown, col = hint, theme.Mix(th.SurfaceSecondary, th.Muted, 0.75)
	}
	call, sz := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), col, shown)
	ty := rect.Min.Y + (rect.Dy()-sz.Y)/2
	o := op.Offset(image.Pt(x, ty)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	if !active {
		return
	}
	cx := x
	if text != "" {
		cx += sz.X + 1
	}
	u.caret(gtx, image.Rect(cx, ty+gtx.Dp(1), cx+gtx.Dp(2), ty+sz.Y-gtx.Dp(1)), since)
	if text != "" && count != "" {
		c, csz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, count)
		o := op.Offset(image.Pt(rect.Max.X-gtx.Dp(12)-csz.X, rect.Min.Y+(rect.Dy()-csz.Y)/2)).Push(gtx.Ops)
		c.Add(gtx.Ops)
		o.Pop()
	}
}

// caret draws a text caret that blinks once a second from since.
func (u *ui) caret(gtx gl.Context, r image.Rectangle, since time.Time) {
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
