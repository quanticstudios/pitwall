package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// A pane chat stopped on a usage limit takes the notice's Continue at:
// ChatSend How "queue" waits in the chat's queue, untyped though the agent
// sits idle at its prompt, held until the limit resets, and it drops the
// limit resume, which would type continue a second time. A plain send at
// an idle prompt still types at once.
func TestPaneLimitQueue(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(`{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"go"}}`+"\n"), 0o600)
	resets := time.Now().Add(2 * time.Hour).Truncate(time.Hour)
	msg := "You've hit your session limit · resets " + strings.ToLower(resets.Local().Format("3pm")) + " (" + time.Local.String() + ")"
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	a := &d.st.Activities[d.activityIndex("a")]
	a.State, a.Detail, a.UpdatedAt = model.StateError, msg, time.Now()
	stopped := false
	d.lim.pending = map[string]*resume{"a": {at: resets.Add(resumeDelay), stop: func() bool { stopped = true; return true }}}
	d.mu.Unlock()
	paneRead(t, d, "a", session.Snapshot{}, 0)

	before := len(f.pane(0).got())
	if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "continue", How: "queue"}); r.(proto.ChatResult).Message != "queued" {
		t.Fatalf("queue at the limit: %+v", r)
	}
	snap, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
	if len(snap.QueuedInputs) != 1 || snap.QueuedInputs[0].Text != "continue" {
		t.Fatalf("queue %+v, want the continue", snap.QueuedInputs)
	}
	// deliver says why the queue waits once it looked.
	waitUntil(t, "the limit's reason", func() bool {
		s, _ := paneRead(t, d, "a", session.Snapshot{}, 0)
		return strings.HasPrefix(s.QueueReason, "Waiting for the usage limit to reset at ")
	})
	d.mu.Lock()
	why, _, until := d.queueWait("a", d.mail.chat["a"], f.pane(0).Snapshot())
	_, armed := d.lim.pending["a"]
	d.mu.Unlock()
	if !until.Equal(resets.Add(resumeDelay)) || why == "" {
		t.Errorf("held %q until %v, want %v", why, until, resets.Add(resumeDelay))
	}
	if armed || !stopped {
		t.Errorf("the limit resume stays armed beside the queued continue (stopped %v)", stopped)
	}
	time.Sleep(50 * time.Millisecond)
	if got := f.pane(0).got()[before:]; got != "" {
		t.Fatalf("typed %q at the limit: the queue holds it", got)
	}

	// Idle with no limit, a plain send types at once.
	d.mu.Lock()
	a = &d.st.Activities[d.activityIndex("a")]
	a.State, a.Detail = model.StateCompleted, ""
	d.mu.Unlock()
	if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "hello"}); r.(proto.ChatResult).Message == "queued" {
		t.Fatalf("an idle send queued: %+v", r)
	}
}

// A New task chat whose turn a plan limit refused: a continue queued at
// the limit drops the limit resume, so continue goes once, from the
// queue, never from both.
func TestChatLimitQueueOnce(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
	var mu sync.Mutex
	d, prov := chatDaemon(t, func(o *Options) {
		o.ResumeAfterLimit = func() bool { return true }
		o.SaveLimit = func(flow.Limit) { mu.Lock(); mu.Unlock() }
	})
	r := chatDo(t, d, proto.ChatNew{Dir: t.TempDir(), Prompt: "fix it"})
	id, drv := r.Chat, prov.driver(t)
	resets := clock.now.Add(time.Hour)
	drv.event(session.Event{Type: session.EventLimit, Limit: &session.Limit{Rejected: true, Resets: resets}})
	drv.end(session.TurnFailed)
	waitUntil(t, "resume set", func() bool { return !resumes(d, id).IsZero() })
	if r := chatDo(t, d, proto.ChatSend{Chat: id, Text: "continue", How: "queue"}); r.Code != "" {
		t.Fatalf("queue: %+v", r)
	}
	waitUntil(t, "resume dropped", func() bool { return resumes(d, id).IsZero() })
	// The fake reset lies in the real past, so the queue goes now.
	waitUntil(t, "continue sent", func() bool {
		n := 0
		for _, g := range drv.got() {
			if g == "send continue" {
				n++
			}
		}
		return n == 1
	})
	clock.fire() // a dropped resume sends nothing
	time.Sleep(50 * time.Millisecond)
	n := 0
	for _, g := range drv.got() {
		if g == "send continue" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("continue sent %d times: %q", n, drv.got())
	}
}
