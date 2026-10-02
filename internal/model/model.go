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
)

type Project struct {
	ID    string
	Name  string
	Root  string // repo root or folder path
	Kind  ProjectKind
	Color string // aide color id: "neutral", "red", "blue", ...
	Icon  string // Nerd Font glyph name; "" means the default folder glyph
}

type Workspace struct {
	ID        string
	ProjectID string
	Name      string
	Branch    string
	Path      string // worktree path; equals Project.Root for folder projects
	Archived  bool
	UpdatedAt time.Time
	Layout    *layout.Node // nil until the first pane opens
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
	Provider    Provider // "" until an agent hook reports from this pane
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
