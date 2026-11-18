package panel

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"
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

// workLog is a turn's calls under one header, "Worked for 48s · 4 tools",
// that a click folds or opens.
func (d *cdraw) workLog(gtx layout.Context, b block, start, end time.Time, running bool) layout.Dimensions {
	th := d.th
	id := "log:" + d.s.Content[b.lo].ID
	open := d.t.toggled(gtx, id, logOpen(running))
	w := gtx.Constraints.Max.X
	hh := gtx.Dp(30)
	took := end.Sub(start)
	if running {
		took = d.t.now.Sub(start)
		d.t.live = true
	}
	if start.IsZero() {
		took = 0
	}
	var rows []layout.Widget
	if open {
		for i := b.lo; i < b.hi; i++ {
			if i == b.reply {
				continue
			}
			it := &d.s.Content[i]
			rows = append(rows, func(gtx layout.Context) layout.Dimensions { return d.logRow(gtx, it) })
		}
	}
	call, dims := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		y := hh
		for _, r := range rows {
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, y), Max: image.Pt(w, y+1)}.Op())
			y++
			g := gtx
			g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
			rd := at(g, image.Pt(0, y), r)
			y += rd.Size.Y
		}
		return layout.Dimensions{Size: image.Pt(w, y)}
	})
	h := dims.Size.Y + 2
	rad := gtx.Dp(theme.RadiusPopover)
	outer := image.Rect(0, 0, w, h)
	rrect(gtx, outer, rad, th.Bg, th.BorderSubtle)
	inner := clip.UniformRRect(outer.Inset(1), rad-1).Push(gtx.Ops)
	o := op.Offset(image.Pt(0, 1)).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, th.Surface, clip.Rect{Max: image.Pt(w, hh)}.Op())
	c := d.t.click(id)
	clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
		if c.Hovered() {
			paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.03), clip.Rect{Max: image.Pt(w, hh)}.Op())
		}
		x := gtx.Dp(10)
		chev := "chevron-right"
		if open {
			chev = "chevron-down"
		}
		icon(gtx, image.Pt(x, (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		x += gtx.Dp(12) + gtx.Dp(8)
		if running {
			at(gtx, image.Pt(x, (hh-gtx.Dp(11))/2), func(gtx layout.Context) layout.Dimensions {
				return spinner(gtx, gtx.Dp(11), th.Surface, th.Blue, d.sec)
			})
			x += gtx.Dp(11) + gtx.Dp(8)
		}
		tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, logTitle(took, b.tools, running))
		})
		at(gtx, image.Pt(x, (hh-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
		if n := turnTokens(d.s, b.turn); n > 0 {
			kc, kd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Caption), th.TextQuiet, tokens(n))
			})
			at(gtx, image.Pt(w-gtx.Dp(10)-kd.Size.X, (hh-kd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { kc.Add(gtx.Ops); return kd })
		}
		return layout.Dimensions{Size: image.Pt(w, hh)}
	})
	call.Add(gtx.Ops)
	o.Pop()
	inner.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
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
			return d.selectable(gtx, d.t.sel(it.ID), th.UIFont, th.Sp(theme.Small), th.Muted, strings.TrimSpace(it.Text))
		})
	case session.KindCommandOutput, session.KindFileChangeOutput:
		return d.foldRow(gtx, it.ID, "terminal", "Output", "", func(gtx layout.Context) layout.Dimensions {
			return d.outBox(gtx, it.ID, "", plainOutput(it.Text))
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
	return layout.Inset{Top: 7, Bottom: 8, Left: 54, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.selectable(gtx, d.t.sel(it.ID), th.UIFont, th.Sp(theme.Small), c, txt)
	})
}

// toolRow is a call's 30 dp row: chevron, icon, verb, argument, result;
// open, what it did.
func (d *cdraw) toolRow(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	c := it.Tool
	lines := d.diff(it)
	sum := summarize(c, lines, parseTime(it.CreatedAt), d.t.now)
	out := plainOutput(c.Output)
	body := d.toolBody(it, sum, lines, out)
	open := body != nil && d.t.toggled(gtx, it.ID, toolOpen(c))
	running, failed := c.Status == "running", c.Status == "failed"
	if running {
		d.t.live = true
	}
	w := gtx.Constraints.Max.X
	hh := gtx.Dp(30)
	head := func(gtx layout.Context) layout.Dimensions {
		ck := d.t.click(it.ID)
		if body != nil && ck.Hovered() {
			paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Fg, 0.035), clip.Rect{Max: image.Pt(w, hh)}.Op())
		}
		x := gtx.Dp(10)
		if body != nil {
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			icon(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		}
		x += gtx.Dp(14) + gtx.Dp(8)
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
		x += isz + gtx.Dp(8)
		rc, rd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.result(gtx, sum, failed) })
		rx := w - gtx.Dp(10) - rd.Size.X
		at(gtx, image.Pt(rx, (hh-rd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { rc.Add(gtx.Ops); return rd })
		vc, vd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Fg, sum.verb)
		})
		at(gtx, image.Pt(x, (hh-vd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { vc.Add(gtx.Ops); return vd })
		x += vd.Size.X + gtx.Dp(7)
		if room := rx - gtx.Dp(12) - x; room > 0 && sum.arg != "" {
			g := gtx
			g.Constraints.Max.X = room
			ac, ad := measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), theme.Mix(th.Muted, th.Fg, 0.62), sum.arg)
			})
			at(gtx, image.Pt(x, (hh-ad.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { ac.Add(gtx.Ops); return ad })
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
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-gtx.Dp(54)-gtx.Dp(10)), gtx.Constraints.Max.Y)}
	bd := at(g, image.Pt(gtx.Dp(54), hd.Size.Y), body)
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
		return hrow(gtx, gtx.Dp(7), part(th.Readable(th.Green, th.Bg), "passed"), part(th.Muted, sum.result))
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
			return d.selectable(gtx, d.t.sel(it.ID), d.th.MonoFont, d.th.Sp(theme.Small), theme.Mix(d.th.Muted, d.th.Fg, 0.8), mdview.Detab(out))
		}
	case sum.kind == "bash":
		cmd := c.Command
		if cmd == "" {
			cmd = sum.arg
		}
		return func(gtx layout.Context) layout.Dimensions { return d.outBox(gtx, it.ID, cmd, out) }
	case out != "":
		return func(gtx layout.Context) layout.Dimensions { return d.outBox(gtx, it.ID, "", out) }
	case c.Status == "running" && c.Progress != "":
		return func(gtx layout.Context) layout.Dimensions { return d.outBox(gtx, it.ID, "", c.Progress) }
	}
	return nil
}

// foldRow is a work log row that a click opens to show body.
func (d *cdraw) foldRow(gtx layout.Context, id, ic, verb, arg string, body layout.Widget) layout.Dimensions {
	th := d.th
	open := d.t.toggled(gtx, id, false)
	w, hh := gtx.Constraints.Max.X, gtx.Dp(30)
	ck := d.t.click(id)
	hd := clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		if ck.Hovered() {
			paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Fg, 0.035), clip.Rect{Max: image.Pt(w, hh)}.Op())
		}
		chev := "chevron-right"
		if open {
			chev = "chevron-down"
		}
		x := gtx.Dp(10)
		icon(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		x += gtx.Dp(22)
		icon(gtx, image.Pt(x, (hh-gtx.Dp(14))/2), ic, 14, th.Muted)
		x += gtx.Dp(22)
		vc, vd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Fg, verb)
		})
		at(gtx, image.Pt(x, (hh-vd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { vc.Add(gtx.Ops); return vd })
		x += vd.Size.X + gtx.Dp(7)
		if room := w - gtx.Dp(10) - x; room > 0 && arg != "" {
			g := gtx
			g.Constraints.Max.X = room
			ac, ad := measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, arg)
			})
			at(gtx, image.Pt(x, (hh-ad.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { ac.Add(gtx.Ops); return ad })
		}
		return layout.Dimensions{Size: image.Pt(w, hh)}
	})
	if !open {
		return hd
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-gtx.Dp(64)), gtx.Constraints.Max.Y)}
	bd := at(g, image.Pt(gtx.Dp(54), hd.Size.Y), body)
	return layout.Dimensions{Size: image.Pt(w, hd.Size.Y+bd.Size.Y+gtx.Dp(10))}
}

// outBox is a command and its output in mono on a panel, the last lines
// first with Show all under it, and a copy button.
func (d *cdraw) outBox(gtx layout.Context, id, cmd, out string) layout.Dimensions {
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
	if strings.TrimSpace(out) == "" {
		txt = strings.TrimSpace(strings.TrimPrefix(txt, "\n") + "\n(no output)")
	}
	w := gtx.Constraints.Max.X
	px, py := gtx.Dp(10), gtx.Dp(8)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px-gtx.Dp(24)), gtx.Constraints.Max.Y)}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.selectable(gtx, d.t.sel("out:"+id), th.MonoFont, th.Sp(theme.Small), theme.Mix(th.Muted, th.Fg, 0.82), mdview.Detab(txt))
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
	at(gtx, image.Pt(w-gtx.Dp(24), gtx.Dp(4)), func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, ck, "clipboard", "", d.t.isCopied("out:"+id), 20)
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
	md := at(gtx, image.Pt(0, h+gtx.Dp(4)), func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, sk, "chevron-down", more, false, 22)
	})
	return layout.Dimensions{Size: image.Pt(w, h+gtx.Dp(4)+md.Size.Y)}
}

// diffBox is an edit's hunk: old and new line numbers when the diff gives
// them, the added and deleted lines on their tints, Copy diff on top.
func (d *cdraw) diffBox(gtx layout.Context, id, path string, lines []diffLine) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	all := d.t.open["all:"+id]
	shown := lines
	if !all && len(shown) > diffHead {
		shown = shown[:diffHead]
	}
	hdr := path
	nums := false
	for _, l := range lines {
		if l.kind == '@' && hdr == path {
			hdr = l.text
		}
		nums = nums || l.old > 0 || l.new > 0
	}
	// The header: the first hunk's @@ line, else the file; Copy diff.
	hh := gtx.Dp(26)
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
		return d.selectable(gtx, sl, th.MonoFont, mono, th.Fg, b.String())
	})
	py := gtx.Dp(4)
	h := hh + td.Size.Y + 2*py
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(0, 0, w, h), rad).Op(gtx.Ops))
	cl := clip.UniformRRect(image.Rect(0, 0, w, h), rad).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, hh-1), Max: image.Pt(w, hh)}.Op())
	ck := d.t.click("copy:diff:" + id)
	if ck.Clicked(gtx) {
		d.t.copy(gtx, "diff:"+id, unifiedText(path, lines))
	}
	bc, bd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, ck, "clipboard", "Copy diff", d.t.isCopied("diff:"+id), 22)
	})
	at(gtx, image.Pt(w-gtx.Dp(2)-bd.Size.X, (hh-bd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { bc.Add(gtx.Ops); return bd })
	g = gtx
	g.Constraints.Max.X = max(0, w-bd.Size.X-gtx.Dp(20))
	hc, hdim := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Caption), th.Muted, hdr)
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
	md := at(gtx, image.Pt(0, h+gtx.Dp(4)), func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, sk, "chevron-down", more, false, 22)
	})
	return layout.Dimensions{Size: image.Pt(w, h+gtx.Dp(4)+md.Size.Y)}
}
