package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Commands for agents and scripts that drive pitwall: ls --json and wait,
// and in talk.go split, prompt, send and read.
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
	// PaneList is every pane of the tab in layout order: N is the N of
	// its address, TAB.N.
	PaneList []paneJSON `json:"pane_list"`
	// Parent is the id of the tab that opened this one, "" for none.
	Parent string `json:"parent"`
	// Since is when the main pane's state started, null without an agent
	// activity.
	Since *time.Time `json:"since"`
	// PRState is why the branch's pull request cannot merge yet, a
	// model.Reason such as "checks_failing", or "ready"; "" with no PR.
	PRState string `json:"pr_state"`
}

// paneJSON is one pane in tabJSON.PaneList, a contract like it.
type paneJSON struct {
	N        int        `json:"n"`
	ID       string     `json:"id"`
	Agent    string     `json:"agent"`
	State    string     `json:"state"`
	Question string     `json:"question"`
	ExitCode *int       `json:"exit_code"`
	Queued   int        `json:"queued"` // messages from pitwall send not yet typed
	Since    *time.Time `json:"since"`
}

func paneOf(state model.State, n int, p model.Pane) paneJSON {
	j := paneJSON{N: n, ID: p.ID, Queued: state.Queued[p.ID]}
	j.Agent, j.State, j.Question = paneState(state, p)
	if p.Exited && !p.ExitUnknown {
		j.ExitCode = &p.ExitCode
	}
	if i := slices.IndexFunc(state.Activities, func(a model.Activity) bool { return a.PaneID == p.ID }); i >= 0 && !p.Exited && !state.Activities[i].UpdatedAt.IsZero() {
		j.Since = &state.Activities[i].UpdatedAt
	}
	return j
}

func tabsJSON(state model.State, session string, only map[string]bool) []tabJSON {
	out := []tabJSON{}
	for i, w := range numbered(state, session) {
		if only != nil && !only[w.ID] {
			continue
		}
		t := tabJSON{N: i + 1, ID: w.ID, Title: tabTitle(w), Cwd: state.LivePath(w), Branch: w.Branch, Detached: w.Detached, PRState: string(state.PRs[w.ID].Reason)}
		if slices.ContainsFunc(state.Workspaces, func(p model.Workspace) bool { return p.ID == w.Parent }) {
			t.Parent = w.Parent
		}
		for _, p := range state.Projects {
			if p.ID == w.ProjectID {
				t.Group = p.Name
			}
		}
		panes := tabPanes(state, w)
		t.Panes = len(panes)
		if p := mainPane(panes); p != nil {
			main := paneOf(state, 0, *p)
			t.Agent, t.State, t.Question, t.ExitCode, t.Since = main.Agent, main.State, main.Question, main.ExitCode, main.Since
		}
		t.PaneList = []paneJSON{}
		for i, p := range panes {
			t.PaneList = append(t.PaneList, paneOf(state, i+1, p))
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

// resolvePane finds a pane by address: a tab, as resolveTab names it, for
// its main pane, TAB.N for its Nth pane in layout order, as in "3.2",
// "#3.2" or "review.2", or @ and its id in any session, which stays valid
// while tabs move. A tab whose whole title is the address wins over
// reading N off its end.
func resolvePane(state model.State, session, addr string) (model.Workspace, model.Pane, error) {
	if id, ok := strings.CutPrefix(addr, "@"); ok {
		i := slices.IndexFunc(state.Panes, func(p model.Pane) bool { return p.ID == id })
		if i < 0 {
			return model.Workspace{}, model.Pane{}, fmt.Errorf("no pane %s: it closed (see pitwall ls --json)", addr)
		}
		p := state.Panes[i]
		w := slices.IndexFunc(state.Workspaces, func(w model.Workspace) bool { return w.ID == p.WorkspaceID })
		if w < 0 {
			return model.Workspace{}, model.Pane{}, fmt.Errorf("pane %s has no tab", addr)
		}
		return state.Workspaces[w], p, nil
	}
	tab, n := addr, -1
	if i := strings.LastIndexByte(addr, '.'); i > 0 && !slices.ContainsFunc(numbered(state, session), func(w model.Workspace) bool { return tabTitle(w) == addr }) {
		if k, err := strconv.Atoi(addr[i+1:]); err == nil {
			tab, n = addr[:i], k
		}
	}
	w, err := resolveTab(state, session, tab)
	if err != nil {
		return w, model.Pane{}, err
	}
	panes := tabPanes(state, w)
	if n < 0 {
		p := mainPane(panes)
		if p == nil {
			return w, model.Pane{}, fmt.Errorf("tab %s has no pane", tabTitle(w))
		}
		return w, *p, nil
	}
	if n < 1 || n > len(panes) {
		return w, model.Pane{}, fmt.Errorf("tab %s has no pane %d; it has %d (see pitwall ls --json)", tabTitle(w), n, len(panes))
	}
	return w, panes[n-1], nil
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
		return string(agent.Identify(strings.TrimSuffix(filepath.Base(p.Cmd[0]), ".exe"), ""))
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
	level int            // the daemon's proto.Level, from its latest StateMsg
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
			w.level = m.Level
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
	children := flags.Bool("children", false, "")
	doneLine := flags.Bool("done-line", false, "")
	args, err := parseFlags(flags, args)
	if err != nil {
		return waitError, err
	}
	if len(args) > 1 || *children && len(args) == 1 || !slices.Contains([]string{"done", "idle", "blocked", "exit"}, *until) {
		return waitError, errors.New("usage: pitwall wait [-s session] [tab[.N] | --children [--done-line]] --until done|idle|blocked|exit [--timeout 10m]")
	}
	if *doneLine && (!*children || *until != "done") {
		return waitError, errors.New("--done-line goes with --children --until done")
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
	if *children {
		code, err := waitChildren(watch, state, session.ID, *until, *doneLine, out)
		if err != nil && timedOut.Load() {
			return waitTimeout, fmt.Errorf("timed out after %s", *timeout)
		}
		return code, err
	}
	w, p, err := resolvePane(state, session.ID, name)
	if err != nil {
		return waitError, err
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

// waitChildren is wait --children: it waits on every tab the calling
// pane's tab opened until each one got to until, then prints a line per
// tab. A tab that blocks ends the wait at once, printing the blocked ones.
// Tabs opened during the wait count too, and a tab with a message from
// pitwall send still to type is not done. With doneLine, a tab is done
// once its agent's last reply ends with a line DONE; BLOCKED: <why> there
// ends the wait like a blocked tab. A tab that finished without either gets
// doneNudge once, and finishing again without one prints no-done-line.
func waitChildren(watch *watcher, state model.State, session, until string, doneLine bool, out io.Writer) (int, error) {
	saw := map[string]bool{}           // a turn ran in the tab during this wait
	nudged := map[string]time.Time{}   // tab: when doneNudge was sent to it
	marks := map[string]doneMarkSeen{} // tab: its done line, for the turn that ended at since
	var conn *cliConn
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	// mark reads pane's done line, once per turn of the tab's: "DONE",
	// "BLOCKED: why" or "".
	mark := func(tab, pane string, since time.Time) (string, error) {
		if m, ok := marks[tab]; ok && m.since.Equal(since) {
			return m.line, nil
		}
		if conn == nil {
			c, err := dialCLI()
			if err != nil {
				return "", err
			}
			conn = c
		}
		line, err := readDoneLine(conn, pane)
		if err != nil {
			return "", err
		}
		marks[tab] = doneMarkSeen{since, line}
		return line, nil
	}
	for {
		only, err := childTabs(state)
		if err != nil {
			return waitError, err
		}
		if len(only) == 0 {
			return waitError, errors.New("this tab opened no tabs; open them with pitwall new or pitwall worktree new")
		}
		var lines, blocked []string
		reached, failed := 0, false
		for i, w := range numbered(state, session) {
			if !only[w.ID] {
				continue
			}
			st, question, code, queued := "exited", "", (*int)(nil), 0
			var pane string
			var since time.Time
			if p := mainPane(tabPanes(state, w)); p != nil {
				j := paneOf(state, 0, *p)
				st, question, code, queued, pane = j.State, j.Question, j.ExitCode, j.Queued, p.ID
				if j.Since != nil {
					since = *j.Since
				}
			}
			saw[w.ID] = saw[w.ID] || st == "working" || st == "blocked"
			head := fmt.Sprintf("#%d %s", i+1, tabTitle(w))
			finished := st == "done" || st == "idle" && saw[w.ID]
			switch {
			case st == "blocked":
				blocked = append(blocked, fmt.Sprintf("%s blocked: %s", head, question))
			case st == "exited":
				reached++
				switch {
				case code == nil:
					lines = append(lines, head+" exit unknown")
				default:
					lines = append(lines, fmt.Sprintf("%s exit %d", head, *code))
					failed = failed || *code != 0
				}
			case until == "done" && finished && queued > 0:
			case until == "done" && finished && doneLine:
				at, ok := nudged[w.ID]
				if ok && !at.IsZero() && !since.After(at) {
					break // the nudge's turn has not ended yet
				}
				line, err := mark(w.ID, pane, since)
				if err != nil {
					return waitError, err
				}
				switch {
				case line == "DONE":
					reached++
					lines = append(lines, head+" done")
				case line != "":
					blocked = append(blocked, head+" "+line)
				case ok:
					reached++
					lines = append(lines, head+" no-done-line")
				default:
					nudged[w.ID] = time.Now()
					if _, err := syncCLI(conn, proto.Send{Pane: pane, From: os.Getenv("PITWALL_PANE"), Text: doneNudge}); err != nil {
						nudged[w.ID] = time.Time{} // pitwall cannot type into it: no turn to wait for
						reached++
						lines = append(lines, head+" no-done-line")
					}
				}
			case until == "done" && finished,
				until == "idle" && (st == "idle" || st == "done"):
				reached++
				lines = append(lines, head+" "+st)
			}
		}
		switch {
		case len(blocked) > 0:
			fmt.Fprintln(out, strings.Join(blocked, "\n"))
			if until == "blocked" {
				return waitReached, nil
			}
			return waitBlocked, nil
		case reached == len(only):
			fmt.Fprintln(out, strings.Join(lines, "\n"))
			if failed && until != "exit" || until == "blocked" {
				return waitExited, nil
			}
			return waitReached, nil
		}
		if state, err = watch.next(); err != nil {
			return waitError, err
		}
	}
}

// doneMarkSeen is a tab's done line as read for the turn that ended at
// since.
type doneMarkSeen struct {
	since time.Time
	line  string
}

// doneNudge is what wait --done-line sends a worker that ended its turn
// without a done line.
// Adapted from forge (MIT): internal/machine/step.go markerNudge
const doneNudge = "You stopped without a final DONE or BLOCKED line. If you are finished, end with a line DONE. If not, continue the task and end with it. If you are blocked, say why and end with a line BLOCKED: <why>. Answer in your reply, not with pitwall send."

// doneLineRe matches a done line, which stands alone: DONE, or BLOCKED: and
// why. Claude Code puts a bullet before its reply, and markdown may wrap
// the word.
// Adapted from forge (MIT): internal/markers/markers.go markerRe
var doneLineRe = regexp.MustCompile("^\\s*(?:[●⏺]\\s*)?[*`]*(DONE|BLOCKED\\b:?[^*`]*?)[*`]*\\s*$")

// readDoneLine is the done line pane's agent ended its last reply with,
// "DONE" or "BLOCKED: why", else "". Without a reply kept, as after a
// daemon restart, it looks at the last lines of the screen instead.
func readDoneLine(conn *cliConn, pane string) (string, error) {
	var lines []string
	if reply, err := readPane(conn, proto.Read{Pane: pane}); err == nil {
		if t := strings.TrimSpace(reply); t != "" {
			lines = strings.Split(t, "\n")
			lines = lines[len(lines)-1:]
		}
	} else {
		screen, err := readPane(conn, proto.Read{Pane: pane, Screen: true})
		if err != nil {
			return "", err
		}
		lines = strings.Split(screen, "\n")
		lines = lines[max(0, len(lines)-20):]
	}
	for _, l := range slices.Backward(lines) {
		if m := doneLineRe.FindStringSubmatch(l); m != nil {
			if m[1] == "DONE" {
				return "DONE", nil
			}
			why := strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(m[1], "BLOCKED"), ":"))
			return "BLOCKED: " + why, nil
		}
	}
	return "", nil
}
