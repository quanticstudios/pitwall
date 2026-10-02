// Package gitstat creates and removes worktrees and reports branch stats,
// all by shelling out to git.
package gitstat

import (
	"context"

	"github.com/quanticstudios/pitwall/internal/model"
)

type Worktree struct {
	Path   string
	Branch string
	Head   string
}

// RepoRoot returns the top-level of the repo containing path.
func RepoRoot(ctx context.Context, path string) (string, bool) { panic("unimplemented") }

func Stats(ctx context.Context, worktree string) (model.BranchStats, error) {
	panic("unimplemented")
}

func ListWorktrees(ctx context.Context, repoRoot string) ([]Worktree, error) {
	panic("unimplemented")
}

// AddWorktree creates branch name off the default branch in a new worktree.
func AddWorktree(ctx context.Context, repoRoot, name string) (path, branch string, err error) {
	panic("unimplemented")
}

func RemoveWorktree(ctx context.Context, repoRoot, path string, deleteBranch bool) error {
	panic("unimplemented")
}
