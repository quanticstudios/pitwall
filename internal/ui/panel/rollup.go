package panel

import (
	"image"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// rollup is what a turn's work did, for its one line: "Worked 2m 14s ·
// 6 tools · 2 files · 1 test failed".
type rollup struct {
	tools    int    // calls, task lists left out
	files    int    // files edits changed, each once
	failed   int    // calls that failed
	failWhat string // what failed when all of one kind: "test", "command"; "call" when mixed
	thoughts int
}

// rollupOf counts the work in content[lo:hi].
func rollupOf(content []session.Item, lo, hi int) rollup {
	var r rollup
	files := map[string]bool{}
	for i := lo; i < hi; i++ {
		it := &content[i]
		if it.Kind == session.KindReasoningText || it.Kind == session.KindReasoningSummaryText {
			r.thoughts++
		}
		c := it.Tool
		if it.Kind != session.KindToolCall || c == nil || isTodo(c) {
			continue
		}
		r.tools++
		if c.Status == "failed" {
			what := failNoun(c)
			if r.failed > 0 && r.failWhat != what {
				what = "call"
			}
			r.failed, r.failWhat = r.failed+1, what
			continue
		}
		if kindOf(c.ToolName) == kindEdit {
			if p := editPath(c); p != "" && !files[p] {
				files[p] = true
				r.files++
			}
		}
	}
	return r
}

// failNoun is what a failed call is called in its chip: a command that
// ran tests is a test.
func failNoun(c *session.ToolCall) string {
	k := kindOf(c.ToolName)
	if k == kindRun {
		cmd := " " + inputMap(c.Input).str("command", "cmd") + " " + c.Command + " "
		for _, w := range []string{" test ", " test,", " tests ", "pytest", "jest", "vitest", "cargo test", "go test"} {
			if strings.Contains(cmd, w) {
				return "test"
			}
		}
	}
	return k.fail
}

// any is whether the turn did work the line can fold.
func (r rollup) any() bool { return r.tools > 0 || r.thoughts > 0 }

// chips are the line's counts; bad marks the failures.
func (r rollup) chips() (out []rollChip) {
	if r.tools > 0 {
		out = append(out, rollChip{text: plural(r.tools, "tool")})
	}
	if r.files > 0 {
		out = append(out, rollChip{text: plural(r.files, "file")})
	}
	if r.failed > 0 {
		out = append(out, rollChip{text: plural(r.failed, r.failWhat) + " failed", bad: true})
	}
	return out
}

type rollChip struct {
	text string
	bad  bool
}

// keptFolded is whether item it of a folded turn still shows: a question
// or a plan always, a failed call in failed, and what zen keeps (reports,
// errors) in failed, none and zen. Folded in all, the user hid it all.
func keptFolded(mode string, it *session.Item) bool {
	if it.Kind == session.KindToolCall && it.Tool != nil && asksUser(it.Tool) {
		return true
	}
	if mode == ToolsAll {
		return false
	}
	if mode == ToolsFailed && it.Kind == session.KindToolCall && it.Tool != nil && it.Tool.Status == "failed" {
		return true
	}
	return zenShows(it)
}

// rollOpen is whether a finished turn's work shows under its line: the
// user's click, else open in all and folded in the other modes.
func (t *Transcript) rollOpen(gtx layout.Context, s *session.Snapshot, whole block, mode string) bool {
	return t.toggled(gtx, turnID(s, whole), mode == ToolsAll)
}

// foldedRows appends the rows a folded turn part b keeps, each on its own,
// and the words between calls while the find bar, which looks in them, is open.
func (t *Transcript) foldedRows(d *cdraw, b block, mode string, quiet map[int]bool, inTurn bool) {
	for i := b.lo; i < b.hi; i++ {
		it := &d.s.Content[i]
		if i == b.reply || quiet[i] || isTodo(it.Tool) || t.tool.shells.isAttached(i) || !keptFolded(mode, it) && !(t.FindOpen() && outside(it)) {
			continue
		}
		seg := segProse
		if it.Kind == session.KindToolCall {
			seg = segItem
		}
		t.rows = append(t.rows, trow{b: b, seg: seg, i: i, top: true, inTurn: inTurn})
	}
}

// turnLast is the last part of block bi's turn: bi when it has no turn.
func (t *Transcript) turnLast(lastOf map[string]int, bi int) block {
	if b := t.blocks[bi]; b.turn == "" {
		return b
	}
	return t.blocks[lastOf[t.blocks[bi].turn]]
}

// turnRuns is whether the turn of block bi runs, as its last part says.
func (t *Transcript) turnRuns(d *cdraw, running func(block, bool) bool, lastOf map[string]int, bi int) bool {
	b := t.turnLast(lastOf, bi)
	return running(b, b.hi == len(d.s.Content))
}

// changeRowIn is changeRow for Ledger: the card inside the turn's column.
func (t *Transcript) changeRowIn(d *cdraw, b block, run bool) {
	n := len(t.rows)
	t.changeRow(d, b, run)
	for i := n; i < len(t.rows); i++ {
		t.rows[i].inTurn = true
	}
}

// turnLine is a turn's work in one line, as the roll-up draws it: a
// chevron, "Worked 2m 14s", the counts as chips with a failure in red,
// the tokens quiet at the end, on a quiet band. A click shows the work
// under it or folds it. A running turn's line says "Working 12s" under
// its work and does not fold.
func (d *cdraw) turnLine(gtx layout.Context, r *trow) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	start, end, _ := turnTimes(d.s, r.b, r.b.hi == len(d.s.Content))
	took := end.Sub(start)
	verb := "Worked"
	if r.running {
		verb, took = "Working", d.t.now.Sub(start)
		d.t.live = true
	}
	label := verb
	if !start.IsZero() && took >= time.Second {
		label += " " + duration(took)
	}
	chips := rollupOf(d.s.Content, r.b.lo, r.b.hi).chips()
	tok := ""
	if n := turnTokens(d.s, r.b.turn); n > 0 {
		tok = tokens(n)
	}
	pt := gtx.Dp(theme.SpaceXS)
	hh := gtx.Dp(rowH)
	band := func(gtx layout.Context, c clicker) layout.Dimensions {
		rad := gtx.Dp(theme.RadiusControl)
		fill := th.Surface
		switch {
		case c.pressed:
			fill = th.Pressed
		case c.hovered:
			fill = th.Hover
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(0, 0, w, hh), rad).Op(gtx.Ops))
		x := gtx.Dp(theme.SpaceS)
		col := th.Muted
		if c.hovered {
			col = th.Fg
		}
		if !r.running {
			chev := "chevron-right"
			if r.open {
				chev = "chevron-down"
			}
			icon(gtx, image.Pt(x, (hh-gtx.Dp(12))/2), chev, 12, col)
			x += gtx.Dp(12) + gtx.Dp(theme.SpaceS)
		} else {
			at(gtx, image.Pt(x, (hh-gtx.Dp(12))/2), func(gtx layout.Context) layout.Dimensions {
				return spinner(gtx, gtx.Dp(12), th.Surface, th.Blue, d.sec)
			})
			x += gtx.Dp(12) + gtx.Dp(theme.SpaceS)
		}
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), col, label)
		})
		base := centerBase(hh, ld)
		at(gtx, image.Pt(x, onBase(base, ld)), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
		x += ld.Size.X + gtx.Dp(theme.SpaceS)
		right := w - gtx.Dp(theme.SpaceS)
		if tok != "" {
			tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.TextQuiet, tok)
			})
			if right-td.Size.X > x {
				right -= td.Size.X
				at(gtx, image.Pt(right, onBase(base, td)), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
				right -= gtx.Dp(theme.SpaceS)
			}
		}
		for _, ch := range chips {
			x += d.rollChip(gtx, ch, x, right, hh) + gtx.Dp(theme.SpaceXS)
		}
		return layout.Dimensions{Size: image.Pt(w, hh)}
	}
	var dims layout.Dimensions
	at(gtx, image.Pt(0, pt), func(gtx layout.Context) layout.Dimensions {
		if r.running {
			dims = band(gtx, clicker{})
			return dims
		}
		c := d.t.click(turnID(d.s, r.b))
		dims = clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
			return band(gtx, clicker{hovered: c.Hovered(), pressed: c.Pressed()})
		})
		return dims
	})
	return layout.Dimensions{Size: image.Pt(w, pt+hh+gtx.Dp(theme.SpaceXS))}
}

type clicker struct{ hovered, pressed bool }

// rollChip draws one count chip at x, a pill with a hairline, and
// returns its width; nothing when it would pass right.
func (d *cdraw) rollChip(gtx layout.Context, ch rollChip, x, right, hh int) int {
	th := d.th
	fg, line := th.Muted, theme.Mix(th.Surface, th.Muted, 0.3)
	if ch.bad {
		fg = th.Readable(th.Red, th.Surface)
		line = theme.Mix(th.Surface, th.Red, 0.45)
	}
	tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), fg, ch.text)
	})
	px := gtx.Dp(theme.SpaceS)
	cw, chh := td.Size.X+2*px, td.Size.Y+gtx.Dp(2)
	if x+cw > right {
		return 0
	}
	y := (hh - chh) / 2
	r := image.Rect(x, y, x+cw, y+chh)
	rrect(gtx, r, chh/2, th.Surface, line)
	at(gtx, image.Pt(x+px, y+(chh-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
	return cw
}

// diffBar is the card head's share of lines added and removed, green
// then red, on a track; w by h px at p.
func diffBar(gtx layout.Context, th *theme.Theme, p image.Point, w, h, add, del int) {
	rad := h / 2
	track := image.Rectangle{Min: p, Max: p.Add(image.Pt(w, h))}
	cl := clip.UniformRRect(track, rad).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, th.Border, clip.Rect(track).Op())
	if add+del > 0 {
		g := addShare(w, add, del)
		paint.FillShape(gtx.Ops, th.Green, clip.Rect{Min: p, Max: p.Add(image.Pt(g, h))}.Op())
		paint.FillShape(gtx.Ops, th.Red, clip.Rect{Min: p.Add(image.Pt(g, 0)), Max: track.Max}.Op())
	}
	cl.Pop()
}

// addShare is the bar's green part of w: add's share of the lines.
func addShare(w, add, del int) int {
	if add+del == 0 {
		return 0
	}
	return w * add / (add + del)
}

// ledgerRoll is the roll-up for Ledger's turn part b, block bi: it adds
// the turn's Worked line above its first part, and the rows a folded
// part keeps. It returns the mode the part's work shows in, and whether
// it is folded, so Ledger lays out nothing more of it.
func (t *Transcript) ledgerRoll(gtx layout.Context, d *cdraw, b block, bi int, mode string, quiet map[int]bool, running func(block, bool) bool, lastOf map[string]int) (string, bool) {
	if t.turnRuns(d, running, lastOf, bi) {
		return mode, false
	}
	whole := b
	whole.lo, whole.hi = b.from, t.turnLast(lastOf, bi).hi
	if !rollupOf(d.s.Content, whole.lo, whole.hi).any() {
		return mode, false
	}
	open := t.rollOpen(gtx, d.s, whole, mode)
	if b.lo == b.from {
		t.rows = append(t.rows, trow{b: whole, seg: segTurn, open: open, inTurn: true})
	}
	if !open {
		t.foldedRows(d, b, mode, quiet, true)
		return mode, true
	}
	return ToolsAll, false
}
