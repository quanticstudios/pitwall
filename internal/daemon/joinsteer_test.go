package daemon

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// joinChat is a chat mid-turn with queue qs.
func joinChat(drv *stubDriver, qs ...session.QueuedMessage) *chat {
	return &chat{id: "c", drv: drv, cfg: session.Config{Queue: qs},
		snap: session.Snapshot{SessionID: "c", Turns: []session.TurnSummary{{ID: "t1", State: session.TurnRunning}}}}
}

// steered is the newest prompt in c's transcript.
func steered(c *chat) session.Item {
	for i := len(c.snap.Content) - 1; i >= 0; i-- {
		if c.snap.Content[i].Kind == session.KindUserPrompt {
			return c.snap.Content[i]
		}
	}
	return session.Item{}
}

// ChatSend Join steers the queue's plain messages, in order, then the
// sent one, as one message; a command, a skill's message and one being
// sent keep their places. A refused steer puts the queue back as it was.
func TestChatJoinSteer(t *testing.T) {
	img := session.Attachment{ID: "image-1", Kind: "image", Name: "a.png", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}
	queue := []session.QueuedMessage{
		{ID: "q1", Text: "one", Error: "it failed"},
		{ID: "q2", Text: "/compact"},
		{ID: "q3", Text: "two [Image #1]", Attachments: []session.Attachment{img}},
		{ID: "q4", Text: "skill", Skill: &session.Skill{Name: "s"}},
		{ID: "q5", Text: "sending", Sending: true},
	}
	drv := &stubDriver{emit: func(session.Event) {}}
	c := joinChat(drv, slices.Clone(queue)...)
	d := &Daemon{}
	if _, note, err := d.chatJoin(context.Background(), c, session.Message{ID: "m", Text: "three [Image #1]", Attachments: []session.Attachment{img}}, "now"); err != nil || note != "" {
		t.Fatalf("join: %q %v", note, err)
	}
	p := steered(c)
	if p.ID != "m" || p.Text != "one\n\ntwo [Image #1]\n\nthree [Image #2]" || len(p.Attachments) != 2 || p.TurnID == nil || *p.TurnID != "t1" {
		t.Fatalf("steered %q (%d attachments, turn %v)", p.Text, len(p.Attachments), p.TurnID)
	}
	if ids := queueIDs(c.cfg.Queue); !slices.Equal(ids, []string{"q2", "q4", "q5"}) {
		t.Fatalf("queue %v, want the command, the skill and the one being sent", ids)
	}

	// An empty draft steers the queue alone.
	c = joinChat(drv, queue[0], queue[2])
	if _, _, err := d.chatJoin(context.Background(), c, session.Message{ID: "e"}, "steer"); err != nil {
		t.Fatal(err)
	}
	if p := steered(c); p.Text != "one\n\ntwo [Image #1]" || len(c.cfg.Queue) != 0 {
		t.Fatalf("queue alone: %q, left %v", p.Text, queueIDs(c.cfg.Queue))
	}
	// Nothing to join and nothing typed: refused, as an empty send is.
	if _, _, err := d.chatJoin(context.Background(), c, session.Message{ID: "x"}, "steer"); err == nil {
		t.Fatal("an empty join was sent")
	}

	// A refused steer leaves the queue as it was.
	drv = &stubDriver{emit: func(session.Event) {}, steer: session.ErrClosed}
	c = joinChat(drv, slices.Clone(queue)...)
	if _, _, err := d.chatJoin(context.Background(), c, session.Message{ID: "m", Text: "three"}, "steer"); err != session.ErrClosed {
		t.Fatalf("refused steer: %v", err)
	}
	if !reflect.DeepEqual(c.cfg.Queue, queue) {
		t.Fatalf("queue after a refused steer %v", queueIDs(c.cfg.Queue))
	}

	// A provider that takes nothing mid-turn queues the joined message
	// whole, to go when the turn ends.
	drv = &stubDriver{emit: func(session.Event) {}, steer: session.ErrUnsupported}
	c = joinChat(drv, queue[0], queue[1], queue[2])
	_, note, err := d.chatJoin(context.Background(), c, session.Message{ID: "m", Text: "three"}, "steer")
	if err != nil || !strings.HasPrefix(note, "queued") {
		t.Fatalf("unsupported steer: %q %v", note, err)
	}
	if q := c.cfg.Queue; len(q) != 2 || q[0].ID != "q2" || q[1].ID != "m" || q[1].Text != "one\n\ntwo [Image #1]\n\nthree" {
		t.Fatalf("queue %+v", q)
	}
}

// Idle, a join starts a turn at once with the queue first, even where a
// failed turn holds the queue.
func TestChatJoinIdle(t *testing.T) {
	drv := &stubDriver{emit: func(session.Event) {}}
	c := &chat{id: "c", drv: drv, cfg: session.Config{Queue: []session.QueuedMessage{{ID: "q1", Text: "one"}, {ID: "q2", Text: "two"}}},
		snap: session.Snapshot{Turns: []session.TurnSummary{{ID: "t1", State: session.TurnFailed}}}}
	if why, _, _ := c.wait(); why != waitFailed {
		t.Fatalf("the queue waits on %q", why)
	}
	if _, _, err := (&Daemon{}).chatJoin(context.Background(), c, session.Message{ID: "m", Text: "three"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := drv.got(); !slices.Equal(got, []string{"send one\n\ntwo\n\nthree"}) || len(c.cfg.Queue) != 0 {
		t.Fatalf("driver %q, queue %v", got, queueIDs(c.cfg.Queue))
	}
}

func queueIDs(qs []session.QueuedMessage) []string {
	var ids []string
	for _, q := range qs {
		ids = append(ids, q.ID)
	}
	return ids
}

// A pane chat's join takes the queue's plain rows into the steer, in
// order, ahead of the draft; a command waits in its place. Antigravity
// CLI takes it with Alt+M.
func TestPaneJoinSteer(t *testing.T) {
	d, p := agyPaneDaemon(t, model.StateWorking, agyScreenFixture(t, "antigravity-working.txt"))
	d.mu.Lock()
	for _, text := range []string{"one", "/compact", "two"} {
		d.chatEnqueue("a", session.QueuedInput{ID: text, Text: text, Recipients: []string{}}, letter{text: text, plain: true, window: true, owner: model.ProviderAntigravity})
	}
	d.mu.Unlock()
	r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "three", How: "steer", Join: true})
	if res := r.(proto.ChatResult); res.Code != "" || res.Message != "" {
		t.Fatalf("join: %+v", res)
	}
	want := "one\n\ntwo\n\nthree"
	if got := gotAfter(t, p, 0, len(want)+2); !strings.Contains(got, "one") || !strings.Contains(got, "two") || !strings.HasSuffix(got, "three\x1bm") ||
		strings.Index(got, "one") > strings.Index(got, "two") || strings.Contains(got, "compact") {
		t.Fatalf("typed %q, want %q then Alt+M", got, want)
	}
	time.Sleep(30 * time.Millisecond)
	snap, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
	if len(snap.QueuedInputs) != 1 || snap.QueuedInputs[0].Text != "/compact" {
		t.Fatalf("queue %+v, want the command alone", snap.QueuedInputs)
	}
}

// An empty join steers the pane's queue alone; joinRows numbers the
// joined rows' images on, and chatUnjoin puts taken rows back first.
func TestPaneJoinRows(t *testing.T) {
	img := session.Attachment{ID: "image-1", Kind: "image"}
	rows := []chatRow{
		{session.QueuedInput{ID: "a", Text: "see [Image #1]", Attachments: []session.Attachment{img}}, letter{text: "see", images: []string{"/a.png"}}},
		{session.QueuedInput{ID: "b", Text: "and"}, letter{text: "and", quoted: []string{"q"}}},
	}
	in, l := joinRows(rows, session.QueuedInput{ID: "m", Text: "more [Image #1]", Attachments: []session.Attachment{img}}, letter{text: "more", images: []string{"/m.png"}})
	if in.ID != "m" || in.Text != "see [Image #1]\n\nand\n\nmore [Image #2]" || len(in.Attachments) != 2 || in.Attachments[1].ID != "image-1-2" {
		t.Fatalf("row %+v", in)
	}
	if l.text != "see\n\nand\n\nmore" || !slices.Equal(l.images, []string{"/a.png", "/m.png"}) || !slices.Equal(l.quoted, []string{"q"}) {
		t.Fatalf("letter %+v", l)
	}
	in, l = joinRows(rows, session.QueuedInput{ID: "e"}, letter{})
	if in.Text != "see [Image #1]\n\nand" || l.text != "see\n\nand" {
		t.Fatalf("queue alone: %q, %q", in.Text, l.text)
	}

	d, _, _ := remoteDaemon(t)
	ids := enqueue(d, "one", "!ls", "two")
	d.mu.Lock()
	taken := d.chatJoinable("a")
	left := d.mail.chat["a"].rows
	d.mu.Unlock()
	if len(taken) != 2 || taken[0].in.ID != ids[0] || taken[1].in.ID != ids[2] || len(left) != 1 || left[0].in.ID != ids[1] {
		t.Fatalf("took %d, left %d", len(taken), len(left))
	}
	d.mu.Lock()
	d.chatUnjoin("a", taken)
	back := d.mail.chat["a"].rows
	letters := len(d.mail.queue["a"])
	d.mu.Unlock()
	if len(back) != 3 || back[0].in.ID != ids[0] || back[1].in.ID != ids[2] || back[2].in.ID != ids[1] || letters != 3 {
		t.Fatalf("back %d rows, %d letters", len(back), letters)
	}
}
