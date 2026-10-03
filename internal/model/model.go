// Package model holds the types every other package shares. It imports only
// the standard library and internal/layout.
package model

import (
	"slices"
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
	// Order is the sidebar order of the top-level items: group IDs
	// (Project.ID) and the IDs of ungrouped tabs, interleaved. A group's
	// tabs keep their relative order from Workspaces. Read it through
	// TopOrder, which repairs a stale or missing Order.
	Order      []string
	Panes      []Pane
	Activities []Activity             // one per pane with an agent or a running command
	Stats      map[string]BranchStats // keyed by workspace id
}

// LivePath is where the tab is now: the live working directory of the first
// pane in its layout when the daemon knows it, else the folder it started in.
func (s *State) LivePath(w Workspace) string {
	for _, t := range w.Tabs {
		if t.ID != w.ActiveTab {
			continue
		}
		if panes := layout.Panes(t.Layout); len(panes) > 0 {
			for _, p := range s.Panes {
				if p.ID == panes[0] && p.Cwd != "" {
					return p.Cwd
				}
			}
		}
	}
	return w.Path
}

// TopOrder is the top-level items in sidebar order: Order with unknown and
// repeated IDs dropped, then any item Order misses, ungrouped tabs before
// groups. A tab whose group is gone counts as ungrouped. Detached tabs keep
// their place.
func (s *State) TopOrder() []string {
	groups := make(map[string]bool, len(s.Projects))
	for _, p := range s.Projects {
		groups[p.ID] = true
	}
	var implied []string
	for _, w := range s.Workspaces {
		if !groups[w.ProjectID] {
			implied = append(implied, w.ID)
		}
	}
	for _, p := range s.Projects {
		implied = append(implied, p.ID)
	}
	top := make(map[string]bool, len(implied))
	for _, id := range implied {
		top[id] = true
	}
	out := make([]string, 0, len(implied))
	for _, id := range s.Order {
		if top[id] {
			out = append(out, id)
			delete(top, id)
		}
	}
	for _, id := range implied {
		if top[id] {
			out = append(out, id)
		}
	}
	return out
}

// Ordered is every tab, detached ones included, in sidebar order: the
// top-level items in TopOrder, each group's tabs at the group's place.
func (s *State) Ordered() []Workspace {
	groups := make(map[string]bool, len(s.Projects))
	for _, p := range s.Projects {
		groups[p.ID] = true
	}
	inGroup := map[string][]Workspace{}
	loose := map[string]Workspace{}
	for _, w := range s.Workspaces {
		if groups[w.ProjectID] {
			inGroup[w.ProjectID] = append(inGroup[w.ProjectID], w)
		} else {
			loose[w.ID] = w
		}
	}
	out := make([]Workspace, 0, len(s.Workspaces))
	for _, id := range s.TopOrder() {
		if w, ok := loose[id]; ok {
			out = append(out, w)
		} else {
			out = append(out, inGroup[id]...)
		}
	}
	return out
}

// DeleteGroup drops group id and ungroups its tabs into its place, in
// their order.
func (s *State) DeleteGroup(id string) {
	order := s.TopOrder()
	at := slices.Index(order, id)
	if at < 0 {
		return
	}
	var tabs []string
	for i := range s.Workspaces {
		if w := &s.Workspaces[i]; w.ProjectID == id {
			w.ProjectID = ""
			tabs = append(tabs, w.ID)
		}
	}
	s.Order = slices.Replace(order, at, at+1, tabs...)
	s.Projects = slices.DeleteFunc(s.Projects, func(p Project) bool { return p.ID == id })
}

// PlaceTop moves the top-level item id before the top-level item before,
// or last when before is "" or not top-level.
func (s *State) PlaceTop(id, before string) {
	o := slices.DeleteFunc(s.TopOrder(), func(x string) bool { return x == id })
	at := slices.Index(o, before)
	if before == "" || at < 0 {
		at = len(o)
	}
	s.Order = slices.Insert(o, at, id)
}

// PlaceTopAfter moves the top-level item id right after the top-level item
// after, or last when after is not top-level.
func (s *State) PlaceTopAfter(id, after string) {
	o := slices.DeleteFunc(s.TopOrder(), func(x string) bool { return x == id })
	at := slices.Index(o, after) + 1
	if at == 0 {
		at = len(o)
	}
	s.Order = slices.Insert(o, at, id)
}
