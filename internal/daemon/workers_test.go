package daemon

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func doneWorkerDaemon(t *testing.T) (*Daemon, *fakes) {
	t.Helper()
	d, f := talkDaemon(t)
	d.mu.Lock()
	d.st.Workspaces[0].Parent = "parent"
	d.mu.Unlock()
	for _, id := range []string{"a", "b"} {
		claudeHook(t, d, id, `{"hook_event_name":"UserPromptSubmit","prompt":"work"}`)
		claudeHook(t, d, id, `{"hook_event_name":"Stop","last_assistant_message":"result\nDONE"}`)
	}
	return d, f
}

func TestCloseDoneWorkersClock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Daemon)
		reset  bool
	}{
		{"done", func(d *Daemon) {}, false},
		{"typing history alone", func(d *Daemon) { d.mail.drafting = map[string]bool{"a": true} }, false},
		{"open PR", func(d *Daemon) { d.st.PRs = map[string]model.PR{"w": {State: model.PROpen}} }, true},
		{"merged PR", func(d *Daemon) { d.st.PRs = map[string]model.PR{"w": {State: model.PRMerged}} }, false},
		{"closed PR", func(d *Daemon) { d.st.PRs = map[string]model.PR{"w": {State: model.PRClosed}} }, false},
		{"working pane", func(d *Daemon) { d.st.Activities[1].State = model.StateWorking }, true},
		{"agent with no activity", func(d *Daemon) { d.st.Activities = d.st.Activities[:1] }, true},
		{"not a worker", func(d *Daemon) { d.st.Workspaces[0].Parent = "" }, true},
		{"pinned", func(d *Daemon) { d.st.Workspaces[0].Pinned = true }, true},
		{"off", func(d *Daemon) { d.o.CloseDoneWorkers = func() time.Duration { return 0 } }, true},
		{"background work", func(d *Daemon) { d.live.background = map[string]bool{"a": true} }, true},
		{"pending message", func(d *Daemon) { d.mail.queue["a"] = []letter{{text: "review"}} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := doneWorkerDaemon(t)
			start := time.Now()
			d.checkDoneWorkers(start)
			d.mu.Lock()
			tc.change(d)
			d.mu.Unlock()
			d.checkDoneWorkers(start.Add(10 * time.Minute))
			d.mu.Lock()
			exists := d.workspace("w") != nil
			d.mu.Unlock()
			if exists != tc.reset {
				t.Fatalf("worker exists %v, want %v", exists, tc.reset)
			}
		})
	}
}

func TestCloseDoneWorkersInteraction(t *testing.T) {
	for _, focus := range []bool{false, true} {
		t.Run(map[bool]string{false: "input", true: "focus"}[focus], func(t *testing.T) {
			d, _ := doneWorkerDaemon(t)
			start := time.Now()
			d.checkDoneWorkers(start)
			d.checkDoneWorkers(start.Add(9 * time.Minute))
			if focus {
				if err := d.seePane("a"); err != nil {
					t.Fatal(err)
				}
			} else {
				d.noteInput("a", []byte("x"))
			}
			d.checkDoneWorkers(start.Add(10 * time.Minute))
			d.checkDoneWorkers(start.Add(19 * time.Minute))
			d.mu.Lock()
			exists := d.workspace("w") != nil
			d.mu.Unlock()
			if !exists {
				t.Fatal("closed less than ten minutes after interaction")
			}
			d.checkDoneWorkers(start.Add(20 * time.Minute))
			d.mu.Lock()
			exists = d.workspace("w") != nil
			d.mu.Unlock()
			if exists {
				t.Fatal("did not close after a fresh ten minutes")
			}
		})
	}
}

func TestCloseDoneWorkersFocused(t *testing.T) {
	d, _ := doneWorkerDaemon(t)
	c := &client{focus: "a"}
	d.mu.Lock()
	d.clients[c] = struct{}{}
	d.mu.Unlock()
	start := time.Now()
	d.checkDoneWorkers(start)
	d.checkDoneWorkers(start.Add(time.Hour))
	d.mu.Lock()
	c.focus = ""
	exists := d.workspace("w") != nil
	d.mu.Unlock()
	if !exists {
		t.Fatal("closed while focused")
	}
	d.checkDoneWorkers(start.Add(2 * time.Hour))
	d.mu.Lock()
	exists = d.workspace("w") != nil
	d.mu.Unlock()
	if !exists {
		t.Fatal("counted focused time toward the delay")
	}
}

func TestCloseDoneWorkersRetainsReply(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "menu", true: "timer"}[automatic], func(t *testing.T) {
			d, f := doneWorkerDaemon(t)
			start := time.Now()
			if automatic {
				d.checkDoneWorkers(start)
				d.checkDoneWorkers(start.Add(10 * time.Minute))
			} else if err := d.handle(t.Context(), proto.CloseDoneWorkers{WorkspaceID: "parent"}); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"a", "b"} {
				r := d.read(proto.Read{Pane: id})
				if r.Err != "" || r.Text != "result\nDONE" {
					t.Fatalf("closed reply: %+v", r)
				}
				if r := d.read(proto.Read{Pane: id, Screen: true}); r.Err == "" {
					t.Fatal("closed screen still exists")
				}
			}
			d.mu.Lock()
			closed := append([]model.ClosedTab(nil), d.st.Closed...)
			live := len(d.st.Workspaces)
			d.mu.Unlock()
			if live != 0 || len(closed) != 1 || !closed[0].FinishedWorker || closed[0].Tab.Parent != "parent" {
				t.Fatalf("live %d, closed %+v", live, closed)
			}
			select {
			case <-f.pane(0).done:
			case <-time.After(2 * time.Second):
				t.Fatal("process still running")
			}
			d.mu.Lock()
			d.st.Closed = nil
			d.pruneWorkerReplies()
			kept := len(d.workerReplies)
			d.mu.Unlock()
			if kept != 0 {
				t.Fatal("replies outlived bounded history")
			}
		})
	}
}

// A new final turn between polls must get its full delay, even if both
// polls see Done.
func TestCloseDoneWorkersNewTurn(t *testing.T) {
	d, _ := doneWorkerDaemon(t)
	start := time.Now()
	d.checkDoneWorkers(start)
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"again"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"again\nDONE"}`)
	d.checkDoneWorkers(start.Add(10 * time.Minute))
	d.mu.Lock()
	exists := d.workspace("w") != nil
	d.mu.Unlock()
	if !exists {
		t.Fatal("closed before the new turn had its full delay")
	}
}

func TestCloseDoneWorkersScreenFallback(t *testing.T) {
	d, f := doneWorkerDaemon(t)
	d.mu.Lock()
	delete(d.replies, "a")
	d.st.Closed = nil
	d.mu.Unlock()
	setScreen(f.pane(0), []string{"screen result", "DONE"})
	if err := d.closeDoneWorkers("parent"); err != nil {
		t.Fatal(err)
	}
	if r := d.read(proto.Read{Pane: "a"}); r.Err != "" || r.Text != "screen result\nDONE" {
		t.Fatalf("screen fallback: %+v", r)
	}
}

func TestCloseDoneWorkersFocusRequest(t *testing.T) {
	d, _ := doneWorkerDaemon(t)
	start := time.Now()
	d.checkDoneWorkers(start)
	if err := d.focusSession(proto.FocusSession{WorkspaceID: "w"}); err != nil {
		t.Fatal(err)
	}
	d.checkDoneWorkers(start.Add(10 * time.Minute))
	d.mu.Lock()
	exists := d.workspace("w") != nil
	d.mu.Unlock()
	if !exists {
		t.Fatal("focus request did not restart the clock")
	}
}
