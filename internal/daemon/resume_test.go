package daemon

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
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
			time.Sleep(10 * time.Millisecond)
			if got := f.pane(0).got(); got != "" {
				t.Errorf("typed %q", got)
			}
		})
	}
}

// Codex reports no limit through its hooks: its screen does, once per
// message. A message without a time resumes at the tracked limit's reset.
func TestResumeFromScreen(t *testing.T) {
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.UTC // Codex prints local time
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
