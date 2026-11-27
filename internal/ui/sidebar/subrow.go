package sidebar

import (
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"
	"time"

	"gioui.org/f32"
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
	choices                  choices
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
	return r.click.Hovered() || r.allow.Hovered() || r.deny.Hovered() || r.plan.Hovered() || r.choices.hovered()
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
// Deny, Request changes… or a question's options.
func (s *Sidebar) subAsks(v *view, e subEntry) bool {
	a := v.prompt(e)
	return s.subAnswering(v, e) || subPlanning(v, e) || a != nil && a.State == model.StateAwaitingInput && len(a.Buttons) > 0
}

// answerHeight is the answer buttons' height: answerH, or more
// for a large ui_size.
func answerHeight(gtx layout.Context, th *theme.Theme) int {
	return max(gtx.Dp(answerH), gtx.Sp(th.Sp(theme.Caption)*1.6))
}

// subPad is the space above and below a sub-row's line, in dp: 26dp
// rows at the default size.
const subPad = 4

// subHeight is a sub-row's height: a line of small text at 1.5, subPad
// above and below, and with Allow and Deny a second line for them.
func subHeight(gtx layout.Context, th *theme.Theme, answering bool) int {
	h := gtx.Sp(th.Sp(theme.Small)*1.5) + 2*gtx.Dp(subPad)
	if answering {
		h += answerHeight(gtx, th) + gtx.Dp(subPad)
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

// subRowAt draws sub-row e under tab, the lifted copy when ghost, and
// reports whether its pill animates.
func (s *Sidebar) subRowAt(gtx layout.Context, v *view, tab string, e subEntry, ghost bool) (layout.Dimensions, bool) {
	th := v.th
	r := s.sub(e.id())
	a, p := v.act(e), v.prompt(e)
	answering := !ghost && s.subAsks(v, e)
	w := gtx.Constraints.Max.X
	h := subHeight(gtx, th, answering)
	line, pad, inset := gtx.Sp(th.Sp(theme.Small)*1.5), gtx.Dp(subPad), gtx.Dp(8)
	// Inside its tab's tile: its right column lines up with the tab's.
	rect := image.Rect(gtx.Dp(subIndent), 0, w-gtx.Dp(4), h)
	size := rect.Size()
	rr := gtx.Dp(theme.RadiusControl)
	focused := !ghost && tab == v.active && e.id() == s.Pane
	if e.worker != nil {
		focused = !ghost && e.worker.ID == v.active
	}
	hovered := !ghost && (r.hovered() || s.menuWS == e.id())
	tile := s.row(tab).tile // the tab's tile, drawn just before
	if tile.A == 0 || ghost {
		tile = th.Sidebar
	}
	base := tile
	switch {
	case ghost, hovered:
		base = tileHover(th, th.SurfaceSecondary)
	case focused:
		base = theme.Mix(tile, th.Primary, 0.12)
	case e.worker != nil && s.selected[e.worker.ID]:
		base = theme.Mix(tile, th.Primary, 0.1)
	}
	cursor := !ghost && s.cursorOn('a', e.id())
	if cursor {
		base = cursorFill(th, base, focused)
	}
	subGuide(gtx, th, tile, rect.Min.X, pad+line/2)
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	if !ghost {
		base = r.fill(gtx, base, hovered)
		if base != tile {
			paint.FillShape(gtx.Ops, base, clip.UniformRRect(image.Rectangle{Max: size}, rr).Op(gtx.Ops))
		}
		if cursor {
			cursorRing(gtx, th, image.Rectangle{Max: size}, rr)
		} else if gtx.Focused(&r.click) {
			g := gtx.Dp(3)
			kit.FocusRing(gtx, th, image.Rectangle{Max: size}.Inset(g), max(rr-g, 0))
		}
	}
	// What it asks to do keeps a few words beside them.
	answerG := gtx
	answerG.Constraints = layout.Constraints{Max: image.Pt(max(size.X-2*inset-gtx.Dp(18+6+36), 0), answerHeight(gtx, th))}
	answerW := 0
	if answering {
		answerW = answerSize(answerG, th, *p, base).X
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
			{w: func(gtx layout.Context) layout.Dimensions {
				// A 13dp mark in a 14dp slot, as the mock's worker rows.
				o := op.Offset(image.Pt(gtx.Dp(0.5), gtx.Dp(0.5))).Push(gtx.Ops)
				s.subMark(gtx, v, e, base)
				o.Pop()
				return layout.Dimensions{Size: image.Pt(gtx.Dp(14), gtx.Dp(14))}
			}},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				if (e.worker != nil && s.renaming == e.worker.ID || s.replying == e.id()) && !ghost {
					return s.renameField(gtx, th)
				}
				return label(gtx, th, medium(th.UIFont), th.Sp(theme.Small), col, subTitle(e))
			}},
		}
		if e.worker != nil {
			// A worker on another machine than the tab it hangs under names it.
			if host := s.hostOf(e.worker.ID); host != s.hostOf(v.rootOf[e.worker.ID]) {
				items = append(items, item{opt: true, w: func(gtx layout.Context) layout.Dimensions { return HostChip(gtx, th, s.Hosts, host, base) }})
			}
		}
		prog := e.pane.Pane.Program
		if prog != nil && agentReport(v.st, e.pane.Pane) {
			prog = nil
		}
		if e.worker != nil {
			prog = v.program[e.worker.ID]
		}
		if prog != nil && stateOnly(*prog) {
			prog = nil // the pill beside it already says the state
		}
		if prog != nil {
			// A build the agent runs: its name and progress, before the agent's pill.
			items = append(items, item{opt: true, w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Readable(StateColor(th, prog.Activity()), base), ProgramChip(*prog))
			}})
		}
		if a != nil {
			// Only the dot: the tab's line 2 counts their states once.
			m := markOf(th, *a)
			items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions { return stateDot(gtx, m, base, v.t(s)) }})
		}
		hrow(g, line, gtx.Dp(8), items...)
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
		answerArea(answerG, th, answerBtns{allow: &r.allow, deny: &r.deny, plan: &r.plan, choices: &r.choices}, *p, base)
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
	return layout.Dimensions{Size: image.Pt(w, h)}, model.Pulses(a) && !anim.Reduced()
}

// subGuide draws a sub-row's corner connector in a tile filled fill: a
// 1dp L whose foot ends at x, at y, rounding its corner by 6dp.
func subGuide(gtx layout.Context, th *theme.Theme, fill color.NRGBA, x, y int) {
	lw := float32(max(gtx.Dp(1), 1))
	x0, x1 := float32(x-gtx.Dp(12))+lw/2, float32(x)
	y0, y1 := float32(-gtx.Dp(5)), float32(y)-lw/2
	r := float32(gtx.Dp(6))
	const k = 0.5523 // a cubic's control distance for a quarter circle
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(x0, y0))
	p.LineTo(f32.Pt(x0, y1-r))
	p.CubeTo(f32.Pt(x0, y1-r+k*r), f32.Pt(x0+r-k*r, y1), f32.Pt(x0+r, y1))
	p.LineTo(f32.Pt(x1, y1))
	paint.FillShape(gtx.Ops, tileHairline(th, fill), clip.Stroke{Path: p.End(), Width: lw}.Op())
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

// ghostSubs draws tab id's sub-rows under its lifted row and returns
// their height.
func (s *Sidebar) ghostSubs(gtx layout.Context, v *view, id string) int {
	subs := s.shownSubs(v, id)
	if len(subs) == 0 {
		return 0
	}
	subsHairline(gtx, v.th, v.th.SurfaceSecondary, 0, gtx.Constraints.Max.X)
	y := subsTop(gtx)
	for _, e := range subs {
		o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		d, _ := s.subRowAt(gtx, v, id, e, true)
		o.Pop()
		y += d.Size.Y
	}
	return y + subsBottom(gtx)
}

// foldChip is the chevron on line 2 of a tab with sub-rows, which turns
// as clicking it folds and unfolds them.
func (s *Sidebar) foldChip(gtx layout.Context, v *view, id string, col color.NRGBA, ghost bool) layout.Dimensions {
	r := s.row(id)
	_, l2 := rowLines(gtx, v.th)
	draw := func(gtx layout.Context) layout.Dimensions {
		c := col
		if !ghost && r.fold.Hovered() {
			c = v.th.Fg
		}
		return hrowFit(gtx, l2, 0, item{w: func(gtx layout.Context) layout.Dimensions {
			return chevron(gtx, &r.foldT, !s.folded[id], gtx.Dp(12), c)
		}})
	}
	if ghost {
		return draw(gtx)
	}
	return clickable(gtx, &r.fold, draw)
}

// workerChip is the people icon and n on line 1 of a tab with sub-rows,
// in col.
func workerChip(gtx layout.Context, th *theme.Theme, n int, col color.NRGBA) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return hrowFit(gtx, gtx.Constraints.Max.Y, gtx.Dp(3),
		item{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icPeople, gtx.Dp(13), col, 0) }},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.MonoFont), th.Sp(theme.Caption), col, strconv.Itoa(n))
		}})
}

// chipColor is the worker chip's color on base: Blue while any of the
// tab's sub-rows works, else Muted.
func chipColor(th *theme.Theme, working bool, base color.NRGBA) color.NRGBA {
	if working {
		return th.Readable(th.Blue, base)
	}
	return th.Readable(th.Muted, base)
}

// workerChip is how many sub-rows tab id has and whether any works:
// Working in the foreground, or Connecting.
func (v *view) workerChip(id string) (n int, working bool) {
	subs := v.subsOf(id)
	for _, e := range subs {
		if a := v.act(e); a != nil && markOf(v.th, *a).loud && model.Tier(a) != model.TierAttention {
			working = true
		}
	}
	return len(subs), working
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
			var asking *model.Activity
			if s.subAnswering(v, e) {
				asking = v.prompt(e)
			}
			s.promptClicks(gtx, &r.allow, &r.deny, &r.choices, asking)
			for r.plan.Clicked(gtx) {
				if subPlanning(v, e) {
					s.startReply(id, v.prompt(e).PaneID)
				}
			}
			s.choiceClicks(gtx, &r.choices, v.prompt(e), func() {
				if e.worker != nil {
					s.click(v, id, 0)
				} else {
					s.clickPane(v, tab, id, 0)
				}
			})
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
// tab as a click on its row does, Alt peeks at the pane, a plain click
// shows the tab with that pane focused.
func (s *Sidebar) clickPane(v *view, tab, pane string, mods key.Modifiers) {
	if mods.Contain(key.ModAlt) {
		s.events = append(s.events, Peek{WorkspaceID: tab, PaneID: pane})
		return
	}
	if mods.Contain(key.ModShortcut) || mods.Contain(key.ModShift) {
		s.click(v, tab, mods)
		return
	}
	clear(s.selected)
	s.anchor = tab
	s.events = append(s.events, SelectWorkspace{WorkspaceID: tab, PaneID: pane})
}
