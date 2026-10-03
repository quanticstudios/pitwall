// Package proto is the wire format between the daemon and its clients over a
// unix socket. Framing and the client/server conn types live beside this file.
package proto

import (
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Version bumps on any incompatible change; the daemon refuses other versions.
// Version 5 added MoveSession and MoveGroup for drag-and-drop ordering.
// Version 4 added Workspace.NameSet and Label and Pane.Prompt.
// Version 3 added tabs, detach, kill, group by folder, Sync and FocusSession.
// Version 2 added sessions and groups (NewSession, SetSessionGroup, NewGroup,
// RenameGroup, DeleteGroup, Hello.Cwd) and length-prefixed frames. Any change
// to a message's fields or meaning must bump it; TestWireFingerprint fails
// until it does.
const Version = 5

// Client to daemon.

type Hello struct {
	Version int
	Kind    string // "gui", "hook", "cli"
	// Cwd is where the GUI was launched. When no session exists yet, the
	// daemon opens one there with a shell, so pitwall starts like tmux.
	Cwd string
}

// NewSession opens an ungrouped session (or one in GroupID) with a shell
// pane in Cwd ("" means $HOME). An empty Name gets a generated one.
type NewSession struct {
	Name    string // "" generates one
	Cwd     string
	GroupID string
	// FromPane, when set, starts the session in that pane's current
	// directory (where its shell is now), falling back to Cwd.
	FromPane string
}

// SetSessionGroup moves a session into a group; "" ungroups it.
type SetSessionGroup struct {
	WorkspaceID string
	GroupID     string
}

// NewGroup makes a group from sessions picked after the fact.
type NewGroup struct {
	Name         string
	WorkspaceIDs []string
}

type RenameGroup struct {
	GroupID string
	Name    string
}

// DeleteGroup removes the group and ungroups its sessions; nothing closes.
type DeleteGroup struct {
	GroupID string
}

type Input struct {
	Pane string
	Data []byte
}

type Resize struct {
	Pane       string
	Cols, Rows int
}

type AddProject struct {
	Path string
}

type NewWorkspace struct {
	ProjectID string
	Name      string // also the branch name for git projects
}

type RenameWorkspace struct {
	WorkspaceID string
	Name        string
}

type ArchiveWorkspace struct {
	WorkspaceID string
	Archived    bool
}

type DeleteWorkspace struct {
	WorkspaceID  string
	RemoveBranch bool
}

// OpenPane splits Target (or creates the first pane when Target is "").
type OpenPane struct {
	WorkspaceID string
	TabID       string // "" means the session's active tab
	Target      string
	Dir         layout.Dir
	Cmd         []string // empty means the user's shell
}

// Scroll moves the pane's view into scrollback. Lines > 0 goes back in
// history; the daemon clamps the offset and snaps to 0 on new input.
type Scroll struct {
	Pane  string
	Lines int
}

type ClosePane struct {
	Pane string
}

type SetLayout struct {
	WorkspaceID string
	TabID       string
	Layout      *layout.Node // ratio changes from dragging dividers
}

// AgentEvent comes from `pitwall hook <provider>`, run by an agent's hook or
// notify config inside a pane. Pane comes from $PITWALL_PANE.
type AgentEvent struct {
	Pane     string
	Provider model.Provider
	Payload  []byte // the hook's JSON, untouched
}

// Daemon to client.

type StateMsg struct {
	State model.State
}

type Frame struct {
	Pane  string
	Grid  vt.Grid
	Modes vt.Modes
	// ScrollOffset is how many lines above the live screen the view starts;
	// ScrollMax is the scrollback length. Both 0 when there is no history.
	ScrollOffset, ScrollMax int
}

type PaneExited struct {
	Pane     string
	ExitCode int
}

type Error struct {
	Message string
}

// NewTab opens a tab with a shell, in FromPane's current directory when set,
// else Cwd, else the session's Path, and makes it the active tab.
type NewTab struct {
	WorkspaceID string
	Cwd         string
	FromPane    string
}

type CloseTab struct {
	WorkspaceID string
	TabID       string
}

// RenameTab names a tab. With Pane set (from `pitwall tab rename` inside a
// pane) the daemon resolves the pane's session and tab itself. An empty
// Name goes back to the automatic title.
type RenameTab struct {
	WorkspaceID string
	TabID       string
	Pane        string
	Name        string
}

// SelectTab records the tab a GUI shows, so attaching opens on it.
type SelectTab struct {
	WorkspaceID string
	TabID       string
}

// DetachSession hides a running session from the sidebar, or brings it back.
// Its processes keep running either way. It replaces ArchiveWorkspace.
type DetachSession struct {
	WorkspaceID string
	Detached    bool
}

// KillSession closes a session's panes and forgets it. It never touches the
// disk, unlike DeleteWorkspace, which removes a worktree pitwall made.
type KillSession struct {
	WorkspaceID string
}

// GroupByFolder puts every ungrouped session with the same RepoRoot as
// WorkspaceID into one group: the group whose Root is that folder, or a new
// one named after it.
type GroupByFolder struct {
	WorkspaceID string
}

// MoveSession drags a session into GroupID ("" for ungrouped) and places it
// before the session Before, or last when Before is "". The sidebar shows
// sessions in this stored order.
type MoveSession struct {
	WorkspaceID string
	GroupID     string
	Before      string
}

// MoveGroup places a group before the group Before, or last when "".
type MoveGroup struct {
	GroupID string
	Before  string
}

// Sync asks the daemon to reply with a StateMsg once every request before it
// on this connection is handled. CLI clients use it as an acknowledgement.
type Sync struct{}

// FocusSession tells GUIs to show a session (un-detaching it), from
// `pitwall attach`.
type FocusSession struct {
	WorkspaceID string
	TabID       string
}

// Messages lists every type that crosses the socket, for gob registration.
var Messages = []any{
	Hello{}, Input{}, Resize{}, AddProject{}, NewWorkspace{}, RenameWorkspace{},
	ArchiveWorkspace{}, DeleteWorkspace{}, OpenPane{}, Scroll{}, ClosePane{}, SetLayout{},
	NewSession{}, SetSessionGroup{}, NewGroup{}, RenameGroup{}, DeleteGroup{},
	NewTab{}, CloseTab{}, RenameTab{}, SelectTab{}, DetachSession{}, KillSession{},
	GroupByFolder{}, Sync{}, FocusSession{}, MoveSession{}, MoveGroup{},
	AgentEvent{}, StateMsg{}, Frame{}, PaneExited{}, Error{},
}
