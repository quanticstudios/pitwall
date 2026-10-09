package app

import (
	"fmt"
	"image"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The copy notice fades in fast, holds, and fades out.
const (
	noticeIn   = anim.Focus
	noticeHold = 1200 * time.Millisecond
	noticeOut  = anim.Long
)

// copiedText is the notice for copying s: its lines when there are several,
// else its characters.
func copiedText(s string) string {
	if n := strings.Count(s, "\n") + 1; n > 1 {
		return fmt.Sprintf("Copied %d lines", n)
	}
	if n := utf8.RuneCountInString(s); n != 1 {
		return fmt.Sprintf("Copied %d characters", n)
	}
	return "Copied 1 character"
}

// showNotice starts the notice over with text under the pane at r,
// replacing any on screen.
func (u *ui) showNotice(gtx gl.Context, text string, r image.Rectangle) {
	u.notice, u.noticeAt, u.noticeIn = text, gtx.Now, r
	gtx.Execute(op.InvalidateCmd{})
}

// drawNotice draws the notice bottom-centre in the pane that copied, in the
// pane area gtx fills: a check and a line of text on a bordered surface chip.
func (u *ui) drawNotice(gtx gl.Context) {
	if u.notice == "" {
		return
	}
	t := gtx.Now.Sub(u.noticeAt)
	alpha := anim.At(gtx, u.noticeAt, noticeIn)
	switch out := u.noticeAt.Add(noticeIn + noticeHold); {
	case t < noticeIn+noticeHold:
		gtx.Execute(op.InvalidateCmd{At: out})
	case t < noticeIn+noticeHold+noticeOut && !anim.Reduced():
		alpha = 1 - anim.At(gtx, out, noticeOut)
	default:
		u.notice = ""
		return
	}

	th := u.th
	call, ts := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, u.notice)
	is, gap := gtx.Dp(14), gtx.Dp(6)
	pad := image.Pt(gtx.Dp(10), gtx.Dp(6))
	h := max(ts.Y, is) + 2*pad.Y
	box := image.Pt(pad.X+is+gap+ts.X+pad.X, h)
	r := u.noticeIn
	at := image.Pt(r.Min.X+(r.Dx()-box.X)/2, r.Max.Y-box.Y-gtx.Dp(24))

	defer paint.PushOpacity(gtx.Ops, alpha).Pop()
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rad := gtx.Dp(6)
	paint.FillShape(gtx.Ops, th.Border, clip.UniformRRect(image.Rectangle{Max: box}, rad).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(1, 1, box.X-1, box.Y-1), rad-1).Op(gtx.Ops))
	o := op.Offset(image.Pt(pad.X, (h-is)/2)).Push(gtx.Ops)
	sidebar.Icon(gtx, "check", is, th.Green)
	o.Pop()
	o = op.Offset(image.Pt(pad.X+is+gap, (h-ts.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
}

// drawStateNotice draws the daemon's State.Notice top-centre in the pane
// area gtx fills: an alert icon, the text wrapped to four lines and a close
// button, which hides it here at once and sends DismissNotice.
func (u *ui) drawStateNotice(gtx gl.Context, text string) {
	if text == "" || text == u.dismissed {
		return
	}
	if u.dismiss.Clicked(gtx) {
		u.dismissed = text
		u.send(proto.DismissNotice{Notice: text})
		return
	}
	for { // presses on the notice stay off the pane under it
		if _, ok := gtx.Event(pointer.Filter{Target: &u.noticeTag, Kinds: pointer.Press}); !ok {
			break
		}
	}
	th := u.th
	is, gap, margin := gtx.Dp(16), gtx.Dp(10), gtx.Dp(12)
	pad := image.Pt(gtx.Dp(14), gtx.Dp(10))
	w := min(gtx.Constraints.Max.X-2*margin, gtx.Dp(640))
	tw := w - 2*pad.X - 2*(is+gap)
	if tw <= 0 {
		return
	}
	m := op.Record(gtx.Ops)
	mat := op.Record(gtx.Ops)
	paint.ColorOp{Color: th.Fg}.Add(gtx.Ops)
	matCall := mat.Stop()
	tgtx := gtx
	tgtx.Constraints = gl.Constraints{Max: image.Pt(tw, gtx.Constraints.Max.Y)}
	ts := widget.Label{MaxLines: 4}.Layout(tgtx, th.Shaper, th.UIFont, th.Sp(theme.Small), text, matCall).Size
	call := m.Stop()
	box := image.Pt(w, max(ts.Y, is)+2*pad.Y)

	defer op.Offset(image.Pt((gtx.Constraints.Max.X-w)/2, margin)).Push(gtx.Ops).Pop()
	rad := gtx.Dp(6)
	paint.FillShape(gtx.Ops, th.Border, clip.UniformRRect(image.Rectangle{Max: box}, rad).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(1, 1, box.X-1, box.Y-1), rad-1).Op(gtx.Ops))
	a := clip.Rect{Max: box}.Push(gtx.Ops)
	event.Op(gtx.Ops, &u.noticeTag)
	a.Pop()
	o := op.Offset(pad).Push(gtx.Ops)
	sidebar.Icon(gtx, "circle-alert", is, th.Yellow)
	o.Pop()
	o = op.Offset(image.Pt(pad.X+is+gap, pad.Y)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	o = op.Offset(image.Pt(box.X-pad.X-is, pad.Y)).Push(gtx.Ops)
	bgtx := gtx
	bgtx.Constraints = gl.Exact(image.Pt(is, is))
	u.dismiss.Layout(bgtx, func(gtx gl.Context) gl.Dimensions {
		c := th.Muted
		if u.dismiss.Hovered() {
			c = th.Fg
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return sidebar.Icon(gtx, "x", is, c)
	})
	o.Pop()
}
