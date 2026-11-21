package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestJoins(t *testing.T) {
	for _, c := range []struct {
		texts []string
		alone []bool
		want  int
	}{
		{[]string{"a", "b", "c"}, []bool{false, false, false}, 3},
		{[]string{"a", "/compact", "c"}, []bool{false, false, false}, 1},
		{[]string{"/compact", "a", "b"}, []bool{false, false, false}, 1},
		{[]string{"  !ls", "a"}, []bool{false, false}, 1},
		{[]string{"a", "b", "!ls"}, []bool{false, false, false}, 2},
		{[]string{"a", "b", "c"}, []bool{false, true, false}, 1},
		{[]string{"a", "b"}, []bool{true, false}, 1},
		{[]string{"a"}, []bool{false}, 1},
	} {
		if got := joins(c.texts, c.alone); got != c.want {
			t.Errorf("joins(%q, %v) = %d, want %d", c.texts, c.alone, got, c.want)
		}
	}
}

func TestJoinPrompts(t *testing.T) {
	got := joinPrompts([]string{"look at [Image #1] and [Image #2]", " then [Image #1] ", "no images", "last [Image #3]"}, []int{2, 1, 0, 1})
	want := "look at [Image #1] and [Image #2]\n\nthen [Image #3]\n\nno images\n\nlast [Image #6]"
	if got != want {
		t.Fatalf("joinPrompts:\n%q\nwant\n%q", got, want)
	}
}

// enqueue queues texts on pane chat a, as paneSend does mid-turn, and
// returns their rows' ids.
func enqueue(d *Daemon, texts ...string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var ids []string
	for _, text := range texts {
		id := newUUID()
		d.chatEnqueue("a", session.QueuedInput{ID: id, Text: text, Recipients: []string{}},
			letter{text: text, images: []string{"/img/" + text}, plain: true, window: true})
		ids = append(ids, id)
	}
	return ids
}

// A pane chat's plain rows go as one letter, a command and a row marked
// Send separately each alone, in their places.
func TestPaneQueueJoins(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	ids := enqueue(d, "one", "two", "/compact", "three", "four", "!ls", "five")
	if err := d.paneQueue("a", proto.ChatQueue{Action: "separate", ID: ids[4]}); err != nil {
		t.Fatal(err)
	}
	if err := d.paneQueue("a", proto.ChatQueue{Action: "together", ID: ids[2]}); err != nil {
		t.Fatal(err)
	}
	var got []string
	d.mu.Lock()
	for len(d.mail.queue["a"]) > 0 {
		q := d.mail.queue["a"]
		l, n := d.chatTake("a", q, 0)
		got = append(got, l.text+" "+strings.Join(l.images, ","))
		d.mail.queue["a"] = slices.Delete(q, 0, n)
		d.chatSent("a", nil)
	}
	d.mu.Unlock()
	want := []string{"one\n\ntwo /img/one,/img/two", "/compact /img//compact", "three /img/three", "four /img/four", "!ls /img/!ls", "five /img/five"}
	if !slices.Equal(got, want) {
		t.Fatalf("letters %q\nwant %q", got, want)
	}
}

// The queue file keeps rows, their order, held state and errors; what was
// being typed comes back as maybe sent, never typed again.
func TestPaneQueuePersist(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat-queues.json")
	d, _, _ := remoteDaemon(t)
	d.o.Queues = file
	ids := enqueue(d, "first", "second", "third")
	d.mu.Lock()
	q := d.mail.chat["a"]
	l, n := d.chatTake("a", d.mail.queue["a"], 0) // first..third join: it stops typing mid-way
	q.rows = append(q.rows, chatRow{session.QueuedInput{ID: "late", Text: "late", Error: "refused"}, letter{text: "late", plain: true, window: true, chat: "late"}})
	q.held = true
	d.mu.Unlock()
	if n != 3 || l.text != "first\n\nsecond\n\nthird" {
		t.Fatalf("took %d: %q", n, l.text)
	}
	d.saveQueues()

	d2, _, _ := remoteDaemon(t)
	d2.o.Queues = file
	d2.mu.Lock()
	d2.loadQueues()
	rows, held, _ := d2.chatQueued("a")
	live := len(d2.mail.queue["a"])
	d2.mu.Unlock()
	var got []string
	for _, r := range rows {
		got = append(got, r.ID+":"+r.Error)
	}
	want := []string{ids[0] + ":" + mayHaveSent, ids[1] + ":" + mayHaveSent, ids[2] + ":" + mayHaveSent, "late:refused"}
	if !slices.Equal(got, want) || !held || live != 0 {
		t.Fatalf("loaded %q held %v, %d live letters\nwant %q held", got, held, live, want)
	}
	// Retry sends a maybe-sent row again, by hand only.
	if err := d2.paneQueue("a", proto.ChatQueue{Action: "remove", ID: ids[1]}); err != nil {
		t.Fatal(err)
	}
	d2.saveQueues()
	d3, _, _ := remoteDaemon(t)
	d3.o.Queues = file
	d3.mu.Lock()
	d3.loadQueues()
	rows, _, _ = d3.chatQueued("a")
	d3.mu.Unlock()
	if len(rows) != 3 || rows[1].ID != ids[2] {
		t.Fatalf("after remove and reload: %+v", rows)
	}
}

// A handoff writes the queues while frozen; a save that looked earlier
// never lands after it, and a frozen daemon writes no more.
func TestPaneQueueHandoffWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chat-queues.json")
	d, _, _ := remoteDaemon(t)
	d.o.Queues = file
	enqueue(d, "one")
	d.mu.Lock()
	stale, staleGen := d.queuesJSON()
	d.chatEnqueue("a", session.QueuedInput{ID: "two", Text: "two"}, letter{text: "two", plain: true, window: true})
	d.frozen = true
	d.writeQueues(d.queuesJSON()) // as freeze does
	d.mu.Unlock()
	d.writeQueues(stale, staleGen)
	d.saveQueues()
	d2, _, _ := remoteDaemon(t)
	d2.o.Queues = file
	d2.mu.Lock()
	d2.loadQueues()
	rows, _, _ := d2.chatQueued("a")
	d2.mu.Unlock()
	if len(rows) != 2 || rows[1].ID != "two" {
		t.Fatalf("the new daemon found %+v, want both rows", rows)
	}
}

// The queue waits, and says why, while the agent asks something, shows a
// form, failed its turn or hit a usage limit; Send lets a failed turn go.
func TestPaneQueueWaits(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	enqueue(d, "later")
	set := func(s model.AgentState, detail string) {
		d.mu.Lock()
		a := &d.st.Activities[d.activityIndex("a")]
		a.State, a.Detail, a.UpdatedAt = s, detail, time.Now()
		d.mu.Unlock()
	}
	wait := func(screen ...string) (string, bool, time.Time) {
		d.mu.Lock()
		defer d.mu.Unlock()
		g := vt.Grid{}
		if screen != nil {
			g = gridOf(screen)
		}
		return d.queueWait("a", d.mail.chat["a"], g)
	}
	for _, c := range []struct {
		state  model.AgentState
		detail string
		why    string
		paused bool
	}{
		{model.StatePendingApproval, "Bash", waitApproval, false},
		{model.StatePlanReady, "", waitApproval, false},
		{model.StateAwaitingInput, "Which one?", waitAnswer, false},
		{model.StateWorking, "", "", false},
		{model.StateCompleted, "", "", false},
		{model.StateError, "API Error: 500", waitFailed, true},
		{model.StateError, "You've hit your weekly limit", waitFailed, true},
	} {
		set(c.state, c.detail)
		if why, paused, _ := wait(); why != c.why || paused != c.paused {
			t.Errorf("%s %q: %q paused %v, want %q paused %v", c.state, c.detail, why, paused, c.why, c.paused)
		}
	}
	resets := time.Now().Add(2 * time.Hour).Truncate(time.Hour)
	set(model.StateError, "You've hit your session limit · resets "+strings.ToLower(resets.Local().Format("3pm"))+" ("+time.Local.String()+")")
	if why, paused, until := wait(); !strings.HasPrefix(why, "Waiting for the usage limit to reset at ") || paused || !until.Equal(resets.Add(resumeDelay)) {
		t.Errorf("limit: %q paused %v until %v, want %v", why, paused, until, resets.Add(resumeDelay))
	}
	set(model.StateCompleted, "")
	if why, _, _ := wait(" Which file?", " > 1. a.go", "   2. b.go", " Esc to cancel"); why != waitAnswer {
		t.Errorf("a form on screen: %q", why)
	}
	// A restart's resumed agent takes nothing until restartWait passed and
	// its input box shows, a hook or not.
	box := []string{"────────", "❯ ", "────────"}
	d.mu.Lock()
	d.resumed["a"] = time.Now().Add(-time.Second)
	d.live.hookAt["a"] = time.Now()
	d.mu.Unlock()
	if why, _, _ := wait(box...); why != waitStart {
		t.Errorf("resuming: %q, want %q", why, waitStart)
	}
	d.mu.Lock()
	d.resumed["a"] = time.Now().Add(-restartWait)
	d.mu.Unlock()
	if why, _, _ := wait(); why != waitStart {
		t.Errorf("resumed, no box: %q, want %q", why, waitStart)
	}
	if why, _, _ := wait(box...); why != "" {
		t.Errorf("resumed with its box: %q", why)
	}
	d.mu.Lock()
	d.mail.queue["a"] = append([]letter{{text: restartLine, plain: true}}, d.mail.queue["a"]...)
	d.mu.Unlock()
	if why, _, _ := wait(box...); why != waitStart {
		t.Errorf("behind the restart line: %q", why)
	}
	d.mu.Lock()
	d.mail.queue["a"] = d.mail.queue["a"][1:]
	delete(d.resumed, "a")
	d.mu.Unlock()
	set(model.StateError, "API Error: 500")
	if err := d.paneQueue("a", proto.ChatQueue{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	if why, _, _ := wait(); why != "" {
		t.Errorf("after Send: %q, want the queue to go", why)
	}
	// The snapshot and State.ChatQueues carry the hold.
	set(model.StateAwaitingInput, "Which one?")
	d.chatReason("a")
	d.mu.Lock()
	_, held, reason := d.chatQueued("a")
	cq := d.chatQueues(nil)["a"]
	d.mu.Unlock()
	if held || reason != waitAnswer || cq != (model.ChatQueue{N: 1, Held: waitAnswer}) {
		t.Errorf("held %v reason %q, ChatQueues %+v", held, reason, cq)
	}
}

// gridOf is a grid with lines from the top.
func gridOf(lines []string) vt.Grid {
	p := &fakePane{}
	p.screen = lines
	return p.Snapshot()
}

// A failed turn holds the pane queue: the turn's end types nothing until Send.
func TestPaneQueueFailedTurnHolds(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	oldQuiet, oldSettle := sendQuiet.set(time.Millisecond), sendSettle.set(time.Millisecond)
	defer func() { sendQuiet.set(oldQuiet); sendSettle.set(oldSettle) }()
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateWorking
	d.mu.Unlock()
	f.pane(0).mu.Lock()
	f.pane(0).screen = []string{"> "}
	f.pane(0).mu.Unlock()
	enqueue(d, "after the error")
	before := len(f.pane(0).got())
	d.mu.Lock()
	a := &d.st.Activities[d.activityIndex("a")]
	a.State, a.Detail, a.UpdatedAt = model.StateError, "API Error: 529 overloaded", time.Now()
	d.live.hookAt["a"] = time.Now()
	d.wake("a", 0)
	d.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if got := f.pane(0).got()[before:]; got != "" {
		t.Fatalf("typed %q after a failed turn", got)
	}
	d.mu.Lock()
	_, paused, reason := d.chatQueued("a")
	d.mu.Unlock()
	if !paused || reason != waitFailed {
		t.Fatalf("queue paused %v reason %q", paused, reason)
	}
	if err := d.paneQueue("a", proto.ChatQueue{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, f.pane(0), before, len("after the error")); !strings.Contains(got, "after the error") {
		t.Fatalf("Send typed %q", got)
	}
}

// Each pane agent steers: Antigravity CLI's Alt+M sends into its running
// turn, where Return would queue a turn of its own.
func TestPaneSteerKey(t *testing.T) {
	for _, p := range []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderAntigravity} {
		if !paneSteers(p) {
			t.Errorf("%s does not steer", p)
		}
	}
	if paneSteers(model.ProviderPi) {
		t.Error("pi steers")
	}
	d, _, _ := remoteDaemon(t)
	d.mu.Lock()
	d.st.Panes[0].Provider = model.ProviderAntigravity
	d.st.Activities[d.activityIndex("a")].State = model.StateWorking
	d.mu.Unlock()
	if got := string(d.steerKey("a", vt.Modes{})); got != "\x1bm" {
		t.Errorf("agy mid-turn: %q, want Alt+M", got)
	}
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateCompleted
	d.mu.Unlock()
	if got := string(d.steerKey("a", vt.Modes{})); got != "\r" {
		t.Errorf("agy idle: %q, want Return", got)
	}
}

// A chat's queue goes as one prompt per turn, commands alone; a restart
// between handing the prompt over and seeing it arrive shows it as maybe
// sent, or drops it once the transcript has it.
func TestChatQueueJoins(t *testing.T) {
	drv := &stubDriver{emit: func(session.Event) {}}
	img := session.Attachment{ID: "i", Kind: "image", Name: "a.png", MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}
	c := &chat{cfg: session.Config{Queue: []session.QueuedMessage{
		{ID: "q1", Text: "see [Image #1]", Attachments: []session.Attachment{img}},
		{ID: "q2", Text: "and [Image #1]", Attachments: []session.Attachment{img}},
		{ID: "q3", Text: "/compact"},
		{ID: "q4", Text: "skill text", Skill: &session.Skill{Name: "s"}},
		{ID: "q5", Text: "last"},
	}}, drv: drv}
	d := &Daemon{}
	for range 4 {
		d.chatNext(context.Background(), c)
		drv.mu.Lock()
		drv.turn = ""
		drv.mu.Unlock()
		c.snap = session.Snapshot{} // the turn ended
	}
	got := drv.got()
	if len(got) == 4 {
		got[0], _, _ = strings.Cut(got[0], "\n\nExplicit evidence") // provide's line for the images
	}
	want := []string{"send see [Image #1]\n\nand [Image #2]", "send /compact", "send skill text", "send last"}
	if !slices.Equal(got, want) || len(c.cfg.Queue) != 0 {
		t.Fatalf("driver %q, queue %+v\nwant %q", got, c.cfg.Queue, want)
	}
	if as := drv.msgs[0].Attachments; len(as) != 2 || as[0].ID != "i" || as[1].ID != "i-2" {
		t.Fatalf("joined prompt's attachments %+v: want i and i-2", as)
	}

	c = &chat{cfg: session.Config{Queue: []session.QueuedMessage{{ID: "s1", Sending: true}, {ID: "s2", Sending: true}, {ID: "q", Text: "next"}}}}
	c.unsent()
	if q := c.cfg.Queue; len(q) != 3 || q[0].Error != mayHaveSent || q[1].Error != mayHaveSent || q[0].Sending || q[2].Error != "" {
		t.Fatalf("unsent, not in the transcript: %+v", q)
	}
	c = &chat{cfg: session.Config{Queue: []session.QueuedMessage{{ID: "s1", Sending: true}, {ID: "s2", Sending: true}, {ID: "q", Text: "next"}}},
		snap: session.Snapshot{Content: []session.Item{{ID: "s1", Kind: session.KindUserPrompt}}}}
	c.unsent()
	if q := c.cfg.Queue; len(q) != 1 || q[0].ID != "q" {
		t.Fatalf("unsent, in the transcript: %+v", q)
	}
}

// A chat's queue waits on a question, an approval, a failed turn until
// Send, and a usage limit until it resets.
func TestChatQueueWaits(t *testing.T) {
	failed := []session.TurnSummary{{ID: "t1", State: session.TurnFailed}}
	resets := time.Now().Add(time.Hour).UTC()
	for _, c := range []struct {
		name   string
		snap   session.Snapshot
		why    string
		paused bool
	}{
		{"question", session.Snapshot{PendingUserInputs: []session.InputRequest{{}}}, waitAnswer, false},
		{"approval", session.Snapshot{PendingApprovals: []session.Approval{{}}}, waitApproval, false},
		{"done", session.Snapshot{Turns: []session.TurnSummary{{ID: "t1", State: session.TurnCompleted}}}, "", false},
		{"failed", session.Snapshot{Turns: failed}, waitFailed, true},
		{"limit", session.Snapshot{Turns: failed, UsageLimit: &session.UsageLimit{ResetsAt: new(session.Stamp(resets))}}, waitLimit(resets), false},
	} {
		ch := &chat{snap: c.snap}
		if why, paused, _ := ch.wait(); why != c.why || paused != c.paused {
			t.Errorf("%s: %q paused %v, want %q paused %v", c.name, why, paused, c.why, c.paused)
		}
	}
	drv := &stubDriver{emit: func(session.Event) {}}
	ch := &chat{snap: session.Snapshot{Turns: failed}, drv: drv, cfg: session.Config{Queue: []session.QueuedMessage{{ID: "q", Text: "after"}}}}
	d := &Daemon{}
	d.chatNext(context.Background(), ch)
	if got := drv.got(); len(got) != 0 {
		t.Fatalf("sent %q past a failed turn", got)
	}
	ch.syncQueue()
	if !ch.snap.QueuePaused || ch.snap.QueueReason != waitFailed {
		t.Fatalf("snapshot paused %v reason %q", ch.snap.QueuePaused, ch.snap.QueueReason)
	}
	if _, _, err := d.chatQueue(context.Background(), ch, proto.ChatQueue{Action: "resume"}); err != nil {
		t.Fatal(err)
	}
	if got := drv.got(); !slices.Equal(got, []string{"send after"}) || ch.cfg.FailedOK != "t1" {
		t.Fatalf("Send: driver %q, FailedOK %q", got, ch.cfg.FailedOK)
	}
}

// State.ChatQueues counts every chat's queue, held or not.
func TestChatQueuesState(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	enqueue(d, "one", "two")
	d.mu.Lock()
	d.chatHold("a", true)
	got := d.chatQueues([]model.Chat{{ID: "c1", Queued: 3}, {ID: "c2"}, {ID: "c3", Queued: 1, QueueHeld: waitFailed}})
	d.mu.Unlock()
	b, _ := json.Marshal(got)
	want := map[string]model.ChatQueue{"a": {N: 2, Held: "Paused"}, "c1": {N: 3}, "c3": {N: 1, Held: waitFailed}}
	if len(got) != len(want) || got["a"] != want["a"] || got["c1"] != want["c1"] || got["c3"] != want["c3"] {
		t.Fatalf("ChatQueues %s", b)
	}
}
