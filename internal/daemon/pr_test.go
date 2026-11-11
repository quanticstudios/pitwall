package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestPollPRs drives the PR poller by the clock: a new branch is asked
// about at once, an open PR again after a minute while a window has focus
// and after five with none, tabs in one folder share one gh call, a
// rate limit pauses every branch, and a merge archives a worktree tab
// when archive_on_merge is on and no other tab uses its folder.
func TestPollPRs(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	var calls []string
	answer := &model.PR{Number: 7, State: model.PROpen, Checks: []model.Check{{Name: "build", State: model.CheckPending}}}
	var answerErr error
	archive := false
	var removed []string
	o := f.options()
	o.PR = func(_ context.Context, dir string) (*model.PR, error) {
		calls = append(calls, dir)
		return answer, answerErr
	}
	o.ArchiveOnMerge = func() bool { return archive }
	o.RemoveWorktree = func(_ context.Context, root, path string, branch, _ bool) error {
		if branch {
			removed = append(removed, path)
		}
		return nil
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	wt := "/r/.worktrees/feature"
	d.st.Sessions = []model.Session{{ID: "s", Name: "s"}}
	d.st.Workspaces = []model.Workspace{
		{ID: "w", SessionID: "s", Branch: "feature", Path: wt, WorktreeRoot: "/r"},
		{ID: "w2", SessionID: "s", Branch: "feature", Path: wt}, // the PR tab create_pr opens there
		{ID: "x", SessionID: "s", Path: "/tmp"},                 // no branch: never asked
	}
	ctx := context.Background()
	t0 := time.Now()
	poll := func(after time.Duration, want int) {
		t.Helper()
		calls = nil
		d.pollPRs(ctx, t0.Add(after))
		d.worktreeJobs.Wait()
		if len(calls) != want {
			t.Fatalf("at %v: %d gh calls %q, want %d", after, len(calls), calls, want)
		}
	}
	pr := func(id string) (model.PR, bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		p, ok := d.st.PRs[id]
		return p, ok
	}

	poll(0, 1) // both tabs on the branch, one call
	if p, ok := pr("w2"); !ok || p.Number != 7 {
		t.Fatalf("PRs = %+v", d.st.PRs)
	}
	poll(4*time.Minute, 0) // no window has focus: every 5 minutes
	poll(5*time.Minute, 1)

	d.mu.Lock()
	d.clients[&client{focus: "p"}] = struct{}{}
	d.mu.Unlock()
	poll(5*time.Minute+30*time.Second, 0)
	poll(6*time.Minute, 1)

	answerErr = forge.ErrRateLimited
	poll(7*time.Minute, 1)
	if _, ok := pr("w"); !ok {
		t.Fatal("a rate limit dropped the PR")
	}
	answerErr = nil
	d.mu.Lock()
	d.st.Workspaces[1].Branch = "other" // a new branch is due at once, but for the pause
	d.mu.Unlock()
	poll(8*time.Minute, 0)
	if _, ok := pr("w2"); ok {
		t.Fatal("w2 kept its old branch's PR")
	}
	poll(22*time.Minute, 2)
	d.mu.Lock()
	d.st.Workspaces[1].Branch = "feature"
	d.mu.Unlock()

	answer = &model.PR{Number: 7, State: model.PRMerged}
	poll(23*time.Minute, 1)
	if p, _ := pr("w"); p.State != model.PRMerged || len(removed) != 0 {
		t.Fatalf("merged with archive_on_merge off: %+v, removed %q", p, removed)
	}
	poll(2*time.Hour, 0) // merged is final

	archive = true
	answer = &model.PR{Number: 7, State: model.PROpen}
	d.mu.Lock()
	d.pokePR("w")
	d.mu.Unlock()
	poll(2*time.Hour, 1)
	answer = &model.PR{Number: 7, State: model.PRMerged}
	poll(2*time.Hour+time.Minute, 1)
	if len(removed) != 0 {
		t.Fatalf("archive removed %q", removed)
	}
	d.mu.Lock()
	if !strings.Contains(d.st.Notice, "feature") {
		t.Errorf("archive refusal notice %q", d.st.Notice)
	}
	if d.workspace("w") == nil || d.workspace("w2") == nil {
		t.Fatalf("archive must keep a folder another tab uses: %+v", d.st.Workspaces)
	}
	d.removeWorkspace("w2")
	d.pokePR("w")
	d.mu.Unlock()
	answer = &model.PR{Number: 7, State: model.PROpen}
	poll(3*time.Hour, 1)
	answer = &model.PR{Number: 7, State: model.PRMerged}
	poll(3*time.Hour+time.Minute, 1)
	st := d.state()
	if !slices.Equal(removed, []string{wt}) || st.Workspace("w") != nil {
		t.Fatalf("unshared worktree not archived: %q", removed)
	}
}

// TestPollPRsErrors: a failing gh keeps the last PR and backs off; gh
// missing shows none.
func TestPollPRsErrors(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	n := 0
	var answerErr error
	o := f.options()
	o.PR = func(context.Context, string) (*model.PR, error) {
		n++
		if answerErr != nil {
			return nil, answerErr
		}
		return &model.PR{Number: 3, State: model.PROpen}, nil
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	d.st.Sessions = []model.Session{{ID: "s", Name: "s"}}
	d.st.Workspaces = []model.Workspace{{ID: "w", SessionID: "s", Branch: "b", Path: "/repo"}}
	d.clients[&client{focus: "p"}] = struct{}{}
	t0 := time.Now()
	ctx := context.Background()
	d.pollPRs(ctx, t0)
	answerErr = errors.New("network down")
	// Errors at 1m, 2m and 4m: each waits twice as long as the one before.
	for _, at := range []time.Duration{time.Minute, 90 * time.Second, 2 * time.Minute, 3 * time.Minute, 4 * time.Minute} {
		d.pollPRs(ctx, t0.Add(at))
	}
	if n != 4 {
		t.Fatalf("%d calls, want 4", n)
	}
	if _, ok := d.st.PRs["w"]; !ok {
		t.Fatal("an error dropped the PR")
	}
	answerErr = forge.ErrNoGH
	d.pollPRs(ctx, t0.Add(time.Hour))
	if _, ok := d.st.PRs["w"]; ok {
		t.Fatal("gh logged out still shows the PR")
	}
}

// Timer reads use the batch path and honor its shared budget before new tabs.
func TestPollPRsBatch(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.PR = func(context.Context, string) (*model.PR, error) {
		t.Fatal("timer used the individual PR reader")
		return nil, nil
	}
	calls := 0
	merged := false
	o.PRs = func(_ context.Context, dirs []string) map[string]forge.Result {
		calls++
		if len(dirs) != 2 {
			t.Fatalf("batch directories: %v", dirs)
		}
		result := map[string]forge.Result{}
		for i, dir := range dirs {
			state := model.PROpen
			if merged {
				state = model.PRMerged
			}
			result[dir] = forge.Result{PR: &model.PR{Number: i + 1, State: state}}
		}
		return result
	}
	now := time.Now()
	pause := time.Time{}
	o.PRWait = func(at time.Time, interval time.Duration) time.Duration {
		if at.Before(pause) {
			return pause.Sub(at)
		}
		return interval * 5
	}
	invalidated := 0
	o.PRInvalidate = func(string) { invalidated++ }
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	d.st.Sessions = []model.Session{{ID: "s"}}
	d.st.Workspaces = []model.Workspace{{ID: "a", SessionID: "s", Path: "/repo/a", Branch: "a"}, {ID: "b", SessionID: "s", Path: "/repo/b", Branch: "b"}}
	d.clients[&client{focus: "p"}] = struct{}{}
	d.pollPRs(context.Background(), now)
	if calls != 1 || len(d.st.PRs) != 2 {
		t.Fatalf("calls %d, PRs %v", calls, d.st.PRs)
	}
	d.pollPRs(context.Background(), now.Add(time.Minute))
	if calls != 1 {
		t.Fatal("daemon ignored the 20% slowdown")
	}
	pause = now.Add(time.Hour)
	d.pokePR("a")
	d.pokePR("b")
	if invalidated != 2 {
		t.Fatal("gh commands did not invalidate the repo snapshot")
	}
	d.pollPRs(context.Background(), now.Add(10*time.Minute))
	if calls != 1 || len(d.st.PRs) != 2 {
		t.Fatal("pause did not retain the current PRs")
	}
	merged = true
	d.pollPRs(context.Background(), pause)
	if calls != 2 || d.st.PRs["a"].State != model.PRMerged {
		t.Fatal("reset did not resume the batch")
	}
	d.pollPRs(context.Background(), pause.Add(time.Hour))
	if calls != 2 {
		t.Fatal("daemon polled a final merged PR")
	}
}
