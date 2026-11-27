package mobile

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// workSheet is an orchestrator chat's Work sheet: its workers grouped
// Needs you, Blocked, Moving and Done, over the chat from the bottom.
// The "Work · N" chip under the title pill opens it; a tap on a worker opens
// that worker's chat.
type workSheet struct {
	open     bool
	chip     widget.Clickable
	scrim    widget.Clickable
	body     widget.Clickable // takes the taps on the sheet between its rows
	fold     widget.Clickable
	showDone bool
	rows     map[string]*widget.Clickable
	list     layout.List
}

// workCounts is how many of ws are in each group.
func workCounts(ws []model.Worker) (n [model.WorkDone + 1]int) {
	for _, w := range ws {
		n[w.Group]++
	}
	return n
}

// workTitle is the sheet's title: "Work · 2 for you · 3 moving".
func workTitle(ws []model.Worker) string {
	n := workCounts(ws)
	parts := []string{"Work"}
	for g, word := range []string{"for you", "blocked", "moving", "done"} {
		if n[g] > 0 {
			parts = append(parts, strconv.Itoa(n[g])+" "+word)
		}
	}
	return strings.Join(parts, " · ")
}

// hasWork is whether the chat's tab has workers to show a Work chip for.
func hasWork(env chatEnv) bool { return env.Open && len(env.Tab.Workers) > 0 }

// workChip is the "Work · N" chip under the title pill, 48dp and floating,
// N the workers not done, with an amber dot while one waits on you.
func (c *chatView) workChip(gtx C, th *theme.Theme, env chatEnv) D {
	if !hasWork(env) {
		return D{}
	}
	ws := env.Tab.Workers
	if c.sheet.chip.Clicked(gtx) {
		c.sheet.open = true
	}
	n := workCounts(ws)
	return c.sheet.chip.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		m := op.Record(gtx.Ops)
		gtx.Constraints.Min = image.Pt(0, gtx.Dp(48))
		d := layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx C) D {
			return midFlex(gtx,
				layout.Rigid(func(gtx C) D {
					if n[model.WorkNeedsYou] == 0 {
						return D{}
					}
					return layout.Inset{Right: 7}.Layout(gtx, func(gtx C) D { return dot(gtx, th.Yellow, 8) })
				}),
				layout.Rigid(func(gtx C) D {
					return label(gtx, th, weight(th.UIFont, font.Medium), 13.5, th.Fg, 1, "Work · "+strconv.Itoa(len(ws)-n[model.WorkDone]))
				}),
			)
		})
		call := m.Stop()
		fill := th.SurfaceElevated
		if c.sheet.chip.Pressed() || c.sheet.open {
			fill = theme.Mix(fill, th.Fg, 0.1)
		}
		kit.Surface(gtx, image.Rectangle{Max: d.Size}, d.Size.Y/2, kit.Floating, th.BorderStrong, fill)
		call.Add(gtx.Ops)
		return d
	})
}

// dot is a filled circle size dp across.
func dot(gtx C, col color.NRGBA, size unit.Dp) D {
	s := gtx.Dp(size)
	paint.FillShape(gtx.Ops, col, clip.Ellipse{Max: image.Pt(s, s)}.Op(gtx.Ops))
	return D{Size: image.Pt(s, s)}
}

// workSheetLayout draws the open sheet over the chat, at most 72% of its
// height, and handles its taps.
func (c *chatView) workSheetLayout(gtx C, th *theme.Theme, env chatEnv) {
	s := &c.sheet
	ws := env.Tab.Workers
	if !s.open {
		return
	}
	if len(ws) == 0 || s.scrim.Clicked(gtx) {
		s.open = false
		return
	}
	if s.fold.Clicked(gtx) {
		s.showDone = !s.showDone
	}
	if s.rows == nil {
		s.rows = map[string]*widget.Clickable{}
	}
	for _, w := range ws {
		if b := s.rows[w.ID]; b != nil && b.Clicked(gtx) && env.OpenTab != nil {
			s.open = false
			env.OpenTab(w.ID)
			return
		}
	}
	size := gtx.Constraints.Max
	s.scrim.Layout(gtx, func(gtx C) D {
		paint.FillShape(gtx.Ops, th.Scrim, clip.Rect{Max: size}.Op())
		return D{Size: size}
	})
	now := env.now()
	var items []layout.Widget
	items = append(items, func(gtx C) D {
		return layout.Inset{Bottom: 4}.Layout(gtx, func(gtx C) D {
			return label(gtx, th, weight(th.UIFont, font.SemiBold), 16, th.Fg, 1, workTitle(ws))
		})
	})
	for g := model.WorkNeedsYou; g <= model.WorkDone; g++ {
		var in []model.Worker
		for _, w := range ws {
			if w.Group == g {
				in = append(in, w)
			}
		}
		if len(in) == 0 {
			continue
		}
		items = append(items, func(gtx C) D { return s.group(gtx, th, g, len(in)) })
		if g == model.WorkDone && !s.showDone {
			continue
		}
		for _, w := range in {
			b := s.rows[w.ID]
			if b == nil {
				b = new(widget.Clickable)
				s.rows[w.ID] = b
			}
			items = append(items, func(gtx C) D { return workCard(gtx, th, b, w, now) })
		}
	}
	s.list.Axis = layout.Vertical
	// The sheet is drawn once to measure it, then placed at the bottom.
	gtx.Constraints = layout.Constraints{Min: image.Pt(size.X, 0), Max: image.Pt(size.X, size.Y*72/100)}
	m := op.Record(gtx.Ops)
	d := s.body.Layout(gtx, func(gtx C) D {
		return layout.Inset{Left: 16, Right: 16, Bottom: 16}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Top: 8, Bottom: 12}.Layout(gtx, func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return layout.Center.Layout(gtx, func(gtx C) D {
							g := image.Pt(gtx.Dp(38), gtx.Dp(4))
							paint.FillShape(gtx.Ops, th.BorderStrong, clip.UniformRRect(image.Rectangle{Max: g}, g.Y/2).Op(gtx.Ops))
							return D{Size: g}
						})
					})
				}),
				layout.Rigid(func(gtx C) D {
					return s.list.Layout(gtx, len(items), func(gtx C, i int) D { return items[i](gtx) })
				}),
			)
		})
	})
	call := m.Stop()
	defer op.Offset(image.Pt(0, size.Y-d.Size.Y)).Push(gtx.Ops).Pop()
	r := gtx.Dp(18)
	rect := image.Rectangle{Max: image.Pt(d.Size.X, d.Size.Y+r)}
	paint.FillShape(gtx.Ops, th.BorderStrong, clip.UniformRRect(rect, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rect(0, 1, rect.Max.X, rect.Max.Y), r).Op(gtx.Ops))
	call.Add(gtx.Ops)
}

// group is a group's heading: its dot, name and count; Done's folds and
// unfolds its cards.
func (s *workSheet) group(gtx C, th *theme.Theme, g model.WorkGroup, n int) D {
	col := map[model.WorkGroup]color.NRGBA{model.WorkNeedsYou: th.Yellow, model.WorkBlocked: th.Red, model.WorkMoving: th.Blue, model.WorkDone: th.Muted}[g]
	draw := func(gtx C) D {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(36))
		if g == model.WorkDone {
			gtx.Constraints.Min.Y = gtx.Dp(touch)
		}
		return midFlex(gtx,
			layout.Rigid(func(gtx C) D { return dot(gtx, col, 7) }),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Flexed(1, func(gtx C) D {
				d := label(gtx, th, weight(th.UIFont, font.SemiBold), 13, th.TextQuiet, 1, g.Name()+" "+strconv.Itoa(n))
				d.Size.X = gtx.Constraints.Max.X
				return d
			}),
			layout.Rigid(func(gtx C) D {
				switch {
				case g != model.WorkDone:
					return D{}
				case s.showDone:
					return label(gtx, th, weight(th.UIFont, font.Medium), 13, th.TextQuiet, 1, "Hide")
				}
				return label(gtx, th, weight(th.UIFont, font.Medium), 13, th.TextQuiet, 1, "Show")
			}),
		)
	}
	if g != model.WorkDone {
		return draw(gtx)
	}
	return s.fold.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		return draw(gtx)
	})
}

// workCard is one worker: its name and machine or age, then its chip.
// The whole card opens the worker's chat.
func workCard(gtx C, th *theme.Theme, b *widget.Clickable, w model.Worker, now time.Time) D {
	return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx C) D {
		return b.Layout(gtx, func(gtx C) D {
			pointer.CursorPointer.Add(gtx.Ops)
			fill := th.Surface
			if b.Pressed() {
				fill = th.Pressed
			}
			return box(gtx, fill, th.BorderSubtle, 12, layout.Inset{Left: 12, Right: 12, Top: 10, Bottom: 10}, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
							layout.Flexed(1, func(gtx C) D {
								return label(gtx, th, weight(th.UIFont, font.SemiBold), 15, th.Fg, 1, w.Name)
							}),
							layout.Rigid(func(gtx C) D {
								meta := w.Age(now)
								if w.Host != "" {
									meta = strings.TrimSuffix(w.Host+" · "+meta, " · ")
								}
								return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
									return label(gtx, th, th.UIFont, 13, th.TextQuiet, 1, meta)
								})
							}),
						)
					}),
					layout.Rigid(layout.Spacer{Height: 6}.Layout),
					layout.Rigid(func(gtx C) D { return chipTag(gtx, th, w, fill) }),
				)
			})
		})
	})
}

// chipTag is a worker's step chip: amber while it waits on you, red when
// it is blocked, quiet otherwise.
func chipTag(gtx C, th *theme.Theme, w model.Worker, base color.NRGBA) D {
	fg, ring := th.TextQuiet, th.BorderSubtle
	switch w.Group {
	case model.WorkNeedsYou:
		fg, ring = th.Readable(th.Yellow, base), theme.Mix(th.BorderSubtle, th.Yellow, 0.45)
	case model.WorkBlocked:
		fg, ring = th.Readable(th.Red, base), theme.Mix(th.BorderSubtle, th.Red, 0.45)
	}
	gtx.Constraints.Min.X = 0
	return box(gtx, base, ring, 11, layout.Inset{Left: 9, Right: 9, Top: 2, Bottom: 2}, func(gtx C) D {
		return label(gtx, th, th.UIFont, 13, fg, 1, w.Chip)
	})
}
