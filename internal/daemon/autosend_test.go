package daemon

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
)

// autoSendDaemon keeps queued deliveries from outliving a test's timing
// overrides and gives stats refreshes the same branch as the PR fixture.
func autoSendDaemon(t *testing.T) (*Daemon, *fakes) {
	t.Helper()
	d, f := talkDaemon(t)
	d.o.Branch = func(context.Context, string) (string, bool) { return "feature", true }
	t.Cleanup(func() {
		d.mu.Lock()
		for _, timer := range d.mail.wake {
			timer.Stop()
		}
		d.mail.queue = map[string][]letter{}
		d.mu.Unlock()
		d.sendMu.Lock()
		d.sendMu.Unlock()
	})
	return d, f
}

// TestAutoSendFailures: with [git] auto_send_failures on, a failed check
// goes to the tab's agent once per head, not while GitHub reports another
// head than the one checked out, and queues only one message while the
// agent works. Ordinary tabs can receive more than two failing commits.
func TestAutoSendFailures(t *testing.T) {
	d, f := autoSendDaemon(t)
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

	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
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
	poll(0) // no agent
	claudeHook(t, d, "a", `{"hook_event_name":"SessionStart","source":"startup"}`)
	answer.Checks = []model.Check{{Name: "test", State: model.CheckPending}}
	poll(0)
	answer.Checks = failing
	poll(1)
	want := "[pitwall: message from a script] These checks failed on pull request #7:"
	if got := gotAfter(t, f.pane(0), 0, len(want)); !strings.HasPrefix(got, want) {
		t.Fatalf("typed %q, want %q", got, want)
	}
	poll(1) // that head went
	answer.Checks = []model.Check{{Name: "test", State: model.CheckPass}}
	poll(1)
	answer.Checks = failing
	poll(1) // a rerun on the same head cannot send twice
	answer.Head = "h2"
	poll(1) // GitHub's head is not the one checked out
	local = "h2"
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"fix"}`)
	poll(2) // working: one message queued
	poll(2) // repeated failure does not add another
	d.mu.Lock()
	queued := len(d.mail.queue["a"])
	d.mu.Unlock()
	if queued != 1 {
		t.Fatalf("%d queued messages, want 1", queued)
	}
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	n := len(f.pane(0).got())
	poll(2)
	if got := gotAfter(t, f.pane(0), n, len(want)); !strings.HasPrefix(got, want) {
		t.Fatalf("typed %q, want %q", got, want)
	}
	waitNotice(t, d, "Sent CI failures to claude")
	n = len(f.pane(0).got())
	answer.Head, local = "h3", "h3"
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	poll(3) // no per-branch cap outside fleet escalation
	gotAfter(t, f.pane(0), n, len(want))
	answer.Head, local = "h1", "h1"
	poll(3) // even an older head is remembered
	waitNotice(t, d, "Sent CI failures to claude")
}

// waitNotice waits for a queued delivery's success, which follows its Enter.
func waitNotice(t *testing.T, d *Daemon, want string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		d.mu.Lock()
		got := d.st.Notice
		d.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("notice %q, want %q", got, want)
		}
	}
}

// Reviews on one head are independent sends; one review is never resent
// when the head changes or GitHub temporarily reports an approval.
func TestAutoSendReviews(t *testing.T) {
	d, f := autoSendDaemon(t)
	answer := model.PR{Number: 7, State: model.PROpen, Head: "h1", Review: model.ReviewChanges, ChangeRequests: []string{"r1"}}
	on, fetches := false, 0
	d.o.PR = func(context.Context, string) (*model.PR, error) { pr := answer; return &pr, nil }
	d.o.SendReviewOnRequest = func() bool { return on }
	d.o.Reviews = func(context.Context, string, int) ([]forge.Thread, []forge.Comment, error) {
		fetches++
		return nil, []forge.Comment{{Author: "reviewer", Body: "Fix the test"}}, nil
	}
	d.st.Workspaces[0].Branch, d.st.Workspaces[0].Path = "feature", "/r"
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	step := 0
	poll := func(want int) {
		t.Helper()
		step++
		d.pollPRs(context.Background(), t0.Add(time.Duration(step)*time.Hour))
		if fetches != want {
			t.Fatalf("step %d: fetched %d, want %d", step, fetches, want)
		}
	}
	poll(0) // off
	on = true
	poll(0) // no agent
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"fix"}`)
	poll(1) // busy: queued, never typed
	poll(1)
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("typed while busy: %q", got)
	}
	d.mu.Lock()
	queued := len(d.mail.queue["a"])
	d.mu.Unlock()
	if queued != 1 {
		t.Fatalf("%d queued messages, want 1", queued)
	}
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	waitNotice(t, d, "Sent review comments to claude")
	if got := f.pane(0).got(); !strings.Contains(got, "Fix the test") {
		t.Fatalf("review prompt %q", got)
	}
	answer.Head = "h2"
	poll(1)
	answer.Review = model.ReviewApproved
	poll(1)
	answer.Review = model.ReviewChanges
	poll(1)
	answer.ChangeRequests = []string{"r1", "r2"}
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"fix"}`)
	poll(2) // another review on the same head
	poll(2)
	d.mu.Lock()
	queued = len(d.mail.queue["a"])
	d.mu.Unlock()
	if queued != 1 {
		t.Fatalf("%d queued messages, want 1", queued)
	}
}

// A failed/empty fetch and a stale GitHub answer do not consume a send.
// An overlapping request cannot start a second fetch.
func TestAutoSendRetryAndBusy(t *testing.T) {
	for _, mode := range []string{"error", "empty", "head moved", "poll error", "busy"} {
		t.Run(mode, func(t *testing.T) {
			d, _ := autoSendDaemon(t)
			answer := model.PR{Number: 7, State: model.PROpen, Head: "h1", Checks: []model.Check{{Name: "test", State: model.CheckFail}}}
			d.o.PR = func(context.Context, string) (*model.PR, error) { pr := answer; return &pr, nil }
			d.o.AutoSendFailures = func() bool { return true }
			local := "h1"
			d.o.Head = func(context.Context, string) (string, error) { return local, nil }
			var fetches atomic.Int32
			started, finish := make(chan struct{}), make(chan struct{})
			d.o.Failures = func(context.Context, string) ([]forge.FailedCheck, error) {
				n := fetches.Add(1)
				if n == 1 {
					switch mode {
					case "error":
						return nil, errors.New("network")
					case "empty":
						return nil, nil
					case "head moved":
						local = "h2"
					case "busy":
						close(started)
						<-finish
					}
				}
				return []forge.FailedCheck{{Name: "test", Log: "boom"}}, nil
			}
			d.st.Workspaces[0].Branch, d.st.Workspaces[0].Path = "feature", "/r"
			claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"fix"}`)
			now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
			if mode == "poll error" {
				d.o.AutoSendFailures = func() bool { return false }
				d.pollPRs(context.Background(), now) // keep a cached failing PR
				d.o.AutoSendFailures = func() bool { return true }
				d.o.PR = func(context.Context, string) (*model.PR, error) { return nil, errors.New("network") }
				d.pollPRs(context.Background(), now.Add(time.Hour))
				if fetches.Load() != 0 {
					t.Fatal("fetched after a failed poll")
				}
				d.o.PR = func(context.Context, string) (*model.PR, error) { pr := answer; return &pr, nil }
			} else if mode == "busy" {
				done := make(chan struct{})
				go func() { d.pollPRs(context.Background(), now); close(done) }()
				<-started
				d.pollPRs(context.Background(), now.Add(time.Hour))
				if n := fetches.Load(); n != 1 {
					t.Fatalf("%d overlapping fetches", n)
				}
				close(finish)
				<-done
			} else {
				d.pollPRs(context.Background(), now)
				d.mu.Lock()
				n := len(d.prs.sent[prKey("/r", "feature")])
				d.mu.Unlock()
				if n != 0 {
					t.Fatalf("consumed %d sends", n)
				}
			}
			local = "h1"
			d.pollPRs(context.Background(), now.Add(2*time.Hour))
			d.mu.Lock()
			n := len(d.mail.queue["a"])
			d.mu.Unlock()
			if n != 1 {
				t.Fatalf("%d queued messages, want 1", n)
			}
		})
	}
}

// CI and review automation share the one queued slot, even when the tab
// contains a second agent. The review waits for the CI delivery.
func TestAutoSendOneSlot(t *testing.T) {
	d, f := autoSendDaemon(t)
	answer := model.PR{Number: 7, State: model.PROpen, Head: "h1", Review: model.ReviewChanges, ChangeRequests: []string{"r1"}, Checks: []model.Check{{Name: "test", State: model.CheckPending}}}
	d.o.PR = func(context.Context, string) (*model.PR, error) { pr := answer; return &pr, nil }
	d.o.AutoSendFailures = func() bool { return true }
	d.o.SendReviewOnRequest = func() bool { return true }
	d.o.Head = func(context.Context, string) (string, error) { return "h1", nil }
	failures, reviews := 0, 0
	d.o.Failures = func(context.Context, string) ([]forge.FailedCheck, error) {
		failures++
		return []forge.FailedCheck{{Name: "test", Log: "boom"}}, nil
	}
	d.o.Reviews = func(context.Context, string, int) ([]forge.Thread, []forge.Comment, error) {
		reviews++
		return nil, []forge.Comment{{Body: "Fix this"}}, nil
	}
	d.st.Workspaces[0].Branch, d.st.Workspaces[0].Path = "feature", "/r"
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"fix"}`)
	claudeHook(t, d, "b", `{"hook_event_name":"SessionStart","source":"startup"}`)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	answer.Review = model.ReviewRequired
	d.pollPRs(context.Background(), now)
	if failures != 0 || reviews != 0 {
		t.Fatal("sent while pending without changes requested")
	}
	answer.State = model.PRClosed
	answer.Checks[0].State = model.CheckFail
	answer.Review = model.ReviewChanges
	d.pollPRs(context.Background(), now.Add(time.Hour))
	if failures != 0 || reviews != 0 {
		t.Fatal("sent for a closed PR")
	}
	answer.State = model.PROpen
	d.pollPRs(context.Background(), now.Add(2*time.Hour))
	d.pollPRs(context.Background(), now.Add(3*time.Hour))
	if failures != 1 || reviews != 0 {
		t.Fatalf("fetches %d/%d, want 1/0", failures, reviews)
	}
	d.mu.Lock()
	queued := len(d.mail.queue["a"]) + len(d.mail.queue["b"])
	d.mu.Unlock()
	if queued != 1 || f.pane(0).got() != "" || f.pane(1).got() != "" {
		t.Fatalf("%d queued messages, want one without typing", queued)
	}
	claudeHook(t, d, "a", `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`)
	d.pollPRs(context.Background(), now.Add(4*time.Hour))
	if f.pane(0).got() != "" {
		t.Fatal("sent over approval")
	}
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	waitNotice(t, d, "Sent CI failures to claude")
	d.pollPRs(context.Background(), now.Add(5*time.Hour))
	if failures != 1 || reviews != 1 {
		t.Fatalf("fetches %d/%d, want 1/1", failures, reviews)
	}
}
