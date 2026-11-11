package app

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestBaseItems lists the default branch first, then the branch the
// worktree came from, the pull request's base and the other local
// branches, as full refs, each once and never the tab's own or a branch
// that is gone; the filter and a pick follow.
func TestBaseItems(t *testing.T) {
	refs := gitstat.Refs{Default: "origin/main", Local: []string{"main", "feature", "stacked", "old"}, Remote: []string{"origin/main", "origin/feature"}}
	got := baseItems(refs, "refs/heads/feature", "feature", "stacked")
	want := []baseItem{
		{"", "origin/main", "default branch"},
		{"refs/heads/feature", "feature", "created from · pull request base"},
		{"refs/heads/old", "old", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("baseItems = %+v\nwant %+v", got, want)
	}
	if got := baseItems(refs, "", "main", "stacked")[0].note; got != "default branch · pull request base" {
		t.Errorf("a PR into the default branch: note %q", got)
	}
	if got := baseItems(refs, "refs/heads/deleted", "", "stacked"); len(got) != 3 || got[1].ref != "refs/heads/feature" {
		t.Errorf("a deleted creation branch is listed: %+v", got)
	}
	if got := shownBases(want, " FEA"); !slices.Equal(got, []int{1}) {
		t.Errorf("shownBases = %v", got)
	}

	st := &model.State{Workspaces: []model.Workspace{{ID: "w", Branch: "stacked"}}}
	u := &ui{}
	u.nav.reviewBase = map[string]reviewPick{"gone": {Ref: "x"}}
	u.review.ws, u.review.bases.open = "w", true
	u.pickBase(st, want[1])
	if p := u.nav.reviewBase["w"]; p != (reviewPick{"refs/heads/feature", "stacked", ""}) || u.review.picked() != "refs/heads/feature" || u.review.bases.open || len(u.nav.reviewBase) != 1 {
		t.Errorf("after a pick: bases %v, picked %q, open %v", u.nav.reviewBase, u.review.picked(), u.review.bases.open)
	}
	u.pickBase(st, want[0])
	if _, ok := u.nav.reviewBase["w"]; ok || u.review.picked() != "" {
		t.Errorf("the default branch kept a pick: %v", u.nav.reviewBase)
	}
}

// TestBasesCloseWithView opens tab a's base list, then the view closes for
// tab b before a's branches are read: the list is closed, and a's late
// read does not fill it.
func TestBasesCloseWithView(t *testing.T) {
	old := readRefs
	t.Cleanup(func() { readRefs = old })
	release := make(chan struct{})
	readRefs = func(context.Context, string) (gitstat.Refs, error) {
		<-release
		return gitstat.Refs{Default: "origin/main", Local: []string{"a-only"}}, nil
	}
	read := make(chan struct{})
	var once sync.Once
	u := &ui{invalidate: func() { once.Do(func() { close(read) }) }}
	st := &model.State{Workspaces: []model.Workspace{{ID: "a", Branch: "x"}, {ID: "b", Branch: "y"}}}
	u.review.on, u.review.ws, u.review.dir = true, "a", t.TempDir()
	u.openBases(st)
	if !u.review.bases.open {
		t.Fatal("the list did not open")
	}
	u.review.hide()
	u.review.ws = "b"
	close(release)
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("the read never finished")
	}
	if items, _ := u.review.bases.shownItems(); u.review.bases.open || items != nil {
		t.Errorf("after the view closed: open %v, items %+v", u.review.bases.open, items)
	}
}

// TestReviewDropsGonePick opens the review on a pick whose branch is
// gone: it diffs from the default branch, and the window forgets the pick.
func TestReviewDropsGonePick(t *testing.T) {
	oldDiff, oldRef := readDiff, branchRef
	t.Cleanup(func() { readDiff, branchRef = oldDiff, oldRef })
	var mu sync.Mutex
	var bases []string
	readDiff = func(_ context.Context, _, base string) (gitstat.Patch, error) {
		mu.Lock()
		bases = append(bases, base)
		mu.Unlock()
		return gitstat.Patch{Base: "refs/heads/main", Root: "/repo"}, nil
	}
	branchRef = func(context.Context, string, string) string { return "" }
	u := &ui{}
	u.nav.reviewBase = map[string]reviewPick{"w": {Ref: "refs/heads/gone", Branch: "stacked"}}
	u.review.show("w", t.TempDir(), "refs/heads/gone", false, func() {})
	cleanupReview(t, &u.review)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, _, _, loaded := u.review.snapshot(); loaded || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	got := slices.Clone(bases)
	mu.Unlock()
	if !slices.Equal(got, []string{""}) || u.review.picked() != "" {
		t.Errorf("read from %q, picked %q", got, u.review.picked())
	}
	u.dropLost()
	if _, ok := u.nav.reviewBase["w"]; ok || u.review.status != "gone is gone; diffing from the default branch" {
		t.Errorf("after dropLost: bases %v, status %q", u.nav.reviewBase, u.review.status)
	}
}

// TestGonePickHoldsDiscard shows a diff from a picked branch, aims the
// discard dialog at a file, then the branch goes: the view blanks until
// the default branch's diff arrives, so no Discard acts on the old files,
// and the dialog already open still restores from the base it named.
func TestGonePickHoldsDiscard(t *testing.T) {
	oldDiff, oldRef, oldDiscard := readDiff, branchRef, discardFiles
	t.Cleanup(func() { readDiff, branchRef, discardFiles = oldDiff, oldRef, oldDiscard })
	var gone atomic.Bool
	branchRef = func(_ context.Context, _, ref string) string {
		if gone.Load() {
			return ""
		}
		return ref
	}
	u := &ui{}
	fallback, release := make(chan bool, 1), make(chan struct{})
	releaseReader := sync.OnceFunc(func() { close(release) })
	defer releaseReader()
	readDiff = func(_ context.Context, _, base string) (gitstat.Patch, error) {
		if base == "" {
			_, _, _, _, loaded := u.review.snapshot()
			fallback <- loaded
			<-release
			return gitstat.Patch{Base: "refs/heads/main", Root: "/repo"}, nil
		}
		return gitstat.Patch{Base: base, Root: "/repo", Diff: []byte("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n")}, nil
	}
	discarded := make(chan string, 1)
	discardFiles = func(_ context.Context, _, base string, _ []string, _ bool) error {
		discarded <- base
		return nil
	}
	u.review.show("w", t.TempDir(), "refs/heads/feature", false, func() {})
	cleanupReview(t, &u.review)
	waitLoaded(t, &u.review)
	files, _, _, _, _ := u.review.snapshot()
	u.review.askDiscard(&files[0])
	gone.Store(true)
	u.review.reload()
	select {
	case loaded := <-fallback:
		if loaded {
			t.Error("the old base's files stayed up while the default's diff was read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no fallback read")
	}
	u.discardFile()
	if got := <-discarded; got != "refs/heads/feature" {
		t.Errorf("Discard restored from %q, not the base the dialog named", got)
	}
	releaseReader()
	waitLoaded(t, &u.review) // the reader is idle again before the stubs go
}

// TestStaleReaderKeepsPick: a closed view's reader that finds its pick
// gone after the view opened again on the same pick leaves the new one be.
func TestStaleReaderKeepsPick(t *testing.T) {
	oldDiff, oldRef, oldTurn := readDiff, branchRef, readTurn
	t.Cleanup(func() { readDiff, branchRef, readTurn = oldDiff, oldRef, oldTurn })
	first, release, stale := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseReader := sync.OnceFunc(func() { close(release) })
	defer releaseReader()
	var calls atomic.Int32
	branchRef = func(_ context.Context, _, ref string) string {
		if calls.Add(1) == 1 {
			close(first)
			<-release
			return ""
		}
		return ref
	}
	readTurn = func(context.Context, string, string) (gitstat.Turn, error) {
		return gitstat.Turn{}, errors.New("none")
	}
	var staleOnce sync.Once
	readDiff = func(ctx context.Context, _, base string) (gitstat.Patch, error) {
		if ctx.Err() != nil {
			// why: hide in cleanup cancels the new reader too, which can call again.
			staleOnce.Do(func() { close(stale) }) // the old reader's last call, past where it would touch the pick
		}
		return gitstat.Patch{Base: base, Root: "/repo"}, nil
	}
	var r reviewView
	dir := t.TempDir()
	r.show("w", dir, "refs/heads/feature", false, func() {})
	<-first
	r.show("w", dir, "refs/heads/feature", false, func() {})
	cleanupReview(t, &r)
	waitLoaded(t, &r)
	releaseReader()
	select {
	case <-stale:
	case <-time.After(5 * time.Second):
		t.Fatal("the old reader never finished")
	}
	if r.picked() != "refs/heads/feature" || r.takeLost() != "" {
		t.Errorf("the old reader changed the new view: picked %q", r.picked())
	}
}

func waitLoaded(t *testing.T, r *reviewView) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, _, _, loaded := r.snapshot(); loaded {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the diff never loaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cleanupReview must be registered after hook restoration so it runs first.
func cleanupReview(t *testing.T, r *reviewView) {
	t.Helper()
	t.Cleanup(func() {
		r.hide()
		r.readers.Wait()
	})
}
