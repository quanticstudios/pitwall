// Package model holds the types every other package shares. It imports only
// the standard library and internal/layout.
package model

import (
	"slices"
	"strconv"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
)

type ProjectKind string

const (
	ProjectGit    ProjectKind = "git"
	ProjectFolder ProjectKind = "folder"
	ProjectGroup  ProjectKind = "group" // made by grouping tabs; no Root
)

// Project is a group of tabs within one Session. Tabs start ungrouped; a
// group is made after the fact from tabs the user picks. Git and folder
// projects also carry a Root, which new-worktree actions use.
type Project struct {
	ID        string
	SessionID string // the Session it belongs to
	Name      string
	Root      string // repo root or folder path; "" for a group
	Kind      ProjectKind
	Color     string // aide color id: "neutral", "red", "blue", ...
	Icon      string // aide lucide icon name ("folder", "code", ...); "" means "folder"
}

// Tab is the split tree of panes a Workspace shows. A workspace has exactly
// one: the user sees the workspace as a tab.
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
	// "claude code", "codex", "pi", "π", or (case-insensitively) the base
	// name of the session directory or repo root, or the user's login name.
	Title  string
	Layout *layout.Node
}

// Workspace is one tab of a Session: a split tree of panes started in Path.
// Name is set only when a person chose one, and is unique in its session.
type Workspace struct {
	ID        string
	SessionID string // the Session that owns this tab
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
	// Ports is the block of TCP ports the daemon reserved for the worktree
	// at Path, set only with WorktreeRoot. Its panes get it as PORT,
	// PITWALL_PORT_BASE and PITWALL_PORTS; no other tab has the block while
	// this one exists.
	Ports PortBlock
	// RepoRoot is the git toplevel of Path, or Path outside a repo. Group by
	// folder uses it.
	RepoRoot  string
	Detached  bool // running but hidden from the sidebar until attached
	UpdatedAt time.Time
	Tabs      []Tab  // exactly one; none for a project workspace before OpenPane
	ActiveTab string // the id of Tabs[0]
}

// PortBlock is the TCP ports First to Last; the zero value is none.
type PortBlock struct{ First, Last int }

// String is "3010-3019", or "" for none.
func (b PortBlock) String() string {
	if b.First == 0 {
		return ""
	}
	return strconv.Itoa(b.First) + "-" + strconv.Itoa(b.Last)
}

type Provider string

const (
	ProviderClaude   Provider = "claude"
	ProviderCodex    Provider = "codex"
	ProviderPi       Provider = "pi"
	ProviderGemini   Provider = "gemini"
	ProviderOpenCode Provider = "opencode"
	ProviderTerminal Provider = "terminal"
)

type Pane struct {
	ID          string
	WorkspaceID string
	Cmd         []string // argv as launched; empty means the user's shell
	// Cwd is where the pane started; the daemon follows a shell at its
	// prompt into the directory it changes to, about once a second.
	Cwd      string
	Title    string // last OSC title, spinner and status glyphs stripped
	Exited   bool
	ExitCode int
	// ExitUnknown marks an Exited pane whose exit code was lost: the daemon
	// restarted while its command ran and did not run it again.
	ExitUnknown bool
	// Held panes run a command from NewSession.Cmd and stay, Exited, after
	// it ends, until closed. A restart keeps them; see daemon.NewWith.
	Held      bool
	Provider  Provider // "" until a hook reports or detection sees an agent
	SessionID string   // agent session id from hooks, used to resume
	// Prompt is the first prompt of the agent session (SessionID), first
	// line only, whitespace collapsed and cut to 48 runes. A new session
	// clears it; the next prompt fills it.
	Prompt string
	// AgentMode is the permission mode a resume starts the agent in: the
	// mode its latest hook reported (agent.PermissionMode), except that an
	// agent that reported bypassPermissions keeps it while the same process
	// runs, so one started with bypass (cdang) resumes with it.
	AgentMode string
	// Transcript is the agent session's own file, from its latest hook:
	// Claude's transcript, Codex's rollout or pi's session. "" until a hook
	// names one. Not saved; the next hook after a restart names it again.
	Transcript string `json:"-"`
	// HooksMissing marks a pane whose agent has hooks `pitwall hooks
	// install` sets up, but none of them reported: the agent runs with a
	// turn on its screen, or has run for a while, without one. The daemon
	// fills it in; it is not saved.
	HooksMissing bool `json:"-"`
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
	// Base is the default branch's ref the rest is counted against, such as
	// "refs/remotes/origin/main"; "" from a daemon before proto.Level 2.
	Base string
}

// Session is a named set of tabs and groups, like a tmux session. A window
// shows one session; every session keeps running in the daemon. A session
// ends when its last tab does.
type Session struct {
	ID   string
	Name string // unique; generated ("swift-otter") until renamed
	// Order is the sidebar order of the session's top-level items: group
	// IDs (Project.ID) and the IDs of ungrouped tabs, interleaved. A
	// group's tabs keep their relative order from State.Workspaces. Read it
	// through State.TopOrder, which repairs a stale or missing Order.
	Order  []string
	UsedAt time.Time // when a window last showed it, or when it was made
	// Windows is how many GUI windows show the session now. The daemon
	// fills it in; it is not saved.
	Windows int `json:"-"`
}

// State is everything a client needs to draw the sidebar and the layout.
// The daemon sends a fresh copy on every change.
type State struct {
	Version    uint64
	Sessions   []Session // in the order the switcher lists them
	Projects   []Project
	Workspaces []Workspace
	Panes      []Pane
	Activities []Activity             // one per pane with an agent or a running command
	Stats      map[string]BranchStats // keyed by workspace id
	// Decide is the decision features' status. The daemon fills it in; it
	// is not saved.
	Decide DecideInfo `json:"-"`
	// Notice is what a window shows until someone dismisses it: why the
	// daemon started without the saved tabs, or that it restored some. It
	// is not saved.
	Notice string `json:"-"`
	// Clipboard is the latest text a program put on the clipboard with OSC
	// 52. The daemon fills it in; it is not saved.
	Clipboard Clipboard `json:"-"`
}

// Clipboard is an OSC 52 clipboard write. A GUI writes Text once per Seq;
// the daemon leaves Text out of states sent a few seconds after the write.
type Clipboard struct {
	Seq  uint64
	Text string
}

// DecideInfo is what clients show about decision models.
type DecideInfo struct {
	Provider string // "jev" or "command" when one is set up and usable, else ""
	Counts   []DecideCount
}

// DecideCount is one feature's calls and failed calls today.
type DecideCount struct {
	Feature       string
	Calls, Errors int
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

// Session returns the session id, or nil.
func (s *State) Session(id string) *Session {
	if i := slices.IndexFunc(s.Sessions, func(x Session) bool { return x.ID == id }); i >= 0 {
		return &s.Sessions[i]
	}
	return nil
}

// SessionNamed returns the session called name, or nil.
func (s *State) SessionNamed(name string) *Session {
	if i := slices.IndexFunc(s.Sessions, func(x Session) bool { return x.Name == name }); i >= 0 {
		return &s.Sessions[i]
	}
	return nil
}

// Recent is the most recently used session, or nil when there is none.
func (s *State) Recent() *Session {
	var best *Session
	for i := range s.Sessions {
		if best == nil || s.Sessions[i].UsedAt.After(best.UsedAt) {
			best = &s.Sessions[i]
		}
	}
	return best
}

// RecentFree is the most recently used session no window shows, or nil.
// Windows must be filled in, as it is in the daemon's snapshots.
func (s *State) RecentFree() *Session {
	var best *Session
	for i := range s.Sessions {
		if s.Sessions[i].Windows == 0 && (best == nil || s.Sessions[i].UsedAt.After(best.UsedAt)) {
			best = &s.Sessions[i]
		}
	}
	return best
}

// SessionOf is the session of the tab or group id, "" when there is none.
func (s *State) SessionOf(id string) string {
	for _, w := range s.Workspaces {
		if w.ID == id {
			return w.SessionID
		}
	}
	for _, p := range s.Projects {
		if p.ID == id {
			return p.SessionID
		}
	}
	return ""
}

// View is the state with only session's groups, tabs, panes, activities
// and stats; Sessions stays whole.
func (s *State) View(session string) State {
	v := *s
	v.Projects = slices.DeleteFunc(slices.Clone(s.Projects), func(p Project) bool { return p.SessionID != session })
	v.Workspaces = slices.DeleteFunc(slices.Clone(s.Workspaces), func(w Workspace) bool { return w.SessionID != session })
	in := make(map[string]bool, len(v.Workspaces))
	for _, w := range v.Workspaces {
		in[w.ID] = true
	}
	v.Panes = slices.DeleteFunc(slices.Clone(s.Panes), func(p Pane) bool { return !in[p.WorkspaceID] })
	v.Activities = slices.DeleteFunc(slices.Clone(s.Activities), func(a Activity) bool { return !in[a.WorkspaceID] })
	v.Stats = make(map[string]BranchStats, len(v.Workspaces))
	for id, st := range s.Stats {
		if in[id] {
			v.Stats[id] = st
		}
	}
	return v
}

// TopOrder is session's top-level items in sidebar order: its Order with
// unknown and repeated IDs dropped, then any item Order misses, ungrouped
// tabs before groups. A tab whose group is gone counts as ungrouped.
// Detached tabs keep their place.
func (s *State) TopOrder(session string) []string {
	groups := make(map[string]bool, len(s.Projects))
	for _, p := range s.Projects {
		if p.SessionID == session {
			groups[p.ID] = true
		}
	}
	var implied []string
	for _, w := range s.Workspaces {
		if w.SessionID == session && !groups[w.ProjectID] {
			implied = append(implied, w.ID)
		}
	}
	for _, p := range s.Projects {
		if p.SessionID == session {
			implied = append(implied, p.ID)
		}
	}
	top := make(map[string]bool, len(implied))
	for _, id := range implied {
		top[id] = true
	}
	var order []string
	if ss := s.Session(session); ss != nil {
		order = ss.Order
	}
	out := make([]string, 0, len(implied))
	for _, id := range order {
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

// Ordered is every tab of session, detached ones included, in sidebar
// order: the top-level items in TopOrder, each group's tabs at the group's
// place.
func (s *State) Ordered(session string) []Workspace {
	groups := make(map[string]bool, len(s.Projects))
	for _, p := range s.Projects {
		if p.SessionID == session {
			groups[p.ID] = true
		}
	}
	inGroup := map[string][]Workspace{}
	loose := map[string]Workspace{}
	for _, w := range s.Workspaces {
		switch {
		case w.SessionID != session:
		case groups[w.ProjectID]:
			inGroup[w.ProjectID] = append(inGroup[w.ProjectID], w)
		default:
			loose[w.ID] = w
		}
	}
	var out []Workspace
	for _, id := range s.TopOrder(session) {
		if w, ok := loose[id]; ok {
			out = append(out, w)
		} else {
			out = append(out, inGroup[id]...)
		}
	}
	return out
}

// setOrder stores order as the Order of id's session.
func (s *State) setOrder(id string, order []string) {
	if ss := s.Session(s.SessionOf(id)); ss != nil {
		ss.Order = order
	}
}

// DeleteGroup drops group id and ungroups its tabs into its place, in
// their order.
func (s *State) DeleteGroup(id string) {
	session := s.SessionOf(id)
	order := s.TopOrder(session)
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
	s.setOrder(id, slices.Replace(order, at, at+1, tabs...))
	s.Projects = slices.DeleteFunc(s.Projects, func(p Project) bool { return p.ID == id })
}

// PlaceTop moves the top-level item id before the top-level item before
// of the same session, or last when before is "" or not top-level.
func (s *State) PlaceTop(id, before string) {
	o := slices.DeleteFunc(s.TopOrder(s.SessionOf(id)), func(x string) bool { return x == id })
	at := slices.Index(o, before)
	if before == "" || at < 0 {
		at = len(o)
	}
	s.setOrder(id, slices.Insert(o, at, id))
}

// PlaceTopAfter moves the top-level item id right after the top-level item
// after of the same session, or last when after is not top-level.
func (s *State) PlaceTopAfter(id, after string) {
	o := slices.DeleteFunc(s.TopOrder(s.SessionOf(id)), func(x string) bool { return x == id })
	at := slices.Index(o, after) + 1
	if at == 0 {
		at = len(o)
	}
	s.setOrder(id, slices.Insert(o, at, id))
}

// SessionSummary is what lists of sessions show about one.
type SessionSummary struct {
	Tabs, Detached int
	// Working counts agent panes at work. NeedsYou counts panes with a
	// question, an approval or an error, and finished turns and plans the
	// user has not seen; Unseen counts the panes with Activity.Unseen.
	Working, NeedsYou, Unseen int
	Agents                    []Provider // the agents running in it, Claude before Codex
	Active                    time.Time  // the newest tab's UpdatedAt
}

// Summary sums up session.
func (s *State) Summary(session string) SessionSummary {
	var out SessionSummary
	in := map[string]bool{}
	for _, w := range s.Workspaces {
		if w.SessionID != session {
			continue
		}
		in[w.ID] = true
		if w.Detached {
			out.Detached++
		} else {
			out.Tabs++
		}
		if w.UpdatedAt.After(out.Active) {
			out.Active = w.UpdatedAt
		}
	}
	agents := map[Provider]bool{}
	for _, p := range s.Panes {
		if in[p.WorkspaceID] && (p.Provider == ProviderClaude || p.Provider == ProviderCodex || p.Provider == ProviderPi) {
			agents[p.Provider] = true
		}
	}
	for _, a := range s.Activities {
		if !in[a.WorkspaceID] {
			continue
		}
		agent := a.Provider == ProviderClaude || a.Provider == ProviderCodex || a.Provider == ProviderPi
		if agent {
			agents[a.Provider] = true
		}
		switch {
		case agent && Pulses(&a):
			out.Working++
		case Tier(&a) == TierAttention || NeedsYou(a.State) && a.Unseen:
			out.NeedsYou++
		}
		if a.Unseen {
			out.Unseen++
		}
	}
	for _, p := range []Provider{ProviderClaude, ProviderCodex, ProviderPi} {
		if agents[p] {
			out.Agents = append(out.Agents, p)
		}
	}
	return out
}
