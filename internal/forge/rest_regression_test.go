package forge

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestRESTRepoActivityDoesNotRefreshDetails(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	now := time.Now().Truncate(QuietEvery)
	p.now = func() time.Time { return now }
	var dirs, pulls []string
	for i := 1; i <= 2; i++ {
		branch := "branch-" + strconv.Itoa(i)
		dirs = append(dirs, f.repo(branch))
		pulls = append(pulls, pullJSON(i, branch, "example/widgets", branch, "u1", "open"))
		f.details(i, branch, branch, "u1")
	}
	for minute := 0; minute <= 5; minute++ {
		// Every push changes the list ETag without changing either tracked PR.
		f.set(pullsURL, fakeResponse{Body: "[" + strings.Join(pulls, ",") + "]", ETag: fmt.Sprint("repo", minute)})
		for _, r := range p.Poll(context.Background(), dirs) {
			if r.Err != nil || r.PR == nil {
				t.Fatalf("poll: %+v", r)
			}
		}
		now = now.Add(OpenEvery)
	}
	if n := f.calls(pullsURL); n != 2 {
		t.Fatalf("quiet repo list: %d calls, want 2", n)
	}
	for i := 1; i <= 2; i++ {
		// The prefix counts the detail and reviews requests together.
		if n := f.calls("repos/example/widgets/pulls/" + strconv.Itoa(i)); n != 2 {
			t.Fatalf("unchanged PR detail/reviews: %d calls, want 2", n)
		}
	}
	if n := f.calls("repos/example/widgets/git/ref/heads/main"); n != 6 {
		t.Fatalf("base ref: %d calls, want one per minute across both PRs", n)
	}
	f.set("repos/example/widgets/git/ref/heads/main", fakeResponse{Body: `{"object":{"sha":"base2"}}`, ETag: "base2"})
	for i := 1; i <= 2; i++ {
		raw := strings.Replace(pulls[i-1], `"mergeable":true`, `"mergeable":false`, 1)
		f.set("repos/example/widgets/pulls/"+strconv.Itoa(i), fakeResponse{Body: raw, ETag: "base2"})
	}
	for _, r := range p.Poll(context.Background(), dirs) {
		if r.Err != nil || !r.PR.Conflicts {
			t.Fatalf("base move: %+v", r)
		}
	}
}

func TestRESTRerunFinishedHead(t *testing.T) {
	for _, invalidate := range []bool{false, true} {
		t.Run(strconv.FormatBool(invalidate), func(t *testing.T) {
			f := newRESTFake(t)
			p := NewPoller()
			dir := f.repo("feature")
			now := time.Now()
			p.now = func() time.Time { return now }
			f.details(1, "feature", "a", "u1")
			f.set(pullsURL, fakeResponse{Body: "[" + pullJSON(1, "feature", "example/widgets", "a", "u1", "open") + "]"})
			f.set("repos/example/widgets/commits/a/check-runs?per_page=100", fakeResponse{Body: `{"check_runs":[{"name":"build","status":"completed","conclusion":"failure"}]}`, ETag: "failed"})
			r := p.Poll(context.Background(), []string{dir})[dir]
			if r.Err != nil || r.PR.CI() != model.CheckFail {
				t.Fatalf("initial failure: %+v", r)
			}
			f.checks("a", "in_progress")
			if invalidate {
				p.Invalidate(dir)
			} else {
				now = now.Add(QuietEvery)
			}
			r = p.Poll(context.Background(), []string{dir})[dir]
			if r.Err != nil || r.PR.CI() != model.CheckPending {
				t.Fatalf("rerun not visible: %+v", r)
			}
			f.checks("a", "completed")
			now = now.Add(OpenEvery)
			r = p.Poll(context.Background(), []string{dir})[dir]
			if r.Err != nil || r.PR.CI() != model.CheckPass || r.PR.Reason != model.ReasonReady {
				t.Fatalf("rerun did not recover: %+v", r)
			}
		})
	}
}

func TestRESTForkRemotes(t *testing.T) {
	for _, tc := range []struct {
		name, head, ref string
		config          [][2]string
	}{
		{"origin fork upstream base", "fork/widgets", "feature", [][2]string{{"remote.origin.url", "https://github.com/fork/widgets.git"}, {"remote.upstream.url", "https://github.com/example/widgets.git"}, {"branch.local.remote", "origin"}, {"branch.local.merge", "refs/heads/feature"}}},
		{"checkout writable fork", "fork/widgets", "feature", [][2]string{{"branch.local.remote", "https://github.com/fork/widgets.git"}, {"branch.local.pushRemote", "https://github.com/fork/widgets.git"}, {"branch.local.merge", "refs/heads/feature"}}},
		{"checkout read-only fork", "fork/widgets", "feature", [][2]string{{"branch.local.remote", "origin"}, {"branch.local.merge", "refs/pull/1/head"}}},
		{"push remote wins", "fork/widgets", "feature", [][2]string{{"remote.fork.url", "git@github.com:fork/widgets.git"}, {"remote.pushDefault", "origin"}, {"branch.local.remote", "origin"}, {"branch.local.pushRemote", "fork"}, {"branch.local.merge", "refs/heads/feature"}}},
		{"push default wins", "fork/widgets", "feature", [][2]string{{"remote.fork.url", "git@github.com:fork/widgets.git"}, {"remote.pushDefault", "fork"}, {"branch.local.remote", "origin"}, {"branch.local.merge", "refs/heads/feature"}}},
		{"resolved base wins", "example/widgets", "local", [][2]string{{"remote.upstream.url", "https://github.com/other/widgets.git"}, {"remote.origin.gh-resolved", "base"}}},
		{"only custom remote", "example/widgets", "local", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRESTFake(t)
			p := NewPoller()
			dir := f.repo("local")
			if tc.name == "only custom remote" {
				if err := exec.Command("git", "-C", dir, "remote", "rename", "origin", "custom").Run(); err != nil {
					t.Fatal(err)
				}
			}
			for _, kv := range tc.config {
				if err := exec.Command("git", "-C", dir, "config", kv[0], kv[1]).Run(); err != nil {
					t.Fatal(err)
				}
			}
			pr := pullJSON(1, tc.ref, tc.head, "a", "u1", "open")
			// A higher-numbered PR with the same head ref belongs to another repo.
			f.set(pullsURL, fakeResponse{Body: "[" + pr + "," + pullJSON(99, tc.ref, "other/widgets", "b", "u2", "open") + "]"})
			f.details(1, tc.ref, "a", "u1")
			f.set("repos/example/widgets/pulls/1", fakeResponse{Body: pr})
			r := p.Poll(context.Background(), []string{dir})[dir]
			if r.Err != nil || r.PR == nil || r.PR.Number != 1 {
				t.Fatalf("fork setup: %+v", r)
			}
			if tc.name == "origin fork upstream base" {
				closed := strings.Replace(pr, `"state":"open"`, `"state":"closed","merged_at":"2026-01-03T00:00:00Z"`, 1)
				f.set(pullsURL, fakeResponse{Body: `[]`})
				f.set("repos/example/widgets/pulls?state=all&head=fork%3Afeature&per_page=100", fakeResponse{Body: "[" + closed + "]"})
				f.set("repos/example/widgets/pulls/1", fakeResponse{Body: closed})
				p.Invalidate(dir)
				r = p.Poll(context.Background(), []string{dir})[dir]
				if r.Err != nil || r.PR == nil || r.PR.State != model.PRMerged {
					t.Fatalf("closed fork lookup: %+v", r)
				}
			}
		})
	}
}

func TestRESTOldHeadBodiesDiscarded(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	dir := f.repo("feature")
	for _, sha := range []string{"a", "b"} {
		f.details(1, "feature", sha, sha)
		f.set(pullsURL, fakeResponse{Body: "[" + pullJSON(1, "feature", "example/widgets", sha, sha, "open") + "]"})
		p.Invalidate(dir)
		r := p.Poll(context.Background(), []string{dir})[dir]
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if sha == "a" {
			p.urls["repos/example/widgets/commits/a/check-runs?per_page=100&page=2"] = response{body: []byte(`{"check_runs":[]}`)}
		}
	}
	for path := range p.urls {
		if strings.Contains(path, "/commits/a/") || strings.HasSuffix(path, "/git/commits/a") {
			t.Fatalf("old head body remains: %s", path)
		}
	}
	if _, ok := p.urls["repos/example/widgets/commits/b/check-runs?per_page=100"]; !ok {
		t.Fatal("new head body discarded")
	}
}

func TestRESTUnknownMergeability(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	dir := f.repo("feature")
	f.details(1, "feature", "a", "u1")
	pr := pullJSON(1, "feature", "example/widgets", "a", "u1", "open")
	f.set(pullsURL, fakeResponse{Body: "[" + pr + "]"})
	f.set("repos/example/widgets/pulls/1", fakeResponse{Body: strings.Replace(pr, `"mergeable":true`, `"mergeable":null`, 1), ETag: "unknown"})
	r := p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.Reason != model.ReasonChecksPending {
		t.Fatalf("unknown mergeability: %+v", r)
	}
	f.set("repos/example/widgets/pulls/1", fakeResponse{Body: pr, ETag: "known"})
	r = p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR.Reason != model.ReasonReady {
		t.Fatalf("mergeability did not resolve: %+v", r)
	}
}

func TestRESTClosedPullWithDeletedBase(t *testing.T) {
	f := newRESTFake(t)
	p := NewPoller()
	dir := f.repo("feature")
	f.details(1, "feature", "a", "u2")
	pr := pullJSON(1, "feature", "example/widgets", "a", "u2", "closed")
	f.set(pullsURL, fakeResponse{Body: `[]`})
	f.set("repos/example/widgets/pulls?state=all&head=example%3Afeature&per_page=100", fakeResponse{Body: "[" + pr + "]"})
	f.set("repos/example/widgets/pulls/1", fakeResponse{Body: pr})
	f.set("repos/example/widgets/git/ref/heads/main", fakeResponse{Status: 404, Exit: 1})
	r := p.Poll(context.Background(), []string{dir})[dir]
	if r.Err != nil || r.PR == nil || r.PR.State != model.PRClosed {
		t.Fatalf("closed pull with deleted base: %+v", r)
	}
}
