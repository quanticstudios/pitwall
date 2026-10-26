package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestAutoSendFailures: with [git] auto_send_failures on, a failed check
// goes to the tab's agent once per head, not while GitHub reports another
// head than the one checked out, not while the agent works, and at most
// autoSendMax times per branch.
func TestAutoSendFailures(t *testing.T) {
	d, f := talkDaemon(t)
	failing := []model.Check{{Name: "test", State: model.CheckFail}}
	answer := model.PR{Number: 7, State: model.PROpen, Checks: failing, Head: "h1"}
	on, local, fetches := false, "h1", 0
	d.o.PR = func(context.Context, string) (*model.PR, error) { pr := answer; return &pr, nil }
	d.o.AutoSendFailures = func() bool { return on }
	d.o.Head = func(context.Context, string) (string, error) { return local, nil }
	d.o.Failures = func(context.Context, string) ([]forge.FailedCheck, error) {
		fetches++
		return []forge.FailedCheck{{Name: "test", URL: "https://ci/1", Log: "boom"}}, nil
	}
	d.mu.Lock()
	d.st.Workspaces[0].Branch, d.st.Workspaces[0].Path = "feature", "/r"
	d.mu.Unlock()
	claudeHook(t, d, "a", `{"hook_event_name":"SessionStart","source":"startup"}`)

	t0 := time.Now()
	step := 0
	poll := func(want int) {
		t.Helper()
		step++
		d.pollPRs(context.Background(), t0.Add(time.Duration(step)*time.Hour)) // every answer due
		if fetches != want {
			t.Fatalf("step %d: %d sends, want %d", step, fetches, want)
		}
	}
	poll(0) // off
	on = true
	poll(1)
	want := "[pitwall: message from a script] These checks failed on pull request #7:"
	if got := gotAfter(t, f.pane(0), 0, len(want)); !strings.HasPrefix(got, want) {
		t.Fatalf("typed %q, want %q", got, want)
	}
	poll(1) // that head went
	answer.Head = "h2"
	poll(1) // GitHub's head is not the one checked out
	local = "h2"
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"fix"}`)
	poll(1) // working
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	n := len(f.pane(0).got())
	poll(2)
	if got := gotAfter(t, f.pane(0), n, len(want)); !strings.HasPrefix(got, want) {
		t.Fatalf("typed %q, want %q", got, want)
	}
	answer.Head, local = "h3", "h3"
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	poll(2) // the cap
}
