package panel

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// logEntry is item i of an open work log: thinking, a harness item and a
// subagent as this file draws them, anything else as logRow does.
func (d *cdraw) logEntry(gtx layout.Context, i int) layout.Dimensions {
	it := &d.s.Content[i]
	switch {
	case it.Kind == session.KindReasoningText || it.Kind == session.KindReasoningSummaryText:
		return d.thinkRow(gtx, i)
	case it.Kind == session.KindSystem:
		return d.sysItem(gtx, it, true)
	case it.Kind == session.KindToolCall && isSubagent(it.Tool):
		return d.agentCard(gtx, it)
	}
	return d.logRow(gtx, it)
}

// --- system items ---

// sysItem draws a system item by its phase: a command's chip and output
// on the user's side, a divider for a compaction or a setting, a quiet
// row for a notice or a hook, a card for a retry or an error. inLog is
// whether it sits in a work log's box.
func (d *cdraw) sysItem(gtx layout.Context, it *session.Item, inLog bool) layout.Dimensions {
	th := d.th
	txt := strings.TrimSpace(it.Text)
	pad := func(w layout.Widget) layout.Dimensions {
		if !inLog {
			return w(gtx)
		}
		return layout.Inset{Top: 6, Bottom: 6, Left: rowPad, Right: rowPad}.Layout(gtx, w)
	}
	switch it.Phase {
	case session.PhaseCommand:
		return pad(func(gtx layout.Context) layout.Dimensions { return d.commandChip(gtx, txt, !inLog) })
	case session.PhaseOutput:
		return pad(func(gtx layout.Context) layout.Dimensions { return d.commandOutput(gtx, it.ID, txt, !inLog) })
	case session.PhaseSetting:
		return pad(func(gtx layout.Context) layout.Dimensions {
			return d.divider(gtx, nil, "", settingLabel(txt), false)
		})
	case session.PhaseCompact:
		return pad(func(gtx layout.Context) layout.Dimensions { return d.compaction(gtx, it) })
	case session.PhaseNotice:
		ic := "circle-alert"
		if strings.HasPrefix(txt, "Background task finished") {
			ic = "check"
		}
		return pad(func(gtx layout.Context) layout.Dimensions { return d.quietRow(gtx, it.ID, ic, th.Muted, txt) })
	case session.PhaseHook:
		return pad(func(gtx layout.Context) layout.Dimensions {
			return d.quietRow(gtx, it.ID, "shield", th.Readable(th.Yellow, th.Bg), txt)
		})
	case session.PhaseWarning:
		return pad(func(gtx layout.Context) layout.Dimensions {
			return d.notice(gtx, it.ID, th.Yellow, "Retrying", txt)
		})
	case session.PhaseError:
		return pad(func(gtx layout.Context) layout.Dimensions { return d.notice(gtx, it.ID, th.Red, "", txt) })
	}
	if txt == "" {
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))}
	}
	if inLog {
		return layout.Inset{Top: 8, Bottom: 9, Left: bodyIndent, Right: rowPad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.selectable(gtx, d.t.sel(it.ID), d.small(th.Muted), txt)
		})
	}
	return d.selectable(gtx, d.t.sel(it.ID), d.small(th.Muted), txt)
}

// commandChip is a slash or ! command the user ran: a mono chip on the
// user's side, right when right.
func (d *cdraw) commandChip(gtx layout.Context, cmd string, right bool) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	px, h := gtx.Dp(theme.SpaceS+theme.Space2XS), gtx.Dp(hit)
	ic := "terminal"
	if strings.HasPrefix(cmd, "/") {
		ic = "zap"
	}
	g := gtx
	g.Constraints.Max.X = max(0, min(w, d.col*84/100)-2*px-gtx.Dp(theme.SpaceL+theme.SpaceXS))
	c, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.MonoFont, th.Sp(theme.Body), th.Fg, cmd)
	})
	cw := td.Size.X + 2*px + gtx.Dp(theme.SpaceL+theme.SpaceXS)
	x := 0
	if right {
		x = w - cw
	}
	rrect(gtx, image.Rect(x, 0, x+cw, h), gtx.Dp(theme.RadiusControl), th.SurfaceSecondary, th.BorderSubtle)
	icon(gtx, image.Pt(x+px, (h-gtx.Dp(14))/2), ic, 14, th.Muted)
	at(gtx, image.Pt(x+px+gtx.Dp(theme.SpaceL+theme.SpaceXS), (h-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return td })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// commandOutput is a local command's output under its chip: quiet mono,
// its colors dropped, the last lines first.
func (d *cdraw) commandOutput(gtx layout.Context, id, out string, right bool) layout.Dimensions {
	w := gtx.Constraints.Max.X
	bw := w
	if right {
		bw = min(w, d.col*84/100)
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(bw, gtx.Constraints.Max.Y)}
	c, od := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.outBox(gtx, id, "", plainOutput(out), false)
	})
	at(gtx, image.Pt(w-od.Size.X, 0), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return od })
	return layout.Dimensions{Size: image.Pt(w, od.Size.Y)}
}

// divider is a line across the column with txt in its middle, an icon
// before it; with ck it is a fold, its chevron saying open.
func (d *cdraw) divider(gtx layout.Context, ck *widget.Clickable, ic, txt string, open bool) layout.Dimensions {
	th := d.th
	w, h := gtx.Constraints.Max.X, gtx.Dp(hit)
	draw := func(gtx layout.Context) layout.Dimensions {
		c := th.Muted
		if ck != nil && ck.Hovered() {
			c = th.Fg
		}
		parts := []layout.Widget{}
		if ic != "" {
			parts = append(parts, func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, ic, gtx.Dp(12), c) })
		}
		parts = append(parts, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), c, txt)
		})
		if ck != nil {
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			parts = append(parts, func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, chev, gtx.Dp(12), c) })
		}
		mc, md := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return hrow(gtx, gtx.Dp(theme.SpaceXS+theme.Space2XS), parts...)
		})
		mx := (w - md.Size.X) / 2
		gap := gtx.Dp(theme.SpaceM)
		line := th.BorderSubtle
		paint.FillShape(gtx.Ops, line, clip.Rect{Min: image.Pt(0, h/2), Max: image.Pt(max(0, mx-gap), h/2+gtx.Dp(1))}.Op())
		paint.FillShape(gtx.Ops, line, clip.Rect{Min: image.Pt(min(w, mx+md.Size.X+gap), h/2), Max: image.Pt(w, h/2+gtx.Dp(1))}.Op())
		at(gtx, image.Pt(mx, (h-md.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { mc.Add(gtx.Ops); return md })
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	if ck == nil {
		return draw(gtx)
	}
	return clickable(gtx, ck, draw)
}

// compaction is a "Conversation compacted" divider that opens to the
// summary Claude kept, as markdown on a quiet panel.
func (d *cdraw) compaction(gtx layout.Context, it *session.Item) layout.Dimensions {
	title, summary := compactParts(it.Text)
	if summary == "" {
		return d.divider(gtx, nil, "layers", title, false)
	}
	id := "compact:" + it.ID
	open := d.t.toggled(gtx, id, false)
	head := d.divider(gtx, d.t.click(id), "layers", title, open)
	if !open {
		return head
	}
	sum := session.Item{ID: id, Text: summary}
	body := at(gtx, image.Pt(0, head.Size.Y+gtx.Dp(theme.SpaceS)), func(gtx layout.Context) layout.Dimensions {
		return d.panelBox(gtx, func(gtx layout.Context) layout.Dimensions { return d.markdown(gtx, id, d.t.reply(&sum)) })
	})
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, head.Size.Y+gtx.Dp(theme.SpaceS)+body.Size.Y)}
}

// panelBox draws body on a quiet panel the column's width.
func (d *cdraw) panelBox(gtx layout.Context, body layout.Widget) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	px, py := gtx.Dp(theme.SpaceL), gtx.Dp(theme.SpaceM)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px), gtx.Constraints.Max.Y)}
	c, bd := measure(g, body)
	h := bd.Size.Y + 2*py
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusPopover), th.Surface, th.BorderSubtle)
	at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return bd })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// quietRow is a compact row of the harness's: a small icon in ic's color
// and the text in muted small type.
func (d *cdraw) quietRow(gtx layout.Context, id, ic string, c color.NRGBA, txt string) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	isz := gtx.Dp(13)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-isz-gtx.Dp(theme.SpaceS)), gtx.Constraints.Max.Y)}
	tc, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.selectable(gtx, d.t.sel(id), d.small(th.Muted), txt)
	})
	line := gtx.Sp(th.Sp(theme.Small)) * 145 / 100
	icon(gtx, image.Pt(0, (line-isz)/2), ic, 13, c)
	at(gtx, image.Pt(isz+gtx.Dp(theme.SpaceS), 0), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
	return layout.Dimensions{Size: image.Pt(w, max(td.Size.Y, line))}
}

// notice is a card in tone, the danger red or the warning yellow: an
// icon, a title when there is one, the message.
func (d *cdraw) notice(gtx layout.Context, id string, tone color.NRGBA, title, msg string) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	fg := th.Readable(tone, th.Surface)
	px, py := gtx.Dp(cardPadX), gtx.Dp(cardPadY)
	isz := gtx.Dp(14)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px-isz-gtx.Dp(theme.SpaceS)), gtx.Constraints.Max.Y)}
	c, bd := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.Space2XS),
			func(gtx layout.Context) layout.Dimensions {
				if title == "" {
					return layout.Dimensions{}
				}
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), fg, title)
			},
			func(gtx layout.Context) layout.Dimensions {
				col := th.Fg
				if title != "" {
					col = th.Muted
				}
				return d.selectable(gtx, d.t.sel("notice:"+id), d.body(col), msg)
			})
	})
	h := bd.Size.Y + 2*py
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusPopover), theme.Mix(th.Surface, tone, 0.07), theme.Mix(th.Surface, tone, 0.35))
	line := gtx.Sp(th.Sp(theme.Body)) * 145 / 100
	icon(gtx, image.Pt(px, py+(line-isz)/2), "circle-alert", 14, fg)
	at(gtx, image.Pt(px+isz+gtx.Dp(theme.SpaceS), py), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return bd })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// --- thinking ---

// thinkRow is a thinking item in a work log: a quiet "Thought for 12s"
// that opens to the thinking in muted italic prose. Claude often hides
// the text; then the row says how long and no more.
func (d *cdraw) thinkRow(gtx layout.Context, i int) layout.Dimensions {
	th := d.th
	it := &d.s.Content[i]
	txt := strings.TrimSpace(it.Text)
	running := i == len(d.s.Content)-1 && d.s.SessionState == session.StateRunning
	took := thinkTook(d.s, i)
	if running {
		took = d.t.now.Sub(parseTime(it.CreatedAt))
		d.t.live = true
	}
	open := txt != "" && d.t.toggled(gtx, it.ID, false)
	w, hh := gtx.Constraints.Max.X, gtx.Dp(rowH)
	head := func(gtx layout.Context) layout.Dimensions {
		ck := d.t.click(it.ID)
		if txt != "" {
			rowFill(gtx, th, ck, image.Pt(w, hh))
		}
		x := gtx.Dp(rowPad)
		if txt != "" {
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			icon(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		}
		x += gtx.Dp(14) + gtx.Dp(theme.SpaceS)
		if running {
			at(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), func(gtx layout.Context) layout.Dimensions {
				return spinner(gtx, gtx.Dp(12), th.Bg, th.Muted, d.sec)
			})
		} else {
			icon(gtx, image.Pt(x, (hh-gtx.Dp(14))/2), "brain", 14, th.TextQuiet)
		}
		x += gtx.Dp(14) + gtx.Dp(theme.SpaceS)
		label := thoughtFor(took, running)
		if running && took >= time.Second {
			label += " " + duration(took)
		}
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), th.Muted, label)
		})
		at(gtx, image.Pt(x, (hh-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
		return layout.Dimensions{Size: image.Pt(w, hh)}
	}
	var hd layout.Dimensions
	if txt != "" {
		hd = clickable(gtx, d.t.click(it.ID), head)
	} else {
		hd = head(gtx)
	}
	if !open {
		return hd
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-gtx.Dp(bodyIndent)-gtx.Dp(rowPad)), gtx.Constraints.Max.Y)}
	italic := th.UIFont
	italic.Style = font.Italic
	bd := at(g, image.Pt(gtx.Dp(bodyIndent), hd.Size.Y), func(gtx layout.Context) layout.Dimensions {
		return d.selectable(gtx, d.t.sel(it.ID), tstyle{italic, th.Sp(theme.Body), th.Muted, 1.55}, txt)
	})
	return layout.Dimensions{Size: image.Pt(w, hd.Size.Y+bd.Size.Y+gtx.Dp(theme.SpaceS+theme.Space2XS))}
}

// --- task lists ---

// todoCard is a turn's task list as a checklist: done of total in its
// header, each task's state, the running one's active form.
func (d *cdraw) todoCard(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	tasks := it.Tool.Tasks
	w := gtx.Constraints.Max.X
	px, py := gtx.Dp(cardPadX), gtx.Dp(cardPadY)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px), gtx.Constraints.Max.Y)}
	c, bd := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceS),
			func(gtx layout.Context) layout.Dimensions { return d.todoHead(gtx, tasks) },
			func(gtx layout.Context) layout.Dimensions { return d.todoList(gtx, tasks) })
	})
	h := bd.Size.Y + 2*py
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusPopover), th.Bg, th.BorderSubtle)
	defer clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusPopover)).Push(gtx.Ops).Pop()
	at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return bd })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// todoHead is a task list's header: Tasks, a progress bar, 3 of 7.
func (d *cdraw) todoHead(gtx layout.Context, tasks []session.Task) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	done := tasksDone(tasks)
	tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, "Tasks")
	})
	nc, nd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, strconv.Itoa(done)+" of "+strconv.Itoa(len(tasks)))
	})
	h := max(td.Size.Y, nd.Size.Y)
	icon(gtx, image.Pt(0, (h-gtx.Dp(14))/2), "check", 14, th.Muted)
	x := gtx.Dp(14) + gtx.Dp(theme.SpaceS)
	at(gtx, image.Pt(x, (h-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
	at(gtx, image.Pt(w-nd.Size.X, (h-nd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { nc.Add(gtx.Ops); return nd })
	bx0 := x + td.Size.X + gtx.Dp(theme.SpaceM)
	bx1 := w - nd.Size.X - gtx.Dp(theme.SpaceM)
	if bw := min(bx1-bx0, gtx.Dp(160)); bw > gtx.Dp(theme.SpaceXL) {
		bx0 = bx1 - bw
		y := h/2 - gtx.Dp(theme.Space2XS)
		rad := gtx.Dp(theme.Space2XS)
		paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(bx0, y, bx1, y+gtx.Dp(theme.SpaceXS)), rad).Op(gtx.Ops))
		if len(tasks) > 0 && done > 0 {
			fx := bx0 + bw*done/len(tasks)
			paint.FillShape(gtx.Ops, th.Readable(th.Green, th.Bg), clip.UniformRRect(image.Rect(bx0, y, fx, y+gtx.Dp(theme.SpaceXS)), rad).Op(gtx.Ops))
		}
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// todoList is a task list's rows.
func (d *cdraw) todoList(gtx layout.Context, tasks []session.Task) layout.Dimensions {
	rows := make([]layout.Widget, len(tasks))
	for i, t := range tasks {
		rows[i] = func(gtx layout.Context) layout.Dimensions { return d.todoRow(gtx, t) }
	}
	return vstack(gtx, gtx.Dp(theme.Space2XS), rows...)
}

// todoRow is one task: an open circle while pending, a spinner while in
// progress, a green check once done, its text struck through.
func (d *cdraw) todoRow(gtx layout.Context, t session.Task) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	isz := gtx.Dp(14)
	f, c := th.UIFont, th.Fg
	switch t.Status {
	case "in_progress":
		f = medium(f)
	case "completed":
		c = th.Muted
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-isz-gtx.Dp(theme.SpaceS+theme.Space2XS)), gtx.Constraints.Max.Y)}
	tc, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, f, th.Sp(theme.Body), c, taskText(t), 0)
	})
	line := gtx.Sp(th.Sp(theme.Body)) * 135 / 100
	h := max(td.Size.Y, line) + gtx.Dp(theme.SpaceXS)
	iy := gtx.Dp(theme.Space2XS) + (line-isz)/2
	switch t.Status {
	case "in_progress":
		if d.s.Running {
			d.t.live = true
			at(gtx, image.Pt(gtx.Dp(1), iy+gtx.Dp(1)), func(gtx layout.Context) layout.Dimensions {
				return spinner(gtx, gtx.Dp(12), th.Bg, th.Blue, d.sec)
			})
		} else {
			icon(gtx, image.Pt(0, iy), "circle", 14, th.Blue)
		}
	case "completed":
		icon(gtx, image.Pt(0, iy), "check", 14, th.Readable(th.Green, th.Bg))
	default:
		icon(gtx, image.Pt(0, iy), "circle", 14, th.TextQuiet)
	}
	x := isz + gtx.Dp(theme.SpaceS+theme.Space2XS)
	at(gtx, image.Pt(x, gtx.Dp(theme.Space2XS)), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
	if t.Status == "completed" && td.Size.Y <= line+gtx.Dp(theme.Space2XS) {
		y := gtx.Dp(theme.Space2XS) + td.Size.Y/2
		paint.FillShape(gtx.Ops, theme.Mix(th.Muted, th.Bg, 0.3), clip.Rect{Min: image.Pt(x, y), Max: image.Pt(x+td.Size.X, y+gtx.Dp(1))}.Op())
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// TodoStrip draws, while a turn runs and its task list has tasks left, a
// band for above the composer: the task in progress and how many are
// done. A click opens it to the whole list. It is as wide as the
// transcript's column; zero size when there is nothing to show.
func (t *Transcript) TodoStrip(gtx layout.Context, th *theme.Theme, s *session.Snapshot) layout.Dimensions {
	tasks := liveTodo(s)
	if tasks == nil {
		return layout.Dimensions{}
	}
	if t.open == nil {
		t.init()
	}
	d := &cdraw{t: t, th: th, s: s, sec: gtx.Now.Sub(t.epoch).Seconds()}
	size := gtx.Constraints.Max
	col := max(0, size.X-2*gtx.Dp(theme.SpaceXL))
	if t.Width > 0 {
		col = min(col, gtx.Dp(t.Width))
	}
	d.col = col
	x0 := (size.X - col) / 2
	open := t.toggled(gtx, "todo-strip", false)
	ck := t.click("todo-strip")
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
				now = "Next: " + taskText(task)
				break
			}
		}
	}
	dims := at(gtx, image.Pt(x0, gtx.Dp(theme.SpaceXS)), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Max: image.Pt(col, gtx.Constraints.Max.Y)}
		w := col
		hh := gtx.Dp(rowH)
		px := gtx.Dp(theme.SpaceM)
		var body layout.Dimensions
		var bc = func() {}
		if open {
			g := gtx
			g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px), gtx.Dp(320))}
			c, bd := measure(g, func(gtx layout.Context) layout.Dimensions { return d.todoList(gtx, tasks) })
			body = bd
			bc = func() {
				at(gtx, image.Pt(px, hh), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return bd })
			}
		}
		h := hh
		if open {
			h += body.Size.Y + gtx.Dp(theme.SpaceS)
		}
		rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusPopover), th.Surface, th.BorderSubtle)
		defer clip.UniformRRect(image.Rect(0, 0, w, h).Inset(gtx.Dp(1)), gtx.Dp(theme.RadiusPopover)-gtx.Dp(1)).Push(gtx.Ops).Pop()
		clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
			rowFill(gtx, th, ck, image.Pt(w, hh))
			x := px
			at(gtx, image.Pt(x, (hh-gtx.Dp(12))/2), func(gtx layout.Context) layout.Dimensions {
				return spinner(gtx, gtx.Dp(12), th.Surface, th.Blue, d.sec)
			})
			d.t.live = true
			x += gtx.Dp(12) + gtx.Dp(theme.SpaceS)
			nc, nd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, strconv.Itoa(tasksDone(tasks))+" of "+strconv.Itoa(len(tasks)))
			})
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			cx := w - px - gtx.Dp(12)
			icon(gtx, image.Pt(cx, (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
			nx := cx - gtx.Dp(theme.SpaceS) - nd.Size.X
			at(gtx, image.Pt(nx, (hh-nd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { nc.Add(gtx.Ops); return nd })
			g := gtx
			g.Constraints.Max.X = max(0, nx-gtx.Dp(theme.SpaceM)-x)
			lc, ld := measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, now)
			})
			at(gtx, image.Pt(x, (hh-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
			return layout.Dimensions{Size: image.Pt(w, hh)}
		})
		bc()
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
	if t.live {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
	}
	return layout.Dimensions{Size: image.Pt(size.X, dims.Size.Y+gtx.Dp(theme.SpaceXS))}
}

// --- subagents ---

// agentCard is a subagent's card in a work log: what it does and its
// type, its state with the time and the calls it made, the calls folded
// inside, and its result's first lines, which open to the whole of it.
func (d *cdraw) agentCard(gtx layout.Context, it *session.Item) layout.Dimensions {
	th := d.th
	c := it.Tool
	in := inputMap(c.Input)
	desc := in.str("description")
	if desc == "" {
		desc = cmpOr(firstLine(in.str("prompt")), c.ToolName)
	}
	kind := in.str("subagent_type")
	running, failed := c.Status == "running", c.Status == "failed"
	if running {
		d.t.live = true
	}
	took := agentTook(it, d.t.now)
	calls := agentCalls(c)
	w := gtx.Constraints.Max.X
	outer := gtx.Dp(theme.SpaceS)
	cw := w - 2*gtx.Dp(rowPad) + 2*outer
	px := gtx.Dp(theme.SpaceM)
	hh := gtx.Dp(rowH) + gtx.Dp(theme.SpaceXS)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(cw, gtx.Constraints.Max.Y)}
	callsOpen := d.t.toggled(gtx, "calls:"+it.ID, false)
	resOpen := d.t.toggled(gtx, "result:"+it.ID, false)
	body, bd := measure(g, func(gtx layout.Context) layout.Dimensions {
		var ws []layout.Widget
		// The header: bot, description, type chip; state on the right.
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			x := px
			switch {
			case running:
				at(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), func(gtx layout.Context) layout.Dimensions {
					return spinner(gtx, gtx.Dp(12), th.Surface, th.Blue, d.sec)
				})
			case failed:
				icon(gtx, image.Pt(x, (hh-gtx.Dp(14))/2), "x", 14, th.Red)
			default:
				icon(gtx, image.Pt(x, (hh-gtx.Dp(14))/2), "bot", 14, th.Muted)
			}
			x += gtx.Dp(14) + gtx.Dp(theme.SpaceS)
			state := "Running"
			sc := th.Muted
			switch {
			case failed:
				state, sc = "Failed", th.Readable(th.Red, th.Surface)
			case !running:
				state = "Done"
			}
			if took >= time.Second {
				state += " · " + duration(took)
			}
			if calls > 0 {
				state += " · " + plural(calls, "tool")
			}
			rc, rd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), sc, state)
			})
			rx := w - px - rd.Size.X
			dc, dd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, desc)
			})
			base := centerBase(hh, dd)
			at(gtx, image.Pt(rx-(w-cw), onBase(base, rd)), func(gtx layout.Context) layout.Dimensions { rc.Add(gtx.Ops); return rd })
			room := rx - (w - cw) - gtx.Dp(theme.SpaceM) - x
			g := gtx
			g.Constraints.Max.X = max(0, room)
			dc, dd = measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, desc)
			})
			at(gtx, image.Pt(x, onBase(base, dd)), func(gtx layout.Context) layout.Dimensions { dc.Add(gtx.Ops); return dd })
			x += dd.Size.X + gtx.Dp(theme.SpaceS)
			if kind != "" && room-dd.Size.X > gtx.Dp(60) {
				g.Constraints.Max.X = room - dd.Size.X - gtx.Dp(theme.SpaceS)
				kc, kd := measure(g, func(gtx layout.Context) layout.Dimensions { return d.tag(gtx, kind) })
				at(gtx, image.Pt(x, (hh-kd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { kc.Add(gtx.Ops); return kd })
			}
			return layout.Dimensions{Size: image.Pt(cw, hh)}
		})
		if running && c.Progress != "" {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(rowPad + 14 + theme.SpaceS), Right: rowPad, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, c.Progress)
				})
			})
		}
		if n := len(c.SubagentCalls); n > 0 {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.agentCalls(gtx, it, callsOpen) })
		}
		if out := strings.TrimSpace(c.Output); out != "" && !running {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.agentResult(gtx, it.ID, out, resOpen) })
		}
		return vstack(gtx, 0, ws...)
	})
	h := bd.Size.Y
	y := gtx.Dp(theme.Space2XS)
	x := gtx.Dp(rowPad) - outer
	r := image.Rect(x, y, x+cw, y+h)
	rrect(gtx, r, gtx.Dp(theme.RadiusPopover), th.Surface, th.BorderSubtle)
	cl := clip.UniformRRect(r.Inset(gtx.Dp(1)), gtx.Dp(theme.RadiusPopover)-gtx.Dp(1)).Push(gtx.Ops)
	at(gtx, image.Pt(x, y), func(gtx layout.Context) layout.Dimensions { body.Add(gtx.Ops); return bd })
	cl.Pop()
	return layout.Dimensions{Size: image.Pt(w, h+2*y)}
}

// agentCalls is a subagent's calls behind a fold, each drawn as the work
// log draws a call.
func (d *cdraw) agentCalls(gtx layout.Context, it *session.Item, open bool) layout.Dimensions {
	th := d.th
	c := it.Tool
	w := gtx.Constraints.Max.X
	hh := gtx.Dp(rowH)
	ck := d.t.click("calls:" + it.ID)
	head := clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Max: image.Pt(w, gtx.Dp(1))}.Op())
		rowFill(gtx, th, ck, image.Pt(w, hh))
		chev := "chevron-right"
		if open {
			chev = "chevron-down"
		}
		x := gtx.Dp(rowPad)
		icon(gtx, image.Pt(x+gtx.Dp(1), (hh-gtx.Dp(12))/2), chev, 12, th.Muted)
		x += gtx.Dp(14) + gtx.Dp(theme.SpaceS)
		last := c.SubagentCalls[len(c.SubagentCalls)-1]
		txt := plural(len(c.SubagentCalls), "call")
		if c.Status == "running" {
			txt += " · " + summarize(&last, nil, time.Time{}, d.t.now).verb
		}
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, txt)
		})
		at(gtx, image.Pt(x, (hh-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
		return layout.Dimensions{Size: image.Pt(w, hh)}
	})
	if !open {
		return head
	}
	rows := []layout.Widget{}
	for i := range c.SubagentCalls {
		call := c.SubagentCalls[i]
		sub := session.Item{ID: it.ID + "/" + call.CallID, Kind: session.KindToolCall, Tool: &call, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt}
		rows = append(rows, func(gtx layout.Context) layout.Dimensions { return d.toolRow(gtx, &sub) })
	}
	body := at(gtx, image.Pt(0, head.Size.Y), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = w
		return vstack(gtx, 0, rows...)
	})
	return layout.Dimensions{Size: image.Pt(w, head.Size.Y+body.Size.Y)}
}

// agentResult is a subagent's result: its first lines, and Show all to
// read the whole of it as markdown.
func (d *cdraw) agentResult(gtx layout.Context, id, out string, open bool) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	short, cut := summaryCut(out, 3, 280)
	px := gtx.Dp(rowPad)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px), gtx.Constraints.Max.Y)}
	key := "result:" + id
	c, bd := measure(g, func(gtx layout.Context) layout.Dimensions {
		var ws []layout.Widget
		if open {
			full := session.Item{ID: key, Text: out}
			ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.markdown(gtx, key, d.t.reply(&full)) })
		} else {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return d.selectable(gtx, d.t.sel(key), d.small(theme.Mix(th.Muted, th.Fg, 0.4)), short)
			})
		}
		if cut || open {
			more := "Show all"
			if open {
				more = "Show less"
			}
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return at(gtx, image.Pt(-gtx.Dp(theme.SpaceS), 0), func(gtx layout.Context) layout.Dimensions {
					return d.ghost(gtx, d.t.click(key), "chevron-down", more, false)
				})
			})
		}
		return vstack(gtx, gtx.Dp(theme.Space2XS), ws...)
	})
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Max: image.Pt(w, gtx.Dp(1))}.Op())
	py := gtx.Dp(theme.SpaceS)
	at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return bd })
	return layout.Dimensions{Size: image.Pt(w, bd.Size.Y+2*py)}
}

// tag is a small muted chip: a subagent's type.
func (d *cdraw) tag(gtx layout.Context, txt string) layout.Dimensions {
	th := d.th
	px := gtx.Dp(theme.SpaceXS + theme.Space2XS)
	g := gtx
	g.Constraints.Max.X = max(0, gtx.Constraints.Max.X-2*px)
	c, td := measure(g, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, txt)
	})
	w, h := td.Size.X+2*px, td.Size.Y+gtx.Dp(theme.SpaceXS)
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	at(gtx, image.Pt(px, gtx.Dp(theme.Space2XS)), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return td })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// --- prompts ---

// pasteChip is a block of pasted text in a prompt: Pasted text · 20
// lines, which a click opens to the text.
func (d *cdraw) pasteChip(gtx layout.Context, id string, p paste) layout.Dimensions {
	th := d.th
	ck := d.t.click(id)
	open := d.t.toggled(gtx, id, false)
	head := clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		txt := "Pasted text · " + plural(p.lines, "line")
		c, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), theme.Mix(th.Fg, th.Bg, 0.2), txt)
		})
		h := gtx.Dp(hit)
		w := gtx.Dp(30) + td.Size.X + gtx.Dp(hit)
		fill := th.SurfaceElevated
		if ck.Hovered() {
			fill = theme.Mix(fill, th.Fg, 0.06)
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		icon(gtx, image.Pt(gtx.Dp(theme.SpaceS+theme.Space2XS), (h-gtx.Dp(14))/2), "clipboard", 14, th.Muted)
		at(gtx, image.Pt(gtx.Dp(30), (h-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return td })
		chev := "chevron-right"
		if open {
			chev = "chevron-down"
		}
		icon(gtx, image.Pt(w-gtx.Dp(theme.SpaceL+theme.SpaceXS), (h-gtx.Dp(12))/2), chev, 12, th.Muted)
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
	if !open {
		return head
	}
	body := at(gtx, image.Pt(0, head.Size.Y+gtx.Dp(theme.SpaceXS)), func(gtx layout.Context) layout.Dimensions {
		return d.outBox(gtx, id, "", mdview.Detab(p.text), false)
	})
	return layout.Dimensions{Size: image.Pt(max(head.Size.X, body.Size.X), head.Size.Y+gtx.Dp(theme.SpaceXS)+body.Size.Y)}
}

// pastes are a prompt's pasted blocks as chips, one under another.
func (d *cdraw) pastes(gtx layout.Context, it *session.Item) layout.Dimensions {
	_, ps := promptParts(it.Text)
	ws := make([]layout.Widget, len(ps))
	for i, p := range ps {
		id := "paste:" + it.ID + "/" + strconv.Itoa(i)
		ws[i] = func(gtx layout.Context) layout.Dimensions { return d.pasteChip(gtx, id, p) }
	}
	return vstack(gtx, gtx.Dp(theme.SpaceXS), ws...)
}

// --- turns ---

// interrupted is the quiet divider where someone stopped a turn.
func (d *cdraw) interrupted(gtx layout.Context) layout.Dimensions {
	return d.divider(gtx, nil, "", "Interrupted", false)
}

// footMeta is a finished turn's footer after its time: the tokens and
// the cost, hover for each model's.
func (d *cdraw) footMeta(gtx layout.Context, turn, when string) layout.Dimensions {
	th := d.th
	u := turnUsage(d.s, turn)
	txt := when
	if u.in+u.out > 0 {
		txt = joinDot(txt, count(u.in)+" in · "+count(u.out)+" out")
	}
	if u.priced && u.cost > 0 {
		txt = joinDot(txt, dollars(u.cost))
	}
	if txt == "" {
		return layout.Dimensions{}
	}
	ck := d.t.click("foot:" + turn)
	dims := clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
		hh := gtx.Dp(hit)
		c, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, txt)
		})
		at(gtx, image.Pt(0, (hh-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return td })
		return layout.Dimensions{Size: image.Pt(td.Size.X, hh)}
	})
	if ck.Hovered() && len(u.lines) > 0 {
		d.footTip(gtx, u.lines)
	}
	return dims
}

// footTip is the footer's breakdown above it: one line per model.
func (d *cdraw) footTip(gtx layout.Context, lines []string) {
	th := d.th
	px, py := gtx.Dp(theme.SpaceS+theme.Space2XS), gtx.Dp(theme.SpaceS)
	ws := make([]layout.Widget, len(lines))
	for i, l := range lines {
		ws[i] = func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Fg, l)
		}
	}
	c, bd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return vstack(gtx, gtx.Dp(theme.Space2XS), ws...) })
	r := image.Rect(0, -bd.Size.Y-2*py-gtx.Dp(theme.SpaceXS), bd.Size.X+2*px, -gtx.Dp(theme.SpaceXS))
	rrect(gtx, r, gtx.Dp(theme.RadiusControl), th.SurfaceElevated, th.BorderStrong)
	at(gtx, r.Min.Add(image.Pt(px, py)), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return bd })
}

func joinDot(a, b string) string {
	if a == "" {
		return b
	}
	return a + " · " + b
}
