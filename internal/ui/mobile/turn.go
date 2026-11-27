package mobile

import (
	"image"
	"image/color"
	"strconv"
	"time"

	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// rowOf is the row that shows entry j: its own, or its turn's Worked
// line when the work is folded.
func (c *chatView) rowOf(j int) int {
	at := 0
	for r, row := range c.rows {
		if row.at > j {
			break
		}
		if row.kind != rowDiff {
			at = r
		}
	}
	return at
}

// logRow draws row of the list.
func (c *chatView) logRow(gtx C, th *theme.Theme, row logRow) D {
	switch row.kind {
	case rowWork:
		return c.workLine(gtx, th, c.turns[row.i])
	case rowFolded:
		return c.folded(gtx, th, c.data.Entries[row.i])
	case rowDiff:
		return diffCard(gtx, th, c.turns[row.i])
	}
	return c.entry(gtx, th, c.data.Entries[row.i])
}

// workLine is a turn's work in one line, "Worked 2m 14s · 6 tools · 1
// failed", which opens the calls under it.
func (c *chatView) workLine(gtx C, th *theme.Theme, t turnWork) D {
	w := c.work(t.key)
	if w.click.Clicked(gtx) {
		w.open = !w.open
		c.gen++
	}
	now := gtx.Now
	if now.IsZero() {
		now = time.Now()
	}
	if t.running {
		gtx.Execute(op.InvalidateCmd{At: now.Truncate(time.Second).Add(time.Second)})
	}
	return w.click.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(48))
		m := op.Record(gtx.Ops)
		d := midFlex(gtx,
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
			layout.Rigid(func(gtx C) D {
				if !t.running {
					return D{}
				}
				return layout.Inset{Right: 8}.Layout(gtx, func(gtx C) D { return circle(gtx, th.Blue, 7) })
			}),
			layout.Rigid(func(gtx C) D {
				return label(gtx, th, weight(th.UIFont, font.Medium), workSize, th.Muted, 1, t.title(now))
			}),
			layout.Rigid(func(gtx C) D {
				if t.tools == 0 {
					return D{}
				}
				return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return chip(gtx, th, plural(t.tools, "tool"), th.Muted, th.Border) })
			}),
			layout.Rigid(func(gtx C) D {
				if t.failed == 0 {
					return D{}
				}
				red := th.Readable(th.Red, th.Surface)
				return layout.Inset{Left: 6}.Layout(gtx, func(gtx C) D {
					return chip(gtx, th, strconv.Itoa(t.failed)+" failed", red, theme.Mix(th.Border, th.Red, 0.45))
				})
			}),
			layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
			layout.Rigid(func(gtx C) D {
				chev := icRight
				if w.open {
					chev = icDown
				}
				return glyph(gtx, chev, 16, th.Muted)
			}),
			layout.Rigid(layout.Spacer{Width: 12}.Layout),
		)
		call := m.Stop()
		// The bar is 38dp of the 48dp tap row, as the mock's pill.
		inset := (d.Size.Y - gtx.Dp(38)) / 2
		fill := th.Surface
		if w.click.Pressed() {
			fill = theme.Mix(fill, th.Fg, 0.08)
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(0, inset, d.Size.X, d.Size.Y-inset), gtx.Dp(9)).Op(gtx.Ops))
		call.Add(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		return d
	})
}

// chip is a small outlined count on the Worked line.
func chip(gtx C, th *theme.Theme, s string, fg, ring color.NRGBA) D {
	m := op.Record(gtx.Ops)
	d := layout.Inset{Left: 8, Right: 8, Top: 2, Bottom: 2}.Layout(gtx, func(gtx C) D {
		return label(gtx, th, th.UIFont, 13, fg, 1, s)
	})
	call := m.Stop()
	r := image.Rectangle{Max: d.Size}
	rr := d.Size.Y / 2
	paint.FillShape(gtx.Ops, ring, clip.Stroke{Path: clip.UniformRRect(r, rr).Path(gtx.Ops), Width: float32(max(1, gtx.Dp(1)))}.Op())
	call.Add(gtx.Ops)
	return d
}

// folded is an entry of an open Worked line: a call on a line of its
// own, or a reply between calls, quieter than the turn's last.
func (c *chatView) folded(gtx C, th *theme.Theme, e flow.Entry) D {
	return layout.Inset{Left: 12}.Layout(gtx, func(gtx C) D {
		d := layout.Inset{Left: 12}.Layout(gtx, func(gtx C) D {
			if e.Kind == flow.EntryTool {
				return c.tool(gtx, th, c.row(e.Seq), e)
			}
			defer paint.PushOpacity(gtx.Ops, 0.72).Pop()
			return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D { return c.replyAt(gtx, th, c.row(e.Seq), e.Text, workSize) })
		})
		// The rule at the left ties the calls to their line.
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Max: image.Pt(max(1, gtx.Dp(1.5)), d.Size.Y+gtx.Dp(10))}.Op())
		return d
	})
}

// diffCard is a turn's edits: "Edited 2 files +18 −4" and a bar, then a
// row per file with its counts.
func diffCard(gtx C, th *theme.Theme, t turnWork) D {
	add, del := t.totals()
	green, red := th.Readable(th.Green, th.Surface), th.Readable(th.Red, th.Surface)
	shown, more := t.files, 0
	if len(shown) > diffFiles+1 { // one more file than the cap shows instead of "1 more"
		shown, more = shown[:diffFiles], len(shown)-diffFiles
	}
	counts := func(gtx C, a, d int, size unit.Sp) D {
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return label(gtx, th, th.MonoFont, size, green, 1, "+"+strconv.Itoa(a)) }),
			layout.Rigid(layout.Spacer{Width: 6}.Layout),
			layout.Rigid(func(gtx C) D { return label(gtx, th, th.MonoFont, size, red, 1, "−"+strconv.Itoa(d)) }),
		)
	}
	rows := []layout.FlexChild{
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(44))
			d := layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min.Y = gtx.Dp(44)
				return midFlex(gtx,
					layout.Rigid(func(gtx C) D {
						return label(gtx, th, weight(th.UIFont, font.SemiBold), workSize, th.Fg, 1, "Edited "+plural(len(t.files), "file"))
					}),
					layout.Rigid(layout.Spacer{Width: 10}.Layout),
					layout.Rigid(func(gtx C) D { return counts(gtx, add, del, 13.5) }),
					layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
					layout.Rigid(func(gtx C) D { return diffBar(gtx, th, add, del) }),
				)
			})
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, d.Size.Y-1), Max: d.Size}.Op())
			return d
		}),
	}
	for _, f := range shown {
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 14, Right: 14, Top: 7, Bottom: 7}.Layout(gtx, func(gtx C) D {
				return midFlex(gtx,
					layout.Flexed(1, func(gtx C) D { return label(gtx, th, th.MonoFont, 13.5, th.Fg, 1, f.name) }),
					layout.Rigid(layout.Spacer{Width: 10}.Layout),
					layout.Rigid(func(gtx C) D { return counts(gtx, f.add, f.del, 13) }),
				)
			})
		}))
	}
	if more > 0 {
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 14, Right: 14, Top: 7, Bottom: 7}.Layout(gtx, func(gtx C) D {
				return label(gtx, th, th.UIFont, 13, th.Muted, 1, strconv.Itoa(more)+" more")
			})
		}))
	}
	rows = append(rows, layout.Rigid(layout.Spacer{Height: 5}.Layout))
	return box(gtx, th.Surface, th.BorderSubtle, 10, layout.Inset{}, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})
}

// diffFiles is how many files the diff card lists before "N more".
const diffFiles = 5

// diffBar is the share of lines added and removed, 64dp across.
func diffBar(gtx C, th *theme.Theme, add, del int) D {
	w, h := gtx.Dp(64), gtx.Dp(5)
	sz := image.Pt(w, h)
	rr := clip.UniformRRect(image.Rectangle{Max: sz}, h/2)
	defer rr.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Max: sz}.Op())
	if add+del > 0 {
		g := w * add / (add + del)
		paint.FillShape(gtx.Ops, th.Green, clip.Rect{Max: image.Pt(g, h)}.Op())
		paint.FillShape(gtx.Ops, th.Red, clip.Rect{Min: image.Pt(g, 0), Max: sz}.Op())
	}
	return D{Size: sz}
}
