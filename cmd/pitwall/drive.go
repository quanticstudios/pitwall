package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Commands for agents and scripts that drive pitwall: ls --json and wait,
// and in talk.go split, prompt, send and read.
// docs/agent-skill.md documents them; keep the two in step.

// The ls --json rows and pane states live in internal/control; these are
// their names here.
type (
	tabJSON  = control.Tab
	paneJSON = control.Pane
)

var (
	paneOf    = control.PaneOf
	tabsJSON  = control.Tabs
	tabRows   = control.Rows
	tabPanes  = control.TabPanes
	mainPane  = control.MainPane
	agentOf   = control.AgentOf
	paneState = control.PaneState
)

// resolvePane is control.ResolvePane for the calling pane.
func resolvePane(state model.State, session, addr string) (model.Workspace, model.Pane, error) {
	return control.ResolvePane(state, envCaller(""), session, addr)
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
	code, err := waitTab(args, out, errOut)
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
	}
	return code
}

func waitTab(args []string, out, errOut io.Writer) (int, error) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var timedOut atomic.Bool
	var cur atomic.Pointer[watcher] // the watch the timeout closes
	cur.Store(watch)
	if *timeout > 0 {
		t := time.AfterFunc(*timeout, func() { timedOut.Store(true); cancel(); cur.Load().Close() })
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
		code, err := waitChildren(watch, state, session.ID, *until, *doneLine, out, errOut)
		if err != nil && timedOut.Load() {
			return waitTimeout, fmt.Errorf("timed out after %s", *timeout)
		}
		return code, err
	}
	r := newRemotes(nil, state, session)
	defer r.close()
	h, w, p, err := r.pane(name)
	if err != nil {
		return waitError, err
	}
	var remote *hostWait
	if h != nil { // a tab on another machine: watch its daemon instead
		remote = &hostWait{name: h.name}
		defer remote.close()
		if state, err = remote.next(ctx, errOut); err != nil {
			if timedOut.Load() {
				return waitTimeout, fmt.Errorf("timed out after %s", *timeout)
			}
			return waitError, err
		}
		watch = remote.watch
		cur.Store(watch)
	}
	pane, wait := p.ID, control.NewWait(*until)
	for {
		looked := false // the exit lookup ran, and may have outlasted the timeout
		o := wait.Step(state, pane, func() (int, bool) { looked = true; return watch.exitOf(state, pane) })
		if looked && timedOut.Load() {
			return waitTimeout, fmt.Errorf("timed out after %s", *timeout)
		}
		switch o.Reason {
		case control.Closed:
			return waitError, fmt.Errorf("tab %s closed", tabTitle(w))
		case control.Exited:
			if o.Exit.Unknown {
				fmt.Fprintln(out, "exit unknown")
				return waitExited, nil
			}
			fmt.Fprintf(out, "exit %d\n", o.Exit.Code)
			switch {
			case !o.Awaited:
				return waitExited, nil
			case o.Exit.Signal:
				return waitError, nil
			}
			return o.Exit.Code, nil
		case control.Reached:
			fmt.Fprintln(out, o.State)
			return waitReached, nil
		case control.Blocked:
			fmt.Fprintf(out, "blocked: %s\n", o.Question)
			return waitBlocked, nil
		}
		if remote != nil {
			state, err = remote.next(ctx, errOut)
			if err == nil {
				watch = remote.watch
				cur.Store(watch)
			}
		} else {
			state, err = watch.next()
		}
		if err != nil {
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
//
// Workers the tab started on other machines (model.Session.Workers) count
// only on a current state of their host, from a live watch connection. A
// host out of reach is dialled again with the window's backoff; one out
// of reach for unreachableAfter ends the wait with 1, and so does a worker
// lost there (see lostLine), once the others got to until.
func waitChildren(watch *watcher, state model.State, session, until string, doneLine bool, out, errOut io.Writer) (int, error) {
	saw := map[string]bool{}           // a turn ran in the tab during this wait
	nudged := map[string]time.Time{}   // tab: when doneNudge was sent to it
	marks := map[string]doneMarkSeen{} // tab: its done line, for the turn that ended at since
	conns := map[string]*cliConn{}     // by host, "" for this machine: for reads and nudges
	dones := map[string]func(){}
	drop := func(host string) {
		if d := dones[host]; d != nil {
			d()
		} else if c := conns[host]; c != nil {
			c.Close()
		}
		delete(conns, host)
		delete(dones, host)
	}
	defer func() {
		for host := range conns {
			drop(host)
		}
	}()
	connTo := func(host string) (*cliConn, error) {
		if c := conns[host]; c != nil {
			return c, nil
		}
		var c *cliConn
		var err error
		if host == "" {
			c, err = dialCLI()
		} else {
			var done func()
			if c, done, err = dialHost(host, "cli"); err == nil {
				dones[host] = done
			}
		}
		if err != nil {
			return nil, err
		}
		conns[host] = c
		return c, nil
	}
	// mark reads pane's done line, once per turn of the tab's: "DONE",
	// "BLOCKED: why" or "".
	mark := func(host, tab, pane string, since time.Time) (string, error) {
		if m, ok := marks[tab]; ok && m.since.Equal(since) {
			return m.line, nil
		}
		conn, err := connTo(host)
		if err != nil {
			return "", err
		}
		line, err := readDoneLine(conn, pane)
		if err != nil {
			return "", err
		}
		marks[tab] = doneMarkSeen{since, line}
		return line, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan hostEvent)
	go func() {
		for {
			st, err := watch.next()
			select {
			case events <- hostEvent{state: st, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	hosts := map[string]*hostWatch{}
	tick := time.NewTicker(waitTick)
	defer tick.Stop()

	for {
		live := numbered(state, session)
		all := withFinishedWorkers(state)
		only, err := childTabs(all)
		if err != nil {
			return waitError, err
		}
		tab, _ := callerTab(state)
		records := workersOf(state, tab)
		if len(only) == 0 && len(records) == 0 {
			return waitError, errors.New("this tab opened no tabs; open them with pitwall new or pitwall worktree new")
		}
		var lines, blocked []string
		reached, failed, lost, unresolved := 0, false, false, false
		// judge counts tab w of all, on host, toward the wait: head names
		// it, open says its tab is still open.
		judge := func(host string, all, raw model.State, w model.Workspace, head string, open bool) error {
			st, question, code, queued := "exited", "", (*int)(nil), 0
			var pane string
			var since time.Time
			if p := mainPane(tabPanes(all, w)); p != nil {
				j := paneOf(all, 0, *p)
				st, question, code, queued, pane = j.State, j.Question, j.ExitCode, j.Queued, p.ID
				if j.Since != nil {
					since = *j.Since
				}
			}
			if host == "" && !open {
				head = fmt.Sprintf("@%s %s", pane, tabTitle(w)) // a closed worker has no tab number; read reaches it by pane id
			} else if host != "" {
				head = remoteHead(host, raw, w, pane)
			}
			if !open && until == "exit" {
				st = "exited" // its process ended with the tab
			}
			saw[w.ID] = saw[w.ID] || st == "working" || st == "blocked"
			finished := st == "done" || st == "parked" || st == "idle" && saw[w.ID]
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
				line, err := mark(host, w.ID, pane, since)
				if err != nil {
					return err
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
					conn, err := connTo(host)
					if err == nil {
						_, err = syncCLI(conn, proto.Send{Pane: pane, From: os.Getenv("PITWALL_PANE"), Text: doneNudge})
					}
					if err != nil {
						nudged[w.ID] = time.Time{} // pitwall cannot type into it: no turn to wait for
						reached++
						lines = append(lines, head+" no-done-line")
					}
				}
			case until == "done" && finished,
				until == "idle" && (st == "idle" || st == "done" || st == "pr-open" || st == "parked"):
				reached++
				lines = append(lines, head+" "+st)
			}
			return nil
		}
		for _, w := range numbered(all, session) {
			if !only[w.ID] {
				continue
			}
			i := slices.IndexFunc(live, func(x model.Workspace) bool { return x.ID == w.ID })
			if err := judge("", all, state, w, fmt.Sprintf("#%d %s", i+1, tabTitle(w)), i >= 0); err != nil {
				return waitError, err
			}
		}
		for _, rec := range records {
			if rec.Lost == "" && hosts[rec.Host] == nil {
				hosts[rec.Host] = &hostWatch{downSince: time.Now()}
				go watchHost(ctx, dialHost, rec.Host, hostRetryMin, hostRetryMax, events)
			}
			h := hosts[rec.Host]
			switch {
			case rec.Lost != "":
				reached, lost = reached+1, true
				lines = append(lines, lostLine(rec, model.State{}, time.Now()))
				continue
			case !h.current:
				unresolved = true
				// A worker on a host out of reach never counts as there,
				// whatever it was when last seen.
				if time.Since(h.downSince) >= unreachableAfter {
					why := "it sent no state"
					if h.err != nil {
						why = h.err.Error()
					}
					return waitError, fmt.Errorf("%s unreachable for %s: %s", rec.Host, unreachableAfter, why)
				}
				continue
			}
			if line := lostLine(rec, h.state, time.Now()); line != "" {
				reached, lost = reached+1, true
				lines = append(lines, line)
				continue
			}
			hs := withFinishedWorkers(h.state)
			w := hs.Workspace(rec.ID)
			if w == nil {
				unresolved = true
				continue // not on the host yet, within lostAfter of its start
			}
			if err := judge(rec.Host, hs, h.state, *w, "", h.state.Workspace(rec.ID) != nil); err != nil {
				unresolved = true
				drop(rec.Host) // the host may have gone; its next state says
			}
		}
		switch total := len(only) + len(records); {
		case len(blocked) > 0 && (until != "blocked" || !unresolved):
			fmt.Fprintln(out, strings.Join(blocked, "\n"))
			if lost {
				fmt.Fprintln(out, strings.Join(lines, "\n"))
				return waitError, nil
			}
			if until == "blocked" {
				return waitReached, nil
			}
			return waitBlocked, nil
		case reached == total:
			fmt.Fprintln(out, strings.Join(lines, "\n"))
			switch {
			case lost:
				return waitError, nil
			case failed && until != "exit" || until == "blocked":
				return waitExited, nil
			}
			return waitReached, nil
		}
		select {
		case ev := <-events:
			if ev.host == "" {
				if ev.err != nil {
					return waitError, ev.err
				}
				state = ev.state
				break
			}
			h := hosts[ev.host]
			if ev.err == nil {
				h.state, h.current, h.err, h.said = ev.state, true, nil, ""
				break
			}
			if h.current {
				h.current, h.downSince = false, time.Now()
			}
			h.err = ev.err
			drop(ev.host)
			if msg := ev.err.Error(); msg != h.said {
				h.said = msg
				fmt.Fprintf(errOut, "pitwall: %s unreachable: %v\n", ev.host, ev.err)
			}
		case <-tick.C:
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

// withFinishedWorkers makes retained results visible only to wait --children.
// They never reappear as live tabs in ls or the sidebar.
func withFinishedWorkers(state model.State) model.State {
	state.Workspaces = slices.Clone(state.Workspaces)
	state.Panes = slices.Clone(state.Panes)
	state.Activities = slices.Clone(state.Activities)
	for _, c := range state.Closed {
		if !c.FinishedWorker || slices.ContainsFunc(state.Workspaces, func(w model.Workspace) bool { return w.ID == c.Tab.ID }) {
			continue
		}
		w, p := c.Tab, c.Pane
		w.Detached = false
		w.Tabs = []model.Tab{{ID: c.ID, Layout: &layout.Node{Pane: p.ID}}}
		p.Exited = false
		state.Workspaces = append(state.Workspaces, w)
		state.Panes = append(state.Panes, p)
		state.Activities = append(state.Activities, model.Activity{
			PaneID: p.ID, WorkspaceID: w.ID, Provider: p.Provider,
			State: model.StateCompleted, UpdatedAt: c.ClosedAt,
		})
	}
	return state
}
