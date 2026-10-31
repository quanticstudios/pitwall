package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/web"
)

// TestWebForge: the web's pull request commands act on the tab's pull
// request in its live folder, with pitwall's own message and gh's words
// on a failure; a merge polls the pull request again.
func TestWebForge(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	var dirs []string
	o.Failures = func(_ context.Context, dir string) ([]forge.FailedCheck, error) {
		dirs = append(dirs, dir)
		return []forge.FailedCheck{{Name: "linux", URL: "https://ci/1"}}, nil
	}
	o.Reviews = func(_ context.Context, dir string, n int) ([]forge.Thread, []forge.Comment, error) {
		dirs = append(dirs, dir)
		if n != 7 {
			return nil, nil, errors.New("gh api: HTTP 502")
		}
		return []forge.Thread{{Path: "a.go", Line: 3, Comments: []forge.Comment{{Author: "rev", Body: "why?"}}}}, nil, nil
	}
	var merged []int
	o.Merge = func(_ context.Context, dir string, n int) error {
		dirs = append(dirs, dir)
		merged = append(merged, n)
		return nil
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	d.st.Sessions = []model.Session{{ID: "s", Name: "s"}}
	d.st.Workspaces = []model.Workspace{{ID: "w", SessionID: "s", Branch: "fix", Path: "/r/.worktrees/fix"}, {ID: "x", SessionID: "s", Path: "/tmp"}}
	d.st.PRs = map[string]model.PR{"w": {Number: 7, State: model.PROpen}}
	d.prs.seen = map[string]*prSeen{prKey("/r/.worktrees/fix", "fix"): {}}
	h := chatHub{d}
	var hub web.Forge = h
	ctx := context.Background()

	if prs := hub.PRs(); prs["w"].Number != 7 || len(prs) != 1 {
		t.Fatalf("PRs = %v", prs)
	}
	text, err := hub.Prompt(ctx, "w", "failures")
	if err != nil || text != forge.FailuresPrompt(7, []forge.FailedCheck{{Name: "linux", URL: "https://ci/1"}}) {
		t.Fatalf("Prompt failures = %q, %v", text, err)
	}
	text, err = hub.Prompt(ctx, "w", "review")
	if err != nil || !strings.Contains(text, "a.go:3\nrev: why?") {
		t.Fatalf("Prompt review = %q, %v", text, err)
	}
	if _, _, err := hub.Reviews(ctx, "x"); code(err) != "not-found" {
		t.Fatalf("Reviews of a tab with no pull request: %v", err)
	}
	if err := hub.Merge(ctx, "w"); err != nil || len(merged) != 1 || merged[0] != 7 {
		t.Fatalf("Merge: %v, merged %v", err, merged)
	}
	if !d.prs.seen[prKey("/r/.worktrees/fix", "fix")].at.IsZero() {
		t.Fatal("the merge did not make the pull request due")
	}
	for _, dir := range dirs {
		if dir != "/r/.worktrees/fix" {
			t.Fatalf("ran in %q", dir)
		}
	}

	d.st.PRs = map[string]model.PR{"w": {Number: 8, State: model.PRMerged}}
	if err := hub.Merge(ctx, "w"); code(err) != "conflict" {
		t.Fatalf("Merge of a merged pull request: %v", err)
	}
	d.st.PRs = map[string]model.PR{"w": {Number: 8, State: model.PROpen}}
	if _, _, err := hub.Reviews(ctx, "w"); code(err) != "forge" || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("Reviews when gh fails: %v", err)
	}
}

// code is err's web result code, "" for none.
func code(err error) string {
	var c interface{ Code() string }
	if errors.As(err, &c) {
		return c.Code()
	}
	return ""
}
