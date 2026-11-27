package chatin

import (
	"reflect"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/pointer"

	"github.com/quanticstudios/pitwall/internal/ui/theme"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

func queue(ids ...string) []session.QueuedInput {
	out := make([]session.QueuedInput, len(ids))
	for i, id := range ids {
		out[i] = session.QueuedInput{ID: id, Text: "text " + id}
	}
	return out
}

func ids(qs []session.QueuedInput) []string {
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = q.ID
	}
	return out
}

// Reorder follows a move made here only while it lists the same rows;
// MoveTo finds the index the chat's own queue needs, past rows hidden here.
func TestQueueReorder(t *testing.T) {
	qs := queue("a", "b", "c")
	if got := ids(Reorder(qs, []string{"b", "a", "c"})); !reflect.DeepEqual(got, []string{"b", "a", "c"}) {
		t.Errorf("reorder %v", got)
	}
	for _, order := range [][]string{nil, {"a", "b"}, {"a", "b", "x"}} {
		if got := ids(Reorder(qs, order)); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
			t.Errorf("order %v: %v, want the chat's", order, got)
		}
	}
	all := queue("a", "gone", "b", "c")
	shown := queue("a", "b", "c")
	for _, c := range []struct {
		i, dir, want int
		ok           bool
	}{
		{1, -1, 0, true}, // b up: before a
		{0, 1, 2, true},  // a down: where b is in the whole queue
		{2, 1, 0, false}, // the last has no one below
		{0, -1, 0, false},
	} {
		got, ok := MoveTo(all, shown, c.i, c.dir)
		if got != c.want || ok != c.ok {
			t.Errorf("move %d by %d: %d %v, want %d %v", c.i, c.dir, got, ok, c.want, c.ok)
		}
	}
}

// The strip: past three rows it starts folded; Move shows at once and asks
// the chat; Send now sends "now", or "steer" to an older daemon; a failed
// row's Retry sends it now; Resume lets a held queue go.
func TestQueueStrip(t *testing.T) {
	h := newHarness(t)
	h.snap.SessionState = session.StateRunning
	h.snap.QueuedInputs = queue("a", "b", "c", "d")
	h.frame()
	if h.c.q.open {
		t.Fatal("four rows start open")
	}
	h.c.click("qfold").Click()
	h.frame()
	if !h.c.q.open {
		t.Fatal("the fold did not open")
	}
	h.c.click("qdown:a").Click()
	m := h.frame()
	if len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "move", ID: "a", Index: 1}) {
		t.Fatalf("move down sent %+v", m)
	}
	if got := ids(h.c.shownQueue(&h.snap, h.now)); !reflect.DeepEqual(got, []string{"b", "a", "c", "d"}) {
		t.Fatalf("shown %v: want the move before the chat has it", got)
	}
	h.c.click("qnow:c").Click()
	if m := h.frame(); len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "now", ID: "c"}) {
		t.Fatalf("send now sent %+v", m)
	}
	h.old = true // an older daemon: its Send now is a steer
	h.snap.QueuedInputs = queue("b", "a")
	h.c.click("qnow:b").Click()
	if m := h.frame(); len(m) != 1 || m[0].(proto.ChatQueue).Action != "steer" {
		t.Fatalf("old daemon send now sent %+v", m)
	}
	h.old = false
	h.snap.QueuedInputs = []session.QueuedInput{{ID: "f", Text: "failed", Error: "pane gone"}}
	h.snap.QueuePaused = true
	h.frame()
	h.c.click("qretry:f").Click()
	if m := h.frame(); len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "now", ID: "f"}) {
		t.Fatalf("retry sent %+v", m)
	}
	h.c.click("qresume").Click()
	if m := h.frame(); len(m) != 1 || m[0].(proto.ChatQueue).Action != "resume" {
		t.Fatalf("resume sent %+v", m)
	}
	h.snap.QueuedInputs = nil
	h.frame()
	if h.c.q.open || len(h.c.q.gone) != 0 {
		t.Fatalf("an empty queue kept state: open %v gone %v", h.c.q.open, h.c.q.gone)
	}
}

// A drag lands on the nearest slot, kept in the list; the rows it passes
// step aside; the drop asks the chat to move the row to where it fell,
// past rows hidden here, and shows the new order at once.
func TestQueueDrag(t *testing.T) {
	for _, c := range []struct {
		from int
		dy   float32
		want int
	}{
		{0, 14, 0}, {0, 16, 1}, {0, 61, 2}, {0, 500, 2}, {2, -40, 1}, {2, -500, 0},
	} {
		if got := dragTo(c.from, c.dy, 30, 3); got != c.want {
			t.Errorf("drag %d by %v: %d, want %d", c.from, c.dy, got, c.want)
		}
	}
	// a dragged down two rows: b and c step up, a follows the pointer but stays in the list.
	for k, want := range []int{60, -30, -30} {
		if got := dragShift(k, 0, 60, 30, 3); got != want {
			t.Errorf("row %d shifts %d, want %d", k, got, want)
		}
	}
	if got := dragShift(0, 0, 900, 30, 3); got != 60 {
		t.Errorf("held row past the end shifts %d, want 60", got)
	}
	h := newHarness(t)
	h.snap.SessionState = session.StateRunning
	h.snap.QueuedInputs = queue("a", "gone", "b", "c")
	h.c.q.gone = map[string]bool{"gone": true}
	h.frame()
	shown := h.c.shownQueue(&h.snap, h.now)
	h.c.q.drag, h.c.q.from, h.c.q.dy = "a", 0, 61
	h.c.drop(h.env(), shown, 30, h.now)
	if h.c.q.drag != "" {
		t.Fatal("the drop kept the row held")
	}
	m := h.c.Messages()
	if len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "move", ID: "a", Index: 3}) {
		t.Fatalf("drop sent %+v", m)
	}
	if got := ids(h.c.shownQueue(&h.snap, h.now)); !reflect.DeepEqual(got, []string{"b", "c", "a"}) {
		t.Fatalf("shown %v after the drop", got)
	}
	// A drop where it started sends nothing.
	h.c.q.order = nil
	h.c.q.drag, h.c.q.from, h.c.q.dy = "b", 1, 5
	h.c.drop(h.env(), shown, 30, h.now)
	if m := h.c.Messages(); len(m) != 0 {
		t.Fatalf("a drop in place sent %+v", m)
	}
}

// Dragging a row's grip with the pointer moves it: the harness's Column
// card sits 12 dp down, under the 28 dp Working row.
func TestQueueDragPointer(t *testing.T) {
	h := newHarness(t)
	h.snap.SessionState = session.StateRunning
	h.snap.QueuedInputs = queue("a", "b", "c")
	h.frame()
	h.frame() // the grips' filters register on the frame after they are drawn
	w := min(1000-2*int(theme.SpaceXL), 960)
	x := float32((1000-w)/2 + 18)
	y := float32(12 + 28 + 15) // the first row's middle
	for _, e := range []pointer.Event{
		{Kind: pointer.Move, Position: f32.Pt(x, y)},
		{Kind: pointer.Press, Position: f32.Pt(x, y), Buttons: pointer.ButtonPrimary},
		{Kind: pointer.Move, Position: f32.Pt(x, y+40), Buttons: pointer.ButtonPrimary},
	} {
		e.Source = pointer.Mouse
		h.r.Queue(e)
	}
	h.frame()
	if h.c.q.drag != "a" {
		t.Fatalf("held %q, want a", h.c.q.drag)
	}
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y+61), Buttons: pointer.ButtonPrimary},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(x, y+61)})
	m := h.frame()
	if len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "move", ID: "a", Index: 2}) {
		t.Fatalf("drag sent %+v", m)
	}
}
