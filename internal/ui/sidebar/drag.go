package sidebar

import (
	"fmt"
	"image"
	"slices"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// MoveSession puts a session in GroupID ("" ungrouped) before the session
// Before ("" last); MoveGroup puts a group before the group Before.
type (
	MoveSession struct{ WorkspaceID, GroupID, Before string }
	MoveGroup   struct{ GroupID, Before string }
)

// dropRow is a session (with its tab rows) or a group header as drawn this
// frame, in tree viewport pixels.
type dropRow struct {
	kind     byte // 's' session, 'g' group header
	id       string
	group    string // a session's group, a header's own id
	top, bot int
}

// drop is where a drag released at the current position lands.
type drop struct {
	group, before string
	into          bool // onto a group header: last in that group
	line          int  // y of the indicator line when !into
	ok            bool
}

// dragState is a press on a session row or group header, a drag once it
// has moved 4dp.
type dragState struct {
	kind       byte // 0 none, 's', 'g'
	id         string
	start, pos f32.Point
	active     bool
}

// Dragging reports whether a session or group is being dragged.
func (s *Sidebar) Dragging() bool { return s.drag.active }

// CancelDrag drops the drag without moving anything (Escape).
func (s *Sidebar) CancelDrag() { s.drag = dragState{} }

func rowAt(rows []dropRow, y int) int {
	if len(rows) == 0 {
		return -1
	}
	for i, r := range rows {
		if y < r.bot {
			if i > 0 && y < r.top && y-rows[i-1].bot < r.top-y {
				return i - 1 // in a gap, nearer the row above
			}
			return i
		}
	}
	return len(rows) - 1
}

// sessionDrop is where a session dragged to y lands: onto a group header
// it goes last into that group, else into the gap nearest y, in the group
// of the row beside it.
func sessionDrop(rows []dropRow, y int) drop {
	i := rowAt(rows, y)
	if i < 0 {
		return drop{}
	}
	r := rows[i]
	if r.kind == 'g' {
		return drop{group: r.id, into: true, ok: true}
	}
	if y < (r.top+r.bot)/2 {
		return drop{group: r.group, before: r.id, line: r.top, ok: true}
	}
	d := drop{group: r.group, line: r.bot, ok: true}
	if i+1 < len(rows) && rows[i+1].kind == 's' && rows[i+1].group == r.group {
		d.before = rows[i+1].id
	}
	return d
}

// groupDrop is where a group dragged to y lands: before the group whose
// block (header and sessions) y is in the top half of, else after it.
// Above every group it goes first.
func groupDrop(rows []dropRow, y int) drop {
	type block struct {
		id       string
		top, bot int
	}
	var bs []block
	for _, r := range rows {
		switch {
		case r.kind == 'g':
			bs = append(bs, block{r.id, r.top, r.bot})
		case len(bs) > 0 && r.group == bs[len(bs)-1].id:
			bs[len(bs)-1].bot = r.bot
		}
	}
	if len(bs) == 0 {
		return drop{}
	}
	for i, b := range bs {
		if y < (b.top+b.bot)/2 {
			return drop{before: b.id, line: b.top, ok: true}
		}
		if y < b.bot || i == len(bs)-1 {
			d := drop{line: b.bot, ok: true}
			if i+1 < len(bs) {
				d.before = bs[i+1].id
			}
			return d
		}
	}
	return drop{}
}

// dragEvents runs the drag gesture from the pointer events over the tree
// viewport. A press only arms it; 4dp of movement starts it, so a click
// still clicks.
func (s *Sidebar) dragEvents(gtx layout.Context, v *view) {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.dragTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Press:
			s.drag = dragState{}
			if e.Buttons != pointer.ButtonPrimary || s.Editing() {
				continue
			}
			if i := rowAt(s.drops, int(e.Position.Y)); i >= 0 {
				r := s.drops[i]
				if int(e.Position.Y) >= r.top && int(e.Position.Y) < r.top+gtx.Dp(56) {
					s.drag = dragState{kind: r.kind, id: r.id, start: e.Position, pos: e.Position}
				}
			}
		case pointer.Drag:
			if s.drag.kind == 0 {
				continue
			}
			s.drag.pos = e.Position
			if d := e.Position.Sub(s.drag.start); !s.drag.active && d.X*d.X+d.Y*d.Y >= float32(gtx.Dp(4)*gtx.Dp(4)) {
				s.drag.active = true
				s.closeMenus()
			}
		case pointer.Release:
			if s.drag.active {
				s.dropAt(v, int(e.Position.Y))
			}
			s.drag = dragState{}
		case pointer.Cancel:
			s.drag = dragState{}
		}
	}
}

// dropAt sends the moves for a drag released at y.
func (s *Sidebar) dropAt(v *view, y int) {
	if s.drag.kind == 'g' {
		if d := groupDrop(s.drops, y); d.ok && d.before != s.drag.id {
			s.events = append(s.events, MoveGroup{GroupID: s.drag.id, Before: d.before})
		}
		return
	}
	d := sessionDrop(s.drops, y)
	if !d.ok {
		return
	}
	ids := []string{s.drag.id}
	if s.selected[s.drag.id] && len(s.selected) > 1 {
		ids = nil
		for _, id := range s.order(v) {
			if s.selected[id] {
				ids = append(ids, id)
			}
		}
	}
	// Moving before one of the moved sessions means before the first
	// session after them that stays.
	if slices.Contains(ids, d.before) {
		i := slices.IndexFunc(s.drops, func(r dropRow) bool { return r.kind == 's' && r.id == d.before })
		d.before = ""
		for _, r := range s.drops[i+1:] {
			if r.kind != 's' || r.group != d.group {
				break
			}
			if !slices.Contains(ids, r.id) {
				d.before = r.id
				break
			}
		}
	}
	for _, id := range ids {
		s.events = append(s.events, MoveSession{WorkspaceID: id, GroupID: d.group, Before: d.before})
	}
	if d.into {
		s.expanded[d.group] = true
	}
}

// dragOverlay registers the gesture over the tree viewport and, during a
// drag, draws the drop indicator, scrolls near the edges, and returns
// whether it needs another frame.
func (s *Sidebar) dragOverlay(gtx layout.Context, v *view, size image.Point) bool {
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	pass := pointer.PassOp{}.Push(gtx.Ops)
	event.Op(gtx.Ops, &s.dragTag)
	pass.Pop()
	area.Pop()
	if !s.drag.active {
		return false
	}
	th := v.th
	y := int(s.drag.pos.Y)
	if s.drag.kind == 'g' {
		if d := groupDrop(s.drops, y); d.ok {
			s.line(gtx, th, d.line, size.X)
		}
	} else if d := sessionDrop(s.drops, y); d.ok {
		if d.into {
			for _, r := range s.drops {
				if r.kind == 'g' && r.id == d.group {
					rect := image.Rect(gtx.Dp(2), r.top, size.X-gtx.Dp(2), r.bot)
					paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Primary, 0.6), clip.Stroke{Path: clip.UniformRRect(rect, gtx.Dp(8)).Path(gtx.Ops), Width: float32(gtx.Dp(1.5))}.Op())
				}
			}
		} else {
			s.line(gtx, th, d.line, size.X)
		}
	}
	// The dragged entry's name follows the pointer.
	name := s.drag.id
	for _, p := range v.st.Projects {
		if p.ID == name {
			name = p.Name
		}
	}
	for _, w := range v.st.Workspaces {
		if w.ID == name {
			name, _ = displayName(w)
		}
	}
	if n := len(s.selected); s.drag.kind == 's' && s.selected[s.drag.id] && n > 1 {
		name = fmt.Sprintf("%d sessions", n)
	}
	m := op.Record(gtx.Ops)
	lg := gtx
	lg.Constraints = layout.Constraints{Max: image.Pt(size.X-gtx.Dp(40), gtx.Dp(24))}
	d := label(lg, th, semibold(th.UIFont), 12, th.Fg, name)
	call := m.Stop()
	pad := gtx.Dp(8)
	box := image.Rect(0, 0, d.Size.X+2*pad, d.Size.Y+gtx.Dp(8))
	at := image.Pt(int(s.drag.pos.X)+gtx.Dp(12), y-box.Dy()/2)
	at.X = min(at.X, size.X-box.Dx()-gtx.Dp(4))
	o := op.Offset(at).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceElevated, th.Primary, 0.5), clip.UniformRRect(box, box.Dy()/2).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(box.Inset(1), box.Dy()/2-1).Op(gtx.Ops))
	co := op.Offset(image.Pt(pad, gtx.Dp(4))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	co.Pop()
	o.Pop()

	edge := gtx.Dp(32)
	switch {
	case y < edge:
		s.list.Position.Offset -= gtx.Dp(6)
	case y > size.Y-edge:
		s.list.Position.Offset += gtx.Dp(6)
	}
	return true
}

func (s *Sidebar) line(gtx layout.Context, th *theme.Theme, y, w int) {
	t := gtx.Dp(2)
	paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(image.Rect(gtx.Dp(4), y-t/2, w-gtx.Dp(4), y-t/2+t), t/2).Op(gtx.Ops))
}
