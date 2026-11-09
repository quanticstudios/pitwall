package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// quietSince is pane's Activity.QuietSince as clients see it.
func quietSince(d *Daemon, pane string) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.snapshot().Activities {
		if a.PaneID == pane {
			return a.QuietSince
		}
	}
	return time.Time{}
}

// age moves pane's last screen change back by ago.
func age(d *Daemon, pane string, ago time.Duration) {
	d.mu.Lock()
	d.quiet[pane].since = d.quiet[pane].since.Add(-ago)
	d.mu.Unlock()
}

// A working agent whose screen stays still past quiet_after gets
// QuietSince; a change on screen clears it, and so does the turn ending.
func TestQuiet(t *testing.T) {
	d, f := talkDaemon(t)
	d.o.QuietAfter = func() time.Duration { return 20 * time.Minute }
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)
	d.checkQuiet()
	if got := quietSince(d, "a"); !got.IsZero() {
		t.Fatalf("QuietSince = %v at the first look", got)
	}
	age(d, "a", 21*time.Minute)
	d.checkQuiet()
	if got := quietSince(d, "a"); got.IsZero() || time.Since(got) < 20*time.Minute {
		t.Fatalf("QuietSince = %v after 21 still minutes", got)
	}
	f.pane(0).mu.Lock()
	f.pane(0).screen = []string{"new output"}
	f.pane(0).mu.Unlock()
	d.checkQuiet()
	if got := quietSince(d, "a"); !got.IsZero() {
		t.Fatalf("QuietSince = %v after the screen changed", got)
	}
	age(d, "a", 21*time.Minute)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop"}`)
	d.checkQuiet()
	if got := quietSince(d, "a"); !got.IsZero() {
		t.Fatalf("QuietSince = %v after the turn ended", got)
	}
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("typed %q with continue_when_quiet off", got)
	}
}

// With continue_when_quiet, a quiet agent gets continue typed once per
// stretch of work, never over a form, and again only after the user typed.
func TestContinueWhenQuiet(t *testing.T) {
	d, f := talkDaemon(t)
	d.o.QuietAfter = func() time.Duration { return 20 * time.Minute }
	d.o.ContinueWhenQuiet = func() bool { return true }
	p := f.pane(0)
	quiet := func() {
		t.Helper()
		d.checkQuiet()
		age(d, "a", 21*time.Minute)
		d.checkQuiet()
	}
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)

	p.mu.Lock()
	p.screen = []string{"Do you want to proceed?", "1. Yes"}
	p.mu.Unlock()
	quiet()
	time.Sleep(50 * time.Millisecond) // typing reaches the pane asynchronously
	if got := p.got(); got != "" {
		t.Fatalf("typed %q over a form", got)
	}

	d.mu.Lock()
	delete(d.quiet, "a") // a new stretch
	d.mu.Unlock()
	p.mu.Lock()
	p.screen = []string{"> "}
	p.mu.Unlock()
	quiet()
	if got := gotAfter(t, p, 0, 9); got != "continue\r" {
		t.Fatalf("typed %q, want continue", got)
	}
	quiet()
	time.Sleep(50 * time.Millisecond)
	if got := p.got(); got != "continue\r" {
		t.Fatalf("typed %q, want one continue", got)
	}
	d.noteUserKey("a", []byte("y"))
	quiet()
	if got := gotAfter(t, p, 9, 9); !strings.HasSuffix(got, "continue\r") {
		t.Fatalf("typed %q after the user's key, want a second continue", got)
	}
}

// pitwall status sets a tab's line on one line, cut to statusRunes, and
// an empty text clears it.
// A quiet agent with the user's draft in its box gets no continue.
func TestContinueWhenQuietDraft(t *testing.T) {
	d, f := talkDaemon(t)
	d.o.QuietAfter = func() time.Duration { return 20 * time.Minute }
	d.o.ContinueWhenQuiet = func() bool { return true }
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)
	setScreen(f.pane(0), claudeBox)
	d.noteUserKey("a", []byte("t"))
	d.checkQuiet()
	age(d, "a", 21*time.Minute)
	d.checkQuiet()
	time.Sleep(50 * time.Millisecond)
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("typed %q into the draft", got)
	}
}

func TestQuietRecoversMissingStop(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			d, f := talkDaemon(t)
			d.o.QuietAfter = func() time.Duration { return time.Minute }
			lp := &livePane{fakePane: f.pane(0)}
			d.mu.Lock()
			d.panes["a"] = lp
			d.mu.Unlock()
			screen := "────────────────\n❯\n────────────────"
			if provider == model.ProviderCodex {
				screen = "›\n\n  ? for shortcuts"
			}
			lp.show(screen, false)
			must(t, d.agentEvent(t.Context(), proto.AgentEvent{Pane: "a", Provider: provider,
				Payload: []byte(`{"hook_event_name":"UserPromptSubmit","prompt":"go","session_id":"main-session"}`)}))
			d.checkQuiet()
			age(d, "a", 2*time.Minute)
			if provider == model.ProviderClaude {
				claudeHook(t, d, "a", `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`)
			}
			d.checkQuiet()
			if got := d.stateOf("a"); got != model.StateCompleted {
				t.Fatalf("missing Stop left state %q at a quiet input box", got)
			}
			if sid := d.activityOf("a").SessionID; sid != "main-session" {
				t.Fatalf("quiet completion lost session ID: %q", sid)
			}
			if got := lp.got(); got != "" {
				t.Fatalf("recovery typed %q", got)
			}
		})
	}
}

func TestQuietRecoveryKeepsWorkingEvidence(t *testing.T) {
	box := "────────────────\n❯\n────────────────"
	for _, tc := range []struct {
		name, screen, hook string
		moving             bool
	}{
		{name: "spinner", screen: "✢ Nesting… (9s · ↓ 232 tokens)\n" + box},
		{name: "tool", screen: box + "\nesc to interrupt"},
		{name: "form", screen: box + "\nDo you want to proceed?"},
		{name: "no box", screen: "a silent tool"},
		{name: "streaming", screen: box, moving: true},
		{name: "subagent", screen: box, hook: `{"hook_event_name":"Stop","background_tasks":[{"type":"subagent","status":"running"}]}`},
		{name: "workflow", screen: box, hook: `{"hook_event_name":"Stop","background_tasks":[{"type":"workflow","status":"running"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, f := talkDaemon(t)
			d.o.QuietAfter = func() time.Duration { return time.Minute }
			lp := &livePane{fakePane: f.pane(0)}
			d.mu.Lock()
			d.panes["a"] = lp
			d.mu.Unlock()
			lp.show(tc.screen, tc.moving)
			claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)
			if tc.hook != "" {
				claudeHook(t, d, "a", tc.hook)
				claudeHook(t, d, "a", `{"hook_event_name":"SubagentStop"}`)
				claudeHook(t, d, "a", `{"hook_event_name":"PostToolUse","transcript_path":null}`)
				claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"/model"}`)
				claudeHook(t, d, "a", `{"hook_event_name":"SessionStart","source":"compact"}`)
			}
			d.checkQuiet()
			age(d, "a", 2*time.Minute)
			d.checkQuiet()
			if got := d.stateOf("a"); got != model.StateWorking {
				t.Fatalf("working evidence became %q", got)
			}
			if tc.hook != "" {
				claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"next task"}`)
				d.checkQuiet()
				if got := d.stateOf("a"); got != model.StateWorking || !quietSince(d, "a").IsZero() {
					t.Fatalf("new prompt did not restart silence: %q", got)
				}
				age(d, "a", 2*time.Minute)
				d.checkQuiet()
				if got := d.stateOf("a"); got != model.StateCompleted {
					t.Fatalf("new turn kept the background veto: %q", got)
				}
			}
		})
	}
}

func TestQuietRecoveryAfterBackgroundWork(t *testing.T) {
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		t.Run(event, func(t *testing.T) {
			d, f := talkDaemon(t)
			d.o.QuietAfter = func() time.Duration { return time.Minute }
			lp := &livePane{fakePane: f.pane(0)}
			d.mu.Lock()
			d.panes["a"] = lp
			d.mu.Unlock()
			lp.show("────────────────\n❯\n────────────────", false)
			claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)
			claudeHook(t, d, "a", `{"hook_event_name":"Stop","background_tasks":[{"type":"subagent","status":"running"}]}`)
			d.checkQuiet()
			age(d, "a", 2*time.Minute)
			claudeHook(t, d, "a", `{"hook_event_name":"`+event+`"}`)
			d.checkQuiet()
			if got := d.stateOf("a"); got != model.StateWorking || !quietSince(d, "a").IsZero() {
				t.Fatalf("resumed work did not restart silence: %q", got)
			}
			age(d, "a", 2*time.Minute)
			d.checkQuiet()
			if got := d.stateOf("a"); got != model.StateCompleted {
				t.Fatalf("resumed main turn kept the background veto: %q", got)
			}
		})
	}
}

func TestQuietRecoveryWaitsAfterHook(t *testing.T) {
	d, f := talkDaemon(t)
	d.o.QuietAfter = func() time.Duration { return time.Minute }
	lp := &livePane{fakePane: f.pane(0)}
	d.mu.Lock()
	d.panes["a"] = lp
	d.mu.Unlock()
	lp.show("────────────────\n❯\n────────────────", false)
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"go"}`)
	d.checkQuiet()
	age(d, "a", 2*time.Minute)
	claudeHook(t, d, "a", `{"hook_event_name":"PostToolUse"}`)
	d.checkQuiet()
	if got := d.stateOf("a"); got != model.StateWorking || !quietSince(d, "a").IsZero() {
		t.Fatalf("hook did not restart silence: %q", got)
	}
}

func TestSetStatus(t *testing.T) {
	d, _ := talkDaemon(t)
	status := func() string {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.workspace("w").Status
	}
	if err := d.handle(t.Context(), proto.SetStatus{WorkspaceID: "w", Text: "CI\n 3/7 "}); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "CI 3/7" {
		t.Fatalf("status %q", got)
	}
	d.handle(t.Context(), proto.SetStatus{WorkspaceID: "w", Text: strings.Repeat("x", 200)})
	if got := []rune(status()); len(got) != statusRunes || got[statusRunes-1] != '…' {
		t.Fatalf("long status %q", string(got))
	}
	d.handle(t.Context(), proto.SetStatus{WorkspaceID: "w"})
	if got := status(); got != "" {
		t.Fatalf("cleared status %q", got)
	}
	if err := d.handle(t.Context(), proto.SetStatus{WorkspaceID: "nope", Text: "x"}); err == nil {
		t.Fatal("a missing tab took a status")
	}
}
