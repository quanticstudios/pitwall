// Package proto is the wire format between the daemon and its clients over a
// unix socket. Framing and the client/server conn types live beside this file.
package proto

import (
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Version bumps on any incompatible change; the daemon refuses other versions.
// Version 12 added NewSession.Cmd and Hello.Kind "watch", and a pane
// opened with NewSession.Cmd stays, Exited, after its process ends, until
// the daemon restarts.
// Version 11 added decision models: Activity.Advice, AdviceP, AdviceRule,
// Urgency and Review, and State.Decide.
// Version 10 added vt.Cell.Link, the OSC 8 hyperlink on each cell of a frame.
// Version 9 added sessions: model.Session and State.Sessions in place of
// State.Order, SessionID on tabs and groups, Hello.Session, SessionNew,
// SessionRename, SessionKill and SessionShow, and NewSession.SessionID,
// AddProject.SessionID and FocusSession.SessionID.
// Version 8 added Activity.Unseen and SeePane, and OSC 9/99/777
// notifications show as an awaiting-input activity.
// Version 7 added State.Order, one order for groups and ungrouped tabs: at
// the top level MoveSession.Before and MoveGroup.Before may name a group or
// a tab. Tabs get no generated name.
// Version 6 has one tab per session: NewTab creates a session, CloseTab
// kills one, RenameTab names one and SelectTab does nothing.
// Version 5 added MoveSession and MoveGroup for drag-and-drop ordering.
// Version 4 added Workspace.NameSet and Label and Pane.Prompt.
// Version 3 added tabs, detach, kill, group by folder, Sync and FocusSession.
// Version 2 added sessions and groups (NewSession, SetSessionGroup, NewGroup,
// RenameGroup, DeleteGroup, Hello.Cwd) and length-prefixed frames. Any change
// to a message's fields or meaning must bump it; TestWireFingerprint fails
// until it does.
const Version = 12

// Client to daemon.

type Hello struct {
	Version int
	// Kind is "gui", "hook", "cli" or "watch". A watch client gets the
	// StateMsg and PaneExited pushes a GUI gets, but no frames, and counts
	// as no window. StateMsg pushes coalesce: close changes arrive as one.
	Kind string
	// Cwd is where the GUI was launched. When no session exists yet, the
	// daemon opens one there with a shell, so pitwall starts like tmux.
	Cwd string
	// Session is the name of the session a GUI opens on, made (with a
	// shell in Cwd) when missing; "" means the most recently used one.
	// The daemon also gives that session a shell when all its tabs are
	// detached.
	Session string
}

// NewSession opens an ungrouped tab (or one in GroupID) with a shell pane
// in Cwd ("" means $HOME), last at the top level of session SessionID.
// SessionID "" means FromPane's session, else the most recently used one;
// with no session at all the daemon makes one. An empty Name leaves the tab
// unnamed.
type NewSession struct {
	Name      string // "" leaves it unnamed
	Cwd       string
	GroupID   string
	SessionID string
	// Cmd, when set, runs in the pane instead of the shell. The pane stays
	// after the command exits, Exited with its ExitCode, until closed or
	// until the daemon restarts, which forgets that it stays.
	Cmd []string
	// FromPane, when set, starts the session in that pane's current
	// directory (where its shell is now), falling back to Cwd.
	FromPane string
}

// SetSessionGroup moves a session into a group; "" ungroups it right after
// the group it was in.
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
	Path      string
	SessionID string // "" means the most recently used session
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

// NewTab opens a tab, which is a session of its own: right after
// WorkspaceID (or FromPane's session when WorkspaceID is "") in its group,
// or in its session's Order when it is ungrouped, unnamed, with a shell in FromPane's current
// directory when set, else Cwd, else that session's Path. A GUI shows the
// newest session.
type NewTab struct {
	WorkspaceID string
	Cwd         string
	FromPane    string
}

// CloseTab kills the session WorkspaceID, as KillSession does. TabID is
// ignored.
type CloseTab struct {
	WorkspaceID string
	TabID       string
}

// RenameTab names the session WorkspaceID, or Pane's session when Pane
// is set (from `pitwall tab rename` inside a pane), as RenameWorkspace does.
// An empty Name clears the name, back to the automatic title. TabID
// is ignored.
type RenameTab struct {
	WorkspaceID string
	TabID       string
	Pane        string
	Name        string
}

// SelectTab does nothing: a session has one tab. Kept so older GUIs need no
// change.
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

// MoveSession drags a session into GroupID and places it before the session
// Before of that group, or last when Before is "". With GroupID "" it goes
// to the top level, before the group or ungrouped tab Before in
// the session's Order, or last.
type MoveSession struct {
	WorkspaceID string
	GroupID     string
	Before      string
}

// MoveGroup places a group in its session's Order before the group or ungrouped tab
// Before, or last when "".
type MoveGroup struct {
	GroupID string
	Before  string
}

// Sync asks the daemon to reply with a StateMsg once every request before it
// on this connection is handled. CLI clients use it as an acknowledgement.
type Sync struct{}

// FocusSession tells the GUIs showing a tab's session to show the tab
// (un-detaching it) and raise their window, from `pitwall attach`. With
// WorkspaceID "" it raises the windows showing session SessionID.
type FocusSession struct {
	WorkspaceID string
	TabID       string
	SessionID   string
}

// SessionNew makes a session with one tab, a shell in FromPane's current
// directory when set, else Cwd, else $HOME. Name "" gets a generated name.
type SessionNew struct {
	Name     string
	Cwd      string
	FromPane string
}

// SessionRename renames a session. Names are unique and not empty.
type SessionRename struct {
	SessionID string
	Name      string
}

// SessionKill ends a session: its panes close and its tabs and groups go.
type SessionKill struct {
	SessionID string
}

// SessionShow tells the daemon a GUI window shows SessionID now, which
// counts it in Session.Windows and makes it the most recently used.
type SessionShow struct {
	SessionID string
}

// SeePane tells the daemon a GUI shows Pane focused in a focused window,
// which marks its activity seen and clears an OSC notification.
type SeePane struct {
	Pane string
}

// Messages lists every type that crosses the socket, for gob registration.
var Messages = []any{
	Hello{}, Input{}, Resize{}, AddProject{}, NewWorkspace{}, RenameWorkspace{},
	ArchiveWorkspace{}, DeleteWorkspace{}, OpenPane{}, Scroll{}, ClosePane{}, SetLayout{},
	NewSession{}, SetSessionGroup{}, NewGroup{}, RenameGroup{}, DeleteGroup{},
	NewTab{}, CloseTab{}, RenameTab{}, SelectTab{}, DetachSession{}, KillSession{},
	GroupByFolder{}, Sync{}, FocusSession{}, MoveSession{}, MoveGroup{}, SeePane{},
	SessionNew{}, SessionRename{}, SessionKill{}, SessionShow{},
	AgentEvent{}, StateMsg{}, Frame{}, PaneExited{}, Error{},
}
