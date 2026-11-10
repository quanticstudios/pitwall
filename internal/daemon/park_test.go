package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// claudeEmpty is Claude Code's input box with nothing in it.
var claudeEmpty = []string{"────────", "❯ ", "────────"}

// parkDaemon is talkDaemon with park_idle_after at an hour and pane a a
// Claude Code agent at an empty box after a turn.
func parkDaemon(t *testing.T) (*Daemon, *fakes) {
	t.Helper()
	return parkDaemonWith(t, func(*Options) {})
}

// parkDaemonWith is parkDaemon with its options edited before it starts.
func parkDaemonWith(t *testing.T, edit func(*Options)) (*Daemon, *fakes) {
	t.Helper()
	d, f := talkDaemonWith(t, func(o *Options) {
		o.ParkIdleAfter = func() time.Duration { return time.Hour }
		edit(o)
	})
	busy := agentBusy
	agentBusy = func(Pane) bool { return false }
	oldSettle := sendSettle.set(time.Millisecond)
	t.Cleanup(func() {
		agentBusy = busy
		d.sendMu.Lock()
		sendSettle.set(oldSettle)
		d.sendMu.Unlock()
	})
	claudeHook(t, d, "a", `{"hook_event_name":"SessionStart","source":"startup"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop"}`)
	setScreen(f.pane(0), claudeEmpty)
	return d, f
}

// stateOf is pane id's activity state and detail.
func stateOf(d *Daemon, id string) (model.AgentState, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i := d.activityIndex(id); i >= 0 {
		return d.st.Activities[i].State, d.st.Activities[i].Detail
	}
	return "", ""
}

func parkedNow(d *Daemon, id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.isParked(id)
}

// lookAt runs checkPark as if at, an offset from start.
func lookAt(d *Daemon, start time.Time, at time.Duration) { d.checkPark(start.Add(at)) }

// The idle timer runs from the last look at a still screen: an hour after
// it parks, a minute short does not, and a screen change starts it over.
func TestParkIdleTimer(t *testing.T) {
	d, f := parkDaemon(t)
	start := time.Now()
	lookAt(d, start, 0)
	lookAt(d, start, 30*time.Minute)
	setScreen(f.pane(0), []string{"output", "────────", "❯ ", "────────"})
	lookAt(d, start, 40*time.Minute)
	lookAt(d, start, 99*time.Minute)
	if parkedNow(d, "a") {
		t.Fatal("parked 59 minutes after its screen changed")
	}
	lookAt(d, start, 101*time.Minute)
	if !parkedNow(d, "a") {
		t.Fatal("not parked an hour after its screen last changed")
	}
	if s, detail := stateOf(d, "a"); s != model.StateParked || detail != "Parked after 1h idle. Press Enter to resume." {
		t.Fatalf("activity %q %q, want parked with its notice", s, detail)
	}
	select { // closeAll ends the process on a goroutine of its own
	case <-f.pane(0).done:
	case <-time.After(2 * time.Second):
		t.Fatal("the agent's process still runs")
	}
	g := d.panes["a"].Snapshot()
	if text := screenText(g); !strings.HasSuffix(text, "\nParked after 1h idle. Press Enter to resume.") || len(strings.Split(text, "\n")) != g.Rows {
		t.Fatalf("the pane shows %q, want the notice alone on its last row", text)
	}
	if f.saved.Activities == nil || !slices.ContainsFunc(f.saved.Activities, func(a model.Activity) bool { return a.State == model.StateParked }) {
		t.Fatal("the parked state was not saved")
	}
}

// Each rule that keeps an agent running, with the agent idle two hours.
func TestParkNever(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(t *testing.T, d *Daemon, f *fakes)
	}{
		{"setting off", func(t *testing.T, d *Daemon, f *fakes) { d.o.ParkIdleAfter = func() time.Duration { return 0 } }},
		{"asking for permission", func(t *testing.T, d *Daemon, f *fakes) {
			claudeHook(t, d, "a", `{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`)
		}},
		{"a turn runs, as a guarded command does", func(t *testing.T, d *Daemon, f *fakes) {
			claudeHook(t, d, "a", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"make"}}`)
		}},
		{"background subagents", func(t *testing.T, d *Daemon, f *fakes) {
			claudeHook(t, d, "a", `{"hook_event_name":"Stop","background_tasks":[{"type":"subagent","status":"running"}]}`)
		}},
		{"child processes", func(t *testing.T, d *Daemon, f *fakes) { agentBusy = func(Pane) bool { return true } }},
		{"no resume command", func(t *testing.T, d *Daemon, f *fakes) {
			d.mu.Lock()
			d.st.Panes[0].SessionID = ""
			d.mu.Unlock()
		}},
		{"a draft in its box", func(t *testing.T, d *Daemon, f *fakes) { setScreen(f.pane(0), claudeBox) }},
		{"a form on screen", func(t *testing.T, d *Daemon, f *fakes) {
			setScreen(f.pane(0), []string{"Do you want to proceed?", "❯ 1. Yes", "  2. No"})
		}},
		{"a message waits", func(t *testing.T, d *Daemon, f *fakes) {
			d.mu.Lock()
			d.mail.queue["a"] = []letter{{text: "hi"}}
			d.mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, f := parkDaemon(t)
			tc.set(t, d, f)
			start := time.Now()
			lookAt(d, start, 0)
			lookAt(d, start, 2*time.Hour)
			if parkedNow(d, "a") {
				t.Fatal("parked")
			}
		})
	}
}

// A pane a window shows never parks.
func TestParkNeverShown(t *testing.T) {
	d, _ := parkDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	sp := d.st.Panes[0]
	if why := d.parkVeto(sp); why != "" {
		t.Fatalf("veto %q with no window", why)
	}
	c := &client{view: map[string]bool{"a": true}}
	d.clients[c] = struct{}{}
	defer delete(d.clients, c)
	if why := d.parkVeto(sp); why != "a window shows it" {
		t.Fatalf("veto %q, want the window's", why)
	}
}

// park parks pane a and returns the fake of the process that ran it.
func park(t *testing.T, d *Daemon, f *fakes) *fakePane {
	t.Helper()
	old := f.pane(len(f.panes) - 1)
	start := time.Now()
	lookAt(d, start, 0)
	lookAt(d, start, 2*time.Hour)
	if !parkedNow(d, "a") {
		t.Fatal("not parked")
	}
	return old
}

// back brings resumed pane a's agent to its input box: its SessionStart
// hook and an empty box, then checkWaking.
func back(t *testing.T, d *Daemon, f *fakes) *fakePane {
	t.Helper()
	p := f.pane(len(f.panes) - 1)
	setScreen(p, claudeEmpty)
	claudeHook(t, d, "a", `{"hook_event_name":"SessionStart","source":"resume"}`)
	time.Sleep(5 * time.Millisecond) // past sendSettle
	d.checkWaking()
	return p
}

// pitwall send to a parked pane resumes its session, and the message goes
// in only once the agent is back at its input box.
func TestParkedSendResumesThenDelivers(t *testing.T) {
	d, f := parkDaemon(t)
	old := park(t, d, f)
	n := len(f.panes)
	if err := d.send(proto.Send{Pane: "a", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(f.panes) != n+1 || !slices.Equal(f.pane(n).cfg.Cmd, []string{"claude", "--resume", "sess-1"}) {
		t.Fatalf("panes %d, last command %v; want the session resumed", len(f.panes), f.pane(len(f.panes)-1).cfg.Cmd)
	}
	d.checkWaking()
	time.Sleep(50 * time.Millisecond)
	if got := f.pane(n).got(); got != "" {
		t.Fatalf("typed %q before the agent was back", got)
	}
	p := back(t, d, f)
	if got := gotAfter(t, p, 0, 40); !strings.Contains(got, "hello") {
		t.Fatalf("typed %q, want the message", got)
	}
	if got := old.got(); got != "" {
		t.Fatalf("the parked process got %q", got)
	}
	if parkedNow(d, "a") {
		t.Fatal("still parked")
	}
}

// pitwall prompt to a parked pane waits for the agent the same way, and
// types the prompt with no header.
func TestParkedPromptResumesThenTypes(t *testing.T) {
	d, f := parkDaemon(t)
	park(t, d, f)
	if err := d.prompt(proto.Prompt{Pane: "a", Text: "fix it"}); err != nil {
		t.Fatal(err)
	}
	p := back(t, d, f)
	if got := gotAfter(t, p, 0, 6); !strings.HasPrefix(got, "fix it") {
		t.Fatalf("typed %q, want the prompt alone", got)
	}
}

// Enter in a parked pane only resumes it; other keys wait until the agent
// is back, then go in first. A message sent meanwhile waits while they
// sit in the box as a draft, so it never glues onto them. Focus reports
// resume nothing.
func TestParkedKeysWaitForTheAgent(t *testing.T) {
	d, f := parkDaemon(t)
	oldQuiet, oldLook := sendQuiet.set(200*time.Millisecond), sendLook.set(10*time.Millisecond)
	t.Cleanup(func() {
		d.sendMu.Lock()
		sendQuiet.set(oldQuiet)
		sendLook.set(oldLook)
		d.sendMu.Unlock()
	})
	park(t, d, f)
	n := len(f.panes)
	ctx := context.Background()
	if err := d.handle(ctx, proto.Input{Pane: "a", Data: []byte("\x1b[I")}); err != nil || len(f.panes) != n {
		t.Fatalf("a focus report: %v, %d panes; want nothing started", err, len(f.panes))
	}
	if err := d.handle(ctx, proto.Input{Pane: "a", Data: []byte("\r")}); err != nil || len(f.panes) != n+1 {
		t.Fatalf("Enter: %v, %d panes; want the agent resumed", err, len(f.panes))
	}
	for _, k := range []string{"o", "k"} {
		if err := d.handle(ctx, proto.Input{Pane: "a", Data: []byte(k)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.send(proto.Send{Pane: "a", Text: "later"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := f.pane(n).got(); got != "" {
		t.Fatalf("typed %q before the agent was back", got)
	}
	p := back(t, d, f)
	time.Sleep(50 * time.Millisecond) // the agent has not drawn the keys yet
	if got := p.got(); got != "ok" {
		t.Fatalf("typed %q before the agent drew the keys, want the keys alone", got)
	}
	setScreen(p, []string{"────────", "❯ ok", "────────"}) // the agent draws the keys as a draft
	time.Sleep(400 * time.Millisecond)
	if got := p.got(); got != "ok" {
		t.Fatalf("typed %q while the keys sat in the box, want the keys alone, without the Enter", got)
	}
	setScreen(p, claudeEmpty) // the user sent or cleared the draft
	if got := gotAfter(t, p, 2, 10); !strings.Contains(got, "later") {
		t.Fatalf("typed %q after the draft went, want the message", got)
	}
}

// Many keys held while the input queue is full all reach the agent, in
// one batch once the queue has room.
func TestParkedKeysWaitForRoom(t *testing.T) {
	d, f := parkDaemon(t)
	park(t, d, f)
	ctx := context.Background()
	if err := d.handle(ctx, proto.Input{Pane: "a", Data: []byte("\r")}); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		if err := d.handle(ctx, proto.Input{Pane: "a", Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	setScreen(f.pane(len(f.panes)-1), claudeEmpty)
	claudeHook(t, d, "a", `{"hook_event_name":"SessionStart","source":"resume"}`)
	time.Sleep(5 * time.Millisecond)
	full := make(chan []byte, inputQueue) // an input queue no writer drains
	for len(full) < cap(full) {
		full <- []byte("b")
	}
	d.mu.Lock()
	live := d.inputs["a"]
	d.inputs["a"] = full
	d.mu.Unlock()
	d.checkWaking()
	if !wakingNow(d, "a") {
		t.Fatal("stopped waking with its keys not sent")
	}
	for len(full) > 0 {
		<-full
	}
	d.checkWaking()
	d.mu.Lock()
	d.inputs["a"] = live
	d.mu.Unlock()
	if len(full) != 1 || wakingNow(d, "a") {
		t.Fatalf("%d batches queued, waking %v; want one batch and the resume done", len(full), wakingNow(d, "a"))
	}
	if got := string(<-full); got != strings.Repeat("x", 100) {
		t.Fatalf("queued %d bytes, want the 100 held keys", len(got))
	}
}

func wakingNow(d *Daemon, id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.park.waking[id]
	return ok
}

// An agent not back at its input box after resumeReadyWait keeps what
// waits for it: a form on its screen never gets the held keys. The user
// is told, and new keys go straight in to answer the form; the held ones
// follow once the input box is back.
func TestParkedResumeTimeoutKeepsKeys(t *testing.T) {
	d, f := parkDaemon(t)
	park(t, d, f)
	ctx := context.Background()
	for _, k := range []string{"\r", "y", "\r"} {
		if err := d.handle(ctx, proto.Input{Pane: "a", Data: []byte(k)}); err != nil {
			t.Fatal(err)
		}
	}
	p := f.pane(len(f.panes) - 1)
	setScreen(p, []string{"Do you want to proceed?", "❯ 1. Yes", "  2. No"})
	claudeHook(t, d, "a", `{"hook_event_name":"SessionStart","source":"resume"}`)
	d.mu.Lock()
	d.park.waking["a"] = d.park.waking["a"].Add(-resumeReadyWait - time.Second)
	at := d.park.waking["a"]
	d.mu.Unlock()
	d.checkWaking()
	time.Sleep(20 * time.Millisecond)
	if got := p.got(); got != "" {
		t.Fatalf("typed %q into a form after the timeout", got)
	}
	d.mu.Lock()
	notice, still := d.st.Notice, d.park.waking["a"] == at
	d.mu.Unlock()
	if !still || !strings.Contains(notice, "not at its input box") {
		t.Fatalf("waking %v, notice %q; want the resume reported and still waiting", still, notice)
	}
	if err := d.handle(ctx, proto.Input{Pane: "a", Data: []byte("2")}); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, p, 0, 1); got != "2" {
		t.Fatalf("typed %q, want the new key alone", got)
	}
	setScreen(p, claudeEmpty)
	d.checkWaking()
	if got := gotAfter(t, p, 1, 2); got != "y\r" {
		t.Fatalf("typed %q, want the held keys once the box is back", got)
	}
}

// Failed checks and requested changes resume a parked agent and wait for
// it, as pitwall send does.
func TestParkedAutoSendResumes(t *testing.T) {
	for _, act := range []string{"failures", "review"} {
		t.Run(act, func(t *testing.T) {
			pr := model.PR{Number: 7, State: model.PROpen, Head: "h1"}
			if act == "failures" {
				pr.Checks = []model.Check{{Name: "test", State: model.CheckFail}}
			} else {
				pr.Review, pr.ChangeRequests = model.ReviewChanges, []string{"r1"}
			}
			d, f := parkDaemonWith(t, func(o *Options) {
				o.Branch = func(context.Context, string) (string, bool) { return "feature", true }
				o.AutoSendFailures = func() bool { return act == "failures" }
				o.SendReviewOnRequest = func() bool { return act == "review" }
				o.PR = func(context.Context, string) (*model.PR, error) { p := pr; return &p, nil }
				o.Head = func(context.Context, string) (string, error) { return "h1", nil }
				o.Failures = func(context.Context, string) ([]forge.FailedCheck, error) {
					return []forge.FailedCheck{{Name: "test", URL: "https://ci/1", Log: "boom"}}, nil
				}
				o.Reviews = func(context.Context, string, int) ([]forge.Thread, []forge.Comment, error) {
					return nil, []forge.Comment{{Author: "reviewer", Body: "Fix the test"}}, nil
				}
			})
			d.mu.Lock()
			d.st.Workspaces[0].Branch, d.st.Workspaces[0].Path = "feature", "/r"
			d.mu.Unlock()
			park(t, d, f)
			n := len(f.panes)
			d.pollPRs(context.Background(), time.Now().Add(time.Hour))
			d.pollPRs(context.Background(), time.Now().Add(2*time.Hour))
			d.mu.Lock()
			queued := len(d.mail.queue["a"])
			d.mu.Unlock()
			if len(f.panes) != n+1 || queued != 1 {
				t.Fatalf("%d panes started, %d queued; want the agent resumed and one message waiting", len(f.panes)-n, queued)
			}
			p := back(t, d, f)
			if got := gotAfter(t, p, 0, 40); !strings.Contains(got, "[pitwall: message from a script]") {
				t.Fatalf("typed %q, want the message", got)
			}
		})
	}
}

// Shutdown that logind cancels thaws the daemon; a parked pane, whose
// handle is done, stays parked and in its tab.
func TestParkedSurvivesThaw(t *testing.T) {
	d, f := parkDaemon(t)
	park(t, d, f)
	d.Freeze()
	d.Thaw()
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.isParked("a") || !slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == "a" }) {
		t.Fatal("the parked pane went with the thaw")
	}
}

// An agent that exits while it resumes parks again, so its message waits
// and nothing goes to a shell.
func TestParkedResumeFailsParksAgain(t *testing.T) {
	d, f := parkDaemon(t)
	park(t, d, f)
	n := len(f.panes)
	if err := d.send(proto.Send{Pane: "a", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	p := f.pane(n)
	p.code = 1
	p.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !parkedNow(d, "a") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !parkedNow(d, "a") {
		t.Fatal("not parked again after the failed resume")
	}
	if _, detail := stateOf(d, "a"); !strings.Contains(detail, "exited with code 1") {
		t.Fatalf("detail %q, want the exit", detail)
	}
	d.mu.Lock()
	queued := len(d.mail.queue["a"])
	d.mu.Unlock()
	if queued != 1 || len(f.panes) != n+1 {
		t.Fatalf("%d queued, %d panes; want the message kept and no shell", queued, len(f.panes))
	}
}

// A daemon restart keeps a parked pane parked, with no process.
func TestParkedAcrossRestart(t *testing.T) {
	d, f := parkDaemon(t)
	park(t, d, f)
	if err := d.saveState(false); err != nil {
		t.Fatal(err)
	}
	n := len(f.panes)
	o := f.options()
	d2, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	if !parkedNow(d2, "a") {
		t.Fatal("not parked after the restart")
	}
	if s, _ := stateOf(d2, "a"); s != model.StateParked {
		t.Fatalf("state %q after the restart", s)
	}
	// Pane b, a shell, starts again; a does not.
	if started := len(f.panes) - n; started != 1 {
		t.Fatalf("%d panes started, want only the shell", started)
	}
}

// A resume that outlasts sendQuiet still keeps a message from gluing onto
// the keys typed while it ran.
func TestParkedSlowResumeDraft(t *testing.T) {
	d, f := parkDaemon(t)
	oldQuiet, oldLook := sendQuiet.set(100*time.Millisecond), sendLook.set(10*time.Millisecond)
	t.Cleanup(func() {
		d.sendMu.Lock()
		sendQuiet.set(oldQuiet)
		sendLook.set(oldLook)
		d.sendMu.Unlock()
	})
	park(t, d, f)
	for _, k := range []string{"\r", "unfinished draft"} {
		if err := d.handle(context.Background(), proto.Input{Pane: "a", Data: []byte(k)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.send(proto.Send{Pane: "a", Text: "automated message"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // past sendQuiet before the agent is back
	p := back(t, d, f)
	time.Sleep(30 * time.Millisecond)
	if got := p.got(); got != "unfinished draft" {
		t.Fatalf("typed %q before the agent drew the keys", got)
	}
	setScreen(p, []string{"────────", "❯ unfinished draft", "────────"})
	time.Sleep(150 * time.Millisecond)
	if got := p.got(); got != "unfinished draft" {
		t.Fatalf("typed %q while the draft sat in the box", got)
	}
	setScreen(p, claudeEmpty)
	if got := gotAfter(t, p, len("unfinished draft"), 20); !strings.Contains(got, "automated message") {
		t.Fatalf("typed %q after the draft went, want the message", got)
	}
}
