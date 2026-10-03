package sidebar

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// flow: two ungrouped tabs, group g1 with a and b, collapsed group g2,
// empty expanded group g3, group g4 with c.
var dragFlow = []elem{
	{kind: 's', id: "u1", top: 4, bot: 60},
	{kind: 's', id: "u2", top: 62, bot: 118},
	{kind: 'g', id: "g1", group: "g1", top: 122, head: 135, bot: 175},
	{kind: 's', id: "a", group: "g1", top: 179, bot: 235},
	{kind: 's', id: "b", group: "g1", top: 237, bot: 293},
	{kind: 'g', id: "g2", group: "g2", top: 297, head: 310, bot: 350},
	{kind: 'g', id: "g3", group: "g3", top: 350, head: 363, bot: 403},
	{kind: 'g', id: "g4", group: "g4", top: 411, head: 424, bot: 464},
	{kind: 's', id: "c", group: "g4", top: 468, bot: 524},
}

func expandedExcept(collapsed ...string) func(string) bool {
	return func(id string) bool { return !slices.Contains(collapsed, id) }
}

func TestTabDrop(t *testing.T) {
	exp := expandedExcept("g2")
	for _, tc := range []struct {
		name  string
		y     int
		dwell string
		want  drop
	}{
		{"above everything", 0, "", drop{before: "u1", at: 0, ok: true}},
		{"lower half of u1", 40, "", drop{before: "u2", at: 1, ok: true}},
		{"gap goes to the nearer row", 61, "", drop{before: "u2", at: 1, ok: true}},
		{"after the last ungrouped, before g1", 100, "", drop{before: "g1", at: 2, ok: true}},
		{"top half of a header goes before it at the top level", 140, "", drop{before: "g1", at: 2, ok: true}},
		{"bottom half of a header starts its group", 170, "", drop{group: "g1", before: "a", at: 3, ok: true}},
		{"a header dwelled on takes it last", 140, "g1", drop{group: "g1", into: true, at: -1, ok: true}},
		{"between a and b", 220, "", drop{group: "g1", before: "b", at: 4, ok: true}},
		{"end of g1", 280, "", drop{group: "g1", at: 5, ok: true}},
		{"a collapsed group takes it at once", 320, "", drop{group: "g2", into: true, at: -1, ok: true}},
		{"between two groups at the top level", 370, "", drop{before: "g3", at: 6, ok: true}},
		{"empty expanded group", 395, "", drop{group: "g3", at: 7, ok: true}},
		{"top half of g4 goes before g4", 430, "", drop{before: "g4", at: 7, ok: true}},
		{"below everything", 900, "", drop{group: "g4", at: 9, ok: true}},
	} {
		if got := tabDrop(dragFlow, tc.y, exp, tc.dwell); got != tc.want {
			t.Errorf("%s (y=%d): %+v, want %+v", tc.name, tc.y, got, tc.want)
		}
	}
	if d := tabDrop(nil, 10, exp, ""); d != (drop{ok: true}) {
		t.Errorf("no other rows: %+v, want last ungrouped", d)
	}
}

func TestGroupDrop(t *testing.T) {
	for _, tc := range []struct {
		y    int
		want drop
	}{
		{20, drop{before: "u1", at: 0, ok: true}},  // above the first ungrouped tab
		{100, drop{before: "g1", at: 2, ok: true}}, // below the last one
		{200, drop{before: "g1", at: 2, ok: true}}, // top half of g1's block
		{280, drop{before: "g2", at: 5, ok: true}}, // bottom half: after g1
		{340, drop{before: "g3", at: 6, ok: true}},
		{500, drop{at: 9, ok: true}}, // last
	} {
		if got := groupDrop(dragFlow, tc.y); got != tc.want {
			t.Errorf("y=%d: %+v, want %+v", tc.y, got, tc.want)
		}
	}
}

// dragHarness drives a Sidebar through Gio's router at 1px per dp with a
// clock the test moves.
type dragHarness struct {
	t   *testing.T
	s   Sidebar
	st  model.State
	r   input.Router
	ops op.Ops
	now time.Time
}

func newDragHarness(t *testing.T) *dragHarness {
	h := &dragHarness{t: t, now: time.Unix(1e9, 0)}
	h.st = model.State{
		Projects: []model.Project{{ID: "g1", Name: "one"}, {ID: "g2", Name: "two"}},
		Workspaces: []model.Workspace{
			{ID: "u1", Name: "u1"}, {ID: "u2", Name: "u2"},
			{ID: "a", Name: "a", ProjectID: "g1"}, {ID: "b", Name: "b", ProjectID: "g2"},
		},
	}
	h.frame()
	h.s.expanded["g1"] = true // g2 stays collapsed
	h.frame()
	return h
}

func (h *dragHarness) frame() []Event {
	h.ops.Reset()
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(288, 800)), Now: h.now}
	_, evs := h.s.Layout(gtx, theme.Dark(), &h.st, "", "u1")
	h.r.Frame(&h.ops)
	return evs
}

// at is the viewport point inside element id, dy below its own top.
func (h *dragHarness) at(id string, dy int) f32.Point {
	for _, e := range h.s.elems {
		if e.id == id {
			top := e.top
			if e.kind == 'g' {
				top = e.head
			}
			return f32.Pt(100, float32(top+dy+56)) // below the 56dp header
		}
	}
	h.t.Fatalf("no element %s", id)
	return f32.Point{}
}

func (h *dragHarness) pointer(kind pointer.Kind, p f32.Point) []Event {
	if kind == pointer.Drag {
		kind = pointer.Move // the router turns a move with a button down into a drag
	}
	ev := pointer.Event{Kind: kind, Position: p, Source: pointer.Mouse, Time: time.Duration(h.now.UnixNano())}
	if kind != pointer.Release {
		ev.Buttons = pointer.ButtonPrimary
	}
	h.r.Queue(ev)
	return h.frame()
}

// TestClickIsNoDrag: a press and release without 4dp of movement selects
// the tab and never starts a drag; 4dp of movement does.
func TestClickIsNoDrag(t *testing.T) {
	h := newDragHarness(t)
	p := h.at("u2", 20)
	h.pointer(pointer.Press, p)
	h.pointer(pointer.Drag, p.Add(f32.Pt(1, 2)))
	if h.s.Dragging() {
		t.Fatal("2dp of movement started a drag")
	}
	evs := h.pointer(pointer.Release, p.Add(f32.Pt(1, 2)))
	evs = append(evs, h.frame()...)
	if !slices.Contains(evs, Event(SelectWorkspace{WorkspaceID: "u2"})) {
		t.Fatalf("click did not select: %v", evs)
	}
	h.pointer(pointer.Press, p)
	h.pointer(pointer.Drag, p.Add(f32.Pt(0, 6)))
	if !h.s.Dragging() || h.s.drag.id != "u2" {
		t.Fatalf("6dp of movement: dragging %v %q", h.s.Dragging(), h.s.drag.id)
	}
	h.s.CancelDrag()
	h.pointer(pointer.Release, p)
}

// TestDragDwellAndDrop: over an expanded group's header the drop goes
// into the group only after the dwell; a collapsed group takes it at
// once; release sends MoveSession, and the release is no click.
func TestDragDwellAndDrop(t *testing.T) {
	h := newDragHarness(t)
	p := h.at("a", 20) // below g1, so g1 stays put in the flow
	h.pointer(pointer.Press, p)
	h.pointer(pointer.Drag, p.Add(f32.Pt(0, -10)))
	h.pointer(pointer.Drag, h.at("g1", 30))
	if d := h.s.drag.target; d.into || d.group != "g1" || d.before != "" {
		t.Fatalf("before the dwell: %+v", d)
	}
	h.now = h.now.Add(dwellDur)
	h.frame()
	if d := h.s.drag.target; !d.into || d.group != "g1" {
		t.Fatalf("after the dwell: %+v", d)
	}
	h.pointer(pointer.Drag, h.at("g2", 20))
	if d := h.s.drag.target; !d.into || d.group != "g2" {
		t.Fatalf("over a collapsed group: %+v", d)
	}
	evs := h.pointer(pointer.Release, h.at("g2", 20))
	if !slices.Equal(evs, []Event{MoveSession{WorkspaceID: "a", GroupID: "g2"}}) {
		t.Fatalf("drop sent %v", evs)
	}
	if !h.s.isExpanded("g2") {
		t.Fatal("the group taking the drop did not open")
	}
}

// TestDragOpensGap: the rows after the drop point head down by one row,
// the ones between the origin and it up, and the drag ends with the
// sidebar idle again.
func TestDragOpensGap(t *testing.T) {
	h := newDragHarness(t)
	p := h.at("u1", 20)
	h.pointer(pointer.Press, p)
	h.pointer(pointer.Drag, p.Add(f32.Pt(0, 10)))
	row := float32(rowHeight(layout.Context{Metric: unit.Metric{PxPerDp: 1}}) + 2)
	// The lower half of a, once u1 is lifted out above it: after a, the
	// end of g1.
	h.pointer(pointer.Drag, h.at("a", 40).Sub(f32.Pt(0, row)))
	h.now = h.now.Add(slideDur)
	h.frame()
	off := map[string]float32{}
	for _, e := range h.s.elems {
		off[e.id] = h.s.drawnOff(e)
	}
	if off["u2"] != -row || off["a"] != -row || off["g2"] != 0 {
		t.Fatalf("offsets %v, want u2 and a up a row, g2 back where it was", off)
	}
	h.s.CancelDrag()
	h.pointer(pointer.Release, p)
	h.now = h.now.Add(time.Second)
	h.frame()
	if len(h.s.slides) != 0 || h.s.drag.active {
		t.Fatalf("still moving after the drag: %v", h.s.slides)
	}
}

// TestDragGroupToTop: the user's case. A group dropped above the first
// ungrouped tab sends MoveGroup before that tab, and an interleaved
// session Order lays out in that order.
func TestDragGroupToTop(t *testing.T) {
	h := newDragHarness(t)
	p := h.at("g1", 10)
	h.pointer(pointer.Press, p)
	h.pointer(pointer.Drag, p.Add(f32.Pt(0, -10)))
	h.pointer(pointer.Drag, h.at("u1", 5))
	evs := h.pointer(pointer.Release, h.at("u1", 5))
	if !slices.Equal(evs, []Event{MoveGroup{GroupID: "g1", Before: "u1"}}) {
		t.Fatalf("drop sent %v", evs)
	}
	h.now = h.now.Add(time.Second)
	h.st.Sessions = []model.Session{{Order: []string{"g1", "u1", "g2", "u2"}}}
	h.frame()
	h.frame()
	var got []string
	for _, e := range h.s.elems {
		got = append(got, e.key())
	}
	if want := []string{"gg1", "sa", "su1", "gg2", "su2"}; !slices.Equal(got, want) {
		t.Fatalf("laid out %v, want %v", got, want)
	}
	if got := h.s.order(newView(layout.Context{}, theme.Dark(), &h.st, "", "")); !slices.Equal(got, []string{"a", "u1", "u2"}) {
		t.Fatalf("order %v", got)
	}
}
