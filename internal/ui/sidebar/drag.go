package sidebar

import (
	"fmt"
	"image"
	"slices"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// MoveSession puts a tab in GroupID before the tab Before of that group
// ("" last); with GroupID "" it goes to the top level, before the group or
// ungrouped tab Before. MoveGroup puts a group before the group or
// ungrouped tab Before.
type (
	MoveSession struct{ WorkspaceID, GroupID, Before string }
	MoveGroup   struct{ GroupID, Before string }
)

const (
	slideDur = anim.Slide // rows sliding apart, back, or into place
	liftDur  = anim.Menu  // the dragged row rising

	dwellDur = 300 * time.Millisecond // over a group header before it takes the drop
	landWait = 400 * time.Millisecond // how long a drop waits for the state to show it
)

// elem is a tab row, an agent's sub-row or a group header as laid out
// this frame, in content pixels (0 is the top of the scrolled list). A
// header's top includes the separator above it. A tab's sub-rows follow
// it and move with it.
type elem struct {
	kind     byte   // 's' tab, 'a' agent sub-row, 'g' group header
	id       string // a sub-row's pane
	parent   string // a sub-row's tab
	group    string // a tab's group, a header's own id
	x        int    // a grouped tab's indent
	top, bot int
	head     int // a header's own top, below its separator
}

func (e elem) key() string { return string(e.kind) + e.id }
func (e elem) mid() int    { return (e.top + e.bot) / 2 }

// drop is where a drag released now would land: in group before the tab
// before or, with group "", at the top level before the group or ungrouped
// tab before ("" last either way).
type drop struct {
	group, before string
	into          bool // onto a group header: last in that group, no gap
	at            int  // index in the flow the gap opens before
	ok            bool
}

// dragState is a press on a tab row or group header, a drag once it has
// moved 4dp, and after release the landing that waits for the state.
type dragState struct {
	kind       byte // 0 none, 's', 'g'
	id         string
	ids        []string  // the tabs it carries: the selection when id is in it
	start, pos f32.Point // viewport px
	grab       int       // press y minus the element's top
	active     bool
	since      time.Time // when it became a drag
	hover      string    // the group header under the pointer
	hoverAt    time.Time
	target     drop // as of the last frame
	released   bool // dropped, gliding into the gap until the state moves
	relAt      time.Time
	fromY, toY float32 // the glide, content px
}

// slide eases an element's offset from its drawn position to a target.
type slide struct {
	from, to float32
	at       time.Time
}

func (sl slide) value(now time.Time) float32 {
	v, _ := anim.Value{From: sl.from, To: sl.to, Start: sl.at, Dur: slideDur}.Eval(now)
	return v
}

func (sl slide) done(now time.Time) bool {
	_, running := anim.Progress(sl.at, now, slideDur)
	return !running
}

// Dragging reports whether a tab or group is being dragged.
func (s *Sidebar) Dragging() bool { return s.drag.active && !s.drag.released }

// CancelDrag drops the drag without moving anything (Escape): the rows
// slide back and the dragged one returns to its place.
func (s *Sidebar) CancelDrag() {
	if s.Dragging() {
		s.land(s.ghostY(time.Time{}))
	}
	s.drag = dragState{}
}

// land starts the dragged elements' slide from y into wherever they are
// laid out next frame.
func (s *Sidebar) land(y float32) {
	if s.landing == nil {
		s.landing = map[string]float32{}
	}
	if s.drag.kind == 'g' {
		s.landing["g"+s.drag.id] = y
		return
	}
	for _, id := range s.drag.ids {
		s.landing["s"+id] = y
		top := 0
		for _, e := range s.elems {
			switch {
			case e.kind == 's' && e.id == id:
				top = e.top
			case e.kind == 'a' && e.parent == id:
				s.landing[e.key()] = y + float32(e.top-top)
			}
		}
	}
}

// rowAt is the element y is over, the nearer one in a gap, or -1.
func rowAt(rows []elem, y int) int {
	if len(rows) == 0 {
		return -1
	}
	for i, r := range rows {
		if y < r.bot {
			if i > 0 && y < r.top && y-rows[i-1].bot < r.top-y {
				return i - 1
			}
			return i
		}
	}
	return len(rows) - 1
}

// tabDrop is where tabs dragged to y in flow (the elements without the
// dragged ones, closed up) land. Over a tab the gap goes to the side of
// its middle the pointer is on. Over a collapsed group's header, or one
// dwelled on, the tabs go last into that group. Over the top half of an
// expanded header they go to the top level before that group, over the
// bottom half they start that group; the end of a group is the bottom
// half of its last tab. A tab and its sub-rows are one block: the gap
// opens above or below the whole of it.
func tabDrop(flow []elem, y int, expanded func(string) bool, dwell string) drop {
	i := rowAt(flow, y)
	if i < 0 {
		return drop{ok: true}
	}
	e := flow[i]
	if e.kind == 'g' {
		if !expanded(e.id) || dwell == e.id {
			return drop{group: e.id, into: true, at: -1, ok: true}
		}
		if y < e.mid() {
			return drop{before: e.id, at: i, ok: true}
		}
		d := drop{group: e.id, at: i + 1, ok: true}
		if i+1 < len(flow) && flow[i+1].kind == 's' && flow[i+1].group == e.id {
			d.before = flow[i+1].id
		}
		return d
	}
	for e.kind == 'a' && i > 0 {
		i--
		e = flow[i]
	}
	end := i
	for end+1 < len(flow) && flow[end+1].kind == 'a' {
		end++
	}
	if y < (e.top+flow[end].bot)/2 {
		return drop{group: e.group, before: e.id, at: i, ok: true}
	}
	d := drop{group: e.group, at: end + 1, ok: true}
	if end+1 < len(flow) {
		// The next element in the same group, or at the top level the
		// next top-level item: a header or an ungrouped tab.
		if n := flow[end+1]; (n.kind == 's' && n.group == e.group) || (e.group == "" && n.kind == 'g') {
			d.before = n.id
		}
	}
	return d
}

// groupDrop is where a group dragged to y in flow lands: before the
// top-level block (a group's header and tabs, or one ungrouped tab) y is
// in the top half of, else after it.
func groupDrop(flow []elem, y int) drop {
	type block struct {
		id       string
		at       int
		top, bot int
	}
	var bs []block
	for i, r := range flow {
		switch {
		case r.kind == 'a' && len(bs) > 0:
			bs[len(bs)-1].bot = r.bot
		case r.kind == 'g' || r.group == "":
			bs = append(bs, block{r.id, i, r.top, r.bot})
		case len(bs) > 0 && r.group == bs[len(bs)-1].id:
			bs[len(bs)-1].bot = r.bot
		}
	}
	for i, b := range bs {
		if y < (b.top+b.bot)/2 {
			return drop{before: b.id, at: b.at, ok: true}
		}
		if y < b.bot && i+1 < len(bs) {
			return drop{before: bs[i+1].id, at: bs[i+1].at, ok: true}
		}
	}
	return drop{at: len(flow), ok: true}
}

// carried reports whether e moves with the drag.
func (s *Sidebar) carried(e elem) bool {
	switch s.drag.kind {
	case 's':
		return e.kind == 's' && slices.Contains(s.drag.ids, e.id) || e.kind == 'a' && slices.Contains(s.drag.ids, e.parent)
	case 'g':
		return e.group == s.drag.id
	}
	return false
}

// flow closes up the carried elements: the rest with their tops moved up
// by what was carried above them, and the height carried in all. Each
// element takes the space down to the next one with it.
func (s *Sidebar) flow(elems []elem, total int) (flow []elem, idx []int, carried int) {
	idx = make([]int, len(elems))
	for i, e := range elems {
		next := total
		if i+1 < len(elems) {
			next = elems[i+1].top
		}
		if s.carried(e) {
			carried += next - e.top
			idx[i] = -1
			continue
		}
		idx[i] = len(flow)
		e.top, e.bot, e.head = e.top-carried, e.bot-carried, e.head-carried
		flow = append(flow, e)
	}
	return flow, idx, carried
}

// gapSize is the height the gap opens to: the dragged tab's row with its
// sub-rows, and its spacing, or the dragged group's whole block.
func (s *Sidebar) gapSize(gtx layout.Context, elems []elem, carried int) int {
	if s.drag.kind == 'g' {
		return carried
	}
	top, bot := 0, 0
	for _, e := range elems {
		switch {
		case e.kind == 's' && e.id == s.drag.id:
			top, bot = e.top, e.bot
		case e.kind == 'a' && e.parent == s.drag.id:
			bot = e.bot
		}
	}
	return bot - top + gtx.Dp(2)
}

// dragFrame updates the drop target from the pointer and returns the
// offsets the elements head for, and when the dwell timer next needs a
// frame.
func (s *Sidebar) dragFrame(gtx layout.Context, elems []elem, total int) (offsets []float32, wake time.Time) {
	offsets = make([]float32, len(elems))
	if !s.drag.active {
		return offsets, wake
	}
	flow, idx, carried := s.flow(elems, total)
	if !s.drag.released {
		y := int(s.drag.pos.Y) + s.scroll
		hover := ""
		if s.drag.kind == 's' {
			if i := rowAt(flow, y); i >= 0 && flow[i].kind == 'g' && y >= flow[i].top && y < flow[i].bot {
				hover = flow[i].id
			}
		}
		if hover != s.drag.hover {
			s.drag.hover, s.drag.hoverAt = hover, gtx.Now
		}
		dwell := ""
		if hover != "" {
			if at := s.drag.hoverAt.Add(dwellDur); gtx.Now.Before(at) {
				wake = at
			} else {
				dwell = hover
			}
		}
		if s.drag.kind == 'g' {
			s.drag.target = groupDrop(flow, y)
		} else {
			s.drag.target = tabDrop(flow, y, s.isExpanded, dwell)
		}
	}
	d := s.drag.target
	gap := s.gapSize(gtx, elems, carried)
	for i, e := range elems {
		if idx[i] < 0 {
			continue
		}
		off := flow[idx[i]].top - e.top
		if d.ok && !d.into && idx[i] >= d.at {
			off += gap
		}
		offsets[i] = float32(off)
	}
	// Where the gap is, for the glide after release.
	switch {
	case !d.ok:
	case d.into:
		for _, e := range flow {
			if e.kind == 'g' && e.id == d.group {
				s.drag.toY = float32(e.head)
			}
		}
	case d.at < len(flow):
		s.drag.toY = float32(flow[d.at].top)
	default:
		end := 0
		if len(flow) > 0 {
			end = flow[len(flow)-1].bot + gtx.Dp(2)
		}
		s.drag.toY = float32(end)
	}
	return offsets, wake
}

// ghostY is the dragged element's top in content px: under the pointer
// while dragging, gliding to the gap after release.
func (s *Sidebar) ghostY(now time.Time) float32 {
	if s.drag.released {
		return slide{from: s.drag.fromY, to: s.drag.toY, at: s.drag.relAt}.value(now)
	}
	return s.drag.pos.Y + float32(s.scroll-s.drag.grab)
}

// animate moves each element's slide toward its offset and returns the
// drawn offsets and whether any still moves. An element whose laid-out
// top changed since last frame starts from where it was drawn, so a
// reorder from the state slides instead of jumping. Ending a landing hands
// the dragged elements over from the ghost.
func (s *Sidebar) animate(gtx layout.Context, elems []elem, total int) ([]float32, bool) {
	now := gtx.Now
	if s.slides == nil {
		s.slides, s.prevTop = map[string]*slide{}, map[string]int{}
	}
	changed := len(elems) != len(s.prevTop)
	for _, e := range elems {
		if t, ok := s.prevTop[e.key()]; !ok || t != e.top {
			changed = true
		}
	}
	if s.drag.released && (changed || now.Sub(s.drag.relAt) >= landWait) {
		s.land(s.ghostY(now))
		s.drag = dragState{}
	}
	targets, wake := s.dragFrame(gtx, elems, total)
	if !wake.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: wake})
	}
	out := make([]float32, len(elems))
	moving := false
	prev := s.prevTop
	s.prevTop = make(map[string]int, len(elems))
	for i, e := range elems {
		k := e.key()
		s.prevTop[k] = e.top
		sl := s.slides[k]
		cur := float32(0)
		if sl != nil {
			cur = sl.value(now)
		}
		to := targets[i]
		if y, ok := s.landing[k]; ok {
			sl = &slide{from: y - float32(e.top), to: to, at: now}
			delete(s.landing, k)
		} else if t, ok := prev[k]; ok && t != e.top {
			sl = &slide{from: cur + float32(t-e.top), to: to, at: now}
		} else if (sl == nil && to != 0) || (sl != nil && sl.to != to) {
			sl = &slide{from: cur, to: to, at: now}
		}
		if sl == nil {
			continue
		}
		if sl.done(now) && sl.to == 0 {
			delete(s.slides, k)
			continue
		}
		s.slides[k] = sl
		out[i] = sl.value(now)
		moving = moving || !sl.done(now)
	}
	clear(s.landing)
	return out, moving
}

// dragEvents runs the gesture from the pointer events over the tree
// viewport. A press only arms it; 4dp of movement starts it, so a click
// still clicks. It reports whether a drag ended, whose release must not
// also click the row under it.
func (s *Sidebar) dragEvents(gtx layout.Context, v *view) bool {
	ended := false
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
			s.hover.dismiss() // a click, a menu or a drag starts
			if s.drag.released {
				continue // still landing
			}
			s.drag = dragState{}
			if e.Buttons != pointer.ButtonPrimary || s.Editing() || s.menuOpen() {
				continue
			}
			y := int(e.Position.Y) + s.scroll
			tab := 0 // the top of the last tab row, a sub-row's tab
			for _, el := range s.elems {
				top := el.top
				switch el.kind {
				case 'g':
					top = el.head
				case 's':
					tab = el.top
				}
				if y >= top && y < el.bot && int(e.Position.X) >= el.x {
					s.drag = dragState{kind: el.kind, id: el.id, start: e.Position, pos: e.Position, grab: y - el.top}
					if el.kind == 'a' { // a sub-row lifts its tab
						s.drag.kind, s.drag.id, s.drag.grab = 's', el.parent, y-tab
					}
				}
			}
		case pointer.Drag:
			if s.drag.kind == 0 || s.drag.released {
				continue
			}
			s.drag.pos = e.Position
			if d := e.Position.Sub(s.drag.start); !s.drag.active && d.X*d.X+d.Y*d.Y >= float32(gtx.Dp(4)*gtx.Dp(4)) {
				s.startDrag(gtx, v)
			}
		case pointer.Release:
			if s.drag.active && !s.drag.released {
				s.drag.pos = e.Position
				s.dropNow(gtx, v)
				ended = true
			} else if !s.drag.released {
				s.drag = dragState{}
			}
		case pointer.Cancel:
			if s.Dragging() {
				ended = true
			}
			s.CancelDrag()
		}
	}
	return ended
}

// menuOpen reports whether a menu or popover covers part of the tree.
func (s *Sidebar) menuOpen() bool {
	return s.menuWS != "" || s.groupMenu != "" || s.appearance != "" || s.detachedOpen
}

func (s *Sidebar) startDrag(gtx layout.Context, v *view) {
	s.drag.active, s.drag.since = true, gtx.Now
	s.closeMenus()
	if s.drag.kind != 's' {
		return
	}
	s.drag.ids = []string{s.drag.id}
	if s.selected[s.drag.id] && len(s.selected) > 1 {
		s.drag.ids = nil
		for _, id := range s.order(v) {
			if s.selected[id] {
				s.drag.ids = append(s.drag.ids, id)
			}
		}
	}
}

// dropNow sends the moves for the target shown last frame and starts the
// glide into the gap. A drop where the drag started just returns.
func (s *Sidebar) dropNow(gtx layout.Context, v *view) {
	d := s.drag.target
	from := s.ghostY(gtx.Now)
	if !d.ok || s.noop(v, d) {
		s.CancelDrag()
		return
	}
	if s.drag.kind == 'g' {
		s.events = append(s.events, MoveGroup{GroupID: s.drag.id, Before: d.before})
	} else {
		for _, id := range s.drag.ids {
			s.events = append(s.events, MoveSession{WorkspaceID: id, GroupID: d.group, Before: d.before})
		}
		if d.into {
			s.expanded[d.group] = true
		}
	}
	s.drag.released, s.drag.relAt, s.drag.fromY = true, gtx.Now, from
}

// noop reports whether d puts a single dragged tab or a group back where
// it is.
func (s *Sidebar) noop(v *view, d drop) bool {
	next := func(ids []string, id string) string {
		if i := slices.Index(ids, id); i >= 0 && i+1 < len(ids) {
			return ids[i+1]
		}
		return ""
	}
	if s.drag.kind == 'g' {
		return d.group == "" && (d.before == next(v.top, s.drag.id) || d.before == s.drag.id)
	}
	if len(s.drag.ids) != 1 || d.into {
		return false
	}
	g := v.groupOf(s.drag.id)
	if g == "" {
		return d.group == "" && d.before == next(v.top, s.drag.id)
	}
	var ids []string
	for _, w := range v.byProject[g] {
		ids = append(ids, w.ID)
	}
	return d.group == g && d.before == next(ids, s.drag.id)
}

// dragOverlay registers the gesture over the tree viewport and, during a
// drag, highlights a group taking the drop, draws the lifted row under the
// pointer, scrolls near the edges, and reports whether it needs another
// frame.
func (s *Sidebar) dragOverlay(gtx layout.Context, v *view, size image.Point) bool {
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	pass := pointer.PassOp{}.Push(gtx.Ops)
	event.Op(gtx.Ops, &s.dragTag)
	if s.Dragging() {
		pointer.CursorGrabbing.Add(gtx.Ops)
	}
	pass.Pop()
	area.Pop()
	if !s.drag.active {
		return false
	}
	th := v.th
	now := gtx.Now
	d := s.drag.target
	if d.ok && d.into {
		for _, e := range s.elems {
			if e.kind == 'g' && e.id == d.group {
				y := e.head - s.scroll + int(s.drawnOff(e))
				rect := image.Rect(gtx.Dp(4), y, size.X-gtx.Dp(4), y+gtx.Dp(40))
				paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Primary, 0.14), clip.UniformRRect(rect, gtx.Dp(8)).Op(gtx.Ops))
				paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Primary, 0.7), clip.Stroke{Path: clip.UniformRRect(rect, gtx.Dp(8)).Path(gtx.Ops), Width: float32(gtx.Dp(1.5))}.Op())
			}
		}
	}

	// The lifted row: scaled up a little over a soft shadow, rising over
	// liftDur and settling back while it glides into the gap.
	lift, _ := anim.Progress(s.drag.since, now, liftDur)
	if s.drag.released {
		down, _ := anim.Progress(s.drag.relAt, now, slideDur)
		lift = 1 - down
	}
	y := int(s.ghostY(now)) - s.scroll
	// A tab headed into a group takes the group's indent.
	gx := 0
	if s.drag.kind == 's' && d.ok && d.group != "" {
		gx = gtx.Dp(groupIndent)
	}
	var h int
	m := op.Record(gtx.Ops)
	gg := gtx
	gg.Constraints = layout.Exact(image.Pt(size.X, gtx.Dp(40)))
	if s.drag.kind == 'g' {
		for _, p := range v.st.Projects {
			if p.ID == s.drag.id {
				h = s.projectHeaderGhost(gg, v, p)
			}
		}
	} else {
		for _, ws := range v.st.Workspaces {
			if ws.ID == s.drag.id {
				gg.Constraints = layout.Exact(image.Pt(size.X-gx, rowHeight(gtx, th)))
				d, _ := s.workspaceRow(gg, v, ws, true, "")
				h = d.Size.Y
				o := op.Offset(image.Pt(0, h)).Push(gtx.Ops)
				gg.Constraints = layout.Constraints{Max: image.Pt(size.X-gx, 1<<16)}
				h += s.ghostSubs(gg, v, ws.ID)
				o.Pop()
			}
		}
	}
	row := m.Stop()
	if h > 0 {
		rect := image.Rect(0, 0, size.X-gx, h)
		r := gtx.Dp(8)
		o := op.Offset(image.Pt(gx, y)).Push(gtx.Ops)
		center := f32.Pt(float32(rect.Dx())/2, float32(h)/2)
		sc := op.Affine(f32.Affine2D{}.Scale(center, f32.Pt(1+0.025*lift, 1+0.025*lift))).Push(gtx.Ops)
		sh := paint.PushOpacity(gtx.Ops, lift)
		kit.Shadow(gtx, rect, r, kit.Raised)
		sh.Pop()
		paint.FillShape(gtx.Ops, theme.Mix(th.SurfaceSecondary, th.Fg, 0.12), clip.UniformRRect(rect, r).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
		row.Add(gtx.Ops)
		if n := s.badgeCount(v); n > 1 {
			s.badge(gtx, th, n, rect.Dx())
		}
		sc.Pop()
		o.Pop()
	}

	if s.drag.released {
		return true
	}
	edge := gtx.Dp(36)
	py := int(s.drag.pos.Y)
	switch {
	case py < edge:
		s.list.Position.Offset -= max(1, gtx.Dp(12)*(edge-py)/edge)
		return true
	case py > size.Y-edge:
		s.list.Position.Offset += max(1, gtx.Dp(12)*(py-size.Y+edge)/edge)
		return true
	}
	return now.Sub(s.drag.since) < liftDur
}

// badgeCount is the number on the lifted row: the tabs carried, or a
// group's tabs.
func (s *Sidebar) badgeCount(v *view) int {
	if s.drag.kind == 'g' {
		return len(v.byProject[s.drag.id])
	}
	return len(s.drag.ids)
}

// badge is a primary count pill at the lifted row's top right corner.
func (s *Sidebar) badge(gtx layout.Context, th *theme.Theme, n, w int) {
	m := op.Record(gtx.Ops)
	d := label(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), th.OnPrimary, fmt.Sprint(n))
	call := m.Stop()
	h := gtx.Dp(18)
	bw := max(h, d.Size.X+gtx.Dp(10))
	o := op.Offset(image.Pt(w-bw-gtx.Dp(2), -h/3)).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(image.Rect(0, 0, bw, h), h/2).Op(gtx.Ops))
	co := op.Offset(image.Pt((bw-d.Size.X)/2, (h-d.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	co.Pop()
	o.Pop()
}

// drawnOff is e's offset as drawn this frame.
func (s *Sidebar) drawnOff(e elem) float32 {
	if sl := s.slides[e.key()]; sl != nil {
		return sl.value(s.now)
	}
	return 0
}
