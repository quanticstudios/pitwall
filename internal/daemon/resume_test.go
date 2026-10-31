package daemon

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// fakeClock stands in for the resume clock: it is always now, and the
// timers it set are run by fire.
type fakeClock struct {
	now   time.Time
	waits []time.Duration
	fns   []func()
}

func useFakeClock(t *testing.T, now time.Time) *fakeClock {
	c := &fakeClock{now: now}
	oldNow, oldAfter := resumeNow, resumeAfter
	resumeNow = func() time.Time { return c.now }
	resumeAfter = func(wait time.Duration, f func()) func() bool {
		c.waits, c.fns = append(c.waits, wait), append(c.fns, f)
		return func() bool { return true }
	}
	t.Cleanup(func() { resumeNow, resumeAfter = oldNow, oldAfter })
	return c
}

// fire runs the last timer set, as if its time came.
func (c *fakeClock) fire() { c.fns[len(c.fns)-1]() }

// resumes is pane's Activity.Resumes as clients see it.
func resumes(d *Daemon, pane string) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.snapshot().Activities {
		if a.PaneID == pane {
			return a.Resumes
		}
	}
	return time.Time{}
}

const claudeLimit = `{"hook_event_name":"StopFailure","error":"rate_limit","last_assistant_message":"You've hit your session limit · resets 3pm (UTC)"}`

// A Claude turn that ends on its session limit types continue a minute
// after the reset, shown as the activity's Resumes until then.
func TestResumeAfterLimit(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
	d, f := talkDaemon(t)
	d.o.ResumeAfterLimit = func() bool { return true }
	claudeHook(t, d, "a", claudeLimit)
	want := time.Date(2026, 10, 22, 15, 1, 0, 0, time.UTC)
	if len(clock.waits) != 1 || clock.waits[0] != want.Sub(clock.now) {
		t.Fatalf("timers %v, want one of %v", clock.waits, want.Sub(clock.now))
	}
	if got := resumes(d, "a"); !got.Equal(want) {
		t.Fatalf("Resumes = %v, want %v", got, want)
	}
	clock.fire()
	if got := gotAfter(t, f.pane(0), 0, 9); got != "continue\r" {
		t.Fatalf("typed %q", got)
	}
	if got := resumes(d, "a"); !got.IsZero() {
		t.Fatalf("Resumes after it fired = %v", got)
	}
	d.mu.Lock()
	a := d.st.Activities[d.activityIndex("a")]
	d.mu.Unlock()
	if a.State != model.StateWorking || a.Detail != model.ResumedDetail {
		t.Fatalf("after the resume %+v, want working with ResumedDetail", a)
	}

	// Off, nothing is set.
	d.o.ResumeAfterLimit = func() bool { return false }
	claudeHook(t, d, "b", claudeLimit)
	if len(clock.fns) != 1 {
		t.Fatal("a resume was set with the setting off")
	}
}

// A resume is dropped when the user types in the pane, the turn changes,
// another agent takes the pane, or the user cancels it.
func TestResumeGuards(t *testing.T) {
	for _, tc := range []struct {
		name string
		then func(d *Daemon)
	}{
		{"user typed", func(d *Daemon) { d.noteUserKey("a", []byte("y")) }},
		{"turn changed", func(d *Daemon) {
			d.agentEvent(t.Context(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(`{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)})
		}},
		{"agent changed", func(d *Daemon) { d.mu.Lock(); d.setProvider("a", model.ProviderCodex); d.mu.Unlock() }},
		{"cancelled", func(d *Daemon) { d.handle(t.Context(), proto.CancelResume{Pane: "a"}) }},
		{"setting off", func(d *Daemon) { d.o.ResumeAfterLimit = func() bool { return false } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
			d, f := talkDaemon(t)
			d.o.ResumeAfterLimit = func() bool { return true }
			claudeHook(t, d, "a", claudeLimit)
			tc.then(d)
			if got := resumes(d, "a"); !got.IsZero() {
				t.Errorf("Resumes = %v after the guard", got)
			}
			// A timer that still fires types nothing.
			clock.fire()
			time.Sleep(50 * time.Millisecond) // typing reaches the pane asynchronously
			if got := f.pane(0).got(); got != "" {
				t.Errorf("typed %q", got)
			}
		})
	}
}

// Codex reports no limit through its hooks: its screen does, once per
// message. A message without a time resumes at the tracked limit's reset.
func TestResumeFromScreen(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
	d, f := talkDaemon(t)
	d.o.ResumeAfterLimit = func() bool { return true }
	tracked := time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC)
	d.o.Limits = func() []flow.Limit {
		return []flow.Limit{
			{Provider: model.ProviderClaude, Windows: []flow.Window{{Minutes: 300, Used: 100, Resets: tracked.Add(time.Hour)}}},
			{Provider: model.ProviderCodex, Windows: []flow.Window{{Minutes: 300, Used: 100, Resets: tracked}}},
		}
	}
	d.mu.Lock()
	d.setProvider("b", model.ProviderCodex)
	d.mu.Unlock()
	p := f.pane(1)
	show := func(rows ...string) {
		p.mu.Lock()
		p.screen = rows
		p.mu.Unlock()
		d.checkLimits()
	}
	show("■ You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage to",
		"purchase more credits or try again at 3:04 PM.", "", "› Ask Codex to do anything")
	want := time.Date(2026, 10, 22, 15, 5, 0, 0, time.UTC)
	if got := resumes(d, "b"); !got.Equal(want) {
		t.Fatalf("Resumes = %v, want %v", got, want)
	}
	d.mu.Lock()
	a := d.st.Activities[d.activityIndex("b")]
	d.mu.Unlock()
	if a.State != model.StateError || a.Provider != model.ProviderCodex {
		t.Fatalf("activity %+v, want a Codex error", a)
	}
	// Cancelled, the same message on screen sets nothing again.
	d.handle(t.Context(), proto.CancelResume{Pane: "b"})
	d.checkLimits()
	if len(clock.fns) != 1 {
		t.Fatalf("%d timers, want 1", len(clock.fns))
	}
	// It left the screen; a new one with no time takes Codex's tracked reset.
	show("› Ask Codex to do anything")
	show("■ You’ve hit your usage limit. Try again later.")
	if got := resumes(d, "b"); !got.Equal(tracked.Add(resumeDelay)) {
		t.Fatalf("Resumes = %v, want %v", got, tracked.Add(resumeDelay))
	}
	clock.fire()
	if got := gotAfter(t, p, 0, 9); got != "continue\r" {
		t.Fatalf("typed %q", got)
	}
}

// A cancel that comes while another typing holds the pane still drops
// the resume waiting behind it.
func TestResumeCancelWhileTyping(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
	d, f := talkDaemon(t)
	d.o.ResumeAfterLimit = func() bool { return true }
	claudeHook(t, d, "a", claudeLimit)
	d.sendMu.Lock()
	done := make(chan struct{})
	go func() { clock.fire(); close(done) }()
	time.Sleep(10 * time.Millisecond)
	d.handle(t.Context(), proto.CancelResume{Pane: "a"})
	d.sendMu.Unlock()
	<-done
	time.Sleep(50 * time.Millisecond) // typing reaches the pane asynchronously
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("typed %q", got)
	}
}

// A key typed after the turn stopped, such as a draft, and a reset
// already past set no resume. A limit message replaces an earlier
// error's Detail.
func TestResumeNotSet(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
	d, f := talkDaemon(t)
	d.o.ResumeAfterLimit = func() bool { return true }
	d.mu.Lock()
	d.setProvider("b", model.ProviderCodex)
	d.mu.Unlock()
	show := func(rows ...string) {
		p := f.pane(1)
		p.mu.Lock()
		p.screen = rows
		p.mu.Unlock()
		d.checkLimits()
	}
	d.agentEvent(t.Context(), proto.AgentEvent{Pane: "b", Provider: model.ProviderCodex, Payload: []byte(`{"hook_event_name":"Stop","last_assistant_message":"ok"}`)})
	time.Sleep(time.Millisecond) // why: the key must come after the stop on a coarse clock
	d.noteUserKey("b", []byte("d"))
	show("■ You’ve hit your usage limit. Try again at 3:04 PM.")
	if len(clock.fns) != 0 {
		t.Fatal("a resume was set over a draft")
	}

	d.agentEvent(t.Context(), proto.AgentEvent{Pane: "b", Provider: model.ProviderCodex, Payload: []byte(`{"hook_event_name":"Stop","last_assistant_message":"later"}`)})
	show("■ You’ve hit your usage limit. Try again at 1:10 PM.") // ten minutes ago
	if len(clock.fns) != 0 {
		t.Fatal("a resume was set for a past reset")
	}

	claudeHook(t, d, "a", `{"hook_event_name":"StopFailure","error":"server_error","last_assistant_message":"API Error: 500"}`)
	d.mu.Lock()
	d.setProvider("a", model.ProviderClaude)
	d.mu.Unlock()
	p := f.pane(0)
	p.mu.Lock()
	p.screen = []string{"  ⎿  You've hit your session limit · resets 3pm (UTC)"}
	p.mu.Unlock()
	d.checkLimits()
	d.mu.Lock()
	a := d.st.Activities[d.activityIndex("a")]
	d.mu.Unlock()
	if a.Detail != "You've hit your session limit · resets 3pm (UTC)" || resumes(d, "a").IsZero() {
		t.Fatalf("activity %+v, want the limit message and a resume", a)
	}
}

// TestRestartCarriesTurns: across a daemon restart, a resumed agent whose
// turn had finished stays done, so pitwall wait counts it, and one that
// was mid-turn gets restartLine typed, without a header, once its resume
// SessionStart arrives. A pane that does not resume keeps neither.
func TestRestartCarriesTurns(t *testing.T) {
	oldSettle := sendSettle.set(10 * time.Millisecond)
	t.Cleanup(func() { sendSettle.set(oldSettle) })
	f := &fakes{statsCalls: map[string]int{}}
	claude := model.ProviderClaude
	f.saved = model.State{
		Sessions: []model.Session{{ID: "s", Name: "work"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Name: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{
			Dir: layout.Horizontal, Children: []*layout.Node{{Pane: "done"}, {Pane: "mid"}, {Pane: "shell"}}}}}, ActiveTab: "t"}},
		Panes: []model.Pane{
			{ID: "done", WorkspaceID: "w", Provider: claude, SessionID: "s1"},
			{ID: "mid", WorkspaceID: "w", Provider: claude, SessionID: "s2"},
			{ID: "shell", WorkspaceID: "w"},
		},
		Activities: []model.Activity{
			{PaneID: "done", WorkspaceID: "w", Provider: claude, State: model.StateCompleted, Detail: "shipped"},
			{PaneID: "mid", WorkspaceID: "w", Provider: claude, State: model.StateWorking},
			{PaneID: "shell", WorkspaceID: "w", State: model.StateCompleted},
		},
	}
	o := f.options()
	o.Derive = agent.Derive
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	acts := d.st.Activities
	d.mu.Unlock()
	if len(acts) != 1 || acts[0].PaneID != "done" || acts[0].State != model.StateCompleted {
		t.Fatalf("activities after the restart: %+v, want done's finished turn alone", acts)
	}
	if got := f.pane(1).got(); got != "" {
		t.Fatalf("typed %q before the agent was back", got)
	}
	claudeHook(t, d, "mid", `{"session_id":"s2","hook_event_name":"SessionStart","source":"resume"}`)
	want := restartLine + "\r"
	if got := gotAfter(t, f.pane(1), 0, len(want)); got != want {
		t.Fatalf("mid got %q, want %q", got, want)
	}
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("done got %q", got)
	}
}
