package forge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestParse reads gh pr view --json output, written by hand in the shapes
// gh prints: check runs and commit statuses mixed, each state mapped.
func TestParse(t *testing.T) {
	for _, tc := range []struct {
		file string
		want model.PR
		ci   model.CheckState
	}{
		{"open.json", model.PR{Number: 123, State: model.PROpen, URL: "https://github.com/example/widgets/pull/123", Review: model.ReviewChanges,
			Checks: []model.Check{
				{Name: "build", State: model.CheckPass}, {Name: "test (ubuntu-latest)", State: model.CheckFail}, {Name: "lint", State: model.CheckPending},
				{Name: "deploy-preview", State: model.CheckSkip}, {Name: "ci/circleci: e2e", State: model.CheckPending}, {Name: "license/cla", State: model.CheckPass},
			}, Reason: model.ReasonChecksFailing, Detail: "test (ubuntu-latest)"}, model.CheckFail},
		{"draft.json", model.PR{Number: 9, State: model.PROpen, Draft: true, URL: "https://github.com/example/widgets/pull/9",
			Reason: model.ReasonChecksPending, Detail: "mergeability"}, model.CheckNone},
		{"merged.json", model.PR{Number: 118, State: model.PRMerged, URL: "https://github.com/example/widgets/pull/118", Review: model.ReviewApproved,
			Checks: []model.Check{{Name: "build", State: model.CheckPass}}, Reason: model.ReasonMerged, Detail: "merged"}, model.CheckPass},
		{"closed.json", model.PR{Number: 77, State: model.PRClosed, URL: "https://github.com/example/widgets/pull/77", Review: model.ReviewRequired, Conflicts: true,
			Checks: []model.Check{{Name: "build", State: model.CheckFail}, {Name: "ci/jenkins", State: model.CheckFail}}, Reason: model.ReasonClosed, Detail: "closed without merging"}, model.CheckFail},
	} {
		data, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.file, got, tc.want)
		}
		if ci := got.CI(); ci != tc.ci {
			t.Errorf("%s: CI %q, want %q", tc.file, ci, tc.ci)
		}
	}
	pr := model.PR{Checks: []model.Check{{Name: "a", State: model.CheckPass}, {Name: "b", State: model.CheckPending}, {Name: "c", State: model.CheckSkip}}}
	if ci := pr.CI(); ci != model.CheckPending {
		t.Errorf("pass and pending roll up to %q", ci)
	}
}

// TestReason: each rule of the reduction, first match winning.
func TestReason(t *testing.T) {
	const (
		green   = `"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"`
		pass    = `"statusCheckRollup":[{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"}]`
		commit  = `"commits":[{"committedDate":"2026-10-20T10:00:00Z"}]`
		before  = `"submittedAt":"2026-10-20T09:00:00Z"`
		after   = `"submittedAt":"2026-10-20T11:00:00Z"`
		approve = `"reviewDecision":"APPROVED","latestReviews":[{"author":{"login":"ann"},"state":"APPROVED",` + after + `}]`
	)
	for _, tc := range []struct {
		json   string
		reason model.Reason
		detail string
	}{
		{`"state":"OPEN","mergeable":"CONFLICTING","baseRefName":"main",` + approve, model.ReasonConflicting, "conflicts with main"},
		{`"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"DIRTY"`, model.ReasonConflicting, "conflicts with the base branch"},
		{`"state":"OPEN",` + green + `,"statusCheckRollup":[{"__typename":"StatusContext","context":"ci/a","state":"FAILURE"},{"__typename":"StatusContext","context":"ci/b","state":"ERROR"},` +
			`{"__typename":"CheckRun","name":"c","status":"COMPLETED","conclusion":"CANCELLED"},{"__typename":"CheckRun","name":"d","status":"COMPLETED","conclusion":"TIMED_OUT"}]`,
			model.ReasonChecksFailing, "ci/a, ci/b, c +1 more"},
		{`"state":"OPEN",` + green + `,` + pass + `,` + commit + `,"reviewDecision":"CHANGES_REQUESTED","latestReviews":[{"author":{"login":"bob"},"state":"CHANGES_REQUESTED",` + after + `}]`,
			model.ReasonChangesRequested, "by bob"},
		{`"state":"OPEN",` + green + `,` + pass + `,` + commit + `,"reviewDecision":"CHANGES_REQUESTED","latestReviews":[{"author":{"login":"bob"},"state":"CHANGES_REQUESTED",` + before + `}]`,
			model.ReasonChangesRequested, "by bob, awaiting re-review"},
		{`"state":"OPEN",` + green + `,"reviewDecision":"CHANGES_REQUESTED"`, model.ReasonChangesRequested, "changes requested"},
		{`"state":"OPEN",` + green + `,"latestReviews":[{"author":{"login":"cy"},"state":"CHANGES_REQUESTED",` + after + `}]`, model.ReasonChangesRequested, "by cy"},
		{`"state":"OPEN",` + green + `,"statusCheckRollup":[{"__typename":"CheckRun","name":"build","status":"IN_PROGRESS"}],` + approve, model.ReasonChecksPending, "build"},
		{`"state":"OPEN","mergeable":"UNKNOWN",` + pass + `,` + approve, model.ReasonChecksPending, "mergeability"},
		{`"state":"OPEN","isDraft":true,` + green + `,` + pass, model.ReasonReviewRequired, "draft"},
		{`"state":"OPEN",` + green + `,` + pass + `,"reviewDecision":"REVIEW_REQUIRED"`, model.ReasonReviewRequired, "no approving review"},
		{`"state":"OPEN",` + green + `,` + pass, model.ReasonReviewRequired, "no approving review"},
		{`"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED",` + pass + `,` + approve, model.ReasonReviewRequired, "merge blocked by branch protection"},
		{`"state":"OPEN",` + green + `,` + pass + `,` + approve, model.ReasonReady, "approved, checks green, mergeable"},
		// No review required by the repo: an approval among the reviews counts.
		{`"state":"OPEN",` + green + `,` + pass + `,"latestReviews":[{"author":{"login":"ann"},"state":"APPROVED",` + after + `}]`, model.ReasonReady, "approved, checks green, mergeable"},
		{`"state":"MERGED","mergeable":"CONFLICTING"`, model.ReasonMerged, "merged"},
	} {
		pr, err := Parse([]byte("{" + tc.json + "}"))
		if err != nil {
			t.Fatalf("%s: %v", tc.json, err)
		}
		if pr.Reason != tc.reason || pr.Detail != tc.detail {
			t.Errorf("%s:\n got %s %q\nwant %s %q", tc.json, pr.Reason, pr.Detail, tc.reason, tc.detail)
		}
	}
}

// TestFailed: what gh's exit and stderr mean.
func TestFailed(t *testing.T) {
	exit := func(code int) error {
		err := exec.Command("sh", "-c", "exit "+string(rune('0'+code))).Run()
		if err == nil {
			t.Fatal("no exit error")
		}
		return err
	}
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	args := []string{"pr", "view"}
	for _, tc := range []struct {
		stderr string
		err    error
		want   error
	}{
		{"no pull requests found for branch \"feature\"\n", exit(1), nil},
		{"GraphQL: API rate limit exceeded for user ID 1.\n", exit(1), ErrRateLimited},
		{"To get started with GitHub CLI, please run:  gh auth login\n", exit(4), ErrNoGH},
		{"", exit(4), ErrNoGH},
		{"none of the git remotes configured for this repository point to a known GitHub host.\n", exit(1), ErrNoGH},
		{"the 'example/widgets' repository has disabled issues\n", exit(1), ErrNoGH},
	} {
		err := failed(args, []byte(tc.stderr), tc.err)
		if !errors.Is(err, tc.want) || tc.want == nil && err != nil {
			t.Errorf("%q: %v; want %v", tc.stderr, err, tc.want)
		}
	}
	if err := failed(args, []byte("Post \"https://api.github.com/graphql\": dial tcp: i/o timeout\n"), exit(1)); err == nil || errors.Is(err, ErrNoGH) {
		t.Errorf("a network error is %v", err)
	}
}

// TestView runs a fake gh from PATH: it must ask for the fields with no
// prompt, in the branch's folder.
func TestView(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script")
	}
	bin, dir := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\n[ \"$*\" = 'pr view --json " + Fields + "' ] && [ \"$GH_PROMPT_DISABLED\" = 1 ] && [ \"$(pwd -P)\" = '" + must(filepath.EvalSymlinks(dir)) + "' ] || { echo \"bad call: $*\" >&2; exit 2; }\ncat '" + filepath.Join(must(filepath.Abs("testdata")), "merged.json") + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	pr, err := View(context.Background(), dir)
	if err != nil || pr == nil || pr.Number != 118 || pr.State != model.PRMerged {
		t.Fatalf("View = %+v, %v", pr, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := View(context.Background(), dir); !errors.Is(err, ErrNoGH) {
		t.Errorf("without gh: %v", err)
	}
}

// TestIssues reads gh issue list --json output newest first, and runs a
// fake gh that must be asked for open issues with the fields and the cap.
func TestIssues(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "issues.json"))
	if err != nil {
		t.Fatal(err)
	}
	is, err := ParseIssues(data)
	if err != nil || len(is) != 3 || is[0].Number != 57 || is[1].Number != 49 || is[2].Number != 41 {
		t.Fatalf("ParseIssues = %+v, %v", is, err)
	}
	if got, want := is[2].Prompt(), "GitHub issue #41: Crash when the config file is empty\nhttps://github.com/example/widgets/issues/41\n\nSteps:\n1. Empty config.toml\n2. Start"; got != want {
		t.Errorf("Prompt = %q, want %q", got, want)
	}
	if got, want := is[0].Prompt(), "GitHub issue #57: Login redirect loops on Safari\nhttps://github.com/example/widgets/issues/57"; got != want {
		t.Errorf("Prompt without a body = %q, want %q", got, want)
	}
	long := Issue{Number: 9, Title: "Huge", URL: "https://github.com/example/widgets/issues/9", Body: "a" + strings.Repeat("ü", 50000)} // 100 KB, byte MaxBody inside a rune
	if p := long.Prompt(); len(p) > MaxBody+200 || !utf8.ValidString(p) || !strings.HasSuffix(p, "(issue body cut here; read the rest at "+long.URL+")") {
		t.Errorf("a 100 KB body makes a %d-byte prompt, valid UTF-8 %v, ending %q", len(p), utf8.ValidString(p), p[max(0, len(p)-80):])
	}
	if _, err := ParseIssues([]byte("{")); err == nil {
		t.Error("bad JSON parsed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n[ \"$*\" = 'issue list --state open --limit 50 --json " + IssueFields + "' ] || { echo \"bad call: $*\" >&2; exit 2; }\ncat '" + filepath.Join(must(filepath.Abs("testdata")), "issues.json") + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if is, err := Issues(context.Background(), t.TempDir()); err != nil || len(is) != 3 || is[0].Number != 57 {
		t.Fatalf("Issues = %+v, %v", is, err)
	}
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

// TestNext is the polling schedule: an open PR every minute, quiet
// branches every five, errors backing off, everything five times slower
// with no window in focus, and a merged PR left alone.
func TestNext(t *testing.T) {
	open, merged, closed := &model.PR{State: model.PROpen}, &model.PR{State: model.PRMerged}, &model.PR{State: model.PRClosed}
	boom := errors.New("network")
	for _, tc := range []struct {
		name   string
		pr     *model.PR
		err    error
		fails  int
		hidden bool
		want   time.Duration
	}{
		{"open", open, nil, 0, false, time.Minute},
		{"open hidden", open, nil, 0, true, 5 * time.Minute},
		{"no PR", nil, nil, 0, false, 5 * time.Minute},
		{"closed hidden", closed, nil, 0, true, 25 * time.Minute},
		{"merged", merged, nil, 0, false, 24 * time.Hour},
		{"merged hidden", merged, nil, 0, true, 24 * time.Hour},
		{"no gh", nil, ErrNoGH, 0, false, 15 * time.Minute},
		{"rate limited hidden", nil, ErrRateLimited, 0, true, 15 * time.Minute},
		{"first error", open, boom, 1, false, time.Minute},
		{"third error", open, boom, 3, false, 4 * time.Minute},
		{"many errors", open, boom, 40, false, 30 * time.Minute},
		{"many errors hidden", open, boom, 40, true, 150 * time.Minute},
	} {
		if got := Next(tc.pr, tc.err, tc.fails, tc.hidden); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The same review survives pushes; a second request by the same author
// has its own identity even when the aggregate decision stays unchanged.
func TestChangeRequests(t *testing.T) {
	for _, tc := range []struct {
		body string
		want []string
	}{
		{`{"id":"r1","state":"CHANGES_REQUESTED"}`, []string{"r1"}},
		{`{"author":{"login":"ann"},"state":"CHANGES_REQUESTED","submittedAt":"2026-10-30T12:00:00Z"}`, []string{"ann:2026-10-30T12:00:00Z"}},
		{`{"author":{"login":"ann"},"state":"CHANGES_REQUESTED","submittedAt":"2026-10-30T13:00:00Z"}`, []string{"ann:2026-10-30T13:00:00Z"}},
		{`{"id":"r2","state":"APPROVED"}`, nil},
		{`{"id":"r3","state":"COMMENTED"}`, nil},
		{`{"id":"r4","state":"DISMISSED"}`, nil},
		{`{"state":"CHANGES_REQUESTED"}`, nil},
	} {
		pr, err := Parse([]byte(`{"state":"OPEN","latestReviews":[` + tc.body + `]}`))
		if err != nil || !reflect.DeepEqual(pr.ChangeRequests, tc.want) {
			t.Errorf("%s: requests %v, error %v, want %v", tc.body, pr.ChangeRequests, err, tc.want)
		}
	}
}

// TestMerge runs a fake gh that must be asked to merge the number by the
// method, in the branch's folder.
func TestMerge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script")
	}
	bin, dir := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\n[ \"$*\" = 'pr merge 41 --rebase' ] && [ \"$(pwd -P)\" = '" + must(filepath.EvalSymlinks(dir)) + "' ] || { echo \"bad call: $*\" >&2; exit 1; }\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := Merge(context.Background(), dir, 41, "rebase"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := Merge(context.Background(), dir, 41, "squash"); err == nil || !strings.Contains(err.Error(), "bad call: pr merge 41 --squash") {
		t.Fatalf("Merge by squash: %v, want gh's words", err)
	}
}
