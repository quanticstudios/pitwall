package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestCloseDoneWorkersBackgroundTasks(t *testing.T) {
	for _, kind := range []string{"shell", "monitor", "subagent", "workflow", "MCP task"} {
		t.Run(kind, func(t *testing.T) {
			d, _ := doneWorkerDaemon(t)
			start := time.Now()
			d.checkDoneWorkers(start)
			claudeHook(t, d, "a", fmt.Sprintf(`{"hook_event_name":"Stop","last_assistant_message":"Waiting","background_tasks":[{"type":%q,"status":"running","server":"mcp"}]}`, kind))
			d.mu.Lock()
			finished := d.finishedWorker(d.st, d.st.Workspaces[0])
			i := d.activityIndex("a")
			working := i >= 0 && d.st.Activities[i].State == model.StateWorking
			d.mu.Unlock()
			if finished || !working {
				t.Fatalf("background task: finished=%v working=%v", finished, working)
			}
			d.checkDoneWorkers(start.Add(time.Hour))
			d.mu.Lock()
			exists := d.workspace("w") != nil
			_, clock := d.doneWorkers["w"]
			d.mu.Unlock()
			if !exists || clock {
				t.Fatalf("background task: exists=%v clock=%v", exists, clock)
			}
			claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"Task finished"}`)
			claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"DONE","background_tasks":[]}`)
			d.checkDoneWorkers(start.Add(2 * time.Hour))
			d.checkDoneWorkers(start.Add(2*time.Hour + 9*time.Minute))
			d.mu.Lock()
			exists = d.workspace("w") != nil
			d.mu.Unlock()
			if !exists {
				t.Fatal("closed before a fresh ten minutes after background completion")
			}
			d.checkDoneWorkers(start.Add(2*time.Hour + 10*time.Minute))
			d.mu.Lock()
			exists = d.workspace("w") != nil
			d.mu.Unlock()
			if exists {
				t.Fatal("did not close after background completion and a fresh delay")
			}
		})
	}
}

// A Stop that waits on Claude Code's own work, which never wakes the agent,
// leaves the worker Done, with the turn's reply as the summary.
func TestCloseDoneWorkersOwnBackgroundWork(t *testing.T) {
	d, _ := doneWorkerDaemon(t)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"APK built.","background_tasks":[{"type":"auto-mode scan","status":"running"},{"type":"dream","status":"running"},{"type":"teammate","status":"running"}]}`)
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.st.Activities[d.activityIndex("a")]
	if a.State != model.StateCompleted || a.Detail != "APK built." || d.live.background["a"] {
		t.Fatalf("activity %q %q, background %v; want completed", a.State, a.Detail, d.live.background["a"])
	}
}

// A Stop that keeps a tab Working shows the turn's reply, and a later
// SubagentStop whose list has no such task left ends the turn.
func TestBackgroundEndsOnSubagentStop(t *testing.T) {
	d, _ := doneWorkerDaemon(t)
	state := func() (model.Activity, bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.st.Activities[d.activityIndex("a")], d.live.background["a"]
	}
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"build it"}`)
	// A foreground subagent ending mid-turn changes nothing.
	claudeHook(t, d, "a", `{"hook_event_name":"SubagentStop","agent_id":"f1","last_assistant_message":"sub","background_tasks":[]}`)
	if a, _ := state(); a.State != model.StateWorking {
		t.Fatalf("mid-turn SubagentStop: %q, want working", a.State)
	}
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"Started the build on box.","background_tasks":[{"type":"shell","status":"running"}]}`)
	if a, bg := state(); a.State != model.StateWorking || a.Detail != "Started the build on box." || !bg || !a.Background || model.PillLabel(a) != "Background" {
		t.Fatalf("Stop with a shell: %q %q background %v %v", a.State, a.Detail, bg, a.Background)
	}
	// Claude Code's own work does not hold the turn either.
	claudeHook(t, d, "a", `{"hook_event_name":"SubagentStop","agent_id":"s1","agent_type":"","last_assistant_message":"memo","background_tasks":[{"type":"shell","status":"running"}]}`)
	if a, _ := state(); a.State != model.StateWorking {
		t.Fatalf("SubagentStop while the shell runs: %q, want working", a.State)
	}
	claudeHook(t, d, "a", `{"hook_event_name":"SubagentStop","agent_id":"s1","agent_type":"","last_assistant_message":"memo","background_tasks":[{"type":"dream","status":"running"}]}`)
	a, bg := state()
	if a.State != model.StateCompleted || a.Detail != "Started the build on box." || bg || a.Background {
		t.Fatalf("SubagentStop after the shell: %q %q background %v; want completed with the Stop's reply", a.State, a.Detail, bg)
	}
	d.mu.Lock()
	reply := d.replies["a"]
	d.mu.Unlock()
	if reply != "Started the build on box." {
		t.Fatalf("kept reply %q, want the Stop's reply", reply)
	}
}

func TestBackgroundReviewEndsOnSubagentStop(t *testing.T) {
	d, _ := talkDaemon(t)
	t.Cleanup(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, timer := range d.mail.wake {
			timer.Stop()
		}
		clear(d.mail.queue)
	})
	// Keep the recipient busy so the review stays queued for inspection.
	claudeHook(t, d, "b", `{"hook_event_name":"UserPromptSubmit","prompt":"work"}`)
	d.mu.Lock()
	d.reviews = map[string]string{"a": "b"}
	d.mu.Unlock()
	reply := "Review complete.\n" + strings.Repeat("Full findings.\n", 30)
	claudeHook(t, d, "a", fmt.Sprintf(`{"hook_event_name":"Stop","last_assistant_message":%q,"background_tasks":[{"type":"shell","status":"running"}]}`, reply))
	d.mu.Lock()
	queued, target := len(d.mail.queue["b"]), d.reviews["a"]
	d.mu.Unlock()
	if queued != 0 || target != "b" {
		t.Fatalf("held Stop relayed review early: queued %d, target %q", queued, target)
	}
	claudeHook(t, d, "a", `{"hook_event_name":"SubagentStop","agent_id":"s1","last_assistant_message":"memo","background_tasks":[]}`)
	if got := d.read(proto.Read{Pane: "a"}); got.Err != "" || got.Text != reply {
		t.Fatalf("completed reply: %+v, want full Stop reply %q", got, reply)
	}
	// Repeated internal-agent hooks must not send the review again.
	claudeHook(t, d, "a", `{"hook_event_name":"SubagentStop","agent_id":"s2","last_assistant_message":"another memo","background_tasks":[]}`)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, pending := d.reviews["a"]; pending || len(d.mail.queue["b"]) != 1 || d.mail.queue["b"][0].text != reviewNote+"\n\n"+reply {
		t.Fatalf("review not relayed: pending %v, queue %+v", pending, d.mail.queue["b"])
	}
}

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

// A turn that ended on a background shell reads Background until the agent
// works again.
func TestBackgroundClearsWhenAgentWorks(t *testing.T) {
	d, _ := doneWorkerDaemon(t)
	act := func() model.Activity {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.st.Activities[d.activityIndex("a")]
	}
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"run the dev server"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"Server is up.","background_tasks":[{"type":"shell","status":"running"}]}`)
	if a := act(); !a.Background || model.Pulses(&a) {
		t.Fatalf("after Stop with a shell: background %v, pulses %v", a.Background, model.Pulses(&a))
	}
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"next"}`)
	if a := act(); a.State != model.StateWorking || a.Background || model.PillLabel(a) != "Working" {
		t.Fatalf("after a new prompt: %q background %v", a.State, a.Background)
	}
}
