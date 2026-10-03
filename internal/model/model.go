// Package model holds the types every other package shares. It imports only
// the standard library and internal/layout.
package model

import (
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
)

type ProjectKind string

const (
	ProjectGit    ProjectKind = "git"
	ProjectFolder ProjectKind = "folder"
	ProjectGroup  ProjectKind = "group" // made by grouping sessions; no Root
)

// Project is a group of sessions. Sessions start ungrouped; a group is made
// after the fact from sessions the user picks. Git and folder projects also
// carry a Root, which new-worktree actions use.
type Project struct {
	ID    string
	Name  string
	Root  string // repo root or folder path; "" for a group
	Kind  ProjectKind
	Color string // aide color id: "neutral", "red", "blue", ...
	Icon  string // aide lucide icon name ("folder", "code", ...); "" means "folder"
}

// Tab is the split tree of panes a session shows. A session has exactly one
// tab: the user sees the session as a tab, so the two words name one thing.
type Tab struct {
	ID string
	// Name is unused: the session's Name is the tab's name. Older state
	// files carried one per tab, which the store moves onto the session.
	Name string
	// Title is what the tab shows; the daemon keeps it current and it is
	// never empty. Highest first: the session's Name when NameSet; the
	// cleaned OSC title of an agent pane unless generic; the first prompt of
	// an agent pane (Pane.Prompt); a non-generic OSC title of a pane running
	// a command; that command's name; the live directory of the first pane
	// ("~" for home, else its base name). Generic means empty, "~", "claude",
	// "claude code", "codex", or (case-insensitively) the base name of the
	// session directory or repo root, or the user's login name.
	Title  string
	Layout *layout.Node
}

// Workspace is one session, which the user sees as one tab: a split tree of
// panes started in Path. Its default Name is generated ("swift-otter") and
// unique among sessions; it is the stable handle the CLI addresses.
type Workspace struct {
	ID        string
	ProjectID string // "" while the session is ungrouped
	Name      string
	// NameSet is true when the user or the CLI chose Name (NewSession.Name,
	// RenameWorkspace, RenameTab, NewWorkspace.Name) and false when pitwall
	// generated it.
	NameSet bool
	// Label is the title of the session's tab (see Tab.Title), so it follows
	// the work and is never empty. A GUI shows Label first and keeps a
	// generated Name as the quiet secondary the CLI addresses.
	Label  string
	Branch string // git branch of Path, "" outside a repo
	Path   string // directory the session started in
	// WorktreeRoot is the repo root when pitwall created Path as a git
	// worktree for this session. Only then does deleting the session remove
	// the directory.
	WorktreeRoot string
	// RepoRoot is the git toplevel of Path, or Path outside a repo. Group by
	// folder uses it.
	RepoRoot  string
	Detached  bool // running but hidden from the sidebar until attached
	UpdatedAt time.Time
	Tabs      []Tab  // exactly one; none for a project workspace before OpenPane
	ActiveTab string // the id of Tabs[0]
}

type Provider string

const (
	ProviderClaude   Provider = "claude"
	ProviderCodex    Provider = "codex"
	ProviderTerminal Provider = "terminal"
)

type Pane struct {
	ID          string
	WorkspaceID string
	Cmd         []string // argv as launched; empty means the user's shell
	// Cwd is where the pane started; the daemon follows a shell at its
	// prompt into the directory it changes to, about once a second.
	Cwd       string
	Title     string // last OSC title, spinner and status glyphs stripped
	Exited    bool
	ExitCode  int
	Provider  Provider // "" until a hook reports or detection sees an agent
	SessionID string   // agent session id from hooks, used to resume
	// Prompt is the first prompt of the agent session (SessionID), first
	// line only, whitespace collapsed and cut to 48 runes. A new session
	// clears it; the next prompt fills it.
	Prompt string
}

type MergeStatus string

const (
	MergeUnknown   MergeStatus = ""
	MergeUpToDate  MergeStatus = "up-to-date"
	MergeClean     MergeStatus = "clean"
	MergeConflicts MergeStatus = "conflicts"
)

type BranchStats struct {
	Additions   int
	Deletions   int
	MergeStatus MergeStatus
	Ahead       int
	Behind      int
}

// State is everything a client needs to draw the sidebar and the layout.
// The daemon sends a fresh copy on every change.
type State struct {
	Version    uint64
	Projects   []Project
	Workspaces []Workspace
	Panes      []Pane
	Activities []Activity             // one per pane with an agent or a running command
	Stats      map[string]BranchStats // keyed by workspace id
}
