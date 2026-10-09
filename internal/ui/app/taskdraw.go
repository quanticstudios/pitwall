package app

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// layoutTask handles the New task dialog's input and draws it over the
// window. Escape or a click outside closes it, Ctrl+Enter starts the task.
func (u *ui) layoutTask(gtx gl.Context, st *model.State) {
	m, t := &u.modal, &u.task
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &m.body, Kinds: pointer.Press}); !ok {
			break
		}
	}
	start, queue := false, false
	eds := []*widget.Editor{&t.prompt, &t.name, &t.base, &t.branch}
	filters := []event.Filter{pointer.Filter{Target: &m.backdrop, Kinds: pointer.Press}, key.Filter{Focus: &m.backdrop, Name: key.NameEscape}}
	for _, e := range eds {
		filters = append(filters, key.Filter{Focus: e, Name: key.NameEscape},
			key.Filter{Focus: e, Name: key.NameReturn, Required: key.ModShortcut}, key.Filter{Focus: e, Name: key.NameEnter, Required: key.ModShortcut})
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case pointer.Event:
			m.close() // the dialog's own area takes presses inside it
		case key.Event:
			switch {
			case e.State != key.Press:
			case e.Name == key.NameEscape:
				m.close()
			default:
				start = true
			}
		}
	}
	if m.kind != modalTask {
		return
	}
	for _, e := range eds {
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				// Enter in the base or branch field takes the first match.
				if ms := t.matches(e); len(ms) > 0 {
					setText(e, ms[0])
				} else {
					start = true
				}
			}
			t.err = ""
		}
	}
	t.syncName()
	grow := func(cs []widget.Clickable, n int) []widget.Clickable {
		if len(cs) < n {
			return make([]widget.Clickable, n)
		}
		return cs
	}
	t.projectBtn = grow(t.projectBtn, len(t.projects))
	t.agentBtn = grow(t.agentBtn, len(t.agents))
	t.modeBtn = grow(t.modeBtn, 4)
	for i := range t.projects {
		for t.projectBtn[i].Clicked(gtx) {
			t.pickProject(i)
		}
	}
	for i := range t.whereBtn {
		for t.whereBtn[i].Clicked(gtx) {
			t.where = i
		}
	}
	for i := range t.agents {
		for t.agentBtn[i].Clicked(gtx) {
			t.agent, t.mode = i, 0
		}
	}
	for i := range t.modeBtn {
		for t.modeBtn[i].Clicked(gtx) {
			t.mode = i
		}
	}
	field := &t.base
	if t.where == whereBranch {
		field = &t.branch
	}
	for i, b := range t.matches(field) {
		for t.matchBtn[i].Clicked(gtx) {
			setText(field, b)
		}
	}
	for t.cancel.Clicked(gtx) {
		m.close()
	}
	for t.queue.Clicked(gtx) {
		queue = true
	}
	for t.start.Clicked(gtx) {
		start = true
	}
	if start || queue {
		u.submitTask(st, queue)
	}
	if m.kind != modalTask {
		return
	}

	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, scrim(u.th, anim.At(gtx, m.openedAt, anim.Dialog)), clip.Rect{Max: size}.Op())
	bg := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &m.backdrop)
	bg.Pop()
	if t.focus {
		t.focus = false
		gtx.Execute(key.FocusCmd{Tag: &t.prompt})
	}
	u.cardW(gtx, &m.body, 560, m.openedAt, func(gtx gl.Context) gl.Dimensions { return u.taskBody(gtx, field) })
}

// syncName fills in the worktree name from the prompt until the user
// changes the name.
func (t *taskDialog) syncName() {
	if t.name.Text() != t.autoName {
		return
	}
	if n := worktreeName(t.prompt.Text()); n != t.autoName {
		setText(&t.name, n)
		t.autoName = n
	}
}

// matches is the branches the base or branch field e offers while it has
// key focus, as New worktree tab offers them: any branch for a base, a
// local one to check out. None for the other fields.
func (t *taskDialog) matches(e *widget.Editor) []string {
	switch e {
	case &t.base:
		return refMatches(candidates(t.refs, model.FromNew), e.Text())
	case &t.branch:
		return refMatches(candidates(t.refs, model.FromBranch), e.Text())
	}
	return nil
}

// setText puts s in e with the caret at its end.
func setText(e *widget.Editor, s string) {
	e.SetText(s)
	n := e.Len()
	e.SetCaret(n, n)
}

func (u *ui) taskBody(gtx gl.Context, field *widget.Editor) gl.Dimensions {
	th, t := u.th, &u.task
	p := t.projects[t.project]
	gap := func(h unit.Dp) gl.FlexChild { return gl.Rigid(gl.Spacer{Height: h}.Layout) }
	text := func(size unit.Sp, col color.NRGBA, s string) gl.FlexChild {
		return gl.Rigid(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, size, col, s) })
	}
	label := func(s string) gl.FlexChild {
		return gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, s)
		})
	}
	chips := func(cs []widget.Clickable, names []string, on int) gl.FlexChild {
		return gl.Rigid(func(gtx gl.Context) gl.Dimensions { return u.chips(gtx, cs, names, on) })
	}
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "New task")
		}),
		gap(6),
		text(14, th.Muted, "Starts an agent on your prompt in a tab of its own, now or when a running agent finishes."),
		gap(16), label("Project"), gap(6),
	}
	var names []string
	for _, pr := range t.projects {
		names = append(names, pr.name)
	}
	kids = append(kids, chips(t.projectBtn, names, t.project), gap(14), label("Where"), gap(6))
	where := []string{"This folder"}
	if p.git {
		where = append(where, "New worktree", "Existing branch")
	}
	kids = append(kids, chips(t.whereBtn[:], where, t.where), gap(6), text(12, th.Muted, t.target()))
	fieldH := gtx.Dp(34)
	switch t.where {
	case whereWorktree:
		kids = append(kids, gap(10), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			// Name and base side by side, each under its label.
			w := gtx.Constraints.Max.X
			half := (w - gtx.Dp(8)) / 2
			lc, ls := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, "New branch")
			lc.Add(gtx.Ops)
			o := op.Offset(image.Pt(w-half, 0)).Push(gtx.Ops)
			bc, _ := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, "Off branch")
			bc.Add(gtx.Ops)
			o.Pop()
			top := ls.Y + gtx.Dp(6)
			g := gtx
			g.Constraints = gl.Exact(image.Pt(half, fieldH))
			o = op.Offset(image.Pt(0, top)).Push(gtx.Ops)
			u.field(g, &t.name, th.MonoFont, fieldH, "name")
			o.Pop()
			o = op.Offset(image.Pt(w-half, top)).Push(gtx.Ops)
			u.field(g, &t.base, th.MonoFont, fieldH, "the default branch")
			o.Pop()
			return gl.Dimensions{Size: image.Pt(w, top+fieldH)}
		}))
	case whereBranch:
		kids = append(kids, gap(10), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.field(gtx, &t.branch, th.MonoFont, fieldH, "branch to check out")
		}))
	}
	if t.where != whereHere && gtx.Focused(field) {
		for i, b := range t.matches(field) {
			kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions { return u.matchRow(gtx, &t.matchBtn[i], b, field == &t.base) }))
		}
	}
	if len(t.agents) > 0 {
		var agents, modes []string
		for _, a := range t.agents {
			agents = append(agents, a.name)
		}
		kids = append(kids, gap(14), label("Agent"), gap(6), chips(t.agentBtn, agents, t.agent))
		for _, md := range taskModes[t.agents[t.agent].cmd] {
			modes = append(modes, md.name)
		}
		if len(modes) > 0 {
			kids = append(kids, gap(14), label("Mode"), gap(6), chips(t.modeBtn, modes, t.mode))
		}
	}
	kids = append(kids, gap(14), label("Prompt"), gap(6), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
		return u.field(gtx, &t.prompt, th.UIFont, gtx.Dp(132), "What should the agent do?")
	}))
	if t.err != "" {
		kids = append(kids, gap(8), text(12, th.Red, t.err))
	}
	kids = append(kids, gap(20), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
		d := u.buttonRow(gtx,
			dialogButton{&t.start, "Start", kit.Primary},
			dialogButton{&t.queue, "Queue", kit.Secondary},
			dialogButton{&t.cancel, "Cancel", kit.Secondary})
		call, sz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, "Ctrl+Enter starts")
		o := op.Offset(image.Pt(0, (d.Size.Y-sz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return d
	}))
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

// chips draws a wrapping row of options with on picked: aide's selected
// icon cell, primary tint and ring, on the field colors.
func (u *ui) chips(gtx gl.Context, cs []widget.Clickable, names []string, on int) gl.Dimensions {
	th := u.th
	h, gap := gtx.Dp(28), gtx.Dp(6)
	w := gtx.Constraints.Max.X
	x, y := 0, 0
	for i, n := range names {
		picked := i == on
		col := theme.Mix(th.SurfaceSecondary, th.Fg, 0.8)
		if picked {
			col = th.Fg
		}
		call, sz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), col, n)
		bw := min(sz.X+2*gtx.Dp(12), w)
		if x > 0 && x+bw > w {
			x, y = 0, y+h+gap
		}
		o := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(bw, h))
		c := &cs[i]
		c.Layout(g, func(gtx gl.Context) gl.Dimensions {
			r := gtx.Dp(8)
			rect := image.Rect(0, 0, bw, h)
			bg, ring := th.SurfaceSecondary, th.BorderSubtle
			switch {
			case picked:
				bg = th.SelectedBg
				ring = theme.Mix(bg, th.Primary, 0.5)
			case c.Hovered():
				bg = th.SurfaceElevated
			}
			paint.FillShape(gtx.Ops, ring, clip.UniformRRect(rect, r).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, bg, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
			pointer.CursorPointer.Add(gtx.Ops)
			t := op.Offset(image.Pt((bw-sz.X)/2, (h-sz.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			t.Pop()
			return gl.Dimensions{Size: rect.Size()}
		})
		o.Pop()
		x += bw + gap
	}
	return gl.Dimensions{Size: image.Pt(w, y+h)}
}

// matchRow is pickRow under the base or branch field, under the right
// half for the base.
func (u *ui) matchRow(gtx gl.Context, c *widget.Clickable, branch string, base bool) gl.Dimensions {
	w := gtx.Constraints.Max.X
	if !base {
		return u.pickRow(gtx, c, branch)
	}
	x := w - (w-gtx.Dp(8))/2
	defer op.Offset(image.Pt(x, 0)).Push(gtx.Ops).Pop()
	g := gtx
	g.Constraints.Max.X = w - x
	d := u.pickRow(g, c, branch)
	return gl.Dimensions{Size: image.Pt(w, d.Size.Y)}
}
