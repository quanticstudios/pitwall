package panel

import (
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// logHead is a fold's header, a line as T3 Code draws a group of calls:
// the chevron, the kind's icon, and what the calls did, "Ran 2 commands
// and read 1 file", quiet, with no box. A fold that holds a failed call
// tints its icon red. The running fold says its newest call instead,
// "Bash · go test ./... · 0:12", in the text color, a spinner for its
// icon. A click opens or folds it. Each of its rows is a list row of its
// own, so a running turn of 2,000 calls lays out only the rows in view.
func (d *cdraw) logHead(gtx layout.Context, r *trow) layout.Dimensions {
	th := d.th
	b := r.b
	w, hh := gtx.Constraints.Max.X, gtx.Dp(rowH)
	label, ic := foldLabel(d.s.Content, b.lo, b.hi)
	col, icol := th.TextQuiet, th.Muted
	if failed(d.s.Content, b.lo, b.hi) { // T3 Code's error tint, at 40%
		icol = theme.Mix(th.Bg, th.Red, 0.4)
	}
	if label == "Thought" {
		if t := firstThought(d.s.Content, b.lo, b.hi); t != "" {
			label = t
		}
	}
	if r.running {
		d.t.live = true
		if st := logStatus(d.s.Content, b.lo, b.hi, d.t.Dir, d.t.now); st != "" {
			label, col = st, th.Fg
		}
	}
	c := d.t.click(logID(d.s, b))
	return clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		rowFill(gtx, th, c, image.Pt(w, hh))
		x := gtx.Dp(rowPad)
		chev := "chevron-right"
		if r.open {
			chev = "chevron-down"
		}
		icon(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		x += gtx.Dp(22)
		if r.running {
			at(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), func(gtx layout.Context) layout.Dimensions {
				return spinner(gtx, gtx.Dp(12), th.Bg, th.Blue, d.sec)
			})
		} else {
			icon(gtx, image.Pt(x, (hh-gtx.Dp(14))/2), ic, 14, icol)
		}
		x += gtx.Dp(22)
		g := gtx
		g.Constraints.Max.X = max(0, w-gtx.Dp(rowPad)-x)
		lc, ld := measure(g, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), col, label)
		})
		at(gtx, image.Pt(x, onBase(centerBase(hh, ld), ld)), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
		return layout.Dimensions{Size: image.Pt(w, hh)}
	})
}

// logItem is item i of a work log, with no box: a row of an open fold,
// or a call on its own.
func (d *cdraw) logItem(gtx layout.Context, i int) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return d.logEntry(gtx, i)
}

// turnID is the id of the Worked for line's fold of turn b's work.
func turnID(s *session.Snapshot, b block) string { return "turn:" + s.Content[b.lo].ID }

// logID is the fold id of b's work log: its first item's.
func logID(s *session.Snapshot, b block) string { return "log:" + s.Content[b.lo].ID }

// failed is whether a call in content[lo:hi] failed.
func failed(content []session.Item, lo, hi int) bool {
	for i := lo; i < hi; i++ {
		if c := content[i].Tool; content[i].Kind == session.KindToolCall && c != nil && c.Status == "failed" {
			return true
		}
	}
	return false
}

// firstThought is the first line of the first reasoning in
// content[lo:hi] that has text, "" for none.
func firstThought(content []session.Item, lo, hi int) string {
	for i := lo; i < hi; i++ {
		if t := strings.TrimSpace(content[i].Text); t != "" {
			return firstLine(t)
		}
	}
	return ""
}

// logRow is one item of a work log.
func (d *cdraw) logRow(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	switch it.Kind {
	case session.KindToolCall:
		if it.Tool != nil {
			return d.toolRow(gtx, it)
		}
	case session.KindReasoningText, session.KindReasoningSummaryText:
		return d.foldRow(gtx, it.ID, "brain", "Thinking", firstLine(it.Text), func(gtx layout.Context) layout.Dimensions {
			return d.selectable(gtx, d.t.sel(it.ID), d.small(th.Muted), strings.TrimSpace(it.Text))
		})
	case session.KindCommandOutput, session.KindFileChangeOutput:
		return d.foldRow(gtx, it.ID, "terminal", "Output", "", func(gtx layout.Context) layout.Dimensions {
			return d.outBox(gtx, it.ID, "", plainOutput(it.Text), false)
		})
	}
	// Any other item: its text, quiet. The assistant's text draws between
	// the folds, not in them.
	txt := strings.TrimSpace(it.Text)
	if txt == "" {
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))}
	}
	return layout.Inset{Top: 8, Bottom: 9, Left: bodyIndent, Right: rowPad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.selectable(gtx, d.t.sel(it.ID), d.small(th.Muted), txt)
	})
}

// foldRow is a work log row that a click opens to show body.
func (d *cdraw) foldRow(gtx layout.Context, id, ic, verb, arg string, body layout.Widget) layout.Dimensions {
	th := d.th
	open := d.t.toggled(gtx, id, false)
	w, hh := gtx.Constraints.Max.X, gtx.Dp(rowH)
	ck := d.t.click(id)
	hd := clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		rowFill(gtx, th, ck, image.Pt(w, hh))
		chev := "chevron-right"
		if open {
			chev = "chevron-down"
		}
		x := gtx.Dp(rowPad)
		icon(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		x += gtx.Dp(22)
		icon(gtx, image.Pt(x, (hh-gtx.Dp(14))/2), ic, 14, th.Muted)
		x += gtx.Dp(22)
		vc, vd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, verb)
		})
		base := centerBase(hh, vd)
		at(gtx, image.Pt(x, onBase(base, vd)), func(gtx layout.Context) layout.Dimensions { vc.Add(gtx.Ops); return vd })
		x += vd.Size.X + gtx.Dp(theme.SpaceS)
		if room := w - gtx.Dp(rowPad) - x; room > 0 && arg != "" {
			g := gtx
			g.Constraints.Max.X = room
			ac, ad := measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.selLine(gtx, "arg:"+id, th.UIFont, th.Sp(theme.Body), th.Muted, arg)
			})
			at(gtx, image.Pt(x, onBase(base, ad)), func(gtx layout.Context) layout.Dimensions { ac.Add(gtx.Ops); return ad })
		}
		return layout.Dimensions{Size: image.Pt(w, hh)}
	})
	if !open {
		return hd
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-gtx.Dp(bodyIndent)-gtx.Dp(rowPad)), gtx.Constraints.Max.Y)}
	bd := at(g, image.Pt(gtx.Dp(bodyIndent), hd.Size.Y), body)
	return layout.Dimensions{Size: image.Pt(w, hd.Size.Y+bd.Size.Y+gtx.Dp(10))}
}
