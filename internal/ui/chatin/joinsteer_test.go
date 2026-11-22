package chatin

import (
	"reflect"
	"strings"
	"testing"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// sends is m's ChatSends, failing on anything else.
func sends(t *testing.T, m []any) []proto.ChatSend {
	t.Helper()
	var out []proto.ChatSend
	for _, v := range m {
		s, ok := v.(proto.ChatSend)
		if !ok {
			t.Fatalf("sent %T %+v", v, v)
		}
		out = append(out, s)
	}
	return out
}

func shownIDs(qs []session.QueuedInput) []string {
	var ids []string
	for _, q := range qs {
		ids = append(ids, q.ID)
	}
	return ids
}

// The default, follow_up queue: Enter queues mid-turn, and the queued
// message shows in the strip at once; Ctrl+Enter steers the draft with
// the queue's plain messages ahead of it, and on an empty box the queue
// alone; a refused steer brings the queue and the draft back.
func TestJoinSteerKeys(t *testing.T) {
	h := newHarness(t)
	h.opts = func(e *Env) { e.Join = true }
	h.snap = queueSnap(session.QueuedInput{ID: "a", Text: "one"}, session.QueuedInput{ID: "b", Text: "/compact"}, session.QueuedInput{ID: "c", Text: "two"})

	h.typ("later")
	m := sends(t, h.press(key.NameReturn, 0))
	if len(m) != 1 || m[0].How != "queue" || m[0].Join || m[0].Text != "later" {
		t.Fatalf("Enter mid-turn sent %+v", m)
	}
	// Before the chat lists it, the strip shows it, with no actions.
	if ids := shownIDs(h.c.stripQueue(&h.snap, h.now)); len(ids) != 4 || ids[3] != localID+"0" {
		t.Fatalf("strip %v: the queued message is not shown at once", ids)
	}
	h.snap.QueuedInputs = append(h.snap.QueuedInputs, session.QueuedInput{ID: "d", Text: "later"})
	if ids := shownIDs(h.c.stripQueue(&h.snap, h.now)); !reflect.DeepEqual(ids, []string{"a", "b", "c", "d"}) {
		t.Fatalf("strip %v once the chat lists it", ids)
	}

	h.typ("three")
	m = sends(t, h.press(key.NameReturn, key.ModShortcut))
	if len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatSend{Chat: "c1", Text: "three", How: "now", Join: true}) {
		t.Fatalf("Ctrl+Enter sent %+v", m)
	}
	if ids := shownIDs(h.c.shownQueue(&h.snap, h.now)); !reflect.DeepEqual(ids, []string{"b"}) {
		t.Fatalf("shown %v: the steer takes all but the command", ids)
	}
	h.c.Result(Result{Req: m[0], Message: "no running pane"}, h.now)
	if ids := shownIDs(h.c.shownQueue(&h.snap, h.now)); len(ids) != 4 || h.c.Text() != "three" {
		t.Fatalf("refused: shown %v, box %q", ids, h.c.Text())
	}
	h.c.SetText("")
	h.frame()

	m = sends(t, h.press(key.NameReturn, key.ModShortcut))
	if len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatSend{Chat: "c1", How: "now", Join: true}) {
		t.Fatalf("Ctrl+Enter on an empty box sent %+v", m)
	}
	h.c.Result(Result{Req: m[0]}, h.now)
	h.snap.QueuedInputs = []session.QueuedInput{{ID: "b", Text: "/compact"}}
	h.frame()
	if m := h.press(key.NameReturn, key.ModShortcut); len(m) != 0 || h.c.note != "Nothing queued to steer" {
		t.Fatalf("only a command queued: sent %+v, note %q", m, h.c.note)
	}

	// A command steers alone, the queue left as it is.
	h.typ("/compact")
	h.press(key.NameEscape, 0) // the slash menu
	m = sends(t, h.press(key.NameReturn, key.ModShortcut))
	if len(m) != 1 || m[0].How != "now" || m[0].Join {
		t.Fatalf("Ctrl+Enter on a command sent %+v", m)
	}

	// Idle, Ctrl+Enter sends with the queue first; Enter sends alone.
	h.snap.SessionState = session.StateReady
	h.snap.QueuedInputs = []session.QueuedInput{{ID: "e", Text: "held"}}
	h.typ("go")
	if m := sends(t, h.press(key.NameReturn, key.ModShortcut)); len(m) != 1 || m[0].How != "" || !m[0].Join {
		t.Fatalf("idle Ctrl+Enter sent %+v", m)
	}
	h.typ("plain")
	if m := sends(t, h.press(key.NameReturn, 0)); len(m) != 1 || m[0].How != "" || m[0].Join {
		t.Fatalf("idle Enter sent %+v", m)
	}
}

// An agent that takes nothing mid-turn: Ctrl+Enter queues with a note
// that says so, and never stops the turn; on an empty box it sends
// nothing. A daemon before proto.JoinLevel steers the draft alone.
func TestJoinSteerLimits(t *testing.T) {
	h := newHarness(t)
	join, steers := true, false
	h.opts = func(e *Env) { e.Join, e.Steers = join, steers }
	h.snap = queueSnap(session.QueuedInput{ID: "a", Text: "one"})
	h.typ("three")
	m := sends(t, h.press(key.NameReturn, key.ModShortcut))
	if len(m) != 1 || m[0].How != "queue" || m[0].Join || !strings.Contains(h.c.note, "goes when the turn ends") {
		t.Fatalf("Ctrl+Enter to an agent that can't steer: %+v, note %q", m, h.c.note)
	}
	if m := h.press(key.NameReturn, key.ModShortcut); len(m) != 0 || !strings.Contains(h.c.note, "the queue goes when the turn ends") {
		t.Fatalf("empty box: %+v, note %q", m, h.c.note)
	}
	if p := placeholder(h.env()); !strings.Contains(p, "when the turn ends") {
		t.Fatalf("placeholder %q", p)
	}

	join, steers = false, true
	h.typ("old")
	m = sends(t, h.press(key.NameReturn, key.ModShortcut))
	if len(m) != 1 || m[0].How != "now" || m[0].Join {
		t.Fatalf("old daemon: %+v", m)
	}
	if ids := shownIDs(h.c.shownQueue(&h.snap, h.now)); len(ids) != 1 {
		t.Fatalf("old daemon: the queue left view: %v", ids)
	}
	if m := h.press(key.NameReturn, key.ModShortcut); len(m) != 0 {
		t.Fatalf("old daemon, empty box: %+v", m)
	}
}

// follow_up steer swaps the keys: Enter steers with the queue, Ctrl+Enter
// queues. With send_key ctrl+enter the steer key is Ctrl+Shift+Enter,
// which on an empty box steers the queue.
func TestJoinSteerSwapped(t *testing.T) {
	h := newHarness(t)
	steer, ctrl := true, false
	h.opts = func(e *Env) { e.Join, e.Steer, e.CtrlEnter = true, steer, ctrl }
	h.snap = queueSnap(session.QueuedInput{ID: "a", Text: "one"})
	if p := placeholder(h.env()); p != "Type to steer the running turn · Ctrl+Enter queues" {
		t.Fatalf("placeholder %q", p)
	}
	h.typ("now")
	if b := h.c.button(h.env()); b != ButtonSteer {
		t.Fatalf("button %v", b)
	}
	if m := sends(t, h.press(key.NameReturn, 0)); len(m) != 1 || m[0].How != "now" || !m[0].Join {
		t.Fatalf("Enter with follow_up steer: %+v", m)
	}
	h.c.Result(Result{Req: proto.ChatSend{}}, h.now)
	h.snap.QueuedInputs = []session.QueuedInput{{ID: "b", Text: "two"}}
	h.typ("later")
	if m := sends(t, h.press(key.NameReturn, key.ModShortcut)); len(m) != 1 || m[0].How != "queue" || m[0].Join {
		t.Fatalf("Ctrl+Enter with follow_up steer: %+v", m)
	}
	if m := sends(t, h.press(key.NameReturn, 0)); len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatSend{Chat: "c1", How: "now", Join: true}) {
		t.Fatalf("Enter on an empty box steers the queue: %+v", m)
	}

	steer, ctrl = false, true
	h.c.q = queueState{}
	h.snap.QueuedInputs = []session.QueuedInput{{ID: "c", Text: "three"}}
	h.frame()
	if p := placeholder(h.env()); p != "Type to queue a follow-up · Ctrl+Shift+Enter steers it in now" {
		t.Fatalf("placeholder %q", p)
	}
	if m := h.press(key.NameReturn, key.ModShortcut|key.ModShift); len(m) != 1 || !m[0].(proto.ChatSend).Join {
		t.Fatalf("Ctrl+Shift+Enter on an empty box: %+v", m)
	}
	h.typ("x")
	if m := sends(t, h.press(key.NameReturn, key.ModShortcut)); len(m) != 1 || m[0].How != "queue" {
		t.Fatalf("Ctrl+Enter with send_key ctrl+enter: %+v", m)
	}
}

// The strip says its messages wait for the turn and names the steer key;
// the button's tooltip names both keys.
func TestJoinSteerWords(t *testing.T) {
	snap := queueSnap()
	e := Env{Snap: &snap, Steers: true, Join: true}
	if s := queueHint(e, ""); s != "Queued: goes when the turn ends · Ctrl+Enter steers it in now" {
		t.Fatalf("hint %q", s)
	}
	if s := sendTip(e, ButtonQueue); s != "Queue: sends when the turn ends (Enter). Steer it in now, with the queue: Ctrl+Enter" {
		t.Fatalf("queue tip %q", s)
	}
	e.Steer = true
	if s := sendTip(e, ButtonSteer); s != "Steer: sends into the running turn (Enter). Queue: Ctrl+Enter" {
		t.Fatalf("steer tip %q", s)
	}
	if s := sendTip(e, ButtonQueue); s != "Queue: sends when the turn ends (Enter)" {
		t.Fatalf("a command's queue tip %q", s)
	}
	e.Steers = false
	if s := queueHint(e, ""); !strings.Contains(s, "takes nothing mid-turn") {
		t.Fatalf("hint %q", s)
	}
	snap.SessionState = session.StateReady
	if s := queueHint(e, ""); s != "" {
		t.Fatalf("idle hint %q", s)
	}
	if p := placeholder(Env{Snap: &snap}); p != "Reply to Claude" {
		t.Fatalf("idle placeholder %q", p)
	}
}

// With the smart queue's line, the hint says when the joined messages go.
func TestJoinSteerHintJoined(t *testing.T) {
	snap := queueSnap()
	e := Env{Snap: &snap, Steers: true, Join: true}
	if s := queueHint(e, "The next 2 go as one message"); s != "The next 2 go as one message when the turn ends · Ctrl+Enter steers them in now" {
		t.Fatalf("hint %q", s)
	}
	snap.SessionState = session.StateReady
	if s := queueHint(e, "The next 2 go as one message"); s != "The next 2 go as one message" {
		t.Fatalf("idle hint %q", s)
	}
}
