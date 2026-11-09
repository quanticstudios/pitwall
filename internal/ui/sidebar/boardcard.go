package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/board"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

const (
	icHand   = "M18 11V6a2 2 0 0 0-4 0v1M14 10V4a2 2 0 0 0-4 0v2M10 10.5V6a2 2 0 0 0-4 0v8M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.86-5.99-2.34l-3.6-3.6a2 2 0 0 1 2.83-2.82L7 15"
	icServer = "M4 2h16a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2zM4 14h16a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-4a2 2 0 0 1 2-2zM6 6h.01M6 18h.01"
	icWorker = "M9 3v12a3 3 0 0 0 3 3h9M17 14l4 4-4 4"
)

func (s *Sidebar) cardState(id string) *boardCard {
	c := s.bd.cards[id]
	if c == nil {
		c = &boardCard{}
		s.bd.cards[id] = c
	}
	return c
}

// boardCard draws card in gtx's width and returns its size: who and where
// on two lines, then what it asks or last said, and its buttons.
func (s *Sidebar) boardCard(gtx layout.Context, th *theme.Theme, st *model.State, card board.Card) layout.Dimensions {
	w := gtx.Constraints.Max.X
	cs := s.cardState(card.ID)
	s.cardClicks(gtx, st, card, cs)
	acc := kindColor(th, card.Kind)
	need := card.Column() == board.NeedsYou || card.Kind == board.KindError
	base := cardBg(th)
	if need {
		base = theme.Mix(base, acc, 0.08)
	}
	if cs.card.Hovered() {
		base = theme.Mix(base, th.Fg, 0.03)
	}
	padL, padR, padT := gtx.Dp(12), gtx.Dp(11), gtx.Dp(9)
	inner := w - padL - padR
	m := op.Record(gtx.Ops)
	y := 0
	line := func(d layout.Dimensions, gap int) { y += d.Size.Y + gap }
	at := func(x int) op.TransformStack { return op.Offset(image.Pt(x, y)).Push(gtx.Ops) }
	ind := gtx.Dp(cardIndent)
	sub := gtx
	sub.Constraints = layout.Constraints{Max: image.Pt(inner-ind, 1<<20)}

	if card.Parent != "" {
		o := at(ind)
		line(s.workerOf(sub, th, st, card.Parent), gtx.Dp(4))
		o.Pop()
	}
	o := at(0)
	g := gtx
	g.Constraints = layout.Exact(image.Pt(inner, gtx.Dp(20)))
	line(s.cardTitle(g, th, st, card, base), gtx.Dp(3))
	o.Pop()
	o = at(ind)
	g.Constraints = layout.Exact(image.Pt(inner-ind, gtx.Dp(17)))
	line(s.cardWhere(g, th, st, card), 0)
	o.Pop()
	if body := s.cardBody(gtx, th, st, card, cs, base, inner-ind); body != nil {
		y += gtx.Dp(8)
		o = at(ind)
		line(body(sub), 0)
		o.Pop()
	}
	call := m.Stop()
	size := image.Pt(w, y+padT+gtx.Dp(10))
	rect := image.Rectangle{Max: size}
	r := gtx.Dp(8)
	// why: the card's own click area goes under its content, so its buttons take their clicks.
	clickable(gtx, &cs.card, func(gtx layout.Context) layout.Dimensions {
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.UniformRRect(rect, r).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, base, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
		if need || card.Act.Unseen {
			paint.FillShape(gtx.Ops, acc, clip.UniformRRect(image.Rect(0, padT, gtx.Dp(3), size.Y-padT), gtx.Dp(2)).Op(gtx.Ops))
		}
		if s.bd.cur == card.ID {
			paint.FillShape(gtx.Ops, th.Primary, clip.Stroke{Path: clip.UniformRRect(rect.Inset(1), r-1).Path(gtx.Ops), Width: float32(gtx.Dp(1.5))}.Op())
		}
		return layout.Dimensions{Size: size}
	})
	o = op.Offset(image.Pt(padL, padT)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: size}
}

// cardClicks turns card's clicks into events: Allow and Deny, through the
// sidebar's own answer paths, which drop a click whose prompt changed
// since the press and so check the screen before pressing; its other
// buttons; and a click on the card itself, which puts the cursor there,
// or opens the tab on a second click, or peeks at it with Alt.
func (s *Sidebar) cardClicks(gtx layout.Context, st *model.State, card board.Card, cs *boardCard) {
	for {
		e, ok := cs.card.Update(gtx)
		if !ok {
			break
		}
		switch {
		case card.Kind == board.KindTask:
		case e.Modifiers.Contain(key.ModAlt):
			s.events = append(s.events, Peek{WorkspaceID: card.ID, PaneID: card.Act.PaneID})
		case e.NumClicks > 1:
			s.events = append(s.events, SelectWorkspace{WorkspaceID: card.ID, PaneID: card.Act.PaneID})
		}
		s.bd.cur = card.ID
		s.bd.focus = true
	}
	var asking *model.Activity
	armed := s.Answers && !card.Held && s.bd.tr.Armed(card.ID, gtx.Now)
	if armed && card.Answers() == board.AnswerAllow {
		asking = &card.Act
	}
	s.promptClicks(gtx, &cs.allow, &cs.deny, &cs.choices, asking)
	var pressing *model.Activity
	if armed {
		pressing = &card.Act
	}
	open := func() { s.events = append(s.events, SelectWorkspace{WorkspaceID: card.ID, PaneID: card.Act.PaneID}) }
	s.choiceClicks(gtx, &cs.choices, pressing, open)
	for cs.open.Clicked(gtx) {
		switch card.Kind {
		case board.KindPR:
			s.events = append(s.events, OpenPR{WorkspaceID: card.ID})
		case board.KindTask:
			s.events = append(s.events, DropTask{ID: card.Task.ID, Start: true})
		default:
			open()
		}
	}
	for cs.act.Clicked(gtx) {
		switch card.Kind {
		case board.KindPR:
			s.events = append(s.events, ViewDiff{WorkspaceID: card.ID})
		case board.KindError:
			s.events = append(s.events, Reply{WorkspaceID: card.ID, PaneID: card.Act.PaneID, Text: "continue"})
		case board.KindTask:
			s.events = append(s.events, DropTask{ID: card.Task.ID})
		}
	}
}

// workerOf is "worker of" and the orchestrator's title.
func (s *Sidebar) workerOf(gtx layout.Context, th *theme.Theme, st *model.State, parent string) layout.Dimensions {
	name := parent
	if w := findWS(st, parent); w != nil {
		name = Title(*w)
	}
	return hrowFit(gtx, gtx.Dp(16), gtx.Dp(5),
		item{w: func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icWorker, gtx.Dp(11), th.TextQuiet, 0)
		}},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.TextQuiet, "worker of")
		}},
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, name)
		}},
	)
}

// cardTitle is the agent's mark, the tab's title, a pin, the host and the
// state's pill.
func (s *Sidebar) cardTitle(gtx layout.Context, th *theme.Theme, st *model.State, card board.Card, base color.NRGBA) layout.Dimensions {
	title := Title(card.Tab)
	if card.Kind == board.KindTask {
		title = card.Task.Title()
	}
	fg, f := th.Fg, medium(th.UIFont)
	if card.Column() == board.Done && !card.Act.Unseen {
		fg, f = theme.Mix(th.Fg, th.Muted, 0.28), th.UIFont
	}
	items := []item{
		{w: func(gtx layout.Context) layout.Dimensions { return s.cardMark(gtx, th, st, card) }},
		{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, f, th.Sp(theme.Body), fg, title)
		}},
	}
	if card.Tab.Pinned {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icPin, gtx.Dp(12), th.Muted, 0)
		}})
	}
	if card.Host != "" && s.bd.scope.Kind != board.ScopeHost {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions { return HostChip(gtx, th, s.Hosts, card.Host, base) }})
	}
	if p := s.cardPill(card); p != nil && HostDown(s.Hosts, card.Host) {
		items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions { return offlinePill(gtx, th, base) }})
	} else if p != nil {
		items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
			return p(gtx, th, base)
		}})
	}
	return hrow(gtx, gtx.Constraints.Max.Y, gtx.Dp(7), items...)
}

// cardMark is the agent's mark, or a terminal for a command or shell.
func (s *Sidebar) cardMark(gtx layout.Context, th *theme.Theme, st *model.State, card board.Card) layout.Dimensions {
	size := gtx.Dp(16)
	p := card.Act.Provider
	switch {
	case card.Kind == board.KindTask:
		if len(card.Task.Cmd) > 0 {
			p = model.Provider(card.Task.Cmd[0])
		}
	case !model.IsAgent(p):
		p = AgentOf(st, card.Tab)
	}
	if !model.IsAgent(p) {
		return drawCentered(gtx, size, func(gtx layout.Context) layout.Dimensions {
			return drawIcon(gtx, icTerminal, gtx.Dp(14), th.Muted, 0)
		})
	}
	return drawCentered(gtx, size, func(gtx layout.Context) layout.Dimensions { return AgentMark(gtx, p, gtx.Dp(14), th.Fg) })
}

// cardPill is the state in words on the sidebar's pill: a usage limit's
// "Resumes 14:01" in a neutral color, "Stopped" in grey, none for an idle
// agent, a shell or a queued task.
func (s *Sidebar) cardPill(card board.Card) func(gtx layout.Context, th *theme.Theme, base color.NRGBA) layout.Dimensions {
	t := s.now.Sub(s.epoch).Seconds()
	a := card.Act
	switch card.Kind {
	case board.KindIdle, board.KindShell, board.KindTask:
		return nil
	case board.KindStopped:
		return func(gtx layout.Context, th *theme.Theme, base color.NRGBA) layout.Dimensions {
			return statePill(gtx, th, t, model.Activity{}, "Stopped", th.Muted, base)
		}
	case board.KindLimit:
		text := "Usage limit"
		if !a.Resumes.IsZero() {
			text = PillText(a)
		}
		a.State = ""
		return func(gtx layout.Context, th *theme.Theme, base color.NRGBA) layout.Dimensions {
			return statePill(gtx, th, t, a, text, th.Muted, base)
		}
	case board.KindRunning:
		if a.State == "" {
			a.State = model.StateTerminalRunning
		}
		return func(gtx layout.Context, th *theme.Theme, base color.NRGBA) layout.Dimensions {
			return statePill(gtx, th, t, a, "Running", StateColor(th, model.StateTerminalRunning), base)
		}
	case board.KindWatching:
		a.State = model.StateWatching
	case board.KindDone:
		if a.State == "" {
			a.State = model.StateCompleted
		}
	}
	return func(gtx layout.Context, th *theme.Theme, base color.NRGBA) layout.Dimensions {
		return statePill(gtx, th, t, a, PillText(a), StateColor(th, a.State), base)
	}
}

// cardWhere is the line under the title: the group while the scope spans
// several, the branch or, outside a group, the folder, the diff's size,
// and how long the card has been in its state.
func (s *Sidebar) cardWhere(gtx layout.Context, th *theme.Theme, st *model.State, card board.Card) layout.Dimensions {
	h := gtx.Constraints.Max.Y
	mono := th.Sp(theme.Small)
	var items []item
	group, where := "", ""
	if card.Kind == board.KindTask {
		group = projectName(st, card.Task.GroupID)
		where = "this folder"
		if card.Task.Worktree != "" || card.Task.Branch != "" {
			where = "new worktree"
		}
	} else {
		if s.bd.scope.Kind != board.ScopeProject {
			group = projectName(st, card.Tab.ProjectID)
		}
		where = card.Tab.Branch
		if card.Tab.ProjectID == "" || where == "" {
			where = model.ShortPath(st.LivePath(card.Tab))
		}
	}
	if group != "" {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, mono, th.Muted, group)
		}})
	}
	items = append(items, item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, th.MonoFont, mono, th.Muted, where)
	}})
	if bs := st.Stats[card.ID]; card.Kind != board.KindTask && (bs.Additions > 0 || bs.Deletions > 0) {
		if bs.Additions > 0 {
			items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.MonoFont, mono, th.Readable(th.Green, cardBg(th)), fmt.Sprintf("+%d", bs.Additions))
			}})
		}
		if bs.Deletions > 0 {
			items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.MonoFont, mono, th.Readable(th.Red, cardBg(th)), fmt.Sprintf("-%d", bs.Deletions))
			}})
		}
	}
	if HostDown(s.Hosts, card.Host) {
		items = append(items, item{right: true, ml: gtx.Dp(6), w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, mono, th.TextQuiet, s.lastSeen(card.Host, s.now))
		}})
	} else if t, warn := cardTime(card, s.now); t != "" {
		col := th.TextQuiet
		if warn {
			col = th.Readable(th.Yellow, cardBg(th))
		}
		items = append(items, item{right: true, ml: gtx.Dp(6), w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, mono, col, t)
		}})
	}
	return hrow(gtx, h, gtx.Dp(6), items...)
}

func projectName(st *model.State, id string) string {
	for _, p := range st.Projects {
		if p.ID == id && id != "" {
			return p.Name
		}
	}
	return ""
}

// cardTime is "waiting 4m" for an ask, "8m ago" for a finished turn,
// "quiet 24m" (warn) for a still agent, else how long it has been at it.
func cardTime(c board.Card, now time.Time) (string, bool) {
	if c.Quiet() {
		return "quiet " + span(now.Sub(c.Act.QuietSince)), true
	}
	if c.Act.UpdatedAt.IsZero() {
		return "", false
	}
	d := now.Sub(c.Act.UpdatedAt)
	switch {
	case d < 5*time.Second:
		return "just now", false
	case c.Column() == board.NeedsYou:
		return "waiting " + span(d), false
	case c.Column() == board.Done:
		return span(d) + " ago", false
	}
	return span(d), false
}

// span is a duration as "40s", "4m" or "1h 2m".
func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d%time.Hour < time.Minute:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

// cardBody is what card asks or last said, with its buttons, nil for
// nothing.
func (s *Sidebar) cardBody(gtx layout.Context, th *theme.Theme, st *model.State, card board.Card, cs *boardCard, base color.NRGBA, w int) layout.Widget {
	var parts []layout.Widget
	text := func(t string, col color.NRGBA, lines int) {
		if t = strings.TrimSpace(t); t != "" {
			parts = append(parts, func(gtx layout.Context) layout.Dimensions {
				return para(gtx, th, th.UIFont, th.Sp(theme.Small), col, t, lines)
			})
		}
	}
	said := theme.Mix(th.Fg, th.Muted, 0.14)
	a := card.Act
	armed := s.Answers && s.bd.tr.Armed(card.ID, s.now)
	down := HostDown(s.Hosts, card.Host)
	switch {
	case down && card.Kind == board.KindApproval:
		parts = append(parts, s.askBox(th, st, card, ""))
		text(answerLater(card.Host)+". The prompt may have changed meanwhile.", th.Muted, 2)
	case down && (card.Kind == board.KindInput || card.Kind == board.KindPlan || card.Kind == board.KindError):
		text(firstLine(a.Detail), th.Fg, 3)
		text(answerLater(card.Host)+".", th.Muted, 2)
	case card.Held:
		parts = append(parts, s.askBox(th, st, card, "Answered"))
	case card.Kind == board.KindApproval:
		parts = append(parts, s.askBox(th, st, card, ""))
		parts = append(parts, s.approvalButtons(th, card, cs, base, armed))
	case card.Kind == board.KindInput:
		text(a.Detail, th.Fg, 6)
		parts = append(parts, s.choiceRow(th, card, cs, base, armed, "Open tab"))
	case card.Kind == board.KindPlan:
		text(cmpStr(a.Detail, "The plan is in the tab."), th.Fg, 6)
		parts = append(parts, s.choiceRow(th, card, cs, base, armed, "Read plan"))
	case card.Kind == board.KindError:
		parts = append(parts, func(gtx layout.Context) layout.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, "Turn failed. "+firstLine(a.Detail), 3)
		})
		parts = append(parts, s.buttonRow(th, cs, base, &cs.act, "Continue", th.Primary, "Open tab"))
	case card.Kind == board.KindLimit:
		msg := "Usage limit."
		if _, resets, ok := agent.UsageLimit(a.Detail, s.now); ok && !resets.IsZero() {
			msg = "Usage limit. Resets at " + resets.Local().Format("15:04") + "."
		}
		if !a.Resumes.IsZero() {
			msg += " pitwall types continue at " + a.Resumes.Local().Format("15:04") + "."
		}
		text(msg, th.Fg, 3)
		parts = append(parts, s.buttonRow(th, cs, base, nil, "", color.NRGBA{}, "Open tab"))
	case card.Kind == board.KindPR:
		if pr, ok := st.PRs[card.ID]; ok {
			text(prLine(pr), said, 2)
		}
		parts = append(parts, func(gtx layout.Context) layout.Dimensions {
			return hrowFit(gtx, gtx.Dp(24), gtx.Dp(6),
				item{w: func(gtx layout.Context) layout.Dimensions {
					return kit.Button(gtx, th, &cs.open, kit.Ghost, kit.Small, "Open PR")
				}},
				item{w: func(gtx layout.Context) layout.Dimensions {
					return kit.Button(gtx, th, &cs.act, kit.Ghost, kit.Small, "View diff")
				}})
		})
	case card.Kind == board.KindTask:
		text(taskWhen(card.Task, s.now), said, 2)
		parts = append(parts, func(gtx layout.Context) layout.Dimensions {
			return hrowFit(gtx, gtx.Dp(24), gtx.Dp(6),
				item{w: func(gtx layout.Context) layout.Dimensions {
					return kit.Button(gtx, th, &cs.open, kit.Ghost, kit.Small, "Start now")
				}},
				item{w: func(gtx layout.Context) layout.Dimensions {
					return kit.Button(gtx, th, &cs.act, kit.Ghost, kit.Small, "Remove")
				}})
		})
	case len(card.Workers) > 0:
		text(cmpStr(a.Detail, card.Tab.Status), said, 2)
		parts = append(parts, s.fleetBox(th, st, card))
	case card.Kind == board.KindStopped:
		text("No agent runs in this tab.", said, 2)
	case card.Kind == board.KindRunning:
		text(cmpStr(a.Detail, card.Tab.Status), said, 2)
		if p := tabProgramOf(st, card.Tab); p != nil && p.Progress >= 0 {
			parts = append(parts, func(gtx layout.Context) layout.Dimensions {
				return progressWide(gtx, th, *p)
			})
		}
	default:
		text(cmpStr(card.Tab.Status, a.Detail), said, 2)
	}
	if card.Held {
		parts = append(parts, func(gtx layout.Context) layout.Dimensions {
			return heldLine(gtx, th, "Now "+board.Words[card.Now]+". It moves when the pointer or focus leaves.")
		})
	}
	if len(parts) == 0 {
		return nil
	}
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Max: image.Pt(w, 1<<20)}
		y := 0
		for i, p := range parts {
			if i > 0 {
				y += gtx.Dp(8)
			}
			o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
			y += p(gtx).Size.Y
			o.Pop()
		}
		return layout.Dimensions{Size: image.Pt(w, y)}
	}
}

func cmpStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// askBox is an approval's tool and folder, then its whole command in mono
// on the card's own background, clamped to four lines when it is too long
// to answer from a card.
// over replaces the tool's name: "Answered" on a held card.
func (s *Sidebar) askBox(th *theme.Theme, st *model.State, card board.Card, over string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		a := card.Act
		w := gtx.Constraints.Max.X
		tool, body := "Approval", a.Command
		if a.Command != "" {
			tool = "Bash"
		}
		if body == "" {
			body = a.Detail
		}
		if over != "" {
			tool = over
		}
		where := paneDir(st, card)
		lines := 1 << 10
		if card.Answers() == board.AnswerLong {
			lines = 4
		}
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(w, 1<<20)}
		head := hrowFit(g, gtx.Dp(15), gtx.Dp(6),
			item{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.TextQuiet, tool)
			}},
			item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.MonoFont, th.Sp(theme.Caption), th.TextQuiet, where)
			}})
		o := op.Offset(image.Pt(0, head.Size.Y+gtx.Dp(3))).Push(gtx.Ops)
		d := cmdPara(g, th, th.Fg, body, lines)
		o.Pop()
		return layout.Dimensions{Size: image.Pt(w, head.Size.Y+gtx.Dp(3)+d.Size.Y)}
	}
}

// paneDir is where card's pane runs: "host:" for a remote tab, then its
// folder.
func paneDir(st *model.State, card board.Card) string {
	dir := card.Tab.Path
	for _, p := range st.Panes {
		if p.ID == card.Act.PaneID && p.Cwd != "" {
			dir = p.Cwd
		}
	}
	dir = model.ShortPath(dir)
	if card.Host != "" {
		return card.Host + ":" + dir
	}
	return dir
}

// approvalButtons are Allow and Deny as the sidebar draws them, with
// Always behind a divider where the prompt offers it; or, for a command
// too long to read on a card or on the guard's deny list, the way to the
// tab and why. Unarmed, they draw faded and take no clicks.
func (s *Sidebar) approvalButtons(th *theme.Theme, card board.Card, cs *boardCard, base color.NRGBA, armed bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(24)
		switch card.Answers() {
		case board.AnswerLong, board.AnswerGuard, board.AnswerUnknown:
			why := "Too long to answer from a card"
			switch card.Answers() {
			case board.AnswerGuard:
				why = "On the guard's deny list"
			case board.AnswerUnknown:
				why = "No command to check from a card"
			}
			d := hrowFit(gtx, h, gtx.Dp(8), item{w: pillButton(&cs.open, th, base, h, "Read it in the tab", th.Yellow)})
			o := op.Offset(image.Pt(0, d.Size.Y+gtx.Dp(6))).Push(gtx.Ops)
			n := label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.TextQuiet, why)
			o.Pop()
			return layout.Dimensions{Size: image.Pt(max(d.Size.X, n.Size.X), d.Size.Y+gtx.Dp(6)+n.Size.Y)}
		case board.AnswerNone:
			return kit.Button(gtx, th, &cs.open, kit.Ghost, kit.Small, "Open tab")
		case board.AnswerChat:
			d := kit.Button(gtx, th, &cs.open, kit.Ghost, kit.Small, "Open tab")
			o := op.Offset(image.Pt(0, d.Size.Y+gtx.Dp(6))).Push(gtx.Ops)
			n := label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.TextQuiet, "A chat's approval is answered in its tab")
			o.Pop()
			return layout.Dimensions{Size: image.Pt(max(d.Size.X, n.Size.X), d.Size.Y+gtx.Dp(6)+n.Size.Y)}
		}
		allow, deny := &cs.allow, &cs.deny
		var always *widget.Clickable
		if len(card.Act.Buttons) > 0 {
			always = &cs.choices.btn[0]
		}
		if !armed {
			allow, deny, always = nil, nil, nil
		}
		items := []item{
			{w: pillButton(allow, th, base, h, "Allow", th.Green)},
			{w: pillButton(deny, th, base, h, "Deny", th.Red)},
		}
		if len(card.Act.Buttons) > 0 {
			items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
				d := gtx.Dp(16)
				paint.FillShape(gtx.Ops, th.BorderStrong, clip.Rect{Min: image.Pt(gtx.Dp(2), 0), Max: image.Pt(gtx.Dp(3), d)}.Op())
				return layout.Dimensions{Size: image.Pt(gtx.Dp(5), d)}
			}}, item{w: func(gtx layout.Context) layout.Dimensions {
				if always == nil {
					return kit.Button(gtx, th, &cs.choices.btn[0], kit.Ghost.Disabled(), kit.Small, "Always…")
				}
				return kit.Button(gtx, th, always, kit.Ghost, kit.Small, "Always…")
			}})
		}
		var fade func()
		if !armed {
			fade = paint.PushOpacity(gtx.Ops, 0.45).Pop
		}
		d := hrowFit(gtx, h, gtx.Dp(6), items...)
		if fade != nil {
			fade()
		}
		return d
	}
}

// choiceRow is a question's or plan's answers from the screen, as the
// sidebar draws them, on a line of their own, then a way to the tab.
func (s *Sidebar) choiceRow(th *theme.Theme, card board.Card, cs *boardCard, base color.NRGBA, armed bool, openText string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(24)
		y := 0
		if n := len(card.Act.Buttons); n > 0 {
			b := answerBtns{choices: &cs.choices}
			if !armed {
				b = answerBtns{}
			}
			g := gtx
			g.Constraints = layout.Constraints{Max: image.Pt(gtx.Constraints.Max.X, h)}
			var d layout.Dimensions
			if card.Kind == board.KindPlan {
				d = pillButton(b.choice(0), th, base, h, card.Act.Buttons[0], th.Purple)(g)
			} else {
				d = choicePills(g, th, b, card.Act.Buttons, base)
			}
			y = d.Size.Y + gtx.Dp(6)
		}
		o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		d := kit.Button(gtx, th, &cs.open, kit.Ghost, kit.Small, openText)
		o.Pop()
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y+d.Size.Y)}
	}
}

// buttonRow is an optional pill in col and a ghost button to the tab.
func (s *Sidebar) buttonRow(th *theme.Theme, cs *boardCard, base color.NRGBA, c *widget.Clickable, text string, col color.NRGBA, openText string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(24)
		var items []item
		if c != nil {
			items = append(items, item{w: pillButton(c, th, base, h, text, col)})
		}
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			return kit.Button(gtx, th, &cs.open, kit.Ghost, kit.Small, openText)
		}})
		return hrowFit(gtx, h, gtx.Dp(6), items...)
	}
}

// heldLine says a held card's tab moved on.
func heldLine(gtx layout.Context, th *theme.Theme, t string) layout.Dimensions {
	px, py := gtx.Dp(8), gtx.Dp(5)
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Constraints.Max.X-2*px-gtx.Dp(18), 1<<20)}
	d := para(g, th, th.UIFont, th.Sp(theme.Small), th.Fg, t, 3)
	call := m.Stop()
	size := image.Pt(gtx.Constraints.Max.X, d.Size.Y+2*py)
	paint.FillShape(gtx.Ops, th.SelectedBg, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	o := op.Offset(image.Pt(px, py+gtx.Dp(2))).Push(gtx.Ops)
	drawIcon(gtx, icHand, gtx.Dp(12), th.Fg, 0)
	o.Pop()
	o = op.Offset(image.Pt(px+gtx.Dp(18), py)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: size}
}

// prLine is "#138 2 of 7 checks running, no review yet".
func prLine(pr model.PR) string {
	n, pass, run, fail := 0, 0, 0, 0
	for _, c := range pr.Checks {
		switch c.State {
		case model.CheckPass:
			pass++
		case model.CheckPending:
			run++
		case model.CheckFail:
			fail++
		default:
			continue
		}
		n++
	}
	checks := "no checks"
	switch {
	case fail > 0:
		checks = fmt.Sprintf("%d of %d checks failed", fail, n)
	case run > 0:
		checks = fmt.Sprintf("%d of %d checks running", run, n)
	case n > 0:
		checks = fmt.Sprintf("%d of %d checks passed", pass, n)
	}
	review := "no review yet"
	switch pr.Review {
	case model.ReviewApproved:
		review = "approved"
	case model.ReviewChanges:
		review = "changes requested"
	case model.ReviewRequired:
		review = "review required"
	}
	return fmt.Sprintf("#%d %s, %s", pr.Number, checks, review)
}

// taskWhen says when a queued task starts.
func taskWhen(t model.Task, now time.Time) string {
	if t.NextStart.IsZero() {
		return "Starts when an agent in its session finishes"
	}
	s := "Starts at " + Clock(t.NextStart.Local(), now)
	if t.Repeat > 0 {
		s += ", every " + span(t.Repeat)
	}
	return s
}

// fleetBox is an orchestrator's workers: a bar of one segment per worker
// in its state's color, and their counts in words.
func (s *Sidebar) fleetBox(th *theme.Theme, st *model.State, card board.Card) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		w := gtx.Constraints.Max.X
		n := len(card.Workers)
		gap, bh := gtx.Dp(2), gtx.Dp(4)
		seg := (w - gap*(n-1)) / max(n, 1)
		for i, id := range card.Workers {
			c := s.bd.b.All[id]
			col := kindColor(th, c.Kind)
			x := i * (seg + gap)
			paint.FillShape(gtx.Ops, col, clip.UniformRRect(image.Rect(x, 0, x+max(seg, gtx.Dp(3)), bh), gtx.Dp(2)).Op(gtx.Ops))
		}
		words := []struct {
			col  board.Column
			icon string
			c    color.NRGBA
			w    string
		}{
			{board.NeedsYou, icCircleAlert, th.Yellow, "need you"},
			{board.Failed, icCircleX, th.Red, "failed"},
			{board.Working, icLoader, th.Blue, "working"},
			{board.PROpen, icGitPullRequest, th.Blue, "PR open"},
			{board.Done, icCircleCheck, th.Green, "done"},
		}
		x, y, lh := 0, bh+gtx.Dp(6), gtx.Dp(18)
		for _, wd := range words {
			cnt := card.Fleet[wd.col]
			if cnt == 0 {
				continue
			}
			col := th.Readable(wd.c, cardBg(th))
			m := op.Record(gtx.Ops)
			d := hrowFit(gtx, lh, gtx.Dp(3),
				item{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, wd.icon, gtx.Dp(11), col, 0) }},
				item{w: func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, medium(th.UIFont), th.Sp(theme.Small), col, fmt.Sprintf("%d %s", cnt, wd.w))
				}})
			call := m.Stop()
			if x > 0 && x+d.Size.X > w {
				x, y = 0, y+lh+gtx.Dp(2)
			}
			o := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
			x += d.Size.X + gtx.Dp(10)
		}
		return layout.Dimensions{Size: image.Pt(w, y+lh)}
	}
}

// tabProgramOf is the most recent status report of ws's panes.
func tabProgramOf(st *model.State, ws model.Workspace) *model.Program {
	var out *model.Program
	for i := range st.Panes {
		if p := st.Panes[i].Program; st.Panes[i].WorkspaceID == ws.ID && p != nil && (out == nil || p.UpdatedAt.After(out.UpdatedAt)) {
			out = p
		}
	}
	return out
}

// progressWide is a watched command's progress across the card.
func progressWide(gtx layout.Context, th *theme.Theme, p model.Program) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Dp(4)
	paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rect(0, 0, w, h), h/2).Op(gtx.Ops))
	if fw := w * p.Progress / 100; fw > 0 {
		paint.FillShape(gtx.Ops, th.Blue, clip.UniformRRect(image.Rect(0, 0, max(fw, h), h), h/2).Op(gtx.Ops))
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}
