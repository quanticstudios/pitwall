package settings

import (
	"strings"
	"time"

	gl "gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type awakePage struct {
	grace   widget.Editor
	focused bool
	err     string
}

func (p *Page) keepAwakeGraceRow() row {
	r := row{label: "Keep awake grace", desc: "After work stops, as 15m or 10s. 0s releases at once. Both settings apply live.",
		extra: "sleep inhibit duration keep_awake_grace", control: p.keepAwakeGraceField()}
	if p.aw.err != "" {
		r.below = func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, p.th.UIFont, p.th.Sp(theme.Small), p.th.Red, p.aw.err)
		}
	}
	return r
}

func (p *Page) keepAwakeGraceField() gl.Widget {
	return func(gtx gl.Context) gl.Dimensions {
		e := &p.aw.grace
		e.SingleLine, e.Submit = true, true
		submit := false
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				submit = true
			}
		}
		focused := gtx.Focused(e)
		if submit || p.aw.focused && !focused {
			p.saveAwakeGrace(e.Text())
		} else if cur := p.s.KeepAwakeGrace.String(); !focused && e.Text() != cur && p.aw.err == "" {
			e.SetText(cur)
		}
		p.aw.focused = focused
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(140))
		return p.field(gtx, e, "15m", false)
	}
}

func (p *Page) saveAwakeGrace(text string) {
	p.aw.err = ""
	text = strings.TrimSpace(text)
	if text == "" {
		p.save("agents", "keep_awake_grace", nil)
		return
	}
	d, err := time.ParseDuration(text)
	if err != nil || d < 0 {
		p.aw.err = "Use a non-negative duration such as 15m or 10s."
		return
	}
	if d == config.DefaultKeepAwakeGrace {
		p.save("agents", "keep_awake_grace", nil)
	} else if d != p.s.KeepAwakeGrace {
		p.saveValue("agents", "keep_awake_grace", config.Quote(d.String()))
	}
}
