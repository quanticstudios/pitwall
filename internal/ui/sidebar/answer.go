package sidebar

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// answerH is the Allow and Deny buttons' height in dp.
const answerH = 18

// answering reports whether tab id's row, with activity a, shows Allow and
// Deny. A tab whose agents show as sub-rows answers on the asking one's.
func (s *Sidebar) answering(v *view, id string, ghost bool, a *model.Activity) bool {
	return s.Answers && !ghost && a != nil && remote.Answerable(*a) && len(s.shownSubs(v, id)) == 0
}

// planning reports whether tab id's row, with activity a, shows Request
// changes… for its agent's plan. A tab whose agents show as sub-rows has
// none.
func (s *Sidebar) planning(v *view, id string, ghost bool, a *model.Activity) bool {
	return !ghost && a != nil && a.State == model.StatePlanReady && len(s.shownSubs(v, id)) == 0
}

// answerClicks turns clicks on row r's Allow and Deny into Answer events
// for the prompt the row shows now, and one on Request changes… into the
// field for the reply.
func (s *Sidebar) answerClicks(gtx layout.Context, v *view, ws string, r *rowState) {
	for r.plan.Clicked(gtx) {
		if a := v.activity[ws]; s.planning(v, ws, false, a) {
			s.startReply(ws, a.PaneID)
		}
	}
	for _, b := range []struct {
		c     *widget.Clickable
		allow bool
	}{{&r.allow, true}, {&r.deny, false}} {
		for b.c.Clicked(gtx) {
			if a := v.activity[ws]; s.answering(v, ws, false, a) {
				s.events = append(s.events, Answer{PaneID: a.PaneID, At: a.UpdatedAt.UnixNano(), Allow: b.allow})
			}
		}
	}
}

// answerSize is the size answerButtons takes, or planButton with plan.
func answerSize(gtx layout.Context, th *theme.Theme, a model.Activity, base color.NRGBA, plan bool) image.Point {
	m := op.Record(gtx.Ops)
	var d layout.Dimensions
	if plan {
		d = planButton(gtx, th, nil, base)
	} else {
		d = answerButtons(gtx, th, nil, nil, a, base)
	}
	m.Stop()
	return d.Size
}

// planButton draws a plan's Request changes…, a purple pill like Allow.
func planButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, base color.NRGBA) layout.Dimensions {
	return pillButton(c, th, base, answerHeight(gtx, th), "Request changes…", th.Purple, false)(gtx)
}

// answerButtons draws a pending approval's Allow and Deny, small pills in
// green and red. The one the decision model advises is outlined, so its
// recommendation in the pill above points at a button. With nil
// clickables they are drawn without input, to measure them.
func answerButtons(gtx layout.Context, th *theme.Theme, allow, deny *widget.Clickable, a model.Activity, base color.NRGBA) layout.Dimensions {
	h := answerHeight(gtx, th)
	return hrowFit(gtx, h, gtx.Dp(4),
		item{w: pillButton(allow, th, base, h, "Allow", th.Green, a.Advice == "allow")},
		item{w: pillButton(deny, th, base, h, "Deny", th.Red, a.Advice == "deny")})
}

// pillButton is one of a row's small answer pills, h high, in col; an
// advised one is outlined. With a nil c it draws without input.
func pillButton(c *widget.Clickable, th *theme.Theme, base color.NRGBA, h int, text string, col color.NRGBA, advised bool) layout.Widget {
	draw := func(gtx layout.Context) layout.Dimensions {
		m := op.Record(gtx.Ops)
		d := label(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), col, text)
		call := m.Stop()
		sz := image.Pt(d.Size.X+gtx.Dp(16), h)
		fill := float32(0.14)
		if c != nil && c.Hovered() {
			fill = 0.28
		}
		rect := image.Rectangle{Max: sz}
		paint.FillShape(gtx.Ops, theme.Mix(base, col, fill), clip.UniformRRect(rect, h/2).Op(gtx.Ops))
		if advised {
			paint.FillShape(gtx.Ops, theme.Mix(base, col, 0.7), clip.Stroke{Path: clip.UniformRRect(rect, h/2).Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
		}
		off := op.Offset(sz.Sub(d.Size).Div(2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		off.Pop()
		return layout.Dimensions{Size: sz}
	}
	if c == nil {
		return draw
	}
	return func(gtx layout.Context) layout.Dimensions { return clickable(gtx, c, draw) }
}
