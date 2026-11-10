package settings

import (
	"strings"
	"time"

	gl "gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// durationField edits an [agents] key that takes a duration such as 15m.
type durationField struct {
	grace   widget.Editor
	focused bool
	err     string
}

func (p *Page) keepAwakeGraceRow() row {
	return p.durationRow(&p.aw, row{label: "Keep awake grace", desc: "After work stops, as 15m or 10s. 0s releases at once. Both settings apply live.",
		extra: "sleep inhibit duration keep_awake_grace"}, p.keepAwakeGraceField())
}

func (p *Page) keepAwakeGraceField() gl.Widget {
	return p.durationEditor(&p.aw, p.s.KeepAwakeGrace, "15m", p.saveAwakeGrace)
}

func (p *Page) saveAwakeGrace(text string) {
	p.saveDuration(&p.aw, "keep_awake_grace", text, p.s.KeepAwakeGrace, config.DefaultKeepAwakeGrace)
}

func (p *Page) parkIdleRow() row {
	return p.durationRow(&p.park, row{label: "Park idle agents", desc: "Ends a Claude Code or Codex agent's process after it sits idle at its prompt this long, as 1h or 30m, and resumes its session on the next key or message. Never one a window shows, one waiting on you, or one running a shell or a dev server. 0s is off.",
		extra: "memory idle park sleep resume park_idle_after"}, p.durationEditor(&p.park, p.s.ParkIdleAfter, "1h", p.saveParkIdle))
}

func (p *Page) saveParkIdle(text string) {
	p.saveDuration(&p.park, "park_idle_after", text, p.s.ParkIdleAfter, 0)
}

// durationRow is r with control and, under it, f's error.
func (p *Page) durationRow(f *durationField, r row, control gl.Widget) row {
	r.control = control
	if f.err != "" {
		r.below = func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, p.th.UIFont, p.th.Sp(theme.Small), p.th.Red, f.err)
		}
	}
	return r
}

// durationEditor is f's field, showing cur and saving with save on Enter
// or when it loses focus.
func (p *Page) durationEditor(f *durationField, cur time.Duration, hint string, save func(string)) gl.Widget {
	return func(gtx gl.Context) gl.Dimensions {
		e := &f.grace
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
		if submit || f.focused && !focused {
			save(e.Text())
		} else if !focused && e.Text() != cur.String() && f.err == "" {
			e.SetText(cur.String())
		}
		f.focused = focused
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(140))
		return p.field(gtx, e, hint, false)
	}
}

// saveDuration writes text to [agents] key, or removes the key when text
// is empty or def; a bad duration sets f's error instead.
func (p *Page) saveDuration(f *durationField, key, text string, cur, def time.Duration) {
	f.err = ""
	text = strings.TrimSpace(text)
	if text == "" {
		p.save("agents", key, nil)
		return
	}
	d, err := time.ParseDuration(text)
	if err != nil || d < 0 {
		f.err = "Use a non-negative duration such as 15m or 10s."
		return
	}
	if d == def {
		p.save("agents", key, nil)
	} else if d != cur {
		p.saveValue("agents", key, config.Quote(d.String()))
	}
}
