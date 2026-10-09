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

// answering reports whether a row with activity a shows Allow and Deny.
func (s *Sidebar) answering(ghost bool, a *model.Activity) bool {
	return s.Answers && !ghost && a != nil && remote.Answerable(*a)
}

// answerClicks turns clicks on row r's Allow and Deny into Answer events
// for the prompt the row shows now.
func (s *Sidebar) answerClicks(gtx layout.Context, v *view, ws string, r *rowState) {
	for _, b := range []struct {
		c     *widget.Clickable
		allow bool
	}{{&r.allow, true}, {&r.deny, false}} {
		for b.c.Clicked(gtx) {
			if a := v.activity[ws]; s.answering(false, a) {
				s.events = append(s.events, Answer{PaneID: a.PaneID, At: a.UpdatedAt.UnixNano(), Allow: b.allow})
			}
		}
	}
}

// answerSize is the size answerButtons takes.
func answerSize(gtx layout.Context, th *theme.Theme, a model.Activity, base color.NRGBA) image.Point {
	m := op.Record(gtx.Ops)
	d := answerButtons(gtx, th, nil, a, base)
	m.Stop()
	return d.Size
}

// answerButtons draws a pending approval's Allow and Deny, small pills in
// green and red. The one the decision model advises is outlined, so its
// recommendation in the pill above points at a button. With r nil they
// are drawn without input, to measure them.
func answerButtons(gtx layout.Context, th *theme.Theme, r *rowState, a model.Activity, base color.NRGBA) layout.Dimensions {
	h := gtx.Dp(answerH)
	btn := func(c *widget.Clickable, text string, col color.NRGBA, advised bool) layout.Widget {
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
	var allow, deny *widget.Clickable
	if r != nil {
		allow, deny = &r.allow, &r.deny
	}
	return hrowFit(gtx, h, gtx.Dp(4),
		item{w: btn(allow, "Allow", th.Green, a.Advice == "allow")},
		item{w: btn(deny, "Deny", th.Red, a.Advice == "deny")})
}
