package panel

import (
	"image"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// logHead is a fold's header, "Worked for 48s · Read 2 files, ran 2
// commands" or, where that does not fit, "Worked for 48s · 4 tools", that a
// click folds or opens: the box's top, and its bottom too when nothing
// is under it. Each of the log's rows is a list row of its own, so a
// running turn of 2,000 calls lays out only the rows in view.
func (d *cdraw) logHead(gtx layout.Context, r *trow) layout.Dimensions {
	th := d.th
	b, open, running, last, start, end := r.b, r.open, r.running, r.last, r.start, r.end
	id := logID(d.s, b)
	w := gtx.Constraints.Max.X
	hh := gtx.Dp(rowH)
	took := end.Sub(start)
	if running {
		took = d.t.now.Sub(start)
		d.t.live = true
	}
	if start.IsZero() {
		took = 0
	}
	h := 1 + hh
	if last {
		h++
	}
	defer d.logFrame(gtx, w, h, true, last)()
	o := op.Offset(image.Pt(0, 1)).Push(gtx.Ops)
	defer o.Pop()
	paint.FillShape(gtx.Ops, th.Surface, clip.Rect{Max: image.Pt(w, hh)}.Op())
	c := d.t.click(id)
	clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		rowFill(gtx, th, c, image.Pt(w, hh))
		x := gtx.Dp(rowPad)
		chev := "chevron-right"
		if open {
			chev = "chevron-down"
		}
		icon(gtx, image.Pt(x, (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		x += gtx.Dp(12) + gtx.Dp(theme.SpaceS)
		if running {
			at(gtx, image.Pt(x, (hh-gtx.Dp(11))/2), func(gtx layout.Context) layout.Dimensions {
				return spinner(gtx, gtx.Dp(11), th.Surface, th.Blue, d.sec)
			})
			x += gtx.Dp(11) + gtx.Dp(theme.SpaceS)
		}
		room := w - x - gtx.Dp(10)
		if n := turnTokens(d.s, b.turn); n > 0 && r.tokens {
			kc, kd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, tokens(n))
			})
			at(gtx, image.Pt(w-gtx.Dp(10)-kd.Size.X, (hh-kd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { kc.Add(gtx.Ops); return kd })
			room -= kd.Size.X + gtx.Dp(theme.SpaceM)
		}
		title := func(t string) (op.CallOp, layout.Dimensions) {
			return measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, t)
			})
		}
		// The calls by kind, as Claude Code counts them, when that fits;
		// else how many.
		var tc op.CallOp
		var td layout.Dimensions
		if thought(d.s.Content, b.lo, b.hi) && !running {
			tc, td = title(strings.Replace(logTitle(took, 0, false), "Worked", "Thought", 1))
		} else if k := toolKinds(d.s.Content, b.lo, b.hi); k != "" {
			tc, td = title(logTitle(took, 0, running) + " · " + k)
		}
		if td.Size.X == 0 || td.Size.X > room {
			tc, td = title(logTitle(took, b.tools, running))
		}
		at(gtx, image.Pt(x, (hh-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
		// A running log, folded, still says what the turn is doing now.
		x += td.Size.X + gtx.Dp(theme.SpaceS)
		if st := logStatus(d.s.Content, b.lo, b.hi, d.t.Dir); running && st != "" && room-td.Size.X > gtx.Dp(48) {
			g := gtx
			g.Constraints.Max.X = room - td.Size.X - gtx.Dp(theme.SpaceS)
			sc, sd := measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.TextQuiet, st)
			})
			at(gtx, image.Pt(x, (hh-sd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { sc.Add(gtx.Ops); return sd })
		}
		return layout.Dimensions{Size: image.Pt(w, hh)}
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// logItem is item i of an open work log: a line over it, and the box's
// bottom when it is the last. top is a call outside a fold, a box of its
// own.
func (d *cdraw) logItem(gtx layout.Context, i int, top, last bool) layout.Dimensions {
	w := gtx.Constraints.Max.X
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
	call, rd := measure(g, func(gtx layout.Context) layout.Dimensions { return d.logEntry(gtx, i) })
	h := 1 + rd.Size.Y
	if last {
		h++
	}
	defer d.logFrame(gtx, w, h, top, last)()
	paint.FillShape(gtx.Ops, d.th.BorderSubtle, clip.Rect{Max: image.Pt(w, 1)}.Op())
	at(gtx, image.Pt(0, 1), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return rd })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// logFrame draws one row's slice of the work log's box, w by h: the
// rounded top when top, the rounded bottom when bottom, the sides
// between. It clips what follows to the inside of the box until the
// returned func runs.
func (d *cdraw) logFrame(gtx layout.Context, w, h int, top, bottom bool) func() {
	rad := gtx.Dp(theme.RadiusPopover)
	r := image.Rect(0, 0, w, h)
	if !top {
		r.Min.Y -= 2*rad + 2
	}
	if !bottom {
		r.Max.Y += 2*rad + 2
	}
	seg := clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops)
	rrect(gtx, r, rad, d.th.Bg, d.th.BorderSubtle)
	inner := clip.UniformRRect(r.Inset(1), rad-1).Push(gtx.Ops)
	return func() { inner.Pop(); seg.Pop() }
}

// logID is the fold id of b's work log: its first item's, or its turn's
// for the empty header of a running turn.
func logID(s *session.Snapshot, b block) string {
	if b.hi <= b.lo {
		return "log:now:" + b.turn
	}
	return "log:" + s.Content[b.lo].ID
}

// thought is whether content[lo:hi] is all reasoning: a fold that says
// "Thought for 4s".
func thought(content []session.Item, lo, hi int) bool {
	for i := lo; i < hi; i++ {
		if k := content[i].Kind; k != session.KindReasoningText && k != session.KindReasoningSummaryText {
			return false
		}
	}
	return hi > lo
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
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), th.Muted, arg)
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
