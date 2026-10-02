// Package proto is the wire format between the daemon and its clients over a
// unix socket. Framing and the client/server conn types live beside this file.
package proto

import (
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Version bumps on any incompatible change; the daemon refuses other versions.
const Version = 1

// Client to daemon.

type Hello struct {
	Version int
	Kind    string // "gui", "hook", "cli"
	// Cwd is where the GUI was launched. When no session exists yet, the
	// daemon opens one there with a shell, so pitwall starts like tmux.
	Cwd string
}

// NewSession opens an ungrouped session (or one in GroupID) with a shell
// pane in Cwd ("" means $HOME). The name defaults to Cwd's base name.
type NewSession struct {
	Cwd     string
	GroupID string
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

// Messages lists every type that crosses the socket, for gob registration.
var Messages = []any{
	Hello{}, Input{}, Resize{}, AddProject{}, NewWorkspace{}, RenameWorkspace{},
	ArchiveWorkspace{}, DeleteWorkspace{}, OpenPane{}, Scroll{}, ClosePane{}, SetLayout{},
	NewSession{}, SetSessionGroup{}, NewGroup{}, RenameGroup{}, DeleteGroup{},
	AgentEvent{}, StateMsg{}, Frame{}, PaneExited{}, Error{},
}
