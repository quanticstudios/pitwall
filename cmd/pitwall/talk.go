package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// split, prompt, send and read let one agent open another in a pane of
// its tab, type a prompt into it or queue a message for it, and read its
// reply. docs/agent-skill.md documents them.

var errOlder = errors.New("the daemon is older than this pitwall; restart it to use prompt, send and read")

// splitCommand opens a pane running cmd next to the last pane of a tab,
// and prints its address.
func splitCommand(args []string, out io.Writer) error {
	var cmd []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, cmd = args[:i], args[i+1:]
		if len(cmd) > 0 {
			if err := absCmd(cmd); err != nil {
				return err
			}
		}
	}
	flags := flag.NewFlagSet("split", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	down := flags.Bool("down", false, "")
	args, err := parseFlags(flags, args)
	if err != nil || len(args) > 1 {
		return errors.New("usage: pitwall split [-s session] [--down] [tab] [-- cmd args...]")
	}
	conn, err := dialCLI()
	if err != nil {
		return err
	}
	defer conn.Close()
	state, err := syncCLI(conn)
	if err != nil {
		return err
	}
	session, err := currentSession(state, *sessionName)
	if err != nil {
		return err
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	w, err := resolveTab(state, session.ID, name)
	if err != nil {
		return err
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
	after, err := syncCLI(conn, open)
	if err != nil {
		return err
	}
	tabs := numbered(after, w.SessionID)
	i := slices.IndexFunc(tabs, func(t model.Workspace) bool { return t.ID == w.ID })
	if i < 0 {
		return fmt.Errorf("tab %s closed", tabTitle(w))
	}
	panes := tabPanes(after, tabs[i])
	n := slices.IndexFunc(panes, func(p model.Pane) bool {
		return !slices.ContainsFunc(state.Panes, func(old model.Pane) bool { return old.ID == p.ID })
	})
	if n < 0 {
		return errors.New("cannot identify the new pane from daemon state")
	}
	_, err = fmt.Fprintf(out, "#%d.%d\n", i+1, n+1)
	return err
}

// readCommand prints the last reply of a pane's agent, or its screen.
func readCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("read", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessionName := flags.String("s", "", "")
	screen := flags.Bool("screen", false, "")
	args, err := parseFlags(flags, args)
	if err != nil || len(args) != 1 {
		return errors.New("usage: pitwall read [-s session] <pane> [--screen]")
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
	_, p, err := resolvePane(m.State, session.ID, args[0])
	if err != nil {
		return err
	}
	text, err := readPane(conn, proto.Read{Pane: p.ID, Screen: *screen})
	if err != nil {
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
		return errors.New("usage: pitwall send [-s session] <pane> [-- text | text | -]")
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
	_, p, err := resolvePane(m.State, session.ID, args[0])
	if err != nil {
		return err
	}
	from := os.Getenv("PITWALL_PANE")
	if !slices.ContainsFunc(m.State.Panes, func(sp model.Pane) bool { return sp.ID == from }) {
		from = "" // a script, or a pane of another daemon
	}
	if p.ID == from {
		return errors.New("that is this pane: send to another agent's pane")
	}
	_, err = syncCLI(conn, proto.Send{Pane: p.ID, From: from, Text: msg})
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
		return waitError, errors.New("usage: pitwall prompt [-s session] <pane> [--wait] [--timeout 10m] [-- text | text | -]")
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
	if *timeout > 0 {
		t := time.AfterFunc(*timeout, func() { timedOut.Store(true); watch.Close() })
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
	if watch.level < proto.Since(proto.Prompt{}) {
		return waitError, errOlder
	}
	session, err := currentSession(state, *sessionName)
	if err != nil {
		return waitError, err
	}
	w, p, err := resolvePane(state, session.ID, args[0])
	if err != nil {
		return waitError, err
	}
	pane := p.ID
	if pane == os.Getenv("PITWALL_PANE") {
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
		if st == "idle" || st == "done" {
			break
		}
		if state, err = watch.next(); err != nil {
			return fail(err)
		}
	}
	conn, err := dialCLI()
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
