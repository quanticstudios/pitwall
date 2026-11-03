package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A tab lists under its row, one compact sub-row each, its agent panes
// when it has two or more, then the worker tabs that hang under it
// (model.State.Root): the agent's mark, the pane's or worker's title, its
// state pill, and Allow and Deny or Request changes… on the one asking.

// subIndent is where a sub-row starts in its tab's width, past the tab's
// mark, so the sub-rows' marks line up with the tab's title.
const subIndent unit.Dp = 24

// chevronDur is how long a disclosure chevron takes to turn.
const chevronDur = 150 * time.Millisecond

// subRow is a sub-row's input state: its click area, Allow and Deny, its
// tag for a right click, and its eased fill.
type subRow struct {
	click, allow, deny, plan widget.Clickable
	ctx                      int
	easedFill
}

// subEntry is one sub-row: an agent pane of its tab, or a worker tab.
type subEntry struct {
	pane   model.AgentPane
	worker *model.Workspace // nil for an agent pane
}

// id is the pane's id, or the worker tab's.
func (e subEntry) id() string {
	if e.worker != nil {
		return e.worker.ID
	}
	return e.pane.Pane.ID
}

// act is the entry's activity: the pane's, or the worker tab's row's.
func (v *view) act(e subEntry) *model.Activity {
	if e.worker != nil {
		return v.activity[e.worker.ID]
	}
	return e.pane.Activity
}

// prompt is what e's sub-row answers: a pane's activity, or for a worker
// the most urgent of its agents' that asks for an answer, which its row's
// state, an error say, may outrank. Else the row's activity.
func (v *view) prompt(e subEntry) *model.Activity {
	if e.worker == nil {
		return e.pane.Activity
	}
	var p *model.Activity
	for i := range v.st.Activities {
		if a := &v.st.Activities[i]; a.WorkspaceID == e.worker.ID && asking(a) {
			p = urgent(p, a)
		}
	}
	if p == nil {
		return v.act(e)
	}
	return p
}

// subsOf is tab id's sub-rows, folded or not: its agent panes when it has
// two or more, then its workers unless they have rows of their own.
func (v *view) subsOf(id string) []subEntry {
	var out []subEntry
	for _, ap := range v.agents[id] {
		out = append(out, subEntry{pane: ap})
	}
	kids := v.subKids(id)
	for i := range kids {
		out = append(out, subEntry{worker: &kids[i]})
	}
	return out
}

// subKids is tab id's workers that are its sub-rows: none when workers
// have rows of their own.
func (v *view) subKids(id string) []model.Workspace {
	if v.separate {
		return nil
	}
	return v.kids[id]
}

// sub is tab tab's sub-row id.
func (v *view) sub(tab, id string) (subEntry, bool) {
	for _, e := range v.subsOf(tab) {
		if e.id() == id {
			return e, true
		}
	}
	return subEntry{}, false
}

// subTab is the tab a sub-row's keys and menu act on: the worker itself,
// or an agent pane's tab.
func subTab(e elem) string {
	if e.worker {
		return e.id
	}
	return e.parent
}

func (r *subRow) hovered() bool {
	return r.click.Hovered() || r.allow.Hovered() || r.deny.Hovered() || r.plan.Hovered()
}

func (s *Sidebar) sub(pane string) *subRow {
	if s.subs == nil {
		s.subs = map[string]*subRow{}
	}
	r := s.subs[pane]
	if r == nil {
		r = &subRow{}
		s.subs[pane] = r
	}
	return r
}

// shownSubs is tab id's sub-rows as drawn: all of them, or while the
// user folded them only those that ask for an answer and the worker on
// screen.
func (s *Sidebar) shownSubs(v *view, id string) []subEntry {
	all := v.subsOf(id)
	if !s.folded[id] {
		return all
	}
	var out []subEntry
	for _, e := range all {
		if asking(v.prompt(e)) || e.worker != nil && e.worker.ID == v.active {
			out = append(out, e)
		}
	}
	return out
}

// asking reports whether a waits on the user's answer: an approval, a
// question or a plan.
func asking(a *model.Activity) bool {
	return a != nil && (a.State == model.StatePendingApproval || a.State == model.StateAwaitingInput || a.State == model.StatePlanReady)
}

// subAnswering reports whether e's sub-row shows Allow and Deny.
func (s *Sidebar) subAnswering(v *view, e subEntry) bool {
	a := v.prompt(e)
	return s.Answers && a != nil && remote.Answerable(*a)
}

// subPlanning reports whether e's sub-row shows Request changes… for its
// agent's plan.
func subPlanning(v *view, e subEntry) bool {
	a := v.prompt(e)
	return a != nil && a.State == model.StatePlanReady
}

// subAsks reports whether e's sub-row has a second line, for Allow and
// Deny or Request changes….
func (s *Sidebar) subAsks(v *view, e subEntry) bool {
	return s.subAnswering(v, e) || subPlanning(v, e)
}

// answerHeight is the Allow and Deny buttons' height: answerH, or more
// for a large ui_size.
func answerHeight(gtx layout.Context, th *theme.Theme) int {
	return max(gtx.Dp(answerH), gtx.Sp(th.Sp(theme.Caption)*1.6))
}

// subHeight is a sub-row's height: a line of small text at 1.5, 3dp
// above and below, and with Allow and Deny a second line for them.
func subHeight(gtx layout.Context, th *theme.Theme, answering bool) int {
	h := gtx.Sp(th.Sp(theme.Small)*1.5) + 2*gtx.Dp(3)
	if answering {
		h += answerHeight(gtx, th) + gtx.Dp(3)
	}
	return h
}

// paneAgent is the agent running in ap.
func paneAgent(ap model.AgentPane) model.Provider {
	if !model.IsAgent(ap.Pane.Provider) && ap.Activity != nil {
		return ap.Activity.Provider
	}
	return ap.Pane.Provider
}

// paneTitle is what an agent pane's sub-row calls it: its own title,
// unless that only names the agent, else its first prompt, else the
// agent's name.
func paneTitle(ap model.AgentPane) string {
	agent := AgentName(paneAgent(ap))
	t := strings.TrimSpace(ap.Pane.Title)
	switch strings.ToLower(t) {
	case "", "~", "claude", "claude code", "codex", "pi", "π", "omp":
	default:
		if !strings.EqualFold(t, agent) {
			return t
		}
	}
	if ap.Pane.Prompt != "" {
		return ap.Pane.Prompt
	}
	return agent
}

// stateNouns names how many panes share a state in agentCount.
var stateNouns = map[model.AgentState][2]string{
	model.StatePendingApproval: {"approval", "approvals"},
	model.StateAwaitingInput:   {"question", "questions"},
	model.StateError:           {"error", "errors"},
	model.StateWorking:         {"working", "working"},
	model.StateConnecting:      {"working", "working"},
	model.StatePlanReady:       {"plan", "plans"},
	model.StatePROpen:          {"PR open", "PRs open"},
	model.StateCompleted:       {"done", "done"},
}

// subCount is a tab row's summary of its sub-rows' activities acts: how
// many agent panes and workers, and how many of them all share the most
// urgent state, "2 agents · 3 workers · 1 approval".
func subCount(agents, workers int, acts []model.Activity) string {
	out := subTotals(agents, workers)
	top := model.Aggregate(acts)
	if top == nil {
		return out
	}
	noun, ok := stateNouns[top.State]
	if !ok {
		return out
	}
	n := 0
	for _, a := range acts {
		if a.State == top.State || model.Pulses(&a) && model.Pulses(top) {
			n++
		}
	}
	if n == 1 {
		return fmt.Sprintf("%s · 1 %s", out, noun[0])
	}
	return fmt.Sprintf("%s · %d %s", out, n, noun[1])
}

// subTotals is "2 agents · 3 workers", either left out at zero.
func subTotals(agents, workers int) string {
	var parts []string
	if agents > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", agents, plural(agents, "agent", "agents")))
	}
	if workers > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", workers, plural(workers, "worker", "workers")))
	}
	return strings.Join(parts, " · ")
}

// familyCount is subCount for tab id's sub-rows.
func (v *view) familyCount(id string) string {
	var acts []model.Activity
	for _, e := range v.subsOf(id) {
		if a := v.act(e); a != nil {
			acts = append(acts, *a)
		}
	}
	return subCount(len(v.agents[id]), len(v.subKids(id)), acts)
}

// subRowAt draws sub-row e under tab, the lifted copy when ghost, and
// reports whether its pill animates.
func (s *Sidebar) subRowAt(gtx layout.Context, v *view, tab string, e subEntry, ghost bool) (layout.Dimensions, bool) {
	th := v.th
	r := s.sub(e.id())
	a, p := v.act(e), v.prompt(e)
	answering := !ghost && s.subAsks(v, e)
	plan := answering && !s.subAnswering(v, e)
	w := gtx.Constraints.Max.X
	h := subHeight(gtx, th, answering)
	line, pad, inset := gtx.Sp(th.Sp(theme.Small)*1.5), gtx.Dp(3), gtx.Dp(8)
	rect := image.Rect(gtx.Dp(subIndent), 0, w, h)
	size := rect.Size()
	rr := gtx.Dp(theme.RadiusControl)
	focused := !ghost && tab == v.active && e.id() == s.Pane
	if e.worker != nil {
		focused = !ghost && e.worker.ID == v.active
	}
	hovered := !ghost && (r.hovered() || s.menuWS == e.id())
	base := th.Sidebar
	switch {
	case ghost, hovered:
		base = th.SurfaceSecondary
	case focused:
		base = theme.Mix(th.Sidebar, th.Primary, 0.08)
	case e.worker != nil && s.selected[e.worker.ID]:
		base = theme.Mix(th.Sidebar, th.Primary, 0.07)
	}
	cursor := !ghost && s.cursorOn('a', e.id())
	if cursor {
		base = cursorFill(th, base, focused)
	}
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	if !ghost {
		base = r.fill(gtx, base, hovered)
		if base != th.Sidebar {
			paint.FillShape(gtx.Ops, base, clip.UniformRRect(image.Rectangle{Max: size}, rr).Op(gtx.Ops))
		}
		if cursor {
			cursorRing(gtx, th, image.Rectangle{Max: size}, rr)
		} else if gtx.Focused(&r.click) {
			g := gtx.Dp(3)
			kit.FocusRing(gtx, th, image.Rectangle{Max: size}.Inset(g), max(rr-g, 0))
		}
	}
	answerW := 0
	if answering {
		answerW = answerSize(gtx, th, *p, base, plan).X
	}
	content := func(gtx layout.Context) layout.Dimensions {
		g := gtx
		g.Constraints = layout.Exact(image.Pt(size.X-2*inset, line))
		o := op.Offset(image.Pt(inset, pad)).Push(gtx.Ops)
		col := theme.Mix(base, th.Fg, 0.85)
		if focused || ghost || model.Tier(a) == model.TierAttention {
			col = th.Fg
		}
		items := []item{
			{w: func(gtx layout.Context) layout.Dimensions { return s.subMark(gtx, v, e, base) }},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				if (e.worker != nil && s.renaming == e.worker.ID || s.replying == e.id()) && !ghost {
					return s.renameField(gtx, th)
				}
				return label(gtx, th, medium(th.UIFont), th.Sp(theme.Small), col, subTitle(e))
			}},
		}
		prog := e.pane.Pane.Program
		if prog != nil && agentReport(v.st, e.pane.Pane) {
			prog = nil
		}
		if e.worker != nil {
			prog = v.program[e.worker.ID]
		}
		if prog != nil {
			// A build the agent runs: its name and progress, before the agent's pill.
			items = append(items, item{opt: true, w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Readable(StateColor(th, prog.Activity()), base), ProgramChip(*prog))
			}})
		}
		if a != nil {
			items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions { return pill(gtx, v, s, *a, base) }})
		}
		hrow(g, line, gtx.Dp(6), items...)
		o.Pop()
		if answering {
			// What it asks to do, then room for Allow and Deny.
			ah := answerHeight(gtx, th)
			g.Constraints = layout.Exact(image.Pt(size.X-2*inset, ah))
			o := op.Offset(image.Pt(inset, pad+line+pad)).Push(gtx.Ops)
			hrow(g, ah, gtx.Dp(6),
				item{shrink: true, ml: gtx.Dp(18), w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Readable(th.Muted, base), clampDetail(p.Detail))
				}},
				item{right: true, w: func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(answerW, 0)}
				}})
			o.Pop()
		}
		return layout.Dimensions{Size: size}
	}
	cg := gtx
	cg.Constraints = layout.Exact(size)
	if ghost {
		content(cg)
		return layout.Dimensions{Size: image.Pt(w, h)}, false
	}
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &r.ctx)
	clickable(cg, &r.click, content)
	area.Pop()
	if answering {
		o := op.Offset(image.Pt(size.X-inset-answerW, pad+line+pad)).Push(gtx.Ops)
		if plan {
			planButton(gtx, th, &r.plan, base)
		} else {
			answerButtons(gtx, th, &r.allow, &r.deny, *p, base)
		}
		o.Pop()
	}
	if e.worker != nil && s.menuWS == e.worker.ID {
		// A worker's menu is its tab's, below its sub-row's right end; the
		// menu places itself from where it is drawn, at the anchor.
		btn := gtx.Dp(24)
		pos := image.Pt(size.X-btn, 0)
		at := s.rowAt.Add(rect.Min).Add(pos)
		o := op.Offset(pos).Push(gtx.Ops)
		m := op.Record(gtx.Ops)
		s.menu(gtx, v, *e.worker, image.Rectangle{Min: at, Max: at.Add(image.Pt(btn, line+2*pad))})
		op.Defer(gtx.Ops, m.Stop())
		o.Pop()
	}
	return layout.Dimensions{Size: image.Pt(w, h)}, model.Pulses(a)
}

// subTitle is what a sub-row calls its pane or worker.
func subTitle(e subEntry) string {
	if e.worker != nil {
		return Title(*e.worker)
	}
	return paneTitle(e.pane)
}

// subMark is a sub-row's 12dp mark: the agent's, or for a worker
// without one its state's glyph or a shell's.
func (s *Sidebar) subMark(gtx layout.Context, v *view, e subEntry, base color.NRGBA) layout.Dimensions {
	th := v.th
	if e.worker == nil {
		return AgentMark(gtx, paneAgent(e.pane), gtx.Dp(12), th.Fg)
	}
	if ag := v.agent[e.worker.ID]; ag != "" {
		return AgentMark(gtx, ag, gtx.Dp(12), th.Fg)
	}
	if a := v.act(e); a != nil {
		return stateGlyph(gtx, th, a.State, base, v.t(s))
	}
	return drawIcon(gtx, icTerminal, gtx.Dp(12), th.Muted, 0)
}

// subGuide draws the tree line from a tab's mark down to sub-row e, h
// tall, an elbow into the last one.
func subGuide(gtx layout.Context, th *theme.Theme, h int, last bool) {
	x, lw := gtx.Dp(18), max(gtx.Dp(1), 1)
	col := theme.Mix(th.Sidebar, th.Muted, 0.35)
	bot := h
	if last {
		bot = h / 2
		paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(x, bot-lw), Max: image.Pt(gtx.Dp(subIndent)-gtx.Dp(2), bot)}.Op())
	}
	paint.FillShape(gtx.Ops, col, clip.Rect{Min: image.Pt(x, 0), Max: image.Pt(x+lw, bot)}.Op())
}

// ghostSubs draws tab id's sub-rows under its lifted row and returns
// their height.
func (s *Sidebar) ghostSubs(gtx layout.Context, v *view, id string) int {
	y := 0
	for _, e := range s.shownSubs(v, id) {
		o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		d, _ := s.subRowAt(gtx, v, id, e, true)
		o.Pop()
		y += d.Size.Y
	}
	return y
}

// foldChip is the count on line 2 of a tab with sub-rows, after a
// chevron that turns as clicking it folds and unfolds them. Short, beside
// Allow and Deny, it only counts them; the pill names the state.
func (s *Sidebar) foldChip(gtx layout.Context, v *view, id string, col color.NRGBA, ghost, short bool) layout.Dimensions {
	th := v.th
	r := s.row(id)
	_, l2 := rowLines(gtx, th)
	text := v.familyCount(id)
	if short {
		text = subTotals(len(v.agents[id]), len(v.subKids(id)))
	}
	draw := func(gtx layout.Context) layout.Dimensions {
		c := col
		if !ghost && r.fold.Hovered() {
			c = th.Fg
		}
		return hrowFit(gtx, l2, gtx.Dp(4),
			item{w: func(gtx layout.Context) layout.Dimensions {
				return chevron(gtx, &r.foldT, !s.folded[id], gtx.Dp(12), c)
			}},
			item{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, medium(th.UIFont), th.Sp(theme.Caption), c, text)
			}})
	}
	if ghost {
		return draw(gtx)
	}
	return clickable(gtx, &r.fold, draw)
}

// chevron draws a disclosure chevron size px wide in col, turning from
// right to down over chevronDur as open becomes true, and back.
func chevron(gtx layout.Context, t *anim.Value, open bool, size int, col color.NRGBA) layout.Dimensions {
	to := float32(0)
	if open {
		to = 1
	}
	t.Set(gtx.Now, to, chevronDur)
	return drawIcon(gtx, icChevronRight, size, col, t.Get(gtx)*math.Pi/2)
}

// subEvents turns clicks on the sub-rows into events: a click on a worker
// is a click on its tab's row, a plain click on a pane focuses it in its
// tab, Allow and Deny answer its prompt, Request changes… opens the field
// for a reply to its plan, a right click opens a worker's tab menu. It
// forgets the sub-rows no longer listed.
func (s *Sidebar) subEvents(gtx layout.Context, v *view, dropped bool) {
	live := map[string]bool{}
	for _, ws := range v.st.Workspaces {
		tab := ws.ID
		for _, e := range v.subsOf(tab) {
			id := e.id()
			live[id] = true
			r := s.sub(id)
			for {
				c, ok := r.click.Update(gtx)
				if !ok {
					break
				}
				switch {
				case dropped:
				case e.worker != nil:
					s.click(v, id, c.Modifiers)
				default:
					s.clickPane(v, tab, id, c.Modifiers)
				}
			}
			for _, b := range []struct {
				c     *widget.Clickable
				allow bool
			}{{&r.allow, true}, {&r.deny, false}} {
				for b.c.Clicked(gtx) {
					if s.subAnswering(v, e) {
						a := v.prompt(e)
						s.events = append(s.events, Answer{PaneID: a.PaneID, At: a.UpdatedAt.UnixNano(), Allow: b.allow})
					}
				}
			}
			for r.plan.Clicked(gtx) {
				if subPlanning(v, e) {
					s.startReply(id, v.prompt(e).PaneID)
				}
			}
			for {
				ev, ok := gtx.Event(pointer.Filter{Target: &r.ctx, Kinds: pointer.Press})
				if !ok {
					break
				}
				if pe, ok := ev.(pointer.Event); ok && pe.Buttons == pointer.ButtonSecondary && e.worker != nil {
					s.toggleMenu(id)
				}
			}
		}
	}
	for id, r := range s.subs {
		if !live[id] {
			delete(s.subs, id)
			continue
		}
		for _, c := range []*widget.Clickable{&r.click, &r.allow, &r.deny, &r.plan} {
			for {
				if _, ok := c.Update(gtx); !ok {
					break
				}
			}
		}
	}
	// why: a fold is the user's choice and outlives the view. The view
	// holds one session of one host, so a tab missing from it may only be
	// on another session or host the window switched to, and forgetting
	// its fold there unfolded it on the way back.
	// ponytail: one entry per folded tab for the window's life; prune by
	// tabs every host closed if that ever matters.
}

// clickPane is a click on pane's sub-row in tab: Ctrl and Shift select the
// tab as a click on its row does, a plain click shows the tab with that
// pane focused.
func (s *Sidebar) clickPane(v *view, tab, pane string, mods key.Modifiers) {
	if mods.Contain(key.ModShortcut) || mods.Contain(key.ModShift) {
		s.click(v, tab, mods)
		return
	}
	clear(s.selected)
	s.anchor = tab
	s.events = append(s.events, SelectWorkspace{WorkspaceID: tab, PaneID: pane})
}
