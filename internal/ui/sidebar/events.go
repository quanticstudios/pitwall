package sidebar

// Event is one of SelectWorkspace, NewTab, CloseTab, RenameTab, AddProject,
// DetachSession, AttachSession, KillSession, GroupByFolder, DeleteWorkspace,
// OpenSettings, SetProjectAppearance, MoveToGroup, NewGroup, RenameGroup,
// Ungroup, NewWorktreeSession, MoveSession, MoveGroup, OpenSessions,
// RunUpdate, ViewDiff, ViewFileDiff, CreatePR, Answer, OpenPR, MergePR,
// RerunChecks, Archive, NewTaskIn, DropTask.
type Event any

// OpenSessions is a click on the session name in the header: show the
// session switcher.
type OpenSessions struct{}

// SelectWorkspace shows a tab.
type SelectWorkspace struct{ WorkspaceID, PaneID string }

// NewTab opens a tab below After (a row's "+"), or with After "" below the
// open tab: the header's "+" (GroupID "") or a group's "+", which puts it
// last in GroupID when the open tab is elsewhere.
type NewTab struct {
	After, GroupID string
	Loose          bool // outside every group, in the active tab's folder
}

// CloseTab closes a tab and all its panes.
type CloseTab struct{ WorkspaceID string }

// RenameTab names a tab; "" goes back to the automatic title.
type RenameTab struct{ WorkspaceID, Name string }
type AddProject struct{}
type DetachSession struct{ WorkspaceID string }
type AttachSession struct{ WorkspaceID string }

// KillSession comes from the detached list after its inline confirm.
type KillSession struct{ WorkspaceID string }

// GroupByFolder groups the ungrouped tabs sharing WorkspaceID's RepoRoot.
type GroupByFolder struct{ WorkspaceID string }
type DeleteWorkspace struct{ WorkspaceID string }
type OpenSettings struct{}

// ViewDiff shows the tab's diff from its default branch.
type ViewDiff struct{ WorkspaceID string }

// ViewFileDiff shows one file's diff from the default branch, Path
// relative to the repo root: a file in the conflict radar of the tab's
// hover card.
type ViewFileDiff struct{ WorkspaceID, Path string }

// CreatePR opens a pull request for the tab's branch.
type CreatePR struct{ WorkspaceID string }

// Answer is a click on a row's Allow or Deny: proto.Answer for its
// pending approval.
type Answer struct {
	PaneID string
	At     int64 // the activity's UpdatedAt in Unix nanoseconds
	Allow  bool
}

// OpenPR opens the tab's pull request in the browser.
type OpenPR struct{ WorkspaceID string }

// MergePR asks to merge the tab's pull request, after a confirm.
type MergePR struct{ WorkspaceID string }

// RerunChecks re-runs the failed checks of the tab's pull request.
type RerunChecks struct{ WorkspaceID string }

// Archive asks to archive a tab whose pull request merged: close its
// panes, remove its worktree and delete its branch, after a confirm.
type Archive struct{ WorkspaceID string }

// RunUpdate is a click on the footer's update button.
type RunUpdate struct{}

// SetProjectAppearance carries the project's whole appearance: a lucide
// icon name and an aide color id.
type SetProjectAppearance struct{ ProjectID, Icon, Color string }

// MoveToGroup puts tabs in GroupID; "" takes them out of their group.
type MoveToGroup struct {
	WorkspaceIDs []string
	GroupID      string
}

// NewGroup makes a group of the picked tabs. The sidebar starts an
// inline rename on the group once it shows up in the state.
type NewGroup struct{ WorkspaceIDs []string }
type RenameGroup struct{ GroupID, Name string }

// Ungroup deletes the group; its tabs become ungrouped.
type Ungroup struct{ GroupID string }

// NewWorktreeSession asks for a tab in a fresh git worktree of the group's
// repository.
type NewWorktreeSession struct{ GroupID string }

// NewTaskIn opens the New task dialog on the group's folder.
type NewTaskIn struct{ GroupID string }

// DropTask takes a queued task off the queue, and with Start starts it.
type DropTask struct {
	ID    string
	Start bool
}
