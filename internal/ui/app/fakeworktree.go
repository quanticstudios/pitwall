package app

import (
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// fakeStray is two worktrees of web-app that no tab uses, one with
// uncommitted files.
func fakeStray(now time.Time) []model.Orphan {
	root := fakeHome + "/src/web-app"
	return []model.Orphan{
		{Root: root, Path: root + "/.worktrees/old-spike", Branch: "old-spike", Committed: now.Add(-9 * 24 * time.Hour)},
		{Root: root, Path: root + "/.worktrees/try-vite-6", Branch: "try-vite-6", Committed: now.Add(-50 * time.Hour), Dirty: true},
	}
}

// Worktree implements Worktreer.
func (f *FakeBackend) Worktree() proto.WorktreeInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tree
}

// worktreeQuery answers q as the daemon would for the fake's repos: the
// worktree tab w4 has uncommitted files on an unmerged branch.
func (f *FakeBackend) worktreeQuery(q proto.WorktreeQuery) proto.WorktreeInfo {
	r := proto.WorktreeInfo{Query: q}
	switch {
	case q.ProjectID != "":
		r.Default, r.GitHub = "origin/main", true
		r.Local = []string{"main", "fix-checkout-race", "release/1.4"}
		r.Remote = []string{"origin/main", "origin/release/1.4", "origin/feat/dark-mode", "origin/feat/saved-carts", "origin/dependabot/npm/vite-6"}
	case q.WorkspaceID == "w4":
		r.Changed = []string{"src/checkout/race.ts", "src/checkout/race.test.ts", "notes.md"}
		r.Unmerged = true
	case q.Orphans:
		r.Orphans = append([]model.Orphan(nil), f.stray...)
	}
	return r
}
