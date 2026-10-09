package sidebar

import (
	"fmt"
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// lucide's play and clock.
const (
	icPlay  = "M6 3L20 12L6 21Z"
	icClock = "M2 12a10 10 0 1 0 20 0a10 10 0 1 0-20 0M12 6v6l4 2"
)

// queueBlock is the queued tasks of a group, under its header and tabs,
// or of no group shown, at the end of the list.
type queueBlock struct {
	group string
	after int // the element it follows, whose slide it shares; -1 for none
	x     int
	top   int
	tasks []model.Task
}

// taskRow is a queued task's row: its hover area and its two buttons.
type taskRow struct{ hover, start, drop widget.Clickable }

// queueHeights are a block's label line and each task's row.
func queueHeights(gtx layout.Context) (head, row int) { return gtx.Dp(22), gtx.Dp(26) }

// placeQueue adds group's block, if it has queued tasks, at y after
// element after, and returns the y below it.
func (s *Sidebar) placeQueue(gtx layout.Context, v *view, group string, after, y int) int {
	tasks := v.queued[group]
	if len(tasks) == 0 {
		return y
	}
	x := 0
	if group != "" {
		x = gtx.Dp(groupIndent)
	}
	head, row := queueHeights(gtx)
	s.queue = append(s.queue, queueBlock{group: group, after: after, x: x, top: y, tasks: tasks})
	return y + head + len(tasks)*row + gtx.Dp(4)
}

func (s *Sidebar) task(id string) *taskRow {
	if s.taskRows == nil {
		s.taskRows = map[string]*taskRow{}
	}
	r := s.taskRows[id]
	if r == nil {
		r = &taskRow{}
		s.taskRows[id] = r
	}
	return r
}

// drawQueue draws block b: "Queued" and the count, then each task's first
// line, with start now and remove on hover.
func (s *Sidebar) drawQueue(gtx layout.Context, th *theme.Theme, b queueBlock) {
	head, rowH := queueHeights(gtx)
	w := gtx.Constraints.Max.X - b.x
	pad := gtx.Dp(12)
	quiet := theme.Mix(th.Sidebar, th.Muted, 0.7)
	o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
	g := gtx
	g.Constraints = layout.Exact(image.Pt(w-2*pad, head))
	hrow(g, head, gtx.Dp(6),
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.UIFont), th.Sp(theme.Caption), quiet, "Queued")
		}},
		item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, medium(th.UIFont), th.Sp(theme.Caption), theme.Mix(th.Sidebar, th.Muted, 0.45), fmt.Sprint(len(b.tasks)))
		}},
	)
	o.Pop()
	for i, t := range b.tasks {
		r := s.task(t.ID)
		o := op.Offset(image.Pt(0, head+i*rowH)).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(w, rowH))
		hovered := r.hover.Hovered() || r.start.Hovered() || r.drop.Hovered()
		r.hover.Layout(g, func(gtx layout.Context) layout.Dimensions {
			if hovered {
				paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(0, 0, w, rowH), gtx.Dp(6)).Op(gtx.Ops))
			}
			return layout.Dimensions{Size: image.Pt(w, rowH)}
		})
		btn := gtx.Dp(20)
		g.Constraints = layout.Exact(image.Pt(w-2*pad, rowH))
		oo := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		items := []item{
			{w: func(gtx layout.Context) layout.Dimensions { return drawIcon(gtx, icClock, gtx.Dp(12), quiet, 0) }},
			{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
				col := quiet
				if hovered {
					col = th.Fg
				}
				return label(gtx, th, th.UIFont, th.Sp(theme.Small), col, t.Title())
			}},
		}
		if hovered {
			items = append(items,
				item{right: true, w: func(gtx layout.Context) layout.Dimensions {
					return iconButton(gtx, th, &r.start, icPlay, btn, gtx.Dp(12), true)
				}},
				item{w: func(gtx layout.Context) layout.Dimensions {
					return iconButton(gtx, th, &r.drop, icX, btn, gtx.Dp(14), true)
				}})
		}
		hrow(g, rowH, gtx.Dp(6), items...)
		oo.Pop()
		o.Pop()
	}
}

// queueEvents turns clicks on the queued tasks' buttons into events and
// forgets the rows of tasks no longer queued.
func (s *Sidebar) queueEvents(gtx layout.Context, st *model.State) {
	for id, r := range s.taskRows {
		if !queuedTask(st, id) {
			delete(s.taskRows, id)
			continue
		}
		for r.start.Clicked(gtx) {
			s.events = append(s.events, DropTask{ID: id, Start: true})
		}
		for r.drop.Clicked(gtx) {
			s.events = append(s.events, DropTask{ID: id})
		}
		for {
			if _, ok := r.hover.Update(gtx); !ok {
				break
			}
		}
	}
}

func queuedTask(st *model.State, id string) bool {
	for _, t := range st.Tasks {
		if t.ID == id {
			return true
		}
	}
	return false
}
