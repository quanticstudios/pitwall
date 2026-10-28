package settings

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// presetsPage is the preset editor on Settings, Agents.
type presetsPage struct {
	open    bool
	old     string // the preset being edited, "" for a new one
	agent   string
	effort  string
	guard   bool
	env     []string // kept as config.toml has them
	name    widget.Editor
	model   widget.Editor
	extra   widget.Editor
	err     string
	confirm bool // Delete was clicked once
	focus   bool // move focus to name this frame
	scroll  bool // scroll the page to the editor
	note    string
	found   []string // the optional agents on PATH
}

// presetAgents are the agents a preset can run, with their names. The
// first four always show; the others when they are on PATH.
var presetAgents = []struct{ cmd, label string }{
	{"claude", "Claude Code"},
	{"codex", "Codex"},
	{"pi", "pi"},
	{"opencode", "OpenCode"},
	{"omp", "Oh My Pi"},
	{"gemini", "Gemini CLI"},
	{"cursor-agent", "Cursor CLI"},
	{"amp", "Amp"},
	{"aider", "Aider"},
}

// findPresetAgents looks up the optional agents on PATH.
func findPresetAgents() []string {
	var out []string
	for _, a := range presetAgents[4:] {
		if _, err := exec.LookPath(a.cmd); err == nil {
			out = append(out, a.cmd)
		}
	}
	return out
}

func agentLabel(cmd string) string {
	for _, a := range presetAgents {
		if a.cmd == cmd {
			return a.label
		}
	}
	return cmd
}

// presetSummary is what ps runs, as "Codex, gpt-x, high".
func presetSummary(ps config.AgentPreset) string {
	m, e, rest := config.SplitArgs(ps.Agent, ps.Args)
	parts := []string{agentLabel(ps.Agent)}
	for _, s := range []string{m, e, config.JoinShell(rest)} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// editPreset opens the editor on ps, or on a new preset for nil.
func (p *Page) editPreset(ps *config.AgentPreset) {
	e := &p.pr
	e.open, e.err, e.note, e.confirm, e.focus, e.scroll = true, "", "", false, true, true
	if ps == nil {
		ps = &config.AgentPreset{Agent: "claude"}
	}
	m, eff, rest := config.SplitArgs(ps.Agent, ps.Args)
	e.old, e.agent, e.effort, e.guard, e.env = ps.Name, ps.Agent, eff, ps.Guard, ps.Env
	e.name.SetText(ps.Name)
	e.model.SetText(m)
	e.extra.SetText(config.JoinShell(rest))
}

// lanesOf are the lanes that start the preset named name.
func (p *Page) lanesOf(name string) []string {
	var out []string
	for _, l := range p.s.Fleet.Lanes {
		if l.Preset == name {
			out = append(out, l.Name)
		}
	}
	return out
}

// savePreset writes the editor's preset, and renames it in the lanes
// that start it.
func (p *Page) savePreset() {
	e := &p.pr
	rest, err := config.SplitShell(e.extra.Text())
	if err != nil {
		e.err = "Extra arguments: " + err.Error()
		return
	}
	model := strings.TrimSpace(e.model.Text())
	if config.Efforts(e.agent) == nil {
		model = ""
	}
	ps := config.AgentPreset{Name: strings.TrimSpace(e.name.Text()), Agent: e.agent,
		Args: config.PresetArgs(e.agent, model, e.effort, rest), Env: e.env, Guard: e.guard}
	if err := config.SetPreset(p.path(), e.old, &ps); err != nil {
		e.err = err.Error()
		return
	}
	if e.old != "" && e.old != ps.Name {
		for _, l := range p.lanesOf(e.old) {
			p.saveValue("fleet."+l, "preset", config.Quote(ps.Name))
		}
	}
	e.open, p.result = false, Saved
}

// deletePreset removes the editor's preset and turns off the lanes that
// started it.
func (p *Page) deletePreset() {
	e := &p.pr
	if err := config.SetPreset(p.path(), e.old, nil); err != nil {
		e.err = err.Error()
		return
	}
	lanes := p.lanesOf(e.old)
	for _, l := range lanes {
		p.save("fleet."+l, "preset", nil)
	}
	e.open, p.result = false, Saved
	if len(lanes) > 0 {
		e.note = fmt.Sprintf("Deleted %s. The %s %s off now. Pick another preset in Settings, Fleet.", e.old, laneList(lanes), pick(lanes, "is", "are"))
	}
}

// laneList is "small lane" or "small and big lanes".
func laneList(lanes []string) string {
	if len(lanes) == 1 {
		return lanes[0] + " lane"
	}
	return strings.Join(lanes[:len(lanes)-1], ", ") + " and " + lanes[len(lanes)-1] + " lanes"
}

// pick is one for a single lane, else many.
func pick(lanes []string, one, many string) string {
	if len(lanes) == 1 {
		return one
	}
	return many
}

// presets are Settings, Agents' Presets section, or the editor while it
// is open.
func (p *Page) presets() section {
	th := p.th
	e := &p.pr
	btn := func(id, label string, kind btnKind, click func()) gl.Widget {
		return func(gtx gl.Context) gl.Dimensions {
			c := p.btn(id)
			for c.Clicked(gtx) {
				click()
				gtx.Execute(op.InvalidateCmd{}) // the rows of this frame are already built
			}
			return p.button(gtx, c, kind, label)
		}
	}
	if e.open {
		return p.presetForm(btn)
	}
	var rows []row
	for _, ps := range p.s.Presets {
		edit := btn("preset-edit:"+ps.Name, "Edit", secondary, func() { p.editPreset(&ps) })
		control := edit
		if ps.Guard {
			control = func(gtx gl.Context) gl.Dimensions {
				return hstack(gtx, 12, func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, th.UIFont, p.th.Sp(theme.Body), th.Muted, "Guarded")
				}, edit)
			}
		}
		rows = append(rows, row{label: ps.Name, desc: presetSummary(ps), extra: "preset model effort " + ps.Agent, control: control})
	}
	desc := "A model and effort for an agent, under a name. Fleet lanes start one each."
	if len(rows) == 0 {
		desc = "No presets yet. A preset is an agent with its model, effort and arguments under a name. Fleet lanes start one each."
	}
	rows = append(rows, row{label: "New preset", desc: desc, extra: "preset add model effort",
		control: btn("preset-new", "New preset", primary, func() { p.editPreset(nil) }),
		below: func(gtx gl.Context) gl.Dimensions {
			if e.note == "" {
				return gl.Dimensions{}
			}
			return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Yellow, e.note)
		}})
	return section{title: "Presets", desc: "The New task dialog lists them after the plain agents, and pitwall new --preset starts one.", rows: rows, anchor: true}
}

// presetForm is the editor's rows.
func (p *Page) presetForm(btn func(id, label string, kind btnKind, click func()) gl.Widget) section {
	th := p.th
	e := &p.pr
	agents := []string{"claude", "codex", "pi", "opencode"}
	agents = append(agents, e.found...)
	if !slices.Contains(agents, e.agent) {
		agents = append(agents, e.agent)
	}
	labels := make([]string, len(agents))
	for i, a := range agents {
		labels[i] = agentLabel(a)
	}
	efforts := config.Efforts(e.agent)
	rows := []row{
		{label: "Name", desc: "What the New task dialog and the fleet lanes show.", extra: "preset name",
			control: p.formField(&e.name, "Quick", 260)},
		{label: "Agent", extra: "preset agent", wide: len(agents) > 5, control: p.labeled("preset-agent", agents, labels, e.agent, func(o string) {
			e.agent = o
			if !slices.Contains(config.Efforts(o), e.effort) {
				e.effort = ""
			}
		})},
	}
	if efforts != nil {
		placeholder := "provider/model"
		switch e.agent {
		case "claude":
			placeholder = "opus"
		case "codex":
			placeholder = "gpt-x"
		}
		rows = append(rows,
			row{label: "Model", desc: "The model's name as the agent knows it. Empty runs the agent's default.", extra: "preset model",
				control: p.formField(&e.model, placeholder, 260)},
			row{label: "Effort", desc: "Empty runs the agent's default.", extra: "preset effort reasoning thinking",
				control: p.labeled("preset-effort", append([]string{""}, efforts...), append([]string{"Default"}, efforts...), e.effort, func(o string) { e.effort = o })})
	}
	rows = append(rows,
		row{label: "Extra arguments", desc: "Passed to the agent after the model and effort, split as a shell would.", extra: "preset args arguments flags", wide: true,
			control: p.formField(&e.extra, "--verbose", 800)},
		row{label: "Guard", desc: "Denies the agent git push, gh pr merge, terraform apply and the like.", extra: "preset guard",
			control: p.switchOf("preset-guard", e.guard, func() { e.guard = !e.guard })})
	if len(e.env) > 0 {
		var names []string
		for _, v := range e.env {
			k, _, _ := strings.Cut(v, "=")
			names = append(names, k)
		}
		rows = append(rows, row{label: "Environment", desc: strings.Join(names, ", ") + ". Edit these in config.toml.", extra: "preset env",
			control: func(gtx gl.Context) gl.Dimensions { return gl.Dimensions{} }})
	}
	buttons := []gl.Widget{btn("preset-save", "Save", primary, p.savePreset), btn("preset-cancel", "Cancel", secondary, func() { e.open = false })}
	if e.old != "" {
		if e.confirm {
			buttons = append(buttons, btn("preset-delete", "Delete for good", danger, p.deletePreset))
		} else {
			buttons = append(buttons, btn("preset-delete", "Delete", secondary, func() { e.confirm = true }))
		}
	}
	del := ""
	if ls := p.lanesOf(e.old); e.old != "" && len(ls) > 0 {
		del = fmt.Sprintf("The %s %s it. Renaming it renames it there, and deleting it turns %s off.", laneList(ls), pick(ls, "runs", "run"), pick(ls, "the lane", "them"))
	}
	rows = append(rows, row{label: "", control: func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return hstack(gtx, 8, buttons...) }),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				switch {
				case e.err != "":
					return gl.Inset{Top: 10}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
						return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Red, e.err)
					})
				case del != "":
					return gl.Inset{Top: 10}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
						return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, del)
					})
				}
				return gl.Dimensions{}
			}),
		)
	}})
	title := "New preset"
	if e.old != "" {
		title = "Edit " + e.old
	}
	return section{title: title, desc: "Saved to config.toml as an [[agents.preset]] table.", rows: rows, anchor: true}
}

// formField is an editor the preset form saves on Save or Enter.
func (p *Page) formField(ed *widget.Editor, placeholder string, width unit.Dp) gl.Widget {
	return func(gtx gl.Context) gl.Dimensions {
		ed.SingleLine, ed.Submit = true, true
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				p.savePreset()
			}
		}
		if ed == &p.pr.name && p.pr.focus {
			p.pr.focus = false
			gtx.Execute(key.FocusCmd{Tag: ed})
		}
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(width))
		return p.field(gtx, ed, placeholder, false)
	}
}
