package model

import (
	"strconv"
	"strings"
	"time"
)

// WorktreeKind is what a new worktree checks out.
type WorktreeKind int

const (
	// FromNew is a new branch named after the worktree, off WorktreeFrom.Ref.
	FromNew WorktreeKind = iota
	// FromBranch is the existing local branch Ref.
	FromBranch
	// FromRemote is a new local branch that tracks the remote branch Ref,
	// such as "origin/fix", named after it without the remote: "fix".
	FromRemote
	// FromPR is GitHub pull request PR's head, fetched from origin's
	// pull/<PR>/head into the local branch pr-<PR>.
	FromPR
)

// WorktreeFrom is what a new worktree starts from. The zero value is a new
// branch off the repo's default branch.
type WorktreeFrom struct {
	Kind WorktreeKind
	// Ref is the base of a new branch, a local or remote branch ("" for
	// the default branch), the local branch to check out, or the remote
	// branch to track.
	Ref string
	PR  int
}

// Name is the worktree's name when none is given: the branch it checks
// out, or "" for a new branch, which needs one.
func (f WorktreeFrom) Name() string {
	switch f.Kind {
	case FromBranch:
		return f.Ref
	case FromRemote:
		return LocalOf(f.Ref)
	case FromPR:
		return "pr-" + strconv.Itoa(f.PR)
	}
	return ""
}

// LocalOf is the local branch for remote branch ref: "fix/a" for
// "origin/fix/a".
func LocalOf(ref string) string {
	_, local, _ := strings.Cut(ref, "/")
	return local
}

// Orphan is a worktree under a git group's <Root>/.worktrees/ that no tab
// uses.
type Orphan struct {
	Root, Path, Branch string
	Committed          time.Time // its last commit; zero when git could not tell
	Dirty              bool      // it has uncommitted or untracked files
}
