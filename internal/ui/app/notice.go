package app

import (
	"fmt"
	"image"
	"strings"
	"time"
	"unicode/utf8"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// The copy notice fades in fast, holds, and fades out.
const (
	noticeIn   = 120 * time.Millisecond
	noticeHold = 1200 * time.Millisecond
	noticeOut  = 250 * time.Millisecond
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
	alpha := float32(1)
	switch {
	case t < noticeIn:
		alpha = float32(t) / float32(noticeIn)
		gtx.Execute(op.InvalidateCmd{})
	case t < noticeIn+noticeHold:
		gtx.Execute(op.InvalidateCmd{At: u.noticeAt.Add(noticeIn + noticeHold)})
	case t < noticeIn+noticeHold+noticeOut:
		alpha = 1 - float32(t-noticeIn-noticeHold)/float32(noticeOut)
		gtx.Execute(op.InvalidateCmd{})
	default:
		u.notice = ""
		return
	}
	th := u.th
	call, ts := textCall(gtx, th, th.UIFont, th.TextSize*unit.Sp(12.0/13), th.Fg, u.notice)
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
