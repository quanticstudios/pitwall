package model

import (
	"fmt"
	"testing"
	"time"
)

// TestPruneClosed keeps the newest ClosedKeep records of the last
// ClosedDays, whichever bound is smaller.
func TestPruneClosed(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	var cs []ClosedTab
	// 250 records an hour apart, the oldest first: all within 30 days.
	for i := range 250 {
		cs = append(cs, ClosedTab{ID: fmt.Sprint(i), ClosedAt: now.Add(time.Duration(i-250) * time.Hour)})
	}
	got := PruneClosed(cs, now)
	if len(got) != ClosedKeep || got[0].ID != "50" || got[len(got)-1].ID != "249" {
		t.Fatalf("count bound: %d records, %s to %s", len(got), got[0].ID, got[len(got)-1].ID)
	}

	old := []ClosedTab{
		{ID: "old", ClosedAt: now.AddDate(0, 0, -ClosedDays).Add(-time.Minute)},
		{ID: "edge", ClosedAt: now.AddDate(0, 0, -ClosedDays).Add(time.Minute)},
		{ID: "new", ClosedAt: now},
	}
	got = PruneClosed(old, now)
	if len(got) != 2 || got[0].ID != "edge" || got[1].ID != "new" {
		t.Fatalf("age bound: %v", got)
	}
}

// TestClosedFolderRoot reopens in the pane's last folder, and falls back
// to the repository a worktree came from, never to the folder itself.
func TestClosedFolderRoot(t *testing.T) {
	c := ClosedTab{Tab: Workspace{Path: "/r/.worktrees/x", RepoRoot: "/r/.worktrees/x", WorktreeRoot: "/r"}}
	if c.Folder() != "/r/.worktrees/x" || c.Root() != "/r" {
		t.Fatalf("worktree: %q, %q", c.Folder(), c.Root())
	}
	c.Pane.Cwd = "/r/.worktrees/x/sub"
	if c.Folder() != "/r/.worktrees/x/sub" || c.Root() != "/r" {
		t.Fatalf("pane cwd: %q, %q", c.Folder(), c.Root())
	}
	c = ClosedTab{Tab: Workspace{Path: "/w", RepoRoot: "/w"}, Group: Project{Root: "/repo"}}
	if c.Root() != "/repo" {
		t.Fatalf("group root: %q", c.Root())
	}
	c = ClosedTab{Tab: Workspace{Path: "/repo", RepoRoot: "/repo"}, Pane: Pane{Cwd: "/repo/sub"}}
	if c.Root() != "/repo" {
		t.Fatalf("pane in a subfolder: %q", c.Root())
	}
	c = ClosedTab{Tab: Workspace{Path: "/w", RepoRoot: "/w"}}
	if c.Root() != "" {
		t.Fatalf("no root: %q", c.Root())
	}
}
