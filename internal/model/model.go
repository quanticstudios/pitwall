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

// Workspace is one session: a split tree of panes started in Path.
type Workspace struct {
	ID        string
	ProjectID string // "" while the session is ungrouped
	Name      string
	Branch    string // git branch of Path, "" outside a repo
	Path      string // directory the session started in
	// WorktreeRoot is the repo root when pitwall created Path as a git
	// worktree for this session. Only then does deleting the session remove
	// the directory.
	WorktreeRoot string
	Archived     bool
	UpdatedAt    time.Time
	Layout       *layout.Node // nil until the first pane opens
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
	Cwd         string
	Title       string // last OSC title
	Exited      bool
	ExitCode    int
	Provider    Provider // "" until a hook reports or detection sees an agent
	SessionID   string   // agent session id from hooks, used to resume
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
