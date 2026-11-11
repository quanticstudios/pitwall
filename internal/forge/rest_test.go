package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

type fakeResponse struct {
	Body, ETag, Link               string
	Status, Limit, Remaining, Exit int
	Reset                          int64
}

// TestRESTHelper is the fake gh subprocess, with responses controlled on disk.
func TestRESTHelper(t *testing.T) {
	root := os.Getenv("FORGE_FAKE")
	if root == "" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	graphql := len(args) > 1 && args[0] == "api" && args[1] == "graphql"
	path := ""
	if graphql {
		for _, arg := range args {
			if strings.HasPrefix(arg, "number=") {
				path = "graphql-decision/" + strings.TrimPrefix(arg, "number=")
			}
		}
	} else {
		if len(args) < 5 || strings.Join(args[:4], " ") != "api --include --method GET" {
			os.Exit(2)
		}
		path = strings.TrimPrefix(args[4], "/")
	}
	f, err := os.OpenFile(filepath.Join(root, "calls"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(3)
	}
	fmt.Fprintln(f, path)
	f.Close()
	key := strings.NewReplacer("/", "_", "?", "_", "&", "_", ":", "_").Replace(path)
	data, err := os.ReadFile(filepath.Join(root, key))
	if err != nil {
		fmt.Fprintln(os.Stderr, "unexpected endpoint", path)
		os.Exit(2)
	}
	var r fakeResponse
	if json.Unmarshal(data, &r) != nil {
		os.Exit(3)
	}
	if graphql {
		fmt.Print(r.Body)
		if r.Exit != 0 {
			fmt.Fprintln(os.Stderr, "GraphQL: API rate limit exceeded")
		}
		os.Exit(r.Exit)
	}
	if r.Status == 0 {
		r.Status = 200
	}
	for _, arg := range args[5:] {
		if r.ETag != "" && arg == "If-None-Match: "+r.ETag {
			r.Status = 304
			r.Exit = 1
		}
	}
	fmt.Printf("HTTP/2.0 %d %s\r\n", r.Status, "response")
	if r.ETag != "" {
		fmt.Printf("ETag: %s\r\n", r.ETag)
	}
	if r.Link != "" {
		fmt.Printf("Link: %s\r\n", r.Link)
	}
	if r.Limit > 0 {
		fmt.Printf("X-RateLimit-Limit: %d\r\nX-RateLimit-Remaining: %d\r\nX-RateLimit-Reset: %d\r\n", r.Limit, r.Remaining, r.Reset)
	}
	fmt.Print("\r\n")
	if r.Status != 304 {
		fmt.Print(r.Body)
	}
	os.Exit(r.Exit)
}

type restFake struct {
	t    *testing.T
	root string
}

func newRESTFake(t *testing.T) *restFake {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh uses sh")
	}
	f := &restFake{t, t.TempDir()}
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\"'\"'") + "' -test.run '^TestRESTHelper$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_FAKE", f.root)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}
func (f *restFake) set(path string, r fakeResponse) {
	f.t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		f.t.Fatal(err)
	}
	key := strings.NewReplacer("/", "_", "?", "_", "&", "_", ":", "_").Replace(path)
	if err := os.WriteFile(filepath.Join(f.root, key), data, 0600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *restFake) calls(prefix string) int {
	data, _ := os.ReadFile(filepath.Join(f.root, "calls"))
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" && strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}
func (f *restFake) repo(branch string) string {
	f.t.Helper()
	dir := f.t.TempDir()
	for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "config", "remote.origin.url", "git@github.com:example/widgets.git"}, {"-C", dir, "symbolic-ref", "HEAD", "refs/heads/" + branch}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			f.t.Fatalf("git: %s %v", out, err)
		}
	}
	return dir
}
func pullJSON(number int, branch, repo, sha, updated, state string) string {
	return fmt.Sprintf(`{"number":%d,"state":%q,"html_url":"https://github.com/example/widgets/pull/%d","updated_at":%q,"mergeable":true,"mergeable_state":"clean","head":{"ref":%q,"sha":%q,"repo":{"full_name":%q}},"base":{"ref":"main"}}`, number, state, number, updated, branch, sha, repo)
}

const pullsURL = "repos/example/widgets/pulls?state=open&per_page=100"

func (f *restFake) details(number int, branch, sha, updated string) {
	f.decision(number, "APPROVED")
	f.set("repos/example/widgets/pulls/"+strconv.Itoa(number), fakeResponse{Body: pullJSON(number, branch, "example/widgets", sha, updated, "open"), ETag: updated + sha})
	f.set("repos/example/widgets/pulls/"+strconv.Itoa(number)+"/reviews?per_page=100", fakeResponse{Body: `[{"id":1,"node_id":"review1","state":"APPROVED","user":{"login":"reviewer"},"submitted_at":"2026-01-02T00:00:00Z"}]`, ETag: "reviews"})
	f.set("repos/example/widgets/git/commits/"+sha, fakeResponse{Body: `{"committer":{"date":"2026-01-01T00:00:00Z"}}`, ETag: sha})
	f.checks(sha, "completed")
}
func (f *restFake) checks(sha, status string) {
	f.set("repos/example/widgets/commits/"+sha+"/check-runs?per_page=100", fakeResponse{Body: `{"check_runs":[{"name":"build","status":"` + status + `","conclusion":"success"}]}`, ETag: sha + status})
	f.set("repos/example/widgets/commits/"+sha+"/status?per_page=100", fakeResponse{Body: `{"statuses":[{"context":"external","state":"success"}]}`, ETag: "status"})
}

func TestRESTBatchAndConditional(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	now := time.Now()
	p.now = func() time.Time { return now }
	var dirs []string
	var pulls []string
	for i := 1; i <= 12; i++ {
		branch := "branch-" + strconv.Itoa(i)
		dir := f.repo(branch)
		if i == 2 {
			if err := exec.Command("git", "-C", dir, "config", "remote.origin.url", "git@github.com-work:example/widgets.git").Run(); err != nil {
				t.Fatal(err)
			}
		}
		dirs = append(dirs, dir)
		pulls = append(pulls, pullJSON(i, branch, "example/widgets", "sha"+branch, "u1", "open"))
		f.details(i, branch, "sha"+branch, "u1")
	}
	f.set(pullsURL, fakeResponse{Body: "[" + strings.Join(pulls, ",") + "]", ETag: "open1"})
	got := p.Poll(context.Background(), dirs)
	for _, dir := range dirs {
		if r := got[dir]; r.Err != nil || r.PR == nil || r.PR.Reason != model.ReasonReady {
			t.Fatalf("poll: %+v", r)
		}
	}
	if n := f.calls(pullsURL); n != 1 {
		t.Fatalf("%d list calls for 12 branches", n)
	}
	before := f.calls("repos/example/widgets/commits/")
	// Separate batches inside a minute also share the list.
	p.Poll(context.Background(), dirs[:1])
	if f.calls(pullsURL) != 1 {
		t.Fatal("another tab caused another repo poll")
	}
	now = now.Add(time.Minute)
	got = p.Poll(context.Background(), dirs)
	if got[dirs[0]].PR.Number != 1 || got[dirs[0]].Err != nil {
		t.Fatalf("304 lost result: %+v", got[dirs[0]])
	}
	if f.calls(pullsURL) != 2 {
		t.Fatal("repo did not revalidate")
	}
	if n := f.calls("repos/example/widgets/commits/"); n != before {
		t.Fatalf("finished SHA fetched again: %d -> %d", before, n)
	}
	if n := f.calls("repos/example/widgets/pulls/1/reviews"); n != 1 {
		t.Fatalf("unchanged reviews fetched %d times", n)
	}
}

func TestRESTMovedAndPendingChecks(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	dir := f.repo("feature")
	now := time.Now()
	p.now = func() time.Time { return now }
	set := func(sha, updated string) {
		f.set(pullsURL, fakeResponse{Body: "[" + pullJSON(1, "feature", "example/widgets", sha, updated, "open") + "]", ETag: sha + updated})
		f.details(1, "feature", sha, updated)
	}
	set("a", "u1")
	f.checks("a", "in_progress")
	r := p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.CI() != model.CheckPending {
		t.Fatalf("pending: %+v", r)
	}
	now = now.Add(time.Minute)
	p.Poll(context.Background(), []string{dir})
	if f.calls("repos/example/widgets/commits/a/check-runs") != 2 {
		t.Fatal("pending checks did not revalidate")
	}
	f.checks("a", "completed")
	now = now.Add(time.Minute)
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.CI() != model.CheckPass {
		t.Fatalf("complete: %+v", r)
	}
	now = now.Add(time.Minute)
	p.Poll(context.Background(), []string{dir})
	if f.calls("repos/example/widgets/commits/a/check-runs") != 3 {
		t.Fatal("finished checks revalidated")
	}
	set("b", "u2")
	now = now.Add(time.Minute)
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.Head != "b" {
		t.Fatalf("moved head: %+v", r)
	}
	now = now.Add(time.Minute)
	p.Poll(context.Background(), []string{dir})
	if f.calls("repos/example/widgets/commits/b/check-runs") != 1 {
		t.Fatal("moved SHA must fetch checks once")
	}
}

func TestRESTForkPaginationAndMerged(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	dir := f.repo("feature")
	f.set(pullsURL, fakeResponse{Body: "[" + pullJSON(90, "feature", "fork/widgets", "fork", "u", "open") + "," + pullJSON(91, "foreign", "fork/widgets", "fork2", "u", "open") + "]", ETag: "page1", Link: `<https://api.github.com/repos/example/widgets/pulls?state=open&per_page=100&page=2>; rel="next"`})
	foreign := f.repo("foreign")
	f.set("repos/example/widgets/pulls?state=all&head=example%3Aforeign&per_page=100", fakeResponse{Body: `[]`})
	page2 := pullsURL + "&page=2"
	f.set(page2, fakeResponse{Body: "[" + pullJSON(1, "feature", "example/widgets", "a", "u1", "open") + "]", ETag: "page2"})
	f.details(1, "feature", "a", "u1")
	batch := p.Poll(context.Background(), []string{dir, foreign})
	if other := batch[foreign]; other.Err != nil || other.PR != nil {
		t.Fatalf("fork-only branch matched: %+v", other)
	}
	r := batch[dir]
	if r.Err != nil || r.PR.Number != 1 {
		t.Fatalf("fork match or pagination: %+v", r)
	}
	p.Invalidate(dir)
	p.Poll(context.Background(), []string{dir})
	if f.calls(page2) != 2 {
		t.Fatal("304 did not follow cached Link")
	}
	f.set(pullsURL, fakeResponse{Body: `[]`, ETag: "empty"})
	merged := strings.Replace(pullJSON(1, "feature", "example/widgets", "a", "u2", "closed"), `"state":"closed"`, `"state":"closed","merged_at":"2026-01-03T00:00:00Z"`, 1)
	f.set("repos/example/widgets/pulls?state=all&head=example%3Afeature&per_page=100", fakeResponse{Body: "[" + merged + "]", ETag: "merged"})
	f.set("repos/example/widgets/pulls/1", fakeResponse{Body: merged, ETag: "merged-detail"})
	p.Invalidate(dir)
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.State != model.PRMerged {
		t.Fatalf("merge: %+v", r)
	}
	p.now = func() time.Time { return time.Now().Add(time.Hour) }
	p.Poll(context.Background(), []string{dir})
	if f.calls("repos/example/widgets/pulls?state=all&head=example%3Afeature") != 1 {
		t.Fatal("merged branch looked up again")
	}

	// A gh command explicitly refreshes even a terminal branch's history.
	p.Invalidate(dir)
	p.Poll(context.Background(), []string{dir})
	if f.calls("repos/example/widgets/pulls?state=all&head=example%3Afeature") != 2 {
		t.Fatal("explicit refresh kept terminal branch history")
	}
}

func TestRESTBudget(t *testing.T) {
	for _, left := range []int{1000, 999, 0} {
		t.Run(strconv.Itoa(left), func(t *testing.T) {
			f := newRESTFake(t)
			p := NewPoller()
			dir := f.repo("feature")
			now := time.Now().Truncate(time.Second)
			p.now = func() time.Time { return now }
			f.set(pullsURL, fakeResponse{Body: `[]`, ETag: "empty", Limit: 5000, Remaining: left, Reset: now.Add(time.Hour).Unix()})
			f.set("repos/example/widgets/pulls?state=all&head=example%3Afeature&per_page=100", fakeResponse{Body: `[]`})
			r := p.Poll(context.Background(), []string{dir})[dir]
			if left == 0 {
				if !errors.Is(r.Err, ErrRateLimited) || p.Wait(now, 0) != time.Hour {
					t.Fatalf("pause: %+v %s", r, p.Wait(now, 0))
				}
				before := f.calls("")
				p.Invalidate(dir)
				p.Poll(context.Background(), []string{dir})
				if f.calls("") != before {
					t.Fatal("request escaped the zero-budget pause")
				}
				now = now.Add(time.Hour)
				if p.Wait(now, 0) != 0 || p.Wait(now, Next(nil, ErrRateLimited, 0, false)) != 0 {
					t.Fatal("did not replace the fallback pause at reset")
				}
				f.set(pullsURL, fakeResponse{Body: `[]`, Limit: 5000, Remaining: 4000, Reset: now.Add(time.Hour).Unix()})
				r = p.Poll(context.Background(), []string{dir})[dir]
				if r.Err != nil {
					t.Fatalf("reset did not resume requests: %v", r.Err)
				}
			} else {
				want := time.Minute
				if left < 1000 {
					want *= 5
				}
				if r.Err != nil || p.Wait(now, time.Minute) != want {
					t.Fatalf("slowdown: %+v %s", r, p.Wait(now, time.Minute))
				}
			}
		})
	}
}

func TestRESTSidebarFields(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	dir := f.repo("feature")
	metadata := func(updated string, draft, conflict bool) {
		raw := pullJSON(7, "feature", "example/widgets", "sha", updated, "open")
		if draft {
			raw = strings.Replace(raw, `"number":7`, `"number":7,"draft":true`, 1)
		}
		if conflict {
			raw = strings.ReplaceAll(raw, `"mergeable":true`, `"mergeable":false`)
			raw = strings.ReplaceAll(raw, `"clean"`, `"dirty"`)
		}
		raw = strings.ReplaceAll(raw, `"ref":"main"`, `"ref":"release"`)
		f.set(pullsURL, fakeResponse{Body: "[" + raw + "]", ETag: updated})
		f.set("repos/example/widgets/pulls/7", fakeResponse{Body: raw, ETag: updated})
		p.Invalidate(dir)
	}
	f.details(7, "feature", "sha", "u1")
	f.set("repos/example/widgets/pulls/7/reviews?per_page=100", fakeResponse{Body: `[{"id":3,"node_id":"request3","state":"CHANGES_REQUESTED","user":{"login":"reviewer"},"submitted_at":"2026-01-02T00:00:00Z"}]`, ETag: "r1"})
	f.decision(7, "CHANGES_REQUESTED")
	metadata("u1", true, true)
	r := p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	pr := r.PR
	if pr.Number != 7 || pr.URL != "https://github.com/example/widgets/pull/7" || !pr.Draft || !pr.Conflicts || pr.Head != "sha" || pr.State != model.PROpen || pr.Review != model.ReviewChanges || len(pr.ChangeRequests) != 1 || pr.ChangeRequests[0] != "request3" || pr.Detail != "conflicts with release" {
		t.Fatalf("sidebar fields: %+v", pr)
	}
	metadata("u2", false, false)
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.Reason != model.ReasonChangesRequested || r.PR.Detail != "by reviewer" {
		t.Fatalf("review and commit time: %+v", r)
	}
	// A comment does not lift an earlier approval by the same reviewer.
	f.set("repos/example/widgets/pulls/7/reviews?per_page=100", fakeResponse{Body: `[{"id":4,"state":"APPROVED","user":{"login":"reviewer"}},{"id":5,"state":"COMMENTED","user":{"login":"reviewer"}}]`, ETag: "r2"})
	f.decision(7, "APPROVED")
	metadata("u3", false, false)
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.Review != model.ReviewApproved || r.PR.Reason != model.ReasonReady || len(r.PR.ChangeRequests) != 0 {
		t.Fatalf("approval: %+v", r)
	}
	f.set("repos/example/widgets/pulls/7/reviews?per_page=100", fakeResponse{Body: `[{"id":4,"state":"DISMISSED","user":{"login":"reviewer"}}]`, ETag: "r3"})
	f.decision(7, "REVIEW_REQUIRED")
	metadata("u4", false, false)
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.Review != model.ReviewRequired {
		t.Fatalf("dismissed: %+v", r)
	}
	if f.calls("repos/example/widgets/commits/sha/check-runs") != 1 {
		t.Fatal("review changes fetched finished checks")
	}

	// The base can introduce conflicts without a push or a PR timestamp change.
	metadata("u4", false, true)
	f.set("repos/example/widgets/pulls/7", fakeResponse{Body: strings.Replace(pullJSON(7, "feature", "example/widgets", "sha", "u4", "open"), `"mergeable":true`, `"mergeable":false`, 1), ETag: "base-moved"})
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || !r.PR.Conflicts {
		t.Fatalf("base update left stale mergeability: %+v", r)
	}
}

func TestRESTHTTPRateLimit(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	dir := f.repo("feature")
	now := time.Now().Truncate(time.Second)
	p.now = func() time.Time { return now }
	f.set(pullsURL, fakeResponse{Status: 403, Exit: 1, Body: `{"message":"rate limit exceeded"}`, Limit: 5000, Remaining: 0, Reset: now.Add(7 * time.Minute).Unix()})
	r := p.Poll(context.Background(), []string{dir})[dir]
	if !errors.Is(r.Err, ErrRateLimited) || p.Wait(now, 0) != 7*time.Minute {
		t.Fatalf("HTTP failure lost budget headers: %+v", r)
	}
}

func (f *restFake) decision(number int, decision string) {
	f.set("graphql-decision/"+strconv.Itoa(number), fakeResponse{Body: `{"data":{"repository":{"pullRequest":{"reviewDecision":"` + decision + `"}}}}`})
}

func TestRESTExactReviewDecision(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	dir := f.repo("feature")
	f.details(1, "feature", "a", "u1")
	f.decision(1, "REVIEW_REQUIRED")
	f.set(pullsURL, fakeResponse{Body: "[" + pullJSON(1, "feature", "example/widgets", "a", "u1", "open") + "]"})
	r := p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.Review != model.ReviewRequired {
		t.Fatalf("aggregate review rule was lost: %+v", r)
	}
	p.Invalidate(dir)
	p.Poll(context.Background(), []string{dir})
	if f.calls("graphql-decision/") != 1 {
		t.Fatal("unchanged metadata asked GraphQL again")
	}
	f.details(1, "feature", "a", "u2")
	f.set("graphql-decision/1", fakeResponse{Exit: 1})
	f.set(pullsURL, fakeResponse{Body: "[" + pullJSON(1, "feature", "example/widgets", "a", "u2", "open") + "]"})
	p.Invalidate(dir)
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.Head != "a" || r.PR.Review != "" {
		t.Fatalf("GraphQL exhaustion blocked REST: %+v", r)
	}
	p.Invalidate(dir)
	p.Poll(context.Background(), []string{dir})
	if f.calls("graphql-decision/") != 2 {
		t.Fatal("GraphQL exhaustion retried the same timestamp")
	}
	if p.Wait(time.Now(), OpenEvery) != OpenEvery {
		t.Fatal("GraphQL exhaustion paused the REST budget")
	}
}

func TestRESTReviewDecisionSurvivesCheckError(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	dir := f.repo("feature")
	f.details(1, "feature", "a", "u1")
	f.set(pullsURL, fakeResponse{Body: "[" + pullJSON(1, "feature", "example/widgets", "a", "u1", "open") + "]"})
	f.set("repos/example/widgets/commits/a/check-runs?per_page=100", fakeResponse{Status: 500, Exit: 1})
	r := p.Poll(context.Background(), []string{dir})[dir]
	if r.Err == nil {
		t.Fatal("check failure was ignored")
	}
	f.checks("a", "completed")
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.Review != model.ReviewApproved {
		t.Fatalf("retry lost review decision: %+v", r)
	}
	if f.calls("graphql-decision/") != 1 {
		t.Fatal("check retry repeated GraphQL for an unchanged timestamp")
	}
}
