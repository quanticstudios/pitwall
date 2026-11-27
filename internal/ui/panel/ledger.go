package panel

import (
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Chat transcript layouts, matching [chat] layout.
const (
	LayoutColumn = "column"
	LayoutLedger = "ledger"
)

// Ledger layout geometry, ported from the Ledger direction mock and user
// notes: a text column capped at 768 dp and centered in the pane, as wide
// as the composer card, with the time and agent gutter hanging left of it.
const (
	ledgerTimeW   unit.Dp = 44
	ledgerNameMin unit.Dp = 58
	ledgerNameMax unit.Dp = 96
	ledgerGap     unit.Dp = theme.SpaceM // 12 dp
	ledgerFadeH   unit.Dp = 28
	planStripH    unit.Dp = 38
)

func (t *Transcript) isLedger() bool { return t.ChatLayout == LayoutLedger }

// ledgerNameDp is the agent name column's width in dp: the mock's 58 dp for
// short names (Claude, Codex, Gemini) and wider for longer names such as
// Antigravity so the name never clips.
func ledgerNameDp(prov model.Provider) unit.Dp {
	name := sidebar.AgentName(prov)
	if len(name) <= 7 {
		return ledgerNameMin
	}
	return min(ledgerNameMax, ledgerNameMin+unit.Dp((len(name)-7)*5))
}

// LedgerGutter is the width in dp of Ledger's time and name gutter for
// prov's chats, which the composer's chatin.LedgerColumn takes so its card
// lines up with the transcript's text.
func LedgerGutter(prov model.Provider) unit.Dp {
	return ledgerTimeW + ledgerGap + ledgerNameDp(prov) + ledgerGap
}

// ledgerGeom is one frame's resolved pixel widths for Ledger.
type ledgerGeom struct {
	timeW, nameW, gap int
	gutterW           int  // time, name and the gaps after each
	textX, textW      int  // the text column, the composer card's too
	outside           bool // the gutter hangs left of the text; else it sits above it
	x0, ledgerW       int  // the gutter and the text together: dividers, the plan strip
}

func ledgerMetrics(gtx layout.Context, prov model.Provider, availW int) ledgerGeom {
	timeW := gtx.Dp(ledgerTimeW)
	nameW := gtx.Dp(ledgerNameDp(prov))
	gap := gtx.Dp(ledgerGap)
	gutterW := timeW + gap + nameW + gap
	textX, textW, outside := chatin.LedgerColumn(gtx, availW, gtx.Dp(LedgerGutter(prov)))
	g := ledgerGeom{
		timeW: timeW, nameW: nameW, gap: gap, gutterW: gutterW,
		textX: textX, textW: textW, outside: outside,
		x0: textX, ledgerW: textW,
	}
	if outside {
		g.x0, g.ledgerW = textX-gutterW, textW+gutterW
	}
	return g
}

// ledgerHeadH is the height of the time and name line that sits above a
// row's text when the gutter does not fit beside it.
func (d *cdraw) ledgerHeadH(gtx layout.Context) int {
	return gtx.Sp(d.th.Sp(theme.Body) * 1.6)
}

// buildLedgerRows lays out the transcript for Ledger: each user prompt and
// each agent turn is a row with the time and name in the gutter on its
// first line, and every visible tool call is its own row in the text column.
func (t *Transcript) buildLedgerRows(gtx layout.Context, d *cdraw) {
	t.rows = t.rows[:0]
	running := runningTurns(d.s)
	stopped := interruptedTurns(d.s)
	quiet := quietSettings(d.s.Content)
	mode := t.toolMode()
	t.tool.shells.index(d.s.Content)
	k := 0
	lastOf := map[string]int{}
	for bi, b := range t.blocks {
		lastOf[b.turn] = bi
	}
	turnHasGutter := map[int]bool{} // keyed by block.from
	markGutter := func(idx, from int, when time.Time) {
		if idx < 0 || idx >= len(t.rows) || turnHasGutter[from] {
			return
		}
		turnHasGutter[from] = true
		t.rows[idx].gutter = true
		t.rows[idx].gutterAt = when
		t.rows[idx].divider = idx > 0
	}
	lastTurnFrom := -1
	lastTurnOpen := false
	for bi, b := range t.blocks {
		if bi > 0 && t.blocks[bi-1].turn != "" && t.blocks[bi-1].turn != b.turn && stopped[t.blocks[bi-1].turn] {
			t.rows = append(t.rows, trow{tail: d.interrupted, inTurn: true})
		}
		for ; k < len(t.done) && t.done[k].at <= b.lo; k++ {
			t.resRowLedger(gtx, block{}, &t.done[k])
		}
		if b.kind == blockAsk {
			start := len(t.rows)
			t.resRowLedger(gtx, b, t.askLine(d.s, b.lo))
			if len(t.rows) > start {
				when := ledgerItemTime(d.s, b.from)
				markGutter(start, b.from, when)
			}
			lastTurnFrom, lastTurnOpen = b.from, true
			continue
		}
		if b.kind != blockTurn {
			if !quiet[b.lo] {
				r := trow{b: b}
				if b.kind == blockPrompt {
					r.gutter, r.gutterAt, r.divider = true, ledgerItemTime(d.s, b.lo), len(t.rows) > 0
				}
				t.rows = append(t.rows, r)
			}
			lastTurnOpen = false
			continue
		}
		last := b.turn == "" || lastOf[b.turn] == bi
		run := running(b, b.hi == len(d.s.Content)) && last
		whole := b
		whole.lo = b.from
		turnStart, _, _ := turnTimes(d.s, whole, b.hi == len(d.s.Content))
		if turnStart.IsZero() {
			turnStart = ledgerItemTime(d.s, b.from)
		}
		start := len(t.rows)
		mode, folded := t.ledgerRoll(gtx, d, b, bi, mode, quiet, running, lastOf)
		for i := b.lo; i < b.hi && !folded; i++ {
			it := &d.s.Content[i]
			switch {
			case i == b.reply || quiet[i] || isTodo(it.Tool) || t.tool.shells.isAttached(i):
			case mode == ToolsZen:
				if zenShows(it) {
					kind := segProse
					if it.Kind == session.KindToolCall {
						kind = segItem
					}
					t.rows = append(t.rows, trow{b: b, seg: kind, i: i, top: true, inTurn: true})
				}
			case it.Kind == session.KindToolCall && callHidden(mode, it.Tool):
			case outside(it):
				t.rows = append(t.rows, trow{b: b, seg: segProse, i: i, inTurn: true})
			case mode != ToolsAll && it.Kind != session.KindToolCall && it.Kind != session.KindReasoningText && it.Kind != session.KindReasoningSummaryText:
				// With calls hidden, hooks and notices left alone make no line.
			default:
				t.rows = append(t.rows, trow{b: b, seg: segItem, i: i, top: true, inTurn: true})
			}
		}
		if b.reply >= 0 && !(mode == ToolsZen && run) {
			for ; k < len(t.done) && t.done[k].at <= b.reply; k++ {
				t.resRowLedger(gtx, block{}, &t.done[k])
			}
			t.rows = append(t.rows, trow{b: b, seg: segReply, running: run, inTurn: true})
		}
		if run && !hasPendingRequest(d.s, t.sent) {
			st := turnStart
			t.rows = append(t.rows, trow{b: whole, tail: func(gtx layout.Context) layout.Dimensions { return d.ledgerWorking(gtx, st) }, inTurn: true})
		}
		t.changeRowIn(d, b, run)
		if len(t.rows) > start {
			markGutter(start, b.from, turnStart)
		}
		lastTurnFrom, lastTurnOpen = b.from, true
	}
	if n := len(t.blocks); n > 0 && stopped[t.blocks[n-1].turn] {
		t.rows = append(t.rows, trow{tail: d.interrupted, inTurn: true})
	}
	for ; k < len(t.done); k++ {
		t.resRowLedger(gtx, block{}, &t.done[k])
	}
	tailStart := len(t.rows)
	for _, w := range d.tail() {
		t.rows = append(t.rows, trow{tail: w, inTurn: true})
	}
	// If tail rows open an agent response that had no items yet (for example
	// right after sending a prompt), give the first tail row the agent gutter.
	if len(t.rows) > tailStart && (!lastTurnOpen || !turnHasGutter[lastTurnFrom]) {
		when := parseTime(d.s.UpdatedAt)
		if n := len(d.s.Content); n > 0 && when.IsZero() {
			when = parseTime(d.s.Content[n-1].UpdatedAt)
		}
		t.rows[tailStart].gutter = true
		t.rows[tailStart].gutterAt = when
		t.rows[tailStart].divider = tailStart > 0
	}
	for i := range t.rows {
		t.rows[i].gap = ledgerRowGap(t.rows, i)
		t.rows[i].rank = rowRank(t.rows, i)
	}
}

func (t *Transcript) resRowLedger(gtx layout.Context, b block, r *resolved) {
	if !r.gone {
		t.rows = append(t.rows, trow{b: b, res: r, open: t.toggled(gtx, "res:"+r.id, false), inTurn: true})
	}
}

func hasPendingRequest(s *session.Snapshot, sent map[string]bool) bool {
	for _, a := range s.PendingApprovals {
		if !sent[a.ID] {
			return true
		}
	}
	for _, in := range s.PendingUserInputs {
		if !sent[in.ID] {
			return true
		}
	}
	return s.PlanApproval != nil && !sent[s.PlanApproval.ID]
}

func ledgerItemTime(s *session.Snapshot, i int) time.Time {
	if i < 0 || i >= len(s.Content) {
		return time.Time{}
	}
	if t := parseTime(s.Content[i].CreatedAt); !t.IsZero() {
		return t
	}
	return parseTime(s.Content[i].UpdatedAt)
}

// ledgerPromptRow is whether r is a user prompt's row.
func ledgerPromptRow(r *trow) bool { return r.tail == nil && r.res == nil && r.b.kind == blockPrompt }

// ledgerRowGap is the space above row i in Ledger.
func ledgerRowGap(rows []trow, i int) unit.Dp {
	if i == 0 {
		return 0
	}
	r, prev := &rows[i], &rows[i-1]
	switch {
	case r.gutter:
		return theme.SpaceM
	case ledgerPromptRow(r):
		return theme.SpaceL
	case r.inTurn && prev.inTurn:
		if r.seg == segItem && prev.seg == segItem && r.tail == nil && prev.tail == nil && r.res == nil && prev.res == nil {
			return theme.Space2XS
		}
		return theme.SpaceS
	}
	return gapInTurn
}

// keepLayoutScroll preserves the reading position when switching live
// between Column and Ledger.
func (t *Transcript) keepLayoutScroll(s *session.Snapshot) {
	if !t.list.Position.BeforeEnd {
		t.ToEnd()
		return
	}
	if t.anchor != "" {
		t.keepPlace(s)
		t.list.Position.Offset = 0
	}
}

// ledgerGutter draws the gutter for a row: HH:MM in mono muted, then the
// agent's name in its color, or You for the user's own message.
func (d *cdraw) ledgerGutter(gtx layout.Context, when time.Time, rowHpx int, you bool) {
	th := d.th
	name := d.agent()
	col := th.Readable(sidebar.AgentColor(d.s.Provider, th.Fg), th.Bg)
	if you {
		name, col = "You", th.Fg
	}
	tm := ""
	if !when.IsZero() {
		tm = when.Local().Format("15:04")
	}
	nc, nd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		g := gtx
		g.Constraints.Max.X = d.geom.nameW
		return d.lbl(g, semibold(th.UIFont), th.Sp(theme.Body), col, name)
	})
	base := centerBase(rowHpx, nd)
	if tm != "" {
		tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			g := gtx
			g.Constraints.Max.X = d.geom.timeW
			return d.lbl(g, th.MonoFont, th.Sp(theme.Small), th.Muted, tm)
		})
		at(gtx, image.Pt(0, onBase(base, td)), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
	}
	at(gtx, image.Pt(d.geom.timeW+d.geom.gap, onBase(base, nd)), func(gtx layout.Context) layout.Dimensions { nc.Add(gtx.Ops); return nd })
}

// ledgerPrompt draws the user's message as a ledger row's text: in the text
// column like a reply, no bubble, with Copy, Edit and Rewind on hover at the
// row's bottom right.
func (d *cdraw) ledgerPrompt(gtx layout.Context, it *session.Item) layout.Dimensions {
	w := d.geom.textW
	var dims layout.Dimensions
	if slashCommand(it.Text) && len(it.Attachments) == 0 {
		dims = d.commandChip(gtx, strings.TrimSpace(it.Text), false)
	} else {
		dims = vstack(gtx, gtx.Dp(theme.SpaceS),
			func(gtx layout.Context) layout.Dimensions { return d.senderLine(gtx, it) },
			func(gtx layout.Context) layout.Dimensions { return d.attachments(gtx, it) },
			func(gtx layout.Context) layout.Dimensions { return d.promptText(gtx, it) },
			func(gtx layout.Context) layout.Dimensions { return d.pastes(gtx, it) },
		)
	}
	d.promptTools(gtx, it, w+gtx.Dp(theme.SpaceXS), dims.Size.Y)
	return layout.Dimensions{Size: image.Pt(w, dims.Size.Y)}
}

// ledgerWorking is the quiet running-turn indicator in Ledger's text column.
func (d *cdraw) ledgerWorking(gtx layout.Context, start time.Time) layout.Dimensions {
	th := d.th
	d.t.live = true
	hh := gtx.Dp(hit)
	at(gtx, image.Pt(0, (hh-gtx.Dp(12))/2), func(gtx layout.Context) layout.Dimensions {
		return spinner(gtx, gtx.Dp(12), th.Bg, th.Blue, d.sec)
	})
	title := "Working…"
	if !start.IsZero() {
		if took := d.t.now.Sub(start); took >= time.Second {
			title = "Working for " + duration(took)
		}
	}
	c, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, title)
	})
	at(gtx, image.Pt(gtx.Dp(12)+gtx.Dp(theme.SpaceS), (hh-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions {
		c.Add(gtx.Ops)
		return td
	})
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, hh)}
}

// ledgerApprovalCard is the Ledger direction's compact approval card (.ask):
// shield + question and mono command on the left, Allow / Allow for session /
// Deny with keycaps on the right (wrapping underneath when narrow).
func (d *cdraw) ledgerApprovalCard(gtx layout.Context, a session.Approval) layout.Dimensions {
	th := d.th
	bs := d.t.answer[a.ID]
	if bs == nil {
		bs = new([3]widget.Clickable)
		d.t.answer[a.ID] = bs
	}
	what, why := requestWhat(a, d.t.Dir), approvalWhy(a)
	e := d.t.reasonField(a.ID)
	w := gtx.Constraints.Max.X
	px, py := gtx.Dp(theme.SpaceM), gtx.Dp(theme.SpaceS+theme.Space2XS)
	bg := theme.Mix(th.Bg, th.Yellow, 0.06)
	ring := theme.Mix(th.Surface, th.Yellow, 0.30)
	yellow := th.Readable(th.Yellow, bg)
	innerW := max(0, w-2*px)

	title := d.requestTitle(a)
	if why != "" {
		title += " · " + why
	}
	left := func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceXS+theme.Space2XS),
			func(gtx layout.Context) layout.Dimensions {
				return hrow(gtx, gtx.Dp(theme.SpaceS),
					func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, "shield", gtx.Dp(14), yellow) },
					func(gtx layout.Context) layout.Dimensions {
						g := gtx
						g.Constraints.Max.X = max(0, gtx.Constraints.Max.X-gtx.Dp(14)-gtx.Dp(theme.SpaceS))
						return label(g, th, medium(th.UIFont), th.Sp(theme.Small), yellow, title, 2)
					})
			},
			func(gtx layout.Context) layout.Dimensions {
				if what == "" {
					return layout.Dimensions{}
				}
				st := d.mono()
				st.c = th.Fg
				return d.selectable(gtx, d.t.sel("req:"+a.ID), st, what)
			},
		)
	}
	btns := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions { return d.keyButton(gtx, &bs[0], true, "Allow", "1", true) },
		func(gtx layout.Context) layout.Dimensions {
			if a.NoSession {
				return layout.Dimensions{}
			}
			return d.keyButton(gtx, &bs[1], false, "Allow for session", "2", true)
		},
		func(gtx layout.Context) layout.Dimensions {
			if e != nil {
				return layout.Dimensions{}
			}
			return d.keyButton(gtx, &bs[2], false, "Deny", denyKey(a), true)
		},
	}

	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(innerW, gtx.Constraints.Max.Y)}
	actCall, actDims := measure(g, func(gtx layout.Context) layout.Dimensions {
		return hrow(gtx, gtx.Dp(6), btns...)
	})
	colGap := gtx.Dp(theme.SpaceL)
	sideBySide := e == nil && innerW-actDims.Size.X-colGap >= gtx.Dp(180)

	bodyCall, bodyDims := measure(g, func(gtx layout.Context) layout.Dimensions {
		if sideBySide {
			lg := gtx
			lg.Constraints = layout.Constraints{Max: image.Pt(innerW-actDims.Size.X-colGap, gtx.Constraints.Max.Y)}
			lc, ld := measure(lg, left)
			h := max(ld.Size.Y, actDims.Size.Y)
			at(gtx, image.Pt(0, (h-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
			at(gtx, image.Pt(innerW-actDims.Size.X, (h-actDims.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { actCall.Add(gtx.Ops); return actDims })
			return layout.Dimensions{Size: image.Pt(innerW, h)}
		}
		return vstack(gtx, gtx.Dp(theme.SpaceS),
			left,
			func(gtx layout.Context) layout.Dimensions { return wrapRow(gtx, gtx.Dp(6), btns...) },
			func(gtx layout.Context) layout.Dimensions {
				if e == nil {
					return layout.Dimensions{}
				}
				return d.reasonRow(gtx, e, &bs[2], "Tell "+d.agent()+" what to do instead", "Deny")
			},
		)
	})
	h := bodyDims.Size.Y + 2*py
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusPopover), bg, ring)
	at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { bodyCall.Add(gtx.Ops); return bodyDims })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// latestTodo is the chat's newest non-empty task list, nil for none.
func latestTodo(s *session.Snapshot) []session.Task {
	for i := len(s.Content) - 1; i >= 0; i-- {
		if c := s.Content[i].Tool; isTodo(c) && len(c.Tasks) > 0 {
			return c.Tasks
		}
	}
	return nil
}

// latestChanges is the newest turn that edited files, its first item id and
// its changed files.
func (t *Transcript) latestChanges(d *cdraw) (string, []fileChange) {
	for i := len(t.blocks) - 1; i >= 0; i-- {
		b := t.blocks[i]
		if b.kind != blockTurn && b.kind != blockAsk || b.more || b.lo >= len(d.s.Content) {
			continue
		}
		b.kind, b.lo = blockTurn, b.from
		if fs := t.changesOf(d, b); len(fs) > 0 {
			return d.s.Content[b.lo].ID, fs
		}
	}
	return "", nil
}

// planStrip draws Ledger's plan & changes strip under the chat header:
// "Plan", step segments, "4 of 5", current step text, and on the right the
// turn's changed files with +/− counts (clicking opens the Changes panel)
// and an expand toggle for the full checklist. Returns 0 height when neither
// a todo list nor changed files exist.
func (d *cdraw) planStrip(gtx layout.Context, size image.Point) int {
	th := d.th
	tasks := latestTodo(d.s)
	turnID, fs := d.t.latestChanges(d)
	if len(tasks) == 0 && len(fs) == 0 {
		return 0
	}
	h := gtx.Dp(planStripH)
	open := len(tasks) > 0 && d.t.toggled(gtx, "plan-strip", false)
	planCk := d.t.click("plan-strip")
	planLeftCk := d.t.click("plan-strip:left")
	for planLeftCk.Clicked(gtx) && len(tasks) > 0 {
		open = !open
		d.t.open["plan-strip"] = open
	}
	chgCk := d.t.click("plan-strip:changes")
	for chgCk.Clicked(gtx) && len(fs) > 0 {
		if d.t.side.on && !d.t.side.file && !d.t.side.servers {
			d.t.CloseFiles()
		} else {
			d.t.showChanges(turnID, "")
		}
	}

	var bodyCall op.CallOp
	var bodyDims layout.Dimensions
	if open {
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(d.geom.ledgerW, gtx.Dp(320))}
		bodyCall, bodyDims = measure(g, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: theme.SpaceS, Bottom: theme.SpaceM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.todoList(gtx, tasks)
			})
		})
	}
	totalH := h + bodyDims.Size.Y
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: image.Pt(size.X, totalH)}.Op())
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, totalH-max(1, gtx.Dp(1))), Max: image.Pt(size.X, totalH)}.Op())

	x0, w := d.geom.x0, d.geom.ledgerW
	if w <= 0 {
		return totalH
	}
	// Right side: changed files button + expand chevron.
	var rightParts []layout.Widget
	if len(fs) > 0 {
		add, del := changeTotals(fs)
		rightParts = append(rightParts, func(gtx layout.Context) layout.Dimensions {
			return clickable(gtx, chgCk, func(gtx layout.Context) layout.Dimensions {
				bh, px := gtx.Dp(hit), gtx.Dp(theme.SpaceS)
				lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
					return hrow(gtx, gtx.Dp(6),
						func(gtx layout.Context) layout.Dimensions {
							return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, plural(len(fs), "file"))
						},
						func(gtx layout.Context) layout.Dimensions { return d.counts(gtx, add, del, false) },
					)
				})
				bw := ld.Size.X + 2*px
				switch {
				case chgCk.Pressed():
					paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(image.Rect(0, 0, bw, bh), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
				case chgCk.Hovered():
					paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rect(0, 0, bw, bh), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
				}
				at(gtx, image.Pt(px, (bh-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
				return layout.Dimensions{Size: image.Pt(bw, bh)}
			})
		})
	}
	if len(tasks) > 0 {
		rightParts = append(rightParts, func(gtx layout.Context) layout.Dimensions {
			chev := "chevron-down"
			if open {
				chev = "chevron-up"
			}
			return d.ghost(gtx, planCk, chev, "", false)
		})
	}
	rc, rd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return hrow(gtx, gtx.Dp(theme.SpaceXS), rightParts...)
	})
	rx := x0 + w - rd.Size.X
	at(gtx, image.Pt(rx, (h-rd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { rc.Add(gtx.Ops); return rd })

	// Left side: Plan label, progress segments, "4 of 5", current step.
	if len(tasks) > 0 {
		done := tasksDone(tasks)
		now := ""
		for _, task := range tasks {
			if task.Status == "in_progress" {
				now = taskText(task)
				break
			}
		}
		if now == "" {
			for _, task := range tasks {
				if task.Status != "completed" {
					now = taskText(task)
					break
				}
			}
		}
		if now == "" {
			now = "All done"
		}
		leftW := max(0, rx-x0-gtx.Dp(theme.SpaceM))
		at(gtx, image.Pt(x0, 0), func(gtx layout.Context) layout.Dimensions {
			g := gtx
			g.Constraints = layout.Constraints{Max: image.Pt(leftW, h)}
			return clickable(g, planLeftCk, func(gtx layout.Context) layout.Dimensions {
				return d.planStripLeft(gtx, tasks, done, now, h)
			})
		})
	}
	if open && bodyDims.Size.Y > 0 {
		at(gtx, image.Pt(x0, h), func(gtx layout.Context) layout.Dimensions {
			bodyCall.Add(gtx.Ops)
			return bodyDims
		})
	}
	return totalH
}

func (d *cdraw) planStripLeft(gtx layout.Context, tasks []session.Task, done int, now string, h int) layout.Dimensions {
	th := d.th
	maxW := gtx.Constraints.Max.X
	x := 0
	gap := gtx.Dp(10)
	// "Plan"
	lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, "Plan")
	})
	base := centerBase(h, ld)
	at(gtx, image.Pt(x, onBase(base, ld)), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
	x += ld.Size.X + gap

	// Segments
	segW, segH, segGap := gtx.Dp(18), gtx.Dp(4), gtx.Dp(3)
	if len(tasks) > 10 {
		segW = max(gtx.Dp(6), (gtx.Dp(180)-segGap*(len(tasks)-1))/len(tasks))
	}
	segsW := len(tasks)*segW + max(0, len(tasks)-1)*segGap
	if x+segsW < maxW {
		sy := (h - segH) / 2
		rad := segH / 2
		for i, task := range tasks {
			col := th.SurfaceElevated
			switch task.Status {
			case "completed":
				col = th.Green
			case "in_progress":
				if d.s.Running {
					d.t.live = true
					col = theme.Mix(th.Bg, th.Blue, pulse(d.sec))
				} else {
					col = th.Blue
				}
			}
			sx := x + i*(segW+segGap)
			paint.FillShape(gtx.Ops, col, clip.UniformRRect(image.Rect(sx, sy, sx+segW, sy+segH), rad).Op(gtx.Ops))
		}
		x += segsW + gap
	}

	// "4 of 5"
	countStr := strconv.Itoa(done) + " of " + strconv.Itoa(len(tasks))
	cc, cd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, countStr)
	})
	if x+cd.Size.X < maxW {
		at(gtx, image.Pt(x, onBase(base, cd)), func(gtx layout.Context) layout.Dimensions { cc.Add(gtx.Ops); return cd })
		x += cd.Size.X + gap
	}

	// Current step text
	if room := maxW - x; room > gtx.Dp(40) {
		g := gtx
		g.Constraints.Max.X = room
		nc, nd := measure(g, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Fg, now)
		})
		at(gtx, image.Pt(x, onBase(base, nd)), func(gtx layout.Context) layout.Dimensions { nc.Add(gtx.Ops); return nd })
		x += nd.Size.X
	}
	return layout.Dimensions{Size: image.Pt(x, h)}
}

// bottomFade paints a smooth vertical gradient at the bottom of the transcript
// so scrolled content fades behind the floating composer card.
func (d *cdraw) bottomFade(gtx layout.Context, size image.Point) {
	fh := min(gtx.Dp(ledgerFadeH), size.Y)
	if fh <= 0 {
		return
	}
	r := image.Rect(0, size.Y-fh, size.X, size.Y)
	cl := clip.Rect(r).Push(gtx.Ops)
	top := d.th.Bg
	top.A = 0
	paint.LinearGradientOp{
		Stop1:  f32.Pt(0, float32(r.Min.Y)),
		Color1: top,
		Stop2:  f32.Pt(0, float32(r.Max.Y)),
		Color2: d.th.Bg,
	}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	cl.Pop()
}

// ledgerRowFirstLineH is the height used to vertically center the gutter's
// time and agent name against the first line of row r.
func (d *cdraw) ledgerRowFirstLineH(gtx layout.Context, r *trow) int {
	if r.tail == nil && r.res == nil && r.seg == segItem {
		return gtx.Dp(rowH)
	}
	if r.tail != nil {
		return gtx.Dp(hit)
	}
	if r.seg == segTurn {
		return gtx.Dp(rowH) + 2*gtx.Dp(theme.SpaceXS)
	}
	return gtx.Sp(d.th.Sp(theme.Large) * 1.45)
}
