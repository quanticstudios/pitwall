package panel

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"unicode/utf8"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// outTail is how many lines of a command's output an opened row shows
// before Show all.
const outTail = 12

// bodyIndent is where a tool row's text starts, and its opened body
// with it: the side padding, the chevron, the icon and their gaps.
const bodyIndent = rowPad + 14 + theme.SpaceS + 14 + theme.SpaceS

// diffHead is how many lines of an edit an opened row shows before Show
// all.
const diffHead = 60

// diffCache is an edit's lines, worked out once per update of its item.
type diffCache struct {
	updated string
	lines   []diffLine
}

// diff is it's edit as lines, cached.
func (d *cdraw) diff(it *session.Item) []diffLine {
	if d.t.diffs == nil {
		d.t.diffs = map[string]diffCache{}
	}
	c, ok := d.t.diffs[it.ID]
	if !ok || c.updated != it.UpdatedAt {
		c = diffCache{updated: it.UpdatedAt, lines: diffLines(it.Tool)}
		d.t.diffs[it.ID] = c
	}
	return c.lines
}

// logHead is a work log's header, "Worked for 48s · 4 tools", that a
// click folds or opens: the box's top, and its bottom too when nothing
// is under it. Each of the log's rows is a list row of its own, so a
// running turn of 2,000 calls lays out only the rows in view.
func (d *cdraw) logHead(gtx layout.Context, b block, open, running, last bool) layout.Dimensions {
	th := d.th
	id := logID(d.s, b)
	start, end, _ := turnTimes(d.s, b, b.hi == len(d.s.Content))
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
		tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, logTitle(took, b.tools, running))
		})
		at(gtx, image.Pt(x, (hh-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
		if n := turnTokens(d.s, b.turn); n > 0 {
			kc, kd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, tokens(n))
			})
			at(gtx, image.Pt(w-gtx.Dp(10)-kd.Size.X, (hh-kd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { kc.Add(gtx.Ops); return kd })
		}
		return layout.Dimensions{Size: image.Pt(w, hh)}
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// logItem is item i of an open work log: a line over it, and the box's
// bottom when it is the last.
func (d *cdraw) logItem(gtx layout.Context, i int, last bool) layout.Dimensions {
	w := gtx.Constraints.Max.X
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
	call, rd := measure(g, func(gtx layout.Context) layout.Dimensions { return d.logEntry(gtx, i) })
	h := 1 + rd.Size.Y
	if last {
		h++
	}
	defer d.logFrame(gtx, w, h, false, last)()
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

// logID is the fold id of b's work log.
func logID(s *session.Snapshot, b block) string { return "log:" + s.Content[b.lo].ID }

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
	// Assistant text between calls, and pitwall's own lines.
	c := theme.Mix(th.Muted, th.Fg, 0.7)
	if it.Kind == session.KindSystem {
		c = th.Muted
	}
	txt := strings.TrimSpace(it.Text)
	if txt == "" {
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))}
	}
	return layout.Inset{Top: 8, Bottom: 9, Left: bodyIndent, Right: rowPad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if it.Kind == session.KindAssistantText {
			return d.markdown(gtx, it.ID, d.t.reply(it, false))
		}
		return d.selectable(gtx, d.t.sel(it.ID), d.small(c), txt)
	})
}

// toolRow is a call's 30 dp row: chevron, icon, verb, argument, result;
// open, what it did.
func (d *cdraw) toolRow(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	c := it.Tool
	lines := d.diff(it)
	sum := summarize(c, lines, parseTime(it.CreatedAt), d.t.now)
	if sum.path {
		sum.arg = relPath(d.t.Dir, sum.arg)
	}
	out := toolOutput(c)
	body := d.toolBody(it, sum, lines, out)
	open := body != nil && d.t.toggled(gtx, it.ID, toolOpen(c))
	running, failed := c.Status == "running", c.Status == "failed"
	if running {
		d.t.live = true
	}
	w := gtx.Constraints.Max.X
	hh := gtx.Dp(rowH)
	head := func(gtx layout.Context) layout.Dimensions {
		ck := d.t.click(it.ID)
		if body != nil {
			rowFill(gtx, th, ck, image.Pt(w, hh))
		}
		x := gtx.Dp(rowPad)
		if body != nil {
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			icon(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		}
		x += gtx.Dp(14) + gtx.Dp(theme.SpaceS)
		isz := gtx.Dp(14)
		switch {
		case running:
			at(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), func(gtx layout.Context) layout.Dimensions {
				return spinner(gtx, gtx.Dp(12), th.Bg, th.Blue, d.sec)
			})
		case failed:
			icon(gtx, image.Pt(x, (hh-isz)/2), "x", 14, th.Red)
		default:
			icon(gtx, image.Pt(x, (hh-isz)/2), sum.icon, 14, th.Muted)
		}
		x += isz + gtx.Dp(theme.SpaceS)
		vc, vd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, sum.verb)
		})
		base := centerBase(hh, vd) // the row's one baseline, the verb centered
		rc, rd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.result(gtx, sum, failed) })
		rx := w - gtx.Dp(rowPad) - rd.Size.X
		at(gtx, image.Pt(rx, onBase(base, rd)), func(gtx layout.Context) layout.Dimensions { rc.Add(gtx.Ops); return rd })
		at(gtx, image.Pt(x, onBase(base, vd)), func(gtx layout.Context) layout.Dimensions { vc.Add(gtx.Ops); return vd })
		x += vd.Size.X + gtx.Dp(theme.SpaceS)
		if room := rx - gtx.Dp(12) - x; room > 0 && sum.arg != "" {
			g := gtx
			g.Constraints.Max.X = room
			ac, ad := measure(g, func(gtx layout.Context) layout.Dimensions {
				arg := sum.arg
				if sum.path {
					arg = middleCut(arg, room, gtx.Sp(th.Sp(theme.Body))*55/100)
				}
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), theme.Mix(th.Fg, th.Bg, 0.2), arg)
			})
			at(gtx, image.Pt(x, onBase(base, ad)), func(gtx layout.Context) layout.Dimensions { ac.Add(gtx.Ops); return ad })
		}
		return layout.Dimensions{Size: image.Pt(w, hh)}
	}
	var hd layout.Dimensions
	if body != nil {
		hd = clickable(gtx, d.t.click(it.ID), head)
	} else {
		hd = head(gtx)
	}
	if !open {
		return hd
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-gtx.Dp(bodyIndent)-gtx.Dp(rowPad)), gtx.Constraints.Max.Y)}
	bd := at(g, image.Pt(gtx.Dp(bodyIndent), hd.Size.Y), body)
	return layout.Dimensions{Size: image.Pt(w, hd.Size.Y+bd.Size.Y+gtx.Dp(10))}
}

// result is a row's right end: +N -M for an edit, passed and the time
// for a command, red when it failed.
func (d *cdraw) result(gtx layout.Context, sum toolSummary, failed bool) layout.Dimensions {
	th := d.th
	f, sz := th.UIFont, th.Sp(theme.Small)
	part := func(c color.NRGBA, s string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions { return d.lbl(gtx, f, sz, c, s) }
	}
	switch {
	case failed:
		return d.lbl(gtx, f, sz, th.Red, sum.result)
	case sum.add > 0 || sum.del > 0:
		return hrow(gtx, gtx.Dp(5), part(th.Readable(th.Green, th.Bg), "+"+strconv.Itoa(sum.add)), part(th.Readable(th.Red, th.Bg), "-"+strconv.Itoa(sum.del)))
	case sum.passed:
		return hrow(gtx, gtx.Dp(theme.SpaceS), part(th.Readable(th.Green, th.Bg), "passed"), part(th.Muted, sum.result))
	}
	if sum.result == "" {
		return layout.Dimensions{}
	}
	return d.lbl(gtx, f, sz, th.Muted, sum.result)
}

// toolBody is what an opened row shows, nil when it has nothing to show.
func (d *cdraw) toolBody(it *session.Item, sum toolSummary, lines []diffLine, out string) layout.Widget {
	c := it.Tool
	switch {
	case sum.kind == "edit" && len(lines) > 0:
		path := sum.arg
		return func(gtx layout.Context) layout.Dimensions { return d.diffBox(gtx, it.ID, path, lines) }
	case sum.kind == "search" && out != "":
		return func(gtx layout.Context) layout.Dimensions {
			return d.selectable(gtx, d.t.sel(it.ID), d.mono(), mdview.Detab(out))
		}
	case sum.kind == "bash":
		cmd := c.Command
		if cmd == "" {
			cmd = sum.arg
		}
		live := c.Status == "running"
		return func(gtx layout.Context) layout.Dimensions { return d.outBox(gtx, it.ID, cmd, out, live) }
	case out != "":
		return func(gtx layout.Context) layout.Dimensions { return d.outBox(gtx, it.ID, "", out, false) }
	}
	return nil
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

// outBox is a command and its output in mono on a panel, the last lines
// first with Show all under it, and a copy button. A live call with
// nothing printed yet shows just its command, not "(no output)".
func (d *cdraw) outBox(gtx layout.Context, id, cmd, out string, live bool) layout.Dimensions {
	th := d.th
	all := d.t.open["all:"+id]
	shown, hidden := out, 0
	if !all {
		shown, hidden = tailLines(out, outTail)
	}
	full := strings.Count(out, "\n") + 1
	txt := shown
	if cmd != "" {
		txt = "$ " + cmd + "\n" + shown
	}
	if strings.TrimSpace(out) == "" && !live {
		txt = strings.TrimSpace(strings.TrimPrefix(txt, "\n") + "\n(no output)")
	}
	w := gtx.Constraints.Max.X
	px, py := gtx.Dp(theme.SpaceM), gtx.Dp(10)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px-gtx.Dp(hit)), gtx.Constraints.Max.Y)}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.selectable(gtx, d.t.sel("out:"+id), d.mono(), mdview.Detab(txt))
	})
	h := td.Size.Y + 2*py
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
	copyText := out
	if cmd != "" {
		copyText = "$ " + cmd + "\n" + out
	}
	ck := d.t.click("copy:out:" + id)
	if ck.Clicked(gtx) {
		d.t.copy(gtx, "out:"+id, copyText)
	}
	at(gtx, image.Pt(w-gtx.Dp(hit)-gtx.Dp(2), gtx.Dp(2)), func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, ck, "clipboard", "", d.t.isCopied("out:"+id))
	})
	if hidden == 0 && !(all && full > outTail) {
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	more := "Show all " + strconv.Itoa(full) + " lines"
	if all {
		more = "Show the last " + strconv.Itoa(outTail) + " lines"
	}
	sk := d.t.click("all:" + id)
	for sk.Clicked(gtx) {
		d.t.open["all:"+id] = !all
	}
	md := at(gtx, image.Pt(0, h+gtx.Dp(gapShowAll)), func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, sk, "chevron-down", more, false)
	})
	return layout.Dimensions{Size: image.Pt(w, h+gtx.Dp(gapShowAll)+md.Size.Y)}
}

// diffBox is an edit's hunk: old and new line numbers when the diff gives
// them, the added and deleted lines on their tints, Copy diff on top.
func (d *cdraw) diffBox(gtx layout.Context, id, path string, lines []diffLine) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	all := d.t.open["all:"+id]
	whole := lines
	hdr, hf := path, th.UIFont
	if len(lines) > 0 && lines[0].kind == '@' { // the header shows the first hunk's line
		hdr, hf, lines = lines[0].text, th.MonoFont, lines[1:]
	}
	shown := lines
	if !all && len(shown) > diffHead {
		shown = shown[:diffHead]
	}
	nums := false
	for _, l := range lines {
		nums = nums || l.old > 0 || l.new > 0
	}
	// The header: the first hunk's @@ line, else the file; Copy diff.
	hh := gtx.Dp(rowH)
	rad := gtx.Dp(theme.RadiusControl)
	var b strings.Builder
	starts := make([]int, 0, len(shown))
	runes, maxNo := 0, 0
	for i, l := range shown {
		if i > 0 {
			b.WriteByte('\n')
			runes++
		}
		starts = append(starts, runes)
		t := mdview.Detab(l.text)
		b.WriteString(t)
		runes += utf8.RuneCountInString(t)
		maxNo = max(maxNo, l.old, l.new)
	}
	mono := th.Sp(theme.Small)
	digits := len(strconv.Itoa(maxNo))
	numW := 0
	if nums {
		_, cd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.MonoFont, mono, th.Muted, strings.Repeat("0", digits))
		})
		numW = cd.Size.X + gtx.Dp(10)
	}
	signW := gtx.Dp(16)
	textX := 2*numW + signW
	if numW == 0 {
		textX = gtx.Dp(6) + signW
	}
	sl := d.t.sel("diff:" + id)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-textX-gtx.Dp(10)), 1<<20)}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.selectable(gtx, sl, d.mono(), b.String())
	})
	py := gtx.Dp(4)
	h := hh + td.Size.Y + 2*py
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(0, 0, w, h), rad).Op(gtx.Ops))
	cl := clip.UniformRRect(image.Rect(0, 0, w, h), rad).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, hh-1), Max: image.Pt(w, hh)}.Op())
	ck := d.t.click("copy:diff:" + id)
	if ck.Clicked(gtx) {
		d.t.copy(gtx, "diff:"+id, unifiedText(path, whole))
	}
	bc, bd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, ck, "clipboard", "Copy diff", d.t.isCopied("diff:"+id))
	})
	at(gtx, image.Pt(w-gtx.Dp(2)-bd.Size.X, (hh-bd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { bc.Add(gtx.Ops); return bd })
	g = gtx
	g.Constraints.Max.X = max(0, w-bd.Size.X-gtx.Dp(20))
	hc, hdim := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, hf, th.Sp(theme.Small), th.Muted, hdr)
	})
	at(gtx, image.Pt(gtx.Dp(10), (hh-hdim.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { hc.Add(gtx.Ops); return hdim })
	// Tints and gutters behind each line, where the text put it.
	add, del := theme.Mix(th.Surface, th.Green, 0.10), theme.Mix(th.Surface, th.Red, 0.10)
	top := hh + py
	var rg []widget.Region
	for i, l := range shown {
		n := utf8.RuneCountInString(mdview.Detab(l.text))
		rg = sl.Regions(starts[i], starts[i]+max(n, 1), rg[:0])
		if len(rg) == 0 {
			continue
		}
		y0, y1 := top+rg[0].Bounds.Min.Y, top+rg[len(rg)-1].Bounds.Max.Y
		sign, sc := " ", th.Muted
		switch l.kind {
		case '+':
			paint.FillShape(gtx.Ops, add, clip.Rect{Min: image.Pt(0, y0), Max: image.Pt(w, y1)}.Op())
			sign, sc = "+", th.Readable(th.Green, add)
		case '-':
			paint.FillShape(gtx.Ops, del, clip.Rect{Min: image.Pt(0, y0), Max: image.Pt(w, y1)}.Op())
			sign, sc = "-", th.Readable(th.Red, del)
		}
		num := func(x, n int) {
			if n == 0 {
				return
			}
			s := strconv.Itoa(n)
			c, nd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.lbl(gtx, th.MonoFont, mono, th.TextQuiet, s) })
			at(gtx, image.Pt(x+numW-gtx.Dp(6)-nd.Size.X, y0), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return nd })
		}
		if numW > 0 {
			num(0, l.old)
			num(numW, l.new)
		}
		if l.kind != '@' {
			sx := textX - signW
			c, sd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.lbl(gtx, th.MonoFont, mono, sc, sign) })
			at(gtx, image.Pt(sx, y0), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return sd })
		}
	}
	at(gtx, image.Pt(textX, top), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
	cl.Pop()
	if len(lines) <= diffHead {
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	more := "Show all " + strconv.Itoa(len(lines)) + " lines"
	if all {
		more = "Show the first " + strconv.Itoa(diffHead) + " lines"
	}
	sk := d.t.click("all:" + id)
	for sk.Clicked(gtx) {
		d.t.open["all:"+id] = !all
	}
	md := at(gtx, image.Pt(0, h+gtx.Dp(gapShowAll)), func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, sk, "chevron-down", more, false)
	})
	return layout.Dimensions{Size: image.Pt(w, h+gtx.Dp(gapShowAll)+md.Size.Y)}
}

// rowFill is a clickable row's Hover fill under the pointer, Pressed while
// held.
func rowFill(gtx layout.Context, th *theme.Theme, c *widget.Clickable, size image.Point) {
	switch {
	case c.Pressed():
		paint.FillShape(gtx.Ops, th.Pressed, clip.Rect{Max: size}.Op())
	case c.Hovered():
		paint.FillShape(gtx.Ops, th.Hover, clip.Rect{Max: size}.Op())
	}
}
