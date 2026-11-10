package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// claudeEmpty is Claude Code's input box with nothing in it.
var claudeEmpty = []string{"────────", "❯ ", "────────"}

// parkDaemon is talkDaemon with park_idle_after at an hour and pane a a
// Claude Code agent at an empty box after a turn.
func parkDaemon(t *testing.T) (*Daemon, *fakes) {
	t.Helper()
	d, f := talkDaemon(t)
	d.o.ParkIdleAfter = func() time.Duration { return time.Hour }
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
// is back, then go in first, ahead of a message sent meanwhile. Focus
// reports resume nothing.
func TestParkedKeysWaitForTheAgent(t *testing.T) {
	d, f := parkDaemon(t)
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
	if got := gotAfter(t, p, 0, 10); !strings.HasPrefix(got, "ok") || !strings.Contains(got, "later") {
		t.Fatalf("typed %q, want the keys without the Enter, then the message", got)
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
