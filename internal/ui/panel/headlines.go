package panel

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// headlineTimeW is the time column of a headline row in Column, as the
// route C mock draws it: "16:20" then the line.
const headlineTimeW unit.Dp = 44

// headlinesKey changes when the Headlines view's rows would: the mode and
// the chat's headlines.
func (t *Transcript) headlinesKey() uint64 {
	if t.ToolCalls != ToolsHeadlines || t.FindOpen() {
		return 0
	}
	var h uint64 = 14695981039346656037
	for _, x := range t.Headlines {
		for _, s := range [2]string{x.Turn, x.Text} {
			for i := range len(s) {
				h ^= uint64(s[i])
				h *= 1099511628211
			}
			h ^= 0xff
			h *= 1099511628211
		}
	}
	return h
}

// headlineRows folds the built rows for the Headlines view: each finished
// turn with a headline, its prompt and all its rows, becomes one line that
// opens in place. The latest turn and one that asks the user something
// stay as they are, and so does a turn the model gave no line. While the
// find bar is open every turn shows, since find looks in them.
func (t *Transcript) headlineRows(gtx layout.Context, s *session.Snapshot) {
	if t.ToolCalls != ToolsHeadlines || t.FindOpen() || len(t.Headlines) == 0 || len(t.rows) == 0 {
		return
	}
	latest := ""
	if n := len(s.Turns); n > 0 {
		latest = s.Turns[n-1].ID
	}
	rows := t.rows
	out := make([]trow, 0, len(rows))
	for i := 0; i < len(rows); {
		turn := rowTurn(s, rows, i)
		j := i + 1
		for j < len(rows) && turn != "" && (rows[j].res != nil || rowTurn(s, rows, j) == turn) {
			j++
		}
		hb, ok := turnSpan(rows[i:j])
		if turn == "" || !ok || turn == latest || model.HeadlineOf(t.Headlines, turn) == "" || asksOpen(s, hb) {
			out = append(out, rows[i:j]...)
			i = j
			continue
		}
		open := t.toggled(gtx, headlineID(turn), false)
		out = append(out, trow{b: hb, seg: segHeadline, open: open})
		if open {
			n := len(out)
			out = append(out, rows[i:j]...)
			out[n].gap, out[n].divider = theme.SpaceXS, false
		}
		i = j
	}
	for i := range out {
		if out[i].seg == segHeadline {
			out[i].gap = 0
			if i > 0 && out[i-1].seg != segHeadline {
				out[i].gap = theme.SpaceS // under an opened turn or one left open
			}
		} else if i > 0 && out[i-1].seg == segHeadline && !out[i-1].open {
			out[i].gap = max(out[i].gap, theme.SpaceS) // the latest turn under the list
		}
		out[i].rank = rowRank(out, i)
	}
	t.rows = out
}

// headlineID is the fold id of turn's headline row.
func headlineID(turn string) string { return "hl:" + turn }

// rowTurn is the turn row i belongs to: a turn's own, or for a prompt the
// turn that follows it; "" for a system line, a tail or an answered line.
func rowTurn(s *session.Snapshot, rows []trow, i int) string {
	r := &rows[i]
	if r.isTail() || r.res != nil {
		return ""
	}
	switch r.b.kind {
	case blockTurn:
		return r.b.turn
	case blockPrompt:
		if t := deref(s.Content[r.b.lo].TurnID); t != "" {
			return t
		}
		if i+1 < len(rows) && !rows[i+1].isTail() && rows[i+1].res == nil && rows[i+1].b.kind == blockTurn {
			return rows[i+1].b.turn
		}
	}
	return ""
}

// turnSpan is the turn block that covers rows: from the first turn row's
// first item to the last one's end; false when none is a turn's.
func turnSpan(rows []trow) (block, bool) {
	var b block
	ok := false
	for _, r := range rows {
		if r.isTail() || r.res != nil || r.b.kind != blockTurn {
			continue
		}
		if !ok {
			b, ok = r.b, true
			b.lo = r.b.from
		}
		b.hi = max(b.hi, r.b.hi)
	}
	b.reply, b.more = -1, false
	return b, ok
}

// asksOpen is whether turn b asks the user something not answered yet.
func asksOpen(s *session.Snapshot, b block) bool {
	for i := b.lo; i < min(b.hi, len(s.Content)); i++ {
		c := s.Content[i].Tool
		if c != nil && asksUser(c) && c.Status != "completed" && c.Status != "failed" {
			return true
		}
	}
	return false
}

// headlineRow draws turn r.b's headline: the time it started, the line,
// and the agent, in Ledger's gutter when it hangs beside the text. A
// click opens the turn under it.
func (d *cdraw) headlineRow(gtx layout.Context, r *trow) layout.Dimensions {
	th := d.th
	text := model.HeadlineOf(d.t.Headlines, r.b.turn)
	start, _, _ := turnTimes(d.s, r.b, false)
	tm := ""
	if !start.IsZero() {
		tm = start.Local().Format("15:04")
	}
	name := d.agent()
	gutter := d.ledger && d.geom.outside
	left := 0
	if gutter {
		left = d.geom.gutterW
	}
	w := gtx.Constraints.Max.X + left
	pad := gtx.Dp(theme.SpaceXS + theme.Space2XS)
	c := d.t.click(headlineID(r.b.turn))
	line := func(gtx layout.Context) layout.Dimensions {
		size := th.Sp(theme.Body)
		h := gtx.Sp(size*1.5) + 2*pad
		switch {
		case c.Pressed():
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		case c.Hovered():
			paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		muted := th.Muted
		f := th.UIFont
		if r.open {
			f = semibold(f)
		}
		x0, textX, nameX := 0, gtx.Dp(headlineTimeW)+gtx.Dp(theme.SpaceS), 0
		nc, nd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), muted, name)
		})
		textW := w - textX - nd.Size.X - gtx.Dp(theme.SpaceS)
		if gutter {
			x0, textX, nameX = 0, left, d.geom.timeW+d.geom.gap
			textW = w - left
		} else {
			x0 += gtx.Dp(theme.Space2XS)
			nameX = w - nd.Size.X - gtx.Dp(theme.Space2XS)
		}
		mid := func(dims layout.Dimensions) int { return (h - dims.Size.Y) / 2 }
		if tm != "" {
			tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.MonoFont, th.Sp(theme.Small), muted, tm)
			})
			at(gtx, image.Pt(x0, mid(td)), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
		}
		hc, hd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = max(0, textW)
			return d.lbl(gtx, f, size, th.Fg, text)
		})
		at(gtx, image.Pt(textX, mid(hd)), func(gtx layout.Context) layout.Dimensions { hc.Add(gtx.Ops); return hd })
		at(gtx, image.Pt(nameX, mid(nd)), func(gtx layout.Context) layout.Dimensions { nc.Add(gtx.Ops); return nd })
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	var ld layout.Dimensions
	at(gtx, image.Pt(-left, 0), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = w
		ld = clickable(gtx, c, line)
		return ld
	})
	y := ld.Size.Y
	if !r.open {
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(-left, y), Max: image.Pt(w-left, y+max(1, gtx.Dp(1)))}.Op())
	}
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y+max(1, gtx.Dp(1)))}
}
