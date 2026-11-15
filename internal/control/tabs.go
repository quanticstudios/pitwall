// Package control is what pitwall's commands for agents and scripts share:
// the ls --json rows, tab and pane addresses, and the wait evaluator. It
// reads a model.State and never the calling process's environment, so the
// CLI and the daemon can both use it; Caller says who asks.
package control

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Tab is one tab in pitwall ls --json. The shape is a contract: fields
// may be added, never renamed or dropped.
type Tab struct {
	N        int    `json:"n"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Group    string `json:"group"`
	Cwd      string `json:"cwd"`
	Branch   string `json:"branch"`
	Agent    string `json:"agent"`
	State    string `json:"state"`
	Question string `json:"question"`
	ExitCode *int   `json:"exit_code"` // null until the process exits, and when its code was lost
	Panes    int    `json:"panes"`
	Detached bool   `json:"detached"`
	// PaneList is every pane of the tab in layout order: N is the N of
	// its address, TAB.N.
	PaneList []Pane `json:"pane_list"`
	// Parent is the id of the tab that opened this one, "" for none.
	Parent string `json:"parent"`
	// Since is when the main pane's state started, null without an agent
	// activity.
	Since *time.Time `json:"since"`
	// PRState is why the branch's pull request cannot merge yet, a
	// model.Reason such as "checks_failing", or "ready"; "" with no PR.
	PRState string `json:"pr_state"`
	// QuietSince is when the working agent's screen last changed, once
	// it has been still for [agents] quiet_after; null otherwise.
	QuietSince *time.Time `json:"quiet_since"`
	Status     string     `json:"status"` // the line pitwall status set
	// Host is the [[hosts]] name of the machine the tab runs on, "" for
	// this one. A tab there has N 0, so "#$n" never reaches a tab here.
	Host string `json:"host"`
	// Addr is the tab's address for the other commands: "#3" here,
	// "box:3" on box, "@<id>" for a worker on a host out of reach.
	Addr string `json:"addr"`
	// Stale marks a worker on a host pitwall cannot reach now: its state
	// is "unknown".
	Stale bool `json:"stale"`
}

// Pane is one pane in Tab.PaneList, a contract like it.
type Pane struct {
	N        int        `json:"n"`
	ID       string     `json:"id"`
	Agent    string     `json:"agent"`
	State    string     `json:"state"`
	Question string     `json:"question"`
	ExitCode *int       `json:"exit_code"`
	Queued   int        `json:"queued"` // messages from pitwall send not yet typed
	Since    *time.Time `json:"since"`
}

// PaneOf is pane p as the Nth pane of its tab.
func PaneOf(state model.State, n int, p model.Pane) Pane {
	j := Pane{N: n, ID: p.ID, Queued: state.Queued[p.ID]}
	j.Agent, j.State, j.Question = PaneState(state, p)
	if p.Exited && !p.ExitUnknown {
		j.ExitCode = &p.ExitCode
	}
	if i := slices.IndexFunc(state.Activities, func(a model.Activity) bool { return a.PaneID == p.ID }); i >= 0 && !p.Exited && !state.Activities[i].UpdatedAt.IsZero() {
		j.Since = &state.Activities[i].UpdatedAt
	}
	return j
}

// Tabs is the rows of session's tabs, or of only when it is not nil.
func Tabs(state model.State, session string, only map[string]bool) []Tab {
	return Rows(state, session, only, nil)
}

// Rows is Tabs of a state that may hold other machines' tabs: where names
// a tab's machine and its number there ("" for this one); nil numbers
// every tab here.
func Rows(state model.State, session string, only map[string]bool, where func(model.Workspace) (string, int)) []Tab {
	out := []Tab{}
	for i, w := range state.Numbered(session) {
		if only != nil && !only[w.ID] {
			continue
		}
		host, n := "", i+1
		if where != nil {
			host, n = where(w)
		}
		t := Tab{N: n, ID: w.ID, Host: host, Addr: Addr(host, n), Title: Title(w), Cwd: state.LivePath(w), Branch: w.Branch, Detached: w.Detached, PRState: string(state.PRs[w.ID].Reason), Status: w.Status}
		if host != "" {
			t.N = 0
		}
		if slices.ContainsFunc(state.Workspaces, func(p model.Workspace) bool { return p.ID == w.Parent }) {
			t.Parent = w.Parent
		}
		for _, p := range state.Projects {
			if p.ID == w.ProjectID {
				t.Group = p.Name
			}
		}
		panes := TabPanes(state, w)
		t.Panes = len(panes)
		if p := MainPane(panes); p != nil {
			main := PaneOf(state, 0, *p)
			t.Agent, t.State, t.Question, t.ExitCode, t.Since = main.Agent, main.State, main.Question, main.ExitCode, main.Since
			for _, a := range state.Activities {
				if a.PaneID == p.ID && !a.QuietSince.IsZero() {
					t.QuietSince = &a.QuietSince
				}
			}
		}
		t.PaneList = []Pane{}
		for i, p := range panes {
			t.PaneList = append(t.PaneList, PaneOf(state, i+1, p))
		}
		out = append(out, t)
	}
	return out
}

// Addr is how a tab is addressed: #N here, box:N on a host.
func Addr(host string, n int) string {
	if host == "" {
		return "#" + strconv.Itoa(n)
	}
	return host + ":" + strconv.Itoa(n)
}

// TabPanes is w's panes in layout order.
func TabPanes(state model.State, w model.Workspace) []model.Pane {
	var out []model.Pane
	for _, t := range w.Tabs {
		for _, id := range layout.Panes(t.Layout) {
			if i := slices.IndexFunc(state.Panes, func(p model.Pane) bool { return p.ID == id }); i >= 0 {
				out = append(out, state.Panes[i])
			}
		}
	}
	return out
}

// MainPane is the pane wait watches and ls --json reports: the first live
// one running an agent, else the first live one, else the first exited one
// that ran an agent, else the first one.
func MainPane(panes []model.Pane) *model.Pane {
	rank := func(p model.Pane) int {
		r := 0
		if p.Exited {
			r += 2
		}
		if AgentOf(p) == "" {
			r++
		}
		return r
	}
	var best *model.Pane
	for i := range panes {
		if best == nil || rank(panes[i]) < rank(*best) {
			best = &panes[i]
		}
	}
	return best
}

// AgentOf names the agent a pane runs: the one hooks or detection saw,
// else the one its command starts; "" for neither.
func AgentOf(p model.Pane) string {
	if p.Provider != "" && p.Provider != model.ProviderTerminal {
		return string(p.Provider)
	}
	if len(p.Cmd) > 0 {
		return string(agent.Identify(strings.TrimSuffix(filepath.Base(p.Cmd[0]), ".exe"), ""))
	}
	return ""
}

// PaneState is a pane's agent and its state as ls --json and wait name
// it:
//
//	working  a turn runs, or, with no agent, a command runs in the shell
//	blocked  the agent waits on a permission prompt, a question or a plan
//	done     the agent finished its turn (or ended it with an error)
//	pr-open  the agent finished its turn and its tab's branch has an open
//	         pull request: done once it merges or closes
//	idle     the agent sits at its prompt with nothing to report
//	parked   the agent sat idle for [agents] park_idle_after and pitwall
//	         ended its process; a message or key resumes it
//	running  no agent is known, and the pane's process runs: a shell at its
//	         prompt, or a command pitwall knows nothing more about
//	exited   the pane's process ended
//	""       an agent the pane was started with has not been seen running yet
//
// question is the blocked agent's question or approval detail.
func PaneState(state model.State, p model.Pane) (agent, st, question string) {
	agent = AgentOf(p)
	if p.Exited {
		return agent, "exited", ""
	}
	i := slices.IndexFunc(state.Activities, func(a model.Activity) bool { return a.PaneID == p.ID })
	if i >= 0 {
		switch a := state.Activities[i]; a.State {
		case model.StatePendingApproval, model.StateAwaitingInput, model.StatePlanReady:
			return agent, "blocked", a.Detail // an OSC notice blocks before pitwall knows the agent
		}
	}
	switch {
	case agent != "" && p.Provider == "":
		return agent, "", ""
	case i < 0 && agent == "":
		return agent, "running", ""
	case i < 0:
		return agent, "idle", ""
	}
	switch state.Activities[i].State {
	case model.StateCompleted, model.StateError:
		return agent, "done", ""
	case model.StateParked:
		return agent, "parked", ""
	case model.StatePROpen:
		return agent, "pr-open", ""
	}
	return agent, "working", ""
}
