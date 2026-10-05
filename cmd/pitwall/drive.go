package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Commands for agents and scripts that drive pitwall: ls --json and wait.
// docs/agent-skill.md documents them; keep the two in step.

// tabJSON is one tab in pitwall ls --json. The shape is a contract: fields
// may be added, never renamed or dropped.
type tabJSON struct {
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
}

func tabsJSON(state model.State, session string) []tabJSON {
	out := []tabJSON{}
	for i, w := range numbered(state, session) {
		t := tabJSON{N: i + 1, ID: w.ID, Title: tabTitle(w), Cwd: state.LivePath(w), Branch: w.Branch, Detached: w.Detached}
		for _, p := range state.Projects {
			if p.ID == w.ProjectID {
				t.Group = p.Name
			}
		}
		panes := tabPanes(state, w)
		t.Panes = len(panes)
		if p := mainPane(panes); p != nil {
			t.Agent, t.State, t.Question = paneState(state, *p)
			if p.Exited && !p.ExitUnknown {
				t.ExitCode = &p.ExitCode
			}
		}
		out = append(out, t)
	}
	return out
}

// tabPanes is w's panes in layout order.
func tabPanes(state model.State, w model.Workspace) []model.Pane {
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

// mainPane is the pane wait watches and ls --json reports: the first live
// one running an agent, else the first live one, else the first exited one
// that ran an agent, else the first one.
func mainPane(panes []model.Pane) *model.Pane {
	rank := func(p model.Pane) int {
		r := 0
		if p.Exited {
			r += 2
		}
		if agentOf(p) == "" {
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

// agentOf names the agent a pane runs: the one hooks or detection saw,
// Jev's screen-read agents included, else the one its command starts; ""
// for neither.
func agentOf(p model.Pane) string {
	if p.Provider != "" && p.Provider != model.ProviderTerminal {
		return string(p.Provider)
	}
	if len(p.Cmd) > 0 {
		switch b := model.Provider(strings.TrimSuffix(filepath.Base(p.Cmd[0]), ".exe")); b {
		case model.ProviderClaude, model.ProviderCodex, model.ProviderPi:
			return string(b)
		}
	}
	return ""
}

// paneState is a pane's agent and its state as ls --json and wait name
// it:
//
//	working  a turn runs, or, with no agent, a command runs in the shell
//	blocked  the agent waits on a permission prompt, a question or a plan
//	done     the agent finished its turn (or ended it with an error)
//	idle     the agent sits at its prompt with nothing to report
//	running  no agent is known, and the pane's process runs: a shell at its
//	         prompt, or a command pitwall knows nothing more about
//	exited   the pane's process ended
//	""       an agent the pane was started with has not been seen running yet
//
// question is the blocked agent's question or approval detail.
func paneState(state model.State, p model.Pane) (agent, st, question string) {
	agent = agentOf(p)
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
	}
	return agent, "working", ""
}

// parseFlags parses flags anywhere among args, as in `pitwall wait 2
// --until done`. Everything after "--" is positional.
func parseFlags(flags *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		rest := flags.Args()
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
}

// watcher is a watch connection: the daemon pushes state and pane exits to
// it. Changes that come close together arrive as one state.
type watcher struct {
	*cliConn
	exits map[string]int // pane: exit code, from PaneExited
}

func dialWatch() (*watcher, error) {
	conn, err := dialKind("watch")
	if err != nil {
		return nil, err
	}
	if err := conn.socket.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return &watcher{cliConn: conn, exits: map[string]int{}}, nil
}

// next returns the next state, collecting pane exits on the way.
func (w *watcher) next() (model.State, error) {
	for {
		msg, err := w.Recv()
		if err != nil {
			return model.State{}, err
		}
		switch m := msg.(type) {
		case proto.Error:
			return model.State{}, errors.New(m.Message)
		case proto.PaneExited:
			w.exits[m.Pane] = m.ExitCode
		case proto.StateMsg:
			return m.State, nil
		}
	}
}

// exitOf is pane's exit code once its process ended, and whether it is
// known. A pane gone from the state without one may still have its
// PaneExited on the way: the daemon sends it after the state.
func (w *watcher) exitOf(state model.State, pane string) (int, bool) {
	if i := slices.IndexFunc(state.Panes, func(p model.Pane) bool { return p.ID == pane }); i >= 0 {
		return state.Panes[i].ExitCode, state.Panes[i].Exited
	}
	if code, ok := w.exits[pane]; ok {
		return code, true
	}
	w.socket.SetReadDeadline(time.Now().Add(exitGrace))
	defer w.socket.SetReadDeadline(time.Time{})
	for {
		msg, err := w.Recv()
		if err != nil {
			return 0, false
		}
		if m, ok := msg.(proto.PaneExited); ok && m.Pane == pane {
			return m.ExitCode, true
		}
	}
}

// exitGrace is how long wait looks for the exit code of a pane gone from
// the state; a pane closed on purpose has none.
var exitGrace = 2 * time.Second

// Exit codes of pitwall wait.
const (
	waitReached = 0
	waitError   = 1
	waitBlocked = 2
	waitExited  = 3
	waitTimeout = 124
)

func waitCommand(args []string, out, errOut io.Writer) int {
	code, err := waitTab(args, out)
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
	}
	return code
}

func waitTab(args []string, out io.Writer) (int, error) {
	flags := flag.NewFlagSet("wait", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	until := flags.String("until", "", "")
	timeout := flags.Duration("timeout", 0, "")
	args, err := parseFlags(flags, args)
	if err != nil {
		return waitError, err
	}
	if len(args) > 1 || !slices.Contains([]string{"done", "idle", "blocked", "exit"}, *until) {
		return waitError, errors.New("usage: pitwall wait [-s session] [tab] --until done|idle|blocked|exit [--timeout 10m]")
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	watch, err := dialWatch()
	if err != nil {
		return waitError, err
	}
	defer watch.Close()
	var timedOut atomic.Bool
	if *timeout > 0 {
		t := time.AfterFunc(*timeout, func() { timedOut.Store(true); watch.Close() })
		defer t.Stop()
	}
	state, err := watch.next()
	if err != nil {
		if timedOut.Load() {
			return waitTimeout, fmt.Errorf("timed out after %s", *timeout)
		}
		return waitError, err
	}
	session, err := currentSession(state, *sessionName)
	if err != nil {
		return waitError, err
	}
	w, err := resolveTab(state, session.ID, name)
	if err != nil {
		return waitError, err
	}
	p := mainPane(tabPanes(state, w))
	if p == nil {
		return waitError, fmt.Errorf("tab %s has no pane", tabTitle(w))
	}
	// saw: a turn ran during this wait; hadAgent: the pane ran an agent.
	pane, saw, hadAgent := p.ID, false, false
	for {
		i := slices.IndexFunc(state.Panes, func(sp model.Pane) bool { return sp.ID == pane })
		var agent, st, question string
		if i >= 0 {
			agent, st, question = paneState(state, state.Panes[i])
		}
		hadAgent = hadAgent || agent != ""
		if i < 0 || st == "exited" {
			if i >= 0 && state.Panes[i].ExitUnknown {
				// why: the daemon restarted while the command ran, and did not run it again.
				fmt.Fprintln(out, "exit unknown")
				return waitExited, nil
			}
			code, ok := watch.exitOf(state, pane)
			if timedOut.Load() {
				return waitTimeout, fmt.Errorf("timed out after %s", *timeout)
			}
			if !ok {
				return waitError, fmt.Errorf("tab %s closed", tabTitle(w))
			}
			fmt.Fprintf(out, "exit %d\n", code)
			if *until == "exit" || (*until == "done" && !hadAgent) {
				if code < 0 {
					return waitError, nil // killed by a signal
				}
				return code, nil
			}
			return waitExited, nil
		}
		saw = saw || st == "working" || st == "blocked"
		switch {
		case st == *until, *until == "idle" && st == "done",
			*until == "done" && st == "idle" && saw: // an interrupted turn ends idle
			fmt.Fprintln(out, st)
			return waitReached, nil
		case st == "blocked":
			fmt.Fprintf(out, "blocked: %s\n", question)
			return waitBlocked, nil
		}
		if state, err = watch.next(); err != nil {
			if timedOut.Load() {
				return waitTimeout, fmt.Errorf("timed out after %s", *timeout)
			}
			return waitError, err
		}
	}
}
