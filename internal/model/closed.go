package model

import (
	"slices"
	"time"
)

// ClosedTab is a tab that closed, kept so tab search can open it again.
// The daemon adds one for every tab of panes that closes, newest last.
type ClosedTab struct {
	ID string // the record's own id, which the reopened tab takes
	// Tab is the tab as it was, without its layout.
	Tab Workspace
	// Pane is the tab's agent pane that RestoreCmd resumes, else its first
	// pane. Its Cwd is where the pane last was.
	Pane Pane
	// Session is the name of the tab's session, which may have ended since.
	Session string
	// Group is the tab's group as it was; the zero Project for none.
	Group Project
	PR    int // the number of its branch's pull request, 0 for none
	// ClosedAt is when the tab closed.
	ClosedAt time.Time
	// Gone is set while Folder no longer exists, as after Archive removed
	// the worktree. The daemon fills it in; it is not saved.
	Gone bool `json:"-"`
}

// ClosedKeep and ClosedDays bound State.Closed: the newest ClosedKeep
// records that closed within ClosedDays.
const (
	ClosedKeep = 200
	ClosedDays = 30
)

// Folder is where the tab reopens: its pane's last directory, else the
// tab's.
func (c ClosedTab) Folder() string {
	if c.Pane.Cwd != "" {
		return c.Pane.Cwd
	}
	return c.Tab.Path
}

// Root is the repository a tab whose Folder is gone can reopen in
// instead: the repo pitwall made its worktree from, its group's folder,
// or its repo root, the first that is not Folder. "" for none.
func (c ClosedTab) Root() string {
	for _, r := range []string{c.Tab.WorktreeRoot, c.Group.Root, c.Tab.RepoRoot} {
		if r != "" && r != c.Folder() && r != c.Tab.Path {
			return r
		}
	}
	return ""
}

// PruneClosed drops the records that closed more than ClosedDays before
// now, then all but the newest ClosedKeep. It keeps the order, oldest
// first.
func PruneClosed(cs []ClosedTab, now time.Time) []ClosedTab {
	cut := now.AddDate(0, 0, -ClosedDays)
	cs = slices.DeleteFunc(cs, func(c ClosedTab) bool { return c.ClosedAt.Before(cut) })
	if n := len(cs) - ClosedKeep; n > 0 {
		cs = slices.Delete(cs, 0, n)
	}
	return cs
}
