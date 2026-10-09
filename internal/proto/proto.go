// Package proto is the wire format between the daemon and its clients over a
// unix socket. Framing and the client/server conn types live beside this file.
package proto

import (
	"fmt"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Version is the protocol major. A client and a daemon interoperate when
// their Versions match, whatever their Levels; a daemon refuses any other
// Version with an Error that RefusedVersion reads.
//
// Level counts additive changes within a Version. When a message changes,
// decide which kind of change it is:
//
//   - Additive, bump Level: a new field in a struct, or a new message type
//     appended to Messages, where a peer that ignores the field or never
//     sends the message still behaves correctly. gob leaves a field the
//     sender lacks at its zero value and skips one the receiver lacks, and
//     Recv returns Unknown for a type this build lacks. A daemon sends a
//     message type added at level N only to clients whose Hello.Level is at
//     least N, and a client sends one only when the daemon's StateMsg.Level
//     is at least N: peers below level 1 drop the connection on a type they
//     do not know.
//   - Breaking, bump Version and set Level to 0: a field removed or renamed,
//     a field's type changed, a message type removed, or a field or message
//     whose meaning changes so that an older peer would act on it wrongly.
//     Running daemons then refuse the new clients, and the GUI asks to
//     restart the daemon, which stops every program running in a pane.
//
// Hello and AgentEvent never change, and neither does the refusal: agents'
// hooks run whatever binary is on PATH, so daemons serve a Hello{Kind:
// "hook"} of any Version, and a hook that an older daemon refuses says
// Hello again at the Version the refusal names.
//
// TestWireFingerprint checks a layout change against testdata/wire.txt and
// fails until the right number is bumped.
//
// Level 11 added NewWorkspace.From and Cmd, DeleteWorkspace.Force,
// WorktreeQuery, WorktreeInfo and DeleteWorktree.
// Level 10 added State.Overlaps, the conflict radar.
// Level 9 added State.PRs, the pull request of each tab's branch.
// Level 8 added Answer, Allow or Deny from a GUI.
// Level 7 added Pane.HooksMissing.
// Level 6 added Workspace.Ports, the port block of a worktree tab.
// Level 5 added Scroll.Prompts and State.Clipboard, and GUIs that send
// SeePane on every change of their focused pane, "" for none.
// Level 4 added Search, SearchResult and Frame.ScrollPushed.
// Level 3 added model.BranchStats.Base, the default branch's ref.
// Level 2 added State.Notice and DismissNotice: why the daemon started
// without the saved tabs.
// Level 1 added Hello.Level, StateMsg.Level and Unknown, and made a
// matching Version enough. Daemons of Version 16 and earlier refuse every
// Version but their own and ignore Level.
//
// Version 16 added NewSession.Loose, a tab outside every group.
// Version 15 added Pane.Transcript, the agent session's own file.
// Version 14 added Pane.Held and ExitUnknown: a held pane survives a daemon
// restart.
// Version 13 added Pane.AgentMode.
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
// RenameGroup, DeleteGroup, Hello.Cwd) and length-prefixed frames.
const Version = 16

// Level is the count of additive changes within Version; see Version.
const Level = 11

// Since is the Level that added msg's type, 0 for one every daemon of this
// Version knows. A client sends msg only to a daemon at that Level or above.
func Since(msg any) int {
	switch msg.(type) {
	case WorktreeQuery, WorktreeInfo, DeleteWorktree:
		return 11
	case Answer:
		return 8
	case Search, SearchResult:
		return 4
	case DismissNotice:
		return 2
	}
	return 0
}

// Client to daemon.

type Hello struct {
	Version int
	// Level is the client's proto.Level, 0 from clients before level 1.
	Level int
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
	// Cmd, when set, runs in the pane instead of the shell. The pane is
	// Held: it stays after the command exits, Exited with its ExitCode,
	// until closed, across daemon restarts too (see daemon.NewWith).
	Cmd []string
	// FromPane, when set, starts the session in that pane's current
	// directory (where its shell is now), falling back to Cwd.
	FromPane string
	// Loose keeps the tab out of every group, even one for its folder.
	Loose bool
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
	// Name names the tab and, in a git group, the worktree's folder and a
	// new branch. "" takes the branch From checks out, else a generated name.
	Name string
	// From is what a git group's worktree checks out; a daemon below Level
	// 8 ignores it and makes a new branch off the default one.
	From model.WorktreeFrom
	// Cmd, when set, runs in the tab's first pane, held as NewSession.Cmd
	// is; else the tab starts with no pane.
	Cmd []string
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
	// Force removes a worktree with changes and an unmerged branch, which
	// git refuses otherwise. A daemon below Level 8 ignores it.
	Force bool
}

// WorktreeQuery asks about worktrees. The daemon answers this client
// alone with a WorktreeInfo: with ProjectID, what a new worktree of that
// git group can start from; with WorkspaceID, what deleting that tab's
// worktree would lose; with Orphans, after git worktree prune in every git
// group's repo, the worktrees no tab uses.
type WorktreeQuery struct {
	ProjectID   string
	WorkspaceID string
	Orphans     bool
}

// WorktreeInfo answers Query. Err is why the daemon could not.
type WorktreeInfo struct {
	Query WorktreeQuery
	Err   string
	// For ProjectID, as gitstat.Refs: the default branch ("origin/main"),
	// the local and remote branches, and whether origin is on GitHub.
	Default       string
	Local, Remote []string
	GitHub        bool
	// For WorkspaceID: what git status lists, and whether the tab's branch
	// has commits the default branch lacks.
	Changed  []string
	Unmerged bool
	// For Orphans, every git group's in the daemon.
	Orphans []model.Orphan
}

// DeleteWorktree removes the worktree at Path under <Root>/.worktrees/,
// where Root is a git group's, when no tab uses it, keeping its branch.
// Force removes it with changes.
type DeleteWorktree struct {
	Root, Path string
	Force      bool
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
// Prompts then moves it that many shell prompts (OSC 133 marks) back, or
// forward when negative, putting the prompt at the top; a daemon below
// Level 5 ignores it and the view stays.
type Scroll struct {
	Pane    string
	Lines   int
	Prompts int
}

// Search asks for every match of Query in Pane's scrollback and screen
// (vt.Emulator.Search). The daemon answers this client alone with a
// SearchResult.
type Search struct {
	Pane  string
	Query string
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
// notify config inside a pane. Pane comes from $PITWALL_PANE. Its fields
// never change; see Version.
type AgentEvent struct {
	Pane     string
	Provider model.Provider
	Payload  []byte // the hook's JSON, untouched
}

// Daemon to client.

type StateMsg struct {
	State model.State
	// Level is the daemon's proto.Level, 0 from daemons before level 1.
	Level int
}

type Frame struct {
	Pane  string
	Grid  vt.Grid
	Modes vt.Modes
	// ScrollOffset is how many lines above the live screen the view starts;
	// ScrollMax is the scrollback length. Both 0 when there is no history.
	ScrollOffset, ScrollMax int
	// ScrollPushed counts lines that ever entered the pane's history: row y
	// of Grid shows line ScrollPushed-ScrollOffset+y, as vt.Match numbers
	// lines. 0 from daemons below Level 4.
	ScrollPushed uint64
}

// SearchResult answers a Search: the newest matches of Query, oldest
// first, and More when older ones were left out.
type SearchResult struct {
	Pane    string
	Query   string
	Matches []vt.Match
	More    bool
}

type PaneExited struct {
	Pane     string
	ExitCode int
}

type Error struct {
	Message string
}

// Unknown is what Recv returns for a message of a type this build does not
// know, from a peer at a higher Level. It never crosses the socket.
type Unknown struct {
	Name string // the type's gob name, such as "<module>/internal/proto.Thing"
}

// refusal is the daemon's answer to a Hello of another Version, unchanged
// since Version 2 so that clients of any Version can read it.
const refusal = "daemon speaks protocol version %d; send Hello{Version: %d} first"

// Refusal is the Error a daemon sends to a Hello of another Version.
func Refusal() Error { return Error{Message: fmt.Sprintf(refusal, Version, Version)} }

// RefusedVersion reports whether e refused a Hello for its Version, and the
// daemon's Version when it did.
func RefusedVersion(e Error) (int, bool) {
	var v, again int
	n, _ := fmt.Sscanf(e.Message, refusal, &v, &again)
	return v, n == 2
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
// which marks its activity seen and clears an OSC notification. A GUI of
// SeeFocusLevel or above sends it whenever that pane changes, with Pane ""
// when it shows none focused, and the daemon keeps a bell in the pane it
// shows quiet. A daemon below that Level ignores Pane "".
type SeePane struct {
	Pane string
}

// SeeFocusLevel is the Level of GUIs whose SeePane follows their focus.
// Older ones send it only for a pane whose activity is unseen, so the
// daemon does not take it as their focus.
const SeeFocusLevel = 5

// DismissNotice clears State.Notice in every window, when it still is
// Notice, from the close button on it.
type DismissNotice struct {
	Notice string
}

// Answer presses Allow or Deny on Pane's permission prompt, as the phone
// page's buttons do: only while Pane's activity is still the one whose
// UpdatedAt, in Unix nanoseconds, is At, and its screen shows the prompt.
type Answer struct {
	Pane  string
	At    int64
	Allow bool
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
	DismissNotice{},
	Search{}, SearchResult{},
	Answer{},
	WorktreeQuery{}, WorktreeInfo{}, DeleteWorktree{},
}
