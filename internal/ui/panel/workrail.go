package panel

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// WorkRailWidth is the work rail's width beside an orchestrator's chat.
const WorkRailWidth unit.Dp = 220

// WorkRail is the column at the right of an orchestrator's chat that
// lists its workers grouped Needs you, Blocked, Moving and Done (folded).
// A click on a worker opens its tab; the x closes the rail for the chat.
type WorkRail struct {
	Closed   bool
	shut     widget.Clickable
	fold     widget.Clickable
	showDone bool
	rows     map[string]*widget.Clickable
	list     layout.List
}

// WorkRailShown reports whether a chat with workers ws draws the rail.
func (r *WorkRail) Shown(ws []model.Worker) bool { return r != nil && !r.Closed && len(ws) > 0 }

// Layout draws the rail for workers ws, filling the constraints, and
// returns the tab of a worker clicked this frame, "" for none.
func (r *WorkRail) Layout(gtx layout.Context, th *theme.Theme, ws []model.Worker, now time.Time) (open string) {
	if r.shut.Clicked(gtx) {
		r.Closed = true
	}
	if r.fold.Clicked(gtx) {
		r.showDone = !r.showDone
	}
	if r.rows == nil {
		r.rows = map[string]*widget.Clickable{}
	}
	for _, w := range ws {
		if c := r.rows[w.ID]; c != nil && c.Clicked(gtx) {
			open = w.ID
		}
	}
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Max: image.Pt(gtx.Dp(1), size.Y)}.Op())

	var items []layout.Widget
	items = append(items, func(gtx layout.Context) layout.Dimensions { return r.head(gtx, th, len(ws)) })
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
		items = append(items, func(gtx layout.Context) layout.Dimensions { return r.group(gtx, th, g, len(in)) })
		if g == model.WorkDone && !r.showDone {
			continue
		}
		for _, w := range in {
			c := r.rows[w.ID]
			if c == nil {
				c = new(widget.Clickable)
				r.rows[w.ID] = c
			}
			items = append(items, func(gtx layout.Context) layout.Dimensions { return workRow(gtx, th, c, w, now) })
		}
	}
	r.list.Axis = layout.Vertical
	gtx.Constraints.Min = size
	layout.Inset{Left: theme.SpaceM, Right: theme.SpaceM, Bottom: theme.SpaceM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return r.list.Layout(gtx, len(items), func(gtx layout.Context, i int) layout.Dimensions { return items[i](gtx) })
	})
	return open
}

// head is "Workers 5" and the close button.
func (r *WorkRail) head(gtx layout.Context, th *theme.Theme, n int) layout.Dimensions {
	return layout.Inset{Top: theme.SpaceS, Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Body), th.Fg, "Workers", 1)
			}),
			layout.Rigid(layout.Spacer{Width: theme.SpaceXS + theme.Space2XS}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return wide(gtx, label(gtx, th, th.UIFont, th.Sp(theme.Body), th.TextQuiet, strconv.Itoa(n), 1))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.IconButton(gtx, th, &r.shut, kit.Ghost, kit.Small, func(gtx layout.Context, c color.NRGBA, s int) { sidebar.Icon(gtx, "x", s, c) })
			}),
		)
	})
}

// group is a group's heading and count, like "Needs you 2"; Done's
// heading folds and unfolds its rows.
func (r *WorkRail) group(gtx layout.Context, th *theme.Theme, g model.WorkGroup, n int) layout.Dimensions {
	draw := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: theme.SpaceM, Bottom: theme.SpaceS - theme.Space2XS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					s := gtx.Dp(7)
					c := groupColor(th, g)
					paint.FillShape(gtx.Ops, c, clip.Ellipse{Max: image.Pt(s, s)}.Op(gtx.Ops))
					return layout.Dimensions{Size: image.Pt(s, s)}
				}),
				layout.Rigid(layout.Spacer{Width: theme.SpaceXS + theme.Space2XS}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return wide(gtx, label(gtx, th, semibold(th.UIFont), th.Sp(theme.Small), th.TextQuiet, g.Name(), 1))
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					s := strconv.Itoa(n)
					switch {
					case g == model.WorkDone && r.showDone:
						s += "  Hide"
					case g == model.WorkDone:
						s += "  Show"
					}
					return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Small), th.TextQuiet, s, 1)
				}),
			)
		})
	}
	if g != model.WorkDone {
		return draw(gtx)
	}
	return kit.Clickable(gtx, &r.fold, draw)
}

// groupColor is a group's dot: amber waits on you, red is stuck, blue
// moves, muted is done.
func groupColor(th *theme.Theme, g model.WorkGroup) color.NRGBA {
	switch g {
	case model.WorkNeedsYou:
		return th.Yellow
	case model.WorkBlocked:
		return th.Red
	case model.WorkMoving:
		return th.Blue
	}
	return th.Muted
}

// workRow is one worker: its name, then its chip and its age or machine.
func workRow(gtx layout.Context, th *theme.Theme, c *widget.Clickable, w model.Worker, now time.Time) layout.Dimensions {
	return layout.Inset{Bottom: theme.SpaceS - theme.Space2XS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return kit.Clickable(gtx, c, func(gtx layout.Context) layout.Dimensions {
			fill := th.Surface
			switch {
			case c.Pressed():
				fill = th.Pressed
			case c.Hovered():
				fill = th.Hover
			}
			m := op.Record(gtx.Ops)
			d := layout.Inset{Top: theme.SpaceS - 1, Bottom: theme.SpaceS - 1, Left: theme.SpaceS + 1, Right: theme.SpaceS + 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return label(gtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, w.Name, 1)
					}),
					layout.Rigid(layout.Spacer{Height: theme.SpaceXS}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return workChip(gtx, th, w, fill) }),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								gtx.Constraints.Min.X = gtx.Constraints.Max.X
								return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
									return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.TextQuiet, workMeta(w, now), 1)
								})
							}),
						)
					}),
				)
			})
			call := m.Stop()
			rrect(gtx, image.Rectangle{Max: d.Size}, gtx.Dp(theme.RadiusPopover), fill, th.BorderSubtle)
			call.Add(gtx.Ops)
			return d
		})
	})
}

// workMeta is a row's right side: its machine and age, "box · 12m".
func workMeta(w model.Worker, now time.Time) string {
	age := w.Age(now)
	switch {
	case w.Host == "":
		return age
	case age == "":
		return w.Host
	}
	return w.Host + " · " + age
}

// workChip is a worker's step chip: a pill with a 1dp ring, amber for a
// worker that waits on you, red for a blocked one.
func workChip(gtx layout.Context, th *theme.Theme, w model.Worker, base color.NRGBA) layout.Dimensions {
	if w.Group == model.WorkMoving && (w.Chip == "working" || w.Chip == "background") {
		return workingMark(gtx, th, w.Chip, base)
	}
	fg, ring := th.TextQuiet, th.BorderSubtle
	switch w.Group {
	case model.WorkNeedsYou:
		fg = th.Readable(th.Yellow, base)
		ring = theme.Mix(th.BorderSubtle, th.Yellow, 0.45)
	case model.WorkBlocked:
		fg = th.Readable(th.Red, base)
		ring = theme.Mix(th.BorderSubtle, th.Red, 0.45)
	}
	m := op.Record(gtx.Ops)
	d := layout.Inset{Left: theme.SpaceS - 1, Right: theme.SpaceS - 1, Top: 1, Bottom: 1}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, th.UIFont, th.Sp(theme.Small), fg, w.Chip, 1)
	})
	call := m.Stop()
	rrect(gtx, image.Rectangle{Max: d.Size}, d.Size.Y/2, base, ring)
	call.Add(gtx.Ops)
	return d
}

// workingMark is a working worker's state as the composer's status line
// draws it: the blue spinner and the word in blue, "Working".
func workingMark(gtx layout.Context, th *theme.Theme, chip string, base color.NRGBA) layout.Dimensions {
	gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
	sec := float64(gtx.Now.UnixMilli()%3_600_000) / 1000
	fg := th.Readable(th.Blue, base)
	name := strings.ToUpper(chip[:1]) + chip[1:]
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return spinner(gtx, gtx.Dp(11), base, th.Blue, sec) }),
		layout.Rigid(layout.Spacer{Width: theme.SpaceXS + theme.Space2XS}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), fg, name, 1)
		}),
	)
}

// wide is d stretched to the width a Flexed child has, so the children
// after it sit at the right.
func wide(gtx layout.Context, d layout.Dimensions) layout.Dimensions {
	d.Size.X = gtx.Constraints.Max.X
	return d
}
