package chatin

import (
	"reflect"
	"testing"

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

// The strip: past two rows it starts folded; Move shows at once and asks
// the chat; Send now sends "now", or "steer" to an older daemon; a failed
// row's Retry sends it now; Resume lets a held queue go.
func TestQueueStrip(t *testing.T) {
	h := newHarness(t)
	h.snap.SessionState = session.StateRunning
	h.snap.QueuedInputs = queue("a", "b", "c")
	h.frame()
	if h.c.q.open {
		t.Fatal("three rows start open")
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
	if got := ids(h.c.shownQueue(&h.snap, h.now)); !reflect.DeepEqual(got, []string{"b", "a", "c"}) {
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
