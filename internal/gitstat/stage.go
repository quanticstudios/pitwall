package gitstat

import (
	"context"
	"fmt"
)

// Staging is a work tree's uncommitted change in the two parts git add -p
// moves hunks between: the work tree against the index, and the index
// against HEAD.
type Staging struct {
	Root      string // the repository's top level
	Unstaged  []byte // git diff, with a/ and b/ prefixes, every line as context
	Staged    []byte // git diff --cached, likewise, renames found
	Untracked []string
	// Unmerged is the paths a merge left in conflict, which neither diff
	// has: they stage whole, which marks them resolved.
	Unmerged []string
}

// ReadStaging returns worktree's Staging, in the format Diff uses.
func ReadStaging(ctx context.Context, worktree string) (Staging, error) {
	var s Staging
	root, ok := RepoRoot(ctx, worktree)
	if !ok {
		return s, notRepo(ctx, worktree)
	}
	diff := func(more ...string) ([]byte, error) {
		args := append([]string{"-c", "core.quotePath=true", "diff", "--no-color", "--no-ext-diff", "--no-textconv",
			"--src-prefix=a/", "--dst-prefix=b/", fmt.Sprintf("--unified=%d", fullContext)}, more...)
		out, err := git(ctx, root, append(args, "--")...)
		return []byte(out), err
	}
	var err error
	if s.Unstaged, err = diff(); err != nil {
		return s, err
	}
	if s.Staged, err = diff("--cached", "-M"); err != nil {
		return s, err
	}
	s.Root = root
	if s.Untracked, err = untracked(ctx, root); err != nil {
		return s, err
	}
	s.Unmerged, err = names(ctx, root, "diff", "--name-only", "--diff-filter=U", "-z")
	return s, err
}

// untracked is the files under root git does not track or ignore.
func untracked(ctx context.Context, root string) ([]string, error) {
	return names(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
}

// ApplyCached applies patch, as review.Patch builds it, to worktree's
// index and leaves the work tree alone: it stages the hunk, or unstages it
// with reverse.
func ApplyCached(ctx context.Context, worktree string, patch []byte, reverse bool) error {
	root, ok := RepoRoot(ctx, worktree)
	if !ok {
		return notRepo(ctx, worktree)
	}
	if err := refuseRepoFilter(ctx, root); err != nil {
		return err
	}
	args := []string{"apply", "--cached", "--whitespace=nowarn"}
	if reverse {
		args = append(args, "-R")
	}
	_, err := gitWith(ctx, root, nil, string(patch), append(args, "-")...)
	return err
}

// StageFiles stages paths, relative to the repository root, as they are
// on disk, a deleted one as gone, or with unstage puts their index entries
// back as HEAD has them.
func StageFiles(ctx context.Context, worktree string, paths []string, unstage bool) error {
	root, ok := RepoRoot(ctx, worktree)
	if !ok {
		return notRepo(ctx, worktree)
	}
	if err := refuseRepoFilter(ctx, root); err != nil {
		return err
	}
	// why: a path is a file's name, never a pattern; *.txt must not take
	// secret.txt along.
	args := []string{"--literal-pathspecs", "add", "-A", "--"}
	if unstage {
		// ponytail: restore needs HEAD, so a repository with no commit yet
		// cannot unstage; git rm --cached would cover it.
		args = []string{"--literal-pathspecs", "restore", "--staged", "--"}
	}
	_, err := git(ctx, root, append(args, paths...)...)
	return err
}
