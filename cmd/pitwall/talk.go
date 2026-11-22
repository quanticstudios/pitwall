package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// split, prompt, send and read let one agent open another in a pane of
// its tab, type a prompt into it or queue a message for it, and read its
// reply; review opens one for a second opinion. docs/agent-skill.md
// documents them.

var errOlder = errors.New("the daemon is older than this pitwall; restart it to use prompt, send and read")

const splitUsage = `usage: pitwall split [-s session] [--down] [--wait] [--json] [--timeout 10m] [tab] [-- cmd args...]

Open a pane running cmd (or a shell) right of the tab's last pane (--down:
below it) and print its address (e.g. #3.2).

Flags:
  -s session      session name or id
  --down          split below instead of to the right
  --wait          print the pane address first, wait for cmd to exit, and exit with its code
  --json          print one JSON result object (tab, tab_id, pane, pane_id, and exit details with --wait)
  --timeout 10m   maximum time to wait (requires --wait)
  -h, --help      show this help`

// splitCommand opens a pane running cmd next to the last pane of a tab,
// prints its address, and with --wait waits for cmd to exit and exits with its code.
func splitCommand(args []string, out, errOut io.Writer) int {
	code, err := runSplit(args, out, errOut)
	if err != nil {
		if v, refused := proto.RefusedVersion(proto.Error{Message: err.Error()}); refused {
			err = versionRefusal(v)
		}
		fmt.Fprintln(errOut, "pitwall:", err)
		if code == 0 {
			return 1
		}
		return code
	}
	return code
}

func runSplit(args []string, out, errOut io.Writer) (int, error) {
	var cmd []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, cmd = args[:i], args[i+1:]
		if len(cmd) > 0 {
			if err := absCmd(cmd); err != nil {
				return 1, err
			}
		}
	}
	flags := flag.NewFlagSet("split", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	down := flags.Bool("down", false, "")
	wait := flags.Bool("wait", false, "")
	asJSON := flags.Bool("json", false, "")
	timeout := flags.Duration("timeout", 0, "")
	args, err := parseFlags(flags, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(out, splitUsage)
		return 0, nil
	}
	if err != nil || len(args) > 1 {
		return 1, errors.New(splitUsage)
	}
	if *timeout > 0 && !*wait {
		return 1, errors.New("--timeout requires --wait")
	}
	conn, err := dialCLI()
	if err != nil {
		return 1, err
	}
	defer conn.Close()
	state, err := syncCLI(conn)
	if err != nil {
		return 1, err
	}
	session, err := currentSession(state, *sessionName)
	if err != nil {
		return 1, err
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	w, err := resolveTab(state, session.ID, name)
	if err != nil {
		return 1, err
	}
	open := proto.OpenPane{WorkspaceID: w.ID, TabID: w.ActiveTab, Dir: layout.Horizontal, Cmd: cmd}
	if *down {
		open.Dir = layout.Vertical
	}
	for _, t := range w.Tabs {
		if panes := layout.Panes(t.Layout); t.ID == w.ActiveTab && len(panes) > 0 {
			open.Target = panes[len(panes)-1]
		}
	}
	var watch *watcher
	if *wait {
		watch, err = dialWatch()
		if err != nil {
			return 1, err
		}
		defer watch.Close()
		if _, err := watch.next(); err != nil {
			return 1, err
		}
	}
	after, err := syncCLI(conn, open)
	if err != nil {
		return 1, err
	}
	if !*wait && !*asJSON {
		return 0, printNewPane(out, state, after, w)
	}
	tabIdx, paneIdx, newPane, err := findNewPane(state, after, w)
	if err != nil && watch != nil {
		// why: a quick command can exit and its pane close before the state above; the watch, opened first, saw the pane come.
		tabIdx, paneIdx, newPane, err = watchNewPane(watch, state, w)
	}
	if err != nil {
		return 1, err
	}
	res := runResult{
		Tab:    fmt.Sprintf("#%d", tabIdx),
		TabID:  w.ID,
		Pane:   fmt.Sprintf("#%d.%d", tabIdx, paneIdx),
		PaneID: newPane.ID,
	}
	if !*wait {
		return 0, json.NewEncoder(out).Encode(res)
	}
	if !*asJSON {
		if _, err := fmt.Fprintln(out, res.Pane); err != nil {
			return 1, err
		}
	}
	o, code, err := waitPaneExit(watch, after, newPane.ID, "", *timeout, errOut)
	if err != nil {
		return code, err
	}
	if *asJSON {
		if err := encodeRunResult(out, res, o); err != nil {
			return 1, err
		}
	}
	return code, nil
}

func findNewPane(before, after model.State, w model.Workspace) (tabIdx, paneIdx int, p model.Pane, err error) {
	tabs := numbered(after, w.SessionID)
	i := slices.IndexFunc(tabs, func(t model.Workspace) bool { return t.ID == w.ID })
	if i < 0 {
		return 0, 0, model.Pane{}, fmt.Errorf("tab %s closed", tabTitle(w))
	}
	panes := tabPanes(after, tabs[i])
	n := slices.IndexFunc(panes, func(p model.Pane) bool {
		return !slices.ContainsFunc(before.Panes, func(old model.Pane) bool { return old.ID == p.ID })
	})
	if n < 0 {
		return 0, 0, model.Pane{}, errors.New("cannot identify the new pane from daemon state")
	}
	return i + 1, n + 1, panes[n], nil
}

// watchNewPane reads watch's states until one has a pane in tab w that
// before had not, for a pane that may have closed again since.
func watchNewPane(watch *watcher, before model.State, w model.Workspace) (int, int, model.Pane, error) {
	watch.socket.SetReadDeadline(time.Now().Add(newPaneLook))
	defer watch.socket.SetReadDeadline(time.Time{})
	for {
		s, err := watch.next()
		if err != nil {
			return 0, 0, model.Pane{}, errors.New("cannot identify the new pane from daemon state")
		}
		if tabIdx, paneIdx, p, err := findNewPane(before, s, w); err == nil {
			return tabIdx, paneIdx, p, nil
		}
	}
}

// newPaneLook is how long watchNewPane reads the watch for a new pane.
const newPaneLook = 5 * time.Second

// printNewPane prints the address of the pane in tab w that after has and
// before had not.
func printNewPane(out io.Writer, before, after model.State, w model.Workspace) error {
	tabIdx, paneIdx, _, err := findNewPane(before, after, w)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "#%d.%d\n", tabIdx, paneIdx)
	return err
}

// reviewCommand asks another agent for a second opinion on a tab's
// changes, and prints the reviewer's address. pitwall sends its reply to
// the tab's agent.
func reviewCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("review", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	reviewer := flags.String("agent", "", "")
	args, err := parseFlags(flags, args)
	if err != nil || len(args) > 1 {
		return errors.New("usage: pitwall review [-s session] [tab] [--agent claude|codex]")
	}
	conn, err := dialCLI()
	if err != nil {
		return err
	}
	defer conn.Close()
	m, err := syncMsg(conn)
	if err != nil {
		return err
	}
	if m.Level < proto.Since(proto.SecondOpinion{}) {
		return errors.New("the daemon is older than this pitwall; restart it to use review")
	}
	session, err := currentSession(m.State, *sessionName)
	if err != nil {
		return err
	}
	w, err := resolveTab(m.State, session.ID, strings.Join(args, ""))
	if err != nil {
		return err
	}
	panes := slices.DeleteFunc(tabPanes(m.State, w), func(p model.Pane) bool { return p.Exited || agentOf(p) == "" })
	if len(panes) == 0 {
		return fmt.Errorf("tab %s runs no agent to review", tabTitle(w))
	}
	if *reviewer == "" {
		if *reviewer = string(agent.Reviewer(panes[0].Provider)); *reviewer == "" {
			return fmt.Errorf("pick the reviewer of %s's changes with --agent claude or --agent codex", agentOf(panes[0]))
		}
	}
	if *reviewer != "claude" && *reviewer != "codex" {
		return fmt.Errorf("pick claude or codex to review, not %q", *reviewer)
	}
	path, err := exec.LookPath(*reviewer)
	if err != nil {
		return fmt.Errorf("%s is not installed: no %s on PATH", *reviewer, *reviewer)
	}
	if path, err = filepath.Abs(path); err != nil {
		return err
	}
	after, err := syncCLI(conn, proto.SecondOpinion{Pane: panes[0].ID, Agent: path})
	if err != nil {
		return err
	}
	return printNewPane(out, m.State, after, w)
}

const readUsage = `usage: pitwall read [--host name] [-s session] <pane> [--screen | --scrollback [--lines N]] [--format text|ansi|html]

Print the last reply of a pane's agent, its current screen (--screen), or its
scrollback and screen (--scrollback).

Flags:
  --host name      read from [[hosts]] machine name (plain text)
  -s session       session name or id
  --screen         print the visible screen instead of the agent's last reply
  --scrollback     print the pane's scrollback and visible screen
  --lines N        last N lines (requires --scrollback)
  --format fmt     output format: text (default), ansi, or html (requires --screen or --scrollback)
  -h, --help       show this help`

// readCommand prints the last reply of a pane's agent, its screen, or its scrollback.
func readCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("read", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	screen := flags.Bool("screen", false, "")
	scrollback := flags.Bool("scrollback", false, "")
	lines := flags.Int("lines", 0, "")
	format := flags.String("format", "", "")
	args, err := parseFlags(flags, args)
	if errors.Is(err, flag.ErrHelp) {
		_, err := fmt.Fprintln(out, readUsage)
		return err
	}
	if err != nil || len(args) != 1 {
		return errors.New(readUsage)
	}
	if *format != "" && !vt.ValidFormat(*format) {
		return fmt.Errorf("invalid --format %q: use text, ansi or html", *format)
	}
	if *screen && *scrollback {
		return errors.New("pick --screen or --scrollback, not both")
	}
	if *lines < 0 {
		return errors.New("--lines must be >= 0")
	}
	if *lines > 0 && !*scrollback {
		return errors.New("--lines requires --scrollback")
	}
	if (*format == "ansi" || *format == "html") && !*screen && !*scrollback {
		return errors.New("--format ansi or html requires --screen or --scrollback")
	}
	if *scrollback || *format == "ansi" || *format == "html" {
		if onHost != "" {
			return errors.New("--format ansi/html and read --scrollback use the local JSON-lines socket; forward the host's socket with PITWALL_SOCKET")
		}
		return readFormattedJSON(args[0], *sessionName, *scrollback, *lines, *format, out)
	}
	conn, err := dialCLI()
	if err != nil {
		return err
	}
	defer conn.Close()
	m, err := syncMsg(conn)
	if err != nil {
		return err
	}
	if m.Level < proto.Since(proto.Read{}) {
		return errOlder
	}
	session, err := currentSession(m.State, *sessionName)
	if err != nil {
		return err
	}
	r := newRemotes(conn, m.State, session)
	defer r.close()
	h, _, p, err := r.pane(args[0])
	if err != nil {
		addr := hostLocal(args[0])
		if name, rest, ok := strings.Cut(strings.TrimPrefix(addr, "#"), ":"); ok && slices.Contains(r.names(), name) {
			h, addr = r.host(name), rest
		}
		id, absolute := strings.CutPrefix(addr, "@")
		if !absolute || *screen || id == "" {
			return err
		}
		// Stable pane ids can address a retained reply after its tab
		// closes, here or on the host whose part had it. A host prefix
		// must still name that part, never another session's reply.
		owner := r.ownerOf(id)
		if h != nil && owner != h {
			return err
		}
		p.ID, h = id, owner
	}
	target := conn
	if h != nil {
		target = h.conn
	}
	text, err := readPane(target, proto.Read{Pane: p.ID, Screen: *screen})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, text)
	return err
}

func readFormattedJSON(pane, sessionName string, scrollback bool, lines int, format string, out io.Writer) error {
	sess := callerSessionForJSON(sessionName, pane)
	c, _, err := dialJSON("pitwall-read")
	if err != nil {
		return err
	}
	defer c.Close()
	if format == "" {
		format = "text"
	}
	var req map[string]any
	if scrollback {
		req = map[string]any{
			"type":   "scrollback",
			"pane":   pane,
			"lines":  lines,
			"format": format,
		}
	} else {
		req = map[string]any{
			"type":   "readPane",
			"pane":   pane,
			"screen": true,
			"format": format,
		}
	}
	if sess != "" {
		req["session"] = sess
	}
	data, err := c.call(req, 30*time.Second)
	if err != nil {
		return err
	}
	text, _ := data["text"].(string)
	if scrollback {
		if format == "html" && text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		_, err = io.WriteString(out, text)
		return err
	}
	_, err = fmt.Fprintln(out, text)
	return err
}

// readPane asks the daemon for r and waits for its ReadResult.
func readPane(conn *cliConn, r proto.Read) (string, error) {
	if err := conn.socket.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}
	if err := conn.Send(r); err != nil {
		return "", err
	}
	for {
		msg, err := conn.Recv()
		if err != nil {
			return "", err
		}
		switch m := msg.(type) {
		case proto.Error:
			return "", errors.New(m.Message)
		case proto.ReadResult:
			if m.Err != "" {
				return "", errors.New(m.Err)
			}
			return m.Text, nil
		}
	}
}

// textArg is the text of a prompt or message: the words of text, or with
// none or "-" all of in.
func textArg(text []string, in io.Reader, what string) (string, error) {
	msg := strings.Join(text, " ")
	if msg == "" || msg == "-" {
		if in == nil {
			return "", fmt.Errorf("no %s: give it as an argument or on stdin", what)
		}
		b, err := io.ReadAll(in)
		if err != nil {
			return "", err
		}
		msg = string(b)
	}
	if strings.TrimSpace(msg) == "" {
		return "", fmt.Errorf("the %s is empty", what)
	}
	return msg, nil
}

// sendCommand queues a message for a pane's agent and returns: the
// daemon types it, under a header naming this pane, when that agent next
// sits at its prompt.
func sendCommand(args []string, in io.Reader) error {
	var text []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, text = args[:i], args[i+1:]
	}
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	args, err := parseFlags(flags, args)
	if len(args) == 2 && text == nil {
		args, text = args[:1], args[1:]
	}
	if err != nil || len(args) != 1 {
		return errors.New("usage: pitwall send [--host name] [-s session] <pane> [-- text | text | -]")
	}
	msg, err := textArg(text, in, "message")
	if err != nil {
		return err
	}
	conn, err := dialCLI()
	if err != nil {
		return err
	}
	defer conn.Close()
	m, err := syncMsg(conn)
	if err != nil {
		return err
	}
	if m.Level < proto.Since(proto.Send{}) {
		return errOlder
	}
	session, err := currentSession(m.State, *sessionName)
	if err != nil {
		return err
	}
	r := newRemotes(conn, m.State, session)
	defer r.close()
	h, _, p, err := r.pane(args[0])
	if err != nil {
		return err
	}
	from := envCaller("").Pane
	if !slices.ContainsFunc(m.State.Panes, func(sp model.Pane) bool { return sp.ID == from }) {
		from = "" // a script, or a pane of another daemon
	}
	target := conn
	if h != nil {
		// A host's daemon does not know from: its header says the message
		// comes from an agent on another machine that reads the reply.
		target = h.conn
	} else if p.ID == from {
		return errors.New("that is this pane: send to another agent's pane")
	}
	_, err = syncCLI(target, proto.Send{Pane: p.ID, From: from, Text: msg})
	return err
}

func promptCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	code, err := promptPane(args, in, out)
	if err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
	}
	return code
}

// startWait is how long prompt --wait waits for the agent to start the
// turn it typed; tests shorten it.
var startWait = 30 * time.Second

// promptPane types a prompt into a pane's agent once it is idle or done,
// and with --wait waits for that turn to end and prints its reply. Its
// exit codes are wait's.
func promptPane(args []string, in io.Reader, out io.Writer) (int, error) {
	var text []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, text = args[:i], args[i+1:]
	}
	flags := flag.NewFlagSet("prompt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	wait := flags.Bool("wait", false, "")
	timeout := flags.Duration("timeout", 0, "")
	args, err := parseFlags(flags, args)
	if len(args) == 2 && text == nil {
		args, text = args[:1], args[1:]
	}
	if err != nil || len(args) != 1 {
		return waitError, errors.New("usage: pitwall prompt [--host name] [-s session] <pane> [--wait] [--timeout 10m] [-- text | text | -]")
	}
	msg, err := textArg(text, in, "prompt")
	if err != nil {
		return waitError, err
	}

	watch, err := dialWatch()
	if err != nil {
		return waitError, err
	}
	defer watch.Close()
	var timedOut, noStart atomic.Bool
	var cur atomic.Pointer[watcher] // the watch the timers close
	cur.Store(watch)
	if *timeout > 0 {
		t := time.AfterFunc(*timeout, func() { timedOut.Store(true); cur.Load().Close() })
		defer t.Stop()
	}
	// fail is what a failed read of the watch means.
	fail := func(err error) (int, error) {
		switch {
		case timedOut.Load():
			return waitTimeout, fmt.Errorf("timed out after %s", *timeout)
		case noStart.Load():
			return waitError, errors.New("the agent did not start a turn")
		}
		return waitError, err
	}
	state, err := watch.next()
	if err != nil {
		return fail(err)
	}
	session, err := currentSession(state, *sessionName)
	if err != nil {
		return waitError, err
	}
	r := newRemotes(nil, state, session)
	defer r.close()
	h, w, p, err := r.pane(args[0])
	if err != nil {
		return waitError, err
	}
	// dial reaches the machine the pane runs on: here, or its host.
	dial := dialCLI
	if h != nil {
		var dropForward func()
		defer func() {
			if dropForward != nil {
				dropForward()
			}
		}()
		dial = func() (*cliConn, error) {
			c, done, err := dialHost(h.name, "cli")
			dropForward = done
			return c, err
		}
		hc, done, err := dialHost(h.name, "watch")
		if err != nil {
			return waitError, err
		}
		defer done()
		if err := hc.socket.SetDeadline(time.Time{}); err != nil {
			return waitError, err
		}
		watch = &watcher{cliConn: hc, exits: map[string]int{}}
		if cur.Store(watch); timedOut.Load() {
			watch.Close()
		}
		if state, err = watch.next(); err != nil {
			return fail(err)
		}
	}
	if watch.level < proto.Since(proto.Prompt{}) {
		return waitError, errOlder
	}
	pane := p.ID
	if h == nil && pane == envCaller("").Pane {
		return waitError, errors.New("that is this pane: prompt another agent's pane")
	}
	find := func(state model.State) int {
		return slices.IndexFunc(state.Panes, func(sp model.Pane) bool { return sp.ID == pane })
	}
	// gone reports the pane closed or exited, with what to return then.
	gone := func(state model.State) (bool, int, error) {
		if i := find(state); i >= 0 && !state.Panes[i].Exited {
			return false, 0, nil
		}
		code, ok := watch.exitOf(state, pane)
		if timedOut.Load() {
			code, err := fail(nil)
			return true, code, err
		}
		if !ok {
			return true, waitError, fmt.Errorf("the pane of tab %s closed", tabTitle(w))
		}
		fmt.Fprintf(out, "exit %d\n", code)
		return true, waitExited, nil
	}

	// The agent may still be on its last turn: type only once it is done.
	for {
		if ok, code, err := gone(state); ok {
			return code, err
		}
		agent, st, question := paneState(state, state.Panes[find(state)])
		if st == "blocked" {
			fmt.Fprintf(out, "blocked: %s\n", question)
			return waitBlocked, nil
		}
		if agent == "" {
			return waitError, errors.New("the pane runs no agent")
		}
		if st == "idle" || st == "done" || st == "parked" {
			break // the daemon resumes a parked agent and types the prompt once it is back
		}
		if state, err = watch.next(); err != nil {
			return fail(err)
		}
	}
	conn, err := dial()
	if err != nil {
		return waitError, err
	}
	defer conn.Close()
	sent := time.Now()
	if _, err := syncCLI(conn, proto.Prompt{Pane: pane, Text: msg}); err != nil {
		return waitError, err
	}
	if !*wait {
		return waitReached, nil
	}

	// This turn: an activity newer than the prompt, through to done, or
	// to idle once it ended without a reply. Snapshots coalesce, so
	// working may never show.
	var started atomic.Bool
	t := time.AfterFunc(startWait, func() {
		if !started.Load() {
			noStart.Store(true)
			watch.Close()
		}
	})
	defer t.Stop()
	for {
		if state, err = watch.next(); err != nil {
			return fail(err)
		}
		if ok, code, err := gone(state); ok {
			return code, err
		}
		if i := slices.IndexFunc(state.Activities, func(a model.Activity) bool { return a.PaneID == pane }); i >= 0 && state.Activities[i].UpdatedAt.After(sent) {
			started.Store(true)
		}
		if !started.Load() {
			continue
		}
		_, st, question := paneState(state, state.Panes[find(state)])
		switch st {
		case "blocked":
			fmt.Fprintf(out, "blocked: %s\n", question)
			return waitBlocked, nil
		case "done", "idle":
			reply, err := readPane(conn, proto.Read{Pane: pane})
			if err != nil {
				return waitError, fmt.Errorf("the turn ended, but: %w", err)
			}
			fmt.Fprintln(out, reply)
			return waitReached, nil
		}
	}
}
