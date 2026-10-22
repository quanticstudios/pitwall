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
			}}, model.CheckFail},
		{"draft.json", model.PR{Number: 9, State: model.PROpen, Draft: true, URL: "https://github.com/example/widgets/pull/9"}, model.CheckNone},
		{"merged.json", model.PR{Number: 118, State: model.PRMerged, URL: "https://github.com/example/widgets/pull/118", Review: model.ReviewApproved,
			Checks: []model.Check{{Name: "build", State: model.CheckPass}}}, model.CheckPass},
		{"closed.json", model.PR{Number: 77, State: model.PRClosed, URL: "https://github.com/example/widgets/pull/77", Review: model.ReviewRequired, Conflicts: true,
			Checks: []model.Check{{Name: "build", State: model.CheckFail}, {Name: "ci/jenkins", State: model.CheckFail}}}, model.CheckFail},
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
