package forge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestParseChecks keeps the failed and cancelled checks of gh pr checks,
// and finds the Actions job in each link.
func TestParseChecks(t *testing.T) {
	got, err := parseChecks(fixture(t, "checks.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []FailedCheck{
		{Name: "test (ubuntu-latest)", URL: "https://github.com/example/widgets/actions/runs/1001/job/2002"},
		{Name: "e2e", URL: "https://github.com/example/widgets/actions/runs/1002/job/2010"},
		{Name: "ci/jenkins", URL: "https://ci.example.com/job/widgets/77"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	for i, job := range []string{"2002", "2010", ""} {
		if j := jobID(got[i].URL); j != job {
			t.Errorf("%s: job %q, want %q", got[i].Name, j, job)
		}
	}
}

// TestLogTail keeps the last lines of gh run view --log-failed without
// the job, step and timestamp before each.
func TestLogTail(t *testing.T) {
	lines := strings.Split(logTail(string(fixture(t, "job.log")), LogLines), "\n")
	if len(lines) != LogLines {
		t.Fatalf("%d lines", len(lines))
	}
	if lines[0] != "line 53" || lines[LogLines-1] != "##[error]Process completed with exit code 1." {
		t.Errorf("first %q, last %q", lines[0], lines[LogLines-1])
	}
	if got := logTail(string(fixture(t, "job.log")), 1000); !strings.HasPrefix(got, "Current runner version: 2.337.0\nline 1\n") {
		t.Errorf("the head with its byte order mark: %q", got[:60])
	}
	if got := logTail("not gh's shape\n", 5); got != "not gh's shape" {
		t.Errorf("a plain line: %q", got)
	}
}

// TestFailuresPrompt lists every failed check, says which have no log,
// and cuts the logs from the top to stay under MaxPrompt.
func TestFailuresPrompt(t *testing.T) {
	log := logTail(string(fixture(t, "job.log")), LogLines)
	got := FailuresPrompt(123, []FailedCheck{
		{Name: "test (ubuntu-latest)", URL: "https://github.com/example/widgets/actions/runs/1001/job/2002", Log: log},
		{Name: "ci/jenkins", URL: "https://ci.example.com/job/widgets/77"},
	})
	want := "These checks failed on pull request #123:\n" +
		"- test (ubuntu-latest): https://github.com/example/widgets/actions/runs/1001/job/2002\n" +
		"- ci/jenkins: https://ci.example.com/job/widgets/77 (no log)\n" +
		"\nThe end of test (ubuntu-latest)'s log:\n```\n" + log + "\n```\n" +
		"\nFix these and push."
	if got != want {
		t.Errorf("got\n%s", got)
	}

	var big strings.Builder
	for i := range 5000 {
		fmt.Fprintf(&big, "line %d of a long log\n", i)
	}
	huge := big.String() + "the last line"
	got = FailuresPrompt(9, []FailedCheck{{Name: "a", Log: huge}, {Name: "b", Log: huge}, {Name: "c"}})
	if len(got) > MaxPrompt {
		t.Errorf("%d bytes, over %d", len(got), MaxPrompt)
	}
	if len(got) < MaxPrompt*9/10 {
		t.Errorf("%d bytes: the cap left most of the room unused", len(got))
	}
	if strings.Count(got, "the last line\n```") != 2 || !strings.Contains(got, "- c (no log)\n") || !strings.HasSuffix(got, "Fix these and push.") {
		t.Errorf("cut prompt:\n%s", got)
	}
	if strings.Contains(got, "```\nline 0 ") || strings.Contains(got, "```\nne ") {
		t.Error("a cut log starts at its head or mid-line")
	}
}

// TestReviews keeps unresolved threads, an outdated one at its old line,
// and only the reviews requesting changes that say something.
func TestReviews(t *testing.T) {
	threads, changes, err := parseReviews(fixture(t, "reviews.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := ReviewPrompt(123, threads, changes)
	want := "Reviewers asked for changes on pull request #123.\n" +
		"\nalice requested changes:\nPlease handle errors before merging.\n" +
		"\ninternal/widget/widget.go:42\nalice: This leaks the file handle.\nbob: Agreed, close it in a defer.\n" +
		"\ninternal/widget/old.go:17\ncarol: Why is this still here?\n" +
		"\nFix these and push."
	if got != want {
		t.Errorf("got\n%s", got)
	}

	threads, changes, err = parseReviews(fixture(t, "reviews-none.json"))
	if err != nil || len(threads) != 0 || len(changes) != 0 {
		t.Errorf("nothing unresolved: %v %v %v", threads, changes, err)
	}
	if p := ReviewPrompt(1, threads, changes); p != "" {
		t.Errorf("nothing to address: %q", p)
	}

	many := make([]Thread, 2000)
	for i := range many {
		many[i] = Thread{Path: "a.go", Line: i + 1, Comments: []Comment{{Author: "alice", Body: "Rename this, please. É"}}}
	}
	got = ReviewPrompt(5, many, nil)
	if len(got) > MaxPrompt || !strings.HasSuffix(got, "[more comments cut]\n\nFix these and push.") {
		t.Errorf("%d bytes, ends %q", len(got), got[len(got)-60:])
	}
}

// TestFailures runs a fake gh: pr checks fails with its JSON on stdout,
// one job's log reads, and another's does not.
func TestFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script")
	}
	bin, td := t.TempDir(), must(filepath.Abs("testdata"))
	script := `#!/bin/sh
case "$*" in
"pr checks --json name,bucket,link") cat '` + td + `/checks.json'; exit 1 ;;
"run view --job 2002 --log-failed") cat '` + td + `/job.log' ;;
*) echo "no log for $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := Failures(context.Background(), t.TempDir())
	if err != nil || len(got) != 3 {
		t.Fatalf("Failures = %+v, %v", got, err)
	}
	if !strings.HasSuffix(got[0].Log, "exit code 1.") || got[1].Log != "" || got[2].Log != "" {
		t.Errorf("logs %q %q %q", got[0].Log[len(got[0].Log)-20:], got[1].Log, got[2].Log)
	}
}
