package settings

import (
	"strings"

	gl "gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// fleetPage is the Fleet category's state: the two text fields.
type fleetPage struct {
	contract, nudge textField
}

// textField is a single-line editor for a string key, saved on Enter or
// when it loses focus.
type textField struct {
	e       widget.Editor
	focused bool // e had focus last frame
}

// layout draws f for table.k, whose value is cur, at most width dp wide.
// An empty or default value removes the key.
func (f *textField) layout(p *Page, gtx gl.Context, table, k, cur, def, placeholder string, width unit.Dp) gl.Dimensions {
	e := &f.e
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
	switch v := strings.TrimSpace(e.Text()); {
	case (submit || f.focused && !focused) && v != cur:
		if v == "" || v == def {
			p.save(table, k, nil)
		} else {
			p.saveValue(table, k, config.Quote(v))
		}
	case !focused && e.Text() != cur:
		e.SetText(cur)
	}
	f.focused = focused
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(width))
	return p.field(gtx, e, placeholder, false)
}

func (p *Page) fleet() []section {
	f := p.s.Fleet
	var names, labels []string
	for _, ps := range p.s.Presets {
		names, labels = append(names, ps.Name), append(labels, ps.Name)
	}
	names, labels = append([]string{""}, names...), append([]string{"None"}, labels...)
	lanes := []row{}
	for i, l := range f.Lanes {
		table := "fleet." + l.Name
		title := strings.ToUpper(l.Name[:1]) + l.Name[1:]
		pick := func(gtx gl.Context) gl.Dimensions {
			return p.text(gtx, p.th.UIFont, p.th.Sp(theme.Body), p.th.Muted, "No presets yet")
		}
		if len(p.s.Presets) > 0 {
			pick = p.labeled(table+".preset", names, labels, l.Preset, func(o string) {
				if o == "" {
					p.save(table, "preset", nil)
				} else {
					p.saveValue(table, "preset", config.Quote(o))
				}
			})
		}
		lanes = append(lanes,
			row{label: title + " lane", desc: config.LaneDescs[i] + " pitwall worktree new --lane " + l.Name + " starts this preset.",
				extra: "fleet lane preset " + l.Name, control: pick},
			row{label: title + " timebox", desc: "Minutes a " + l.Name + " worker may run with no pull request open before pitwall nudges it once. 0 turns it off.",
				extra: "fleet lane timebox nudge minutes " + l.Name, control: p.stepper(table, "timebox", l.Timebox.Minutes(), 0, 1440, 5, config.DefaultTimeboxes[i])})
	}
	return []section{
		{rows: []row{
			{label: "Fleet mode", desc: "Lanes start a preset by a task's size, the worker contract goes before each brief, workers past their timebox get a nudge, and repeat red checks escalate. Off by default.",
				extra: "fleet orchestrator worker lane enabled", control: p.toggle("fleet", "enabled", f.On)},
		}},
		{title: "Lanes", desc: "Each lane names one of your presets (Settings, Agents). An orchestrator agent sends one worker per task, by its size; pitwall worktree lanes lists them.", rows: lanes},
		{title: "Workers", rows: []row{
			{label: "Worker contract", desc: "A text file whose contents go before every --lane brief, such as how to commit and when to stop. Empty for none.",
				extra: "fleet contract brief file path", control: func(gtx gl.Context) gl.Dimensions {
					return p.fl.contract.layout(p, gtx, "fleet", "contract", shortPath(f.Contract), "", "~/fleet/contract.md", 260)
				}},
			{label: "Nudge", desc: "Sent once, at its prompt, to a worker past its timebox with no pull request open. Past twice the timebox, the tab that started it gets a line.",
				extra: "fleet nudge timebox message", wide: true, control: func(gtx gl.Context) gl.Dimensions {
					return p.fl.nudge.layout(p, gtx, "fleet", "nudge", f.Nudge, config.DefaultNudge, config.DefaultNudge, 800)
				}},
			{label: "Escalate repeat red", desc: "When CI fails again after pitwall sent a worker its failures twice (auto_send_failures under [git], which must be on): tell the tab that started it, or close the worker's agent and start the big lane in the same worktree.",
				extra: "fleet escalate respawn parent ci red failures", control: p.labeled("fleet.escalate", []string{"parent", "respawn", "off"}, []string{"Tell parent", "Respawn on big", "Off"}, f.Escalate,
					func(o string) {
						if o == "parent" {
							p.save("fleet", "escalate", nil)
						} else {
							p.saveValue("fleet", "escalate", config.Quote(o))
						}
					})},
		}},
	}
}
