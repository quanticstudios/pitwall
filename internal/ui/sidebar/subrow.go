package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"time"

	"gioui.org/io/key"
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

// A tab with two or more agent panes lists them under its row, one
// compact sub-row each: the agent's mark, the pane's own title, its state
// pill, and Allow and Deny on the one asking.

// subIndent is where a sub-row starts in its tab's width, past the tab's
// mark, so the sub-rows' marks line up with the tab's title.
const subIndent unit.Dp = 24

// chevronDur is how long a disclosure chevron takes to turn.
const chevronDur = 150 * time.Millisecond

// subRow is an agent pane's sub-row: its click area, Allow and Deny, and
// its eased fill.
type subRow struct {
	click, allow, deny widget.Clickable
	easedFill
}

func (r *subRow) hovered() bool { return r.click.Hovered() || r.allow.Hovered() || r.deny.Hovered() }

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

// shownSubs is tab id's sub-rows as drawn: its agent panes, unless it has
// fewer than two or the user folded them.
func (s *Sidebar) shownSubs(v *view, id string) []model.AgentPane {
	if s.folded[id] {
		return nil
	}
	return v.agents[id]
}

// subAnswering reports whether ap's sub-row shows Allow and Deny.
func (s *Sidebar) subAnswering(ap model.AgentPane) bool {
	return s.Answers && ap.Activity != nil && remote.Answerable(*ap.Activity)
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
	case "", "~", "claude", "claude code", "codex", "pi", "π":
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
	model.StateCompleted:       {"done", "done"},
}

// agentCount is a tab row's summary of its agent panes: how many, and how
// many share the most urgent state, "3 agents · 1 approval".
func agentCount(aps []model.AgentPane) string {
	out := fmt.Sprintf("%d agents", len(aps))
	var acts []model.Activity
	for _, ap := range aps {
		if ap.Activity != nil {
			acts = append(acts, *ap.Activity)
		}
	}
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

// agentPane is pane of tab id from the view.
func (v *view) agentPane(id, pane string) (model.AgentPane, bool) {
	for _, ap := range v.agents[id] {
		if ap.Pane.ID == pane {
			return ap, true
		}
	}
	return model.AgentPane{}, false
}

// subRowAt draws agent pane ap's sub-row under tab, the lifted copy when
// ghost, and reports whether its pill animates.
func (s *Sidebar) subRowAt(gtx layout.Context, v *view, tab string, ap model.AgentPane, ghost bool) (layout.Dimensions, bool) {
	th := v.th
	r := s.sub(ap.Pane.ID)
	a := ap.Activity
	answering := !ghost && s.subAnswering(ap)
	w := gtx.Constraints.Max.X
	h := subHeight(gtx, th, answering)
	line, pad, inset := gtx.Sp(th.Sp(theme.Small)*1.5), gtx.Dp(3), gtx.Dp(8)
	rect := image.Rect(gtx.Dp(subIndent), 0, w, h)
	size := rect.Size()
	rr := gtx.Dp(theme.RadiusControl)
	focused := !ghost && tab == v.active && ap.Pane.ID == s.Pane
	hovered := !ghost && r.hovered()
	base := th.Sidebar
	switch {
	case ghost, hovered:
		base = th.SurfaceSecondary
	case focused:
		base = theme.Mix(th.Sidebar, th.Primary, 0.08)
	}
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	if !ghost {
		base = r.fill(gtx, base, hovered)
		if base != th.Sidebar {
			paint.FillShape(gtx.Ops, base, clip.UniformRRect(image.Rectangle{Max: size}, rr).Op(gtx.Ops))
		}
		if gtx.Focused(&r.click) {
			g := gtx.Dp(3)
			kit.FocusRing(gtx, th, image.Rectangle{Max: size}.Inset(g), max(rr-g, 0))
		}
	}
	answerW := 0
	if answering {
		answerW = answerSize(gtx, th, *a, base).X
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
				return AgentMark(gtx, paneAgent(ap), gtx.Dp(12), th.Fg)
			}},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, medium(th.UIFont), th.Sp(theme.Small), col, paneTitle(ap))
			}},
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
					return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Readable(th.Muted, base), clampDetail(a.Detail))
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
	clickable(cg, &r.click, content)
	if answering {
		o := op.Offset(image.Pt(size.X-inset-answerW, pad+line+pad)).Push(gtx.Ops)
		answerButtons(gtx, th, &r.allow, &r.deny, *a, base)
		o.Pop()
	}
	return layout.Dimensions{Size: image.Pt(w, h)}, model.Pulses(a)
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
	for _, ap := range s.shownSubs(v, id) {
		o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		d, _ := s.subRowAt(gtx, v, id, ap, true)
		o.Pop()
		y += d.Size.Y
	}
	return y
}

// foldChip is the agent count on line 2 of a tab with sub-rows, after a
// chevron that turns as clicking it folds and unfolds them. Short, beside
// Allow and Deny, it only counts them; the pill names the state.
func (s *Sidebar) foldChip(gtx layout.Context, v *view, id string, col color.NRGBA, ghost, short bool) layout.Dimensions {
	th := v.th
	r := s.row(id)
	_, l2 := rowLines(gtx, th)
	text := agentCount(v.agents[id])
	if short {
		text = fmt.Sprintf("%d agents", len(v.agents[id]))
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

// subEvents turns clicks on the sub-rows into events: a plain click
// focuses that pane in its tab, Allow and Deny answer that pane's prompt.
// It forgets the sub-rows of panes no longer shown.
func (s *Sidebar) subEvents(gtx layout.Context, v *view, dropped bool) {
	live := map[string]bool{}
	for tab, aps := range v.agents {
		for _, ap := range aps {
			live[ap.Pane.ID] = true
			r := s.sub(ap.Pane.ID)
			for {
				c, ok := r.click.Update(gtx)
				if !ok {
					break
				}
				if !dropped {
					s.clickPane(v, tab, ap.Pane.ID, c.Modifiers)
				}
			}
			for _, b := range []struct {
				c     *widget.Clickable
				allow bool
			}{{&r.allow, true}, {&r.deny, false}} {
				for b.c.Clicked(gtx) {
					if s.subAnswering(ap) {
						s.events = append(s.events, Answer{PaneID: ap.Pane.ID, At: ap.Activity.UpdatedAt.UnixNano(), Allow: b.allow})
					}
				}
			}
		}
	}
	for id, r := range s.subs {
		if !live[id] {
			delete(s.subs, id)
			continue
		}
		for _, c := range []*widget.Clickable{&r.click, &r.allow, &r.deny} {
			for {
				if _, ok := c.Update(gtx); !ok {
					break
				}
			}
		}
	}
	for id := range s.folded {
		if v.agents[id] == nil {
			delete(s.folded, id)
		}
	}
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
