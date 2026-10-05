package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

var errNotRunning = errors.New("pitwall is not running")

type cliConn struct {
	*proto.Conn
	socket net.Conn
}

func socketPath() (string, error) {
	if path := os.Getenv("PITWALL_SOCKET"); path != "" {
		return path, nil
	}
	return proto.SocketPath()
}

func dialCLI() (*cliConn, error) { return dialKind("cli") }

// dialKind connects as a client of kind (see proto.Hello).
func dialKind(kind string) (*cliConn, error) {
	path, err := socketPath()
	if err != nil {
		return nil, err
	}
	nc, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.Errno(10061)) {
			return nil, errNotRunning // 10061 is Windows' WSAECONNREFUSED
		}
		return nil, err
	}
	conn := proto.NewConn(nc)
	if err := nc.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Send(proto.Hello{Version: proto.Version, Kind: kind}); err != nil {
		conn.Close()
		return nil, err
	}
	return &cliConn{Conn: conn, socket: nc}, nil
}

// syncCLI acknowledges all requests before Sync, including daemon errors.
func syncCLI(conn *cliConn, requests ...any) (model.State, error) {
	return syncCLIWithin(conn, 5*time.Second, requests...)
}

// syncCLIWithin is syncCLI giving the daemon timeout to answer.
func syncCLIWithin(conn *cliConn, timeout time.Duration, requests ...any) (model.State, error) {
	// Confirmation can take longer than the socket timeout.
	if err := conn.socket.SetDeadline(time.Now().Add(timeout)); err != nil {
		return model.State{}, err
	}
	for _, request := range append(requests, proto.Sync{}) {
		if err := conn.Send(request); err != nil {
			return model.State{}, err
		}
	}
	var daemonErr error
	for {
		msg, err := conn.Recv()
		if err != nil {
			if daemonErr != nil {
				return model.State{}, daemonErr
			}
			return model.State{}, err
		}
		switch m := msg.(type) {
		case proto.Error:
			daemonErr = errors.Join(daemonErr, errors.New(m.Message))
		case proto.StateMsg:
			return m.State, daemonErr
		}
	}
}

func runCLI(args []string, in *os.File, out, errOut io.Writer) int {
	if len(args) > 0 && args[0] == "wait" {
		return waitCommand(args[1:], out, errOut)
	}
	if err := sessionCommand(args, in, out, errOut); err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	return 0
}

func sessionCommand(args []string, in *os.File, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	command := args[0]
	if command == "tab" {
		return tabCommand(args[1:])
	}
	if command == "session" {
		return sessionsCommand(args[1:], in, out, errOut)
	}
	if command == "send" {
		return sendCommand(args[1:], errOut)
	}
	args = args[1:]
	var cmd []string
	if i := slices.Index(args, "--"); i >= 0 && command == "new" {
		args, cmd = args[:i], args[i+1:]
		if len(cmd) == 0 {
			return errors.New(usage)
		}
		// why: the daemon runs cmd in dir with its own PATH; resolve both here.
		if strings.ContainsRune(cmd[0], '/') || strings.ContainsRune(cmd[0], filepath.Separator) {
			abs, err := filepath.Abs(cmd[0])
			if err != nil {
				return err
			}
			cmd[0] = abs
		}
		path, err := exec.LookPath(cmd[0])
		if err != nil {
			return err
		}
		cmd[0] = path
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var asJSON, detached, force bool
	var name, sessionName string
	flags.StringVar(&sessionName, "s", "", "")
	switch command {
	case "ls":
		flags.BoolVar(&asJSON, "json", false, "")
	case "new":
		flags.StringVar(&name, "n", "", "")
		flags.BoolVar(&detached, "d", false, "")
	case "kill":
		flags.BoolVar(&force, "f", false, "")
	case "attach", "detach", "rename":
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	args, err := parseFlags(flags, args)
	if err != nil {
		return err
	}
	if (command == "ls" && len(args) != 0) ||
		((command == "new" || command == "attach" || command == "detach") && len(args) > 1) ||
		(command == "kill" && len(args) != 1) ||
		(command == "rename" && (len(args) < 1 || len(args) > 2)) {
		return errors.New(usage)
	}
	conn, err := dialCLI()
	if errors.Is(err, errNotRunning) && command == "ls" {
		if asJSON {
			_, err := fmt.Fprintln(out, "[]")
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	state, err := syncCLI(conn)
	if err != nil {
		return err
	}
	session, err := currentSession(state, sessionName)
	if err != nil && (sessionName != "" || command != "ls" && command != "new") {
		return err // with no session, ls lists nothing and new makes one
	}
	switch command {
	case "ls":
		if asJSON {
			return json.NewEncoder(out).Encode(tabsJSON(state, session.ID))
		}
		return listTabs(out, state, session.ID)
	case "new":
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		dir, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		after, err := syncCLI(conn, proto.NewSession{Name: name, Cwd: dir, SessionID: session.ID, Cmd: cmd})
		if err != nil {
			return err
		}
		var added []model.Workspace
		for _, w := range after.Workspaces {
			if !slices.ContainsFunc(state.Workspaces, func(old model.Workspace) bool { return old.ID == w.ID }) && (name == "" || w.Name == name) {
				added = append(added, w)
			}
		}
		if len(added) != 1 {
			return errors.New("cannot identify the new tab from daemon state")
		}
		if detached {
			if after, err = syncCLI(conn, proto.DetachSession{WorkspaceID: added[0].ID, Detached: true}); err != nil {
				return err
			}
		}
		n := slices.IndexFunc(numbered(after, added[0].SessionID), func(w model.Workspace) bool { return w.ID == added[0].ID })
		_, err = fmt.Fprintf(out, "#%d\n", n+1)
		return err
	}
	old := ""
	if len(args) != 0 && (command != "rename" || len(args) == 2) {
		old = args[0]
	}
	w, err := resolveTab(state, session.ID, old)
	if err != nil {
		return err
	}
	var request any
	switch command {
	case "attach":
		after, err := syncCLI(conn, proto.FocusSession{WorkspaceID: w.ID})
		if err != nil {
			return err
		}
		return attachGUI(after, w.SessionID, w.ID)
	case "detach":
		request = proto.DetachSession{WorkspaceID: w.ID, Detached: true}
	case "rename":
		request = proto.RenameWorkspace{WorkspaceID: w.ID, Name: args[len(args)-1]}
	case "kill":
		if !force && in != nil && term.IsTerminal(in.Fd()) {
			if _, err := fmt.Fprintf(errOut, "kill %s? [y/N] ", tabTitle(w)); err != nil {
				return err
			}
			answer, err := bufio.NewReader(in).ReadString('\n')
			if err != nil {
				return err
			}
			if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
				return nil
			}
		}
		request = proto.KillSession{WorkspaceID: w.ID}
	}
	_, err = syncCLI(conn, request)
	return err
}

// currentSession is the session tab commands act on: the one named name
// (exactly, else by unique prefix), else the session of $PITWALL_PANE,
// else the most recently used one.
func currentSession(state model.State, name string) (model.Session, error) {
	if name != "" {
		return resolveSession(state, name)
	}
	if pane := os.Getenv("PITWALL_PANE"); pane != "" {
		for _, p := range state.Panes {
			if p.ID == pane {
				if s := state.Session(state.SessionOf(p.WorkspaceID)); s != nil {
					return *s, nil
				}
			}
		}
	}
	if s := state.Recent(); s != nil {
		return *s, nil
	}
	return model.Session{}, errors.New("no session is open")
}

// resolveSession finds a session by exact name, then by unique prefix.
func resolveSession(state model.State, name string) (model.Session, error) {
	if s := state.SessionNamed(name); s != nil {
		return *s, nil
	}
	var found []model.Session
	for _, s := range state.Sessions {
		if strings.HasPrefix(s.Name, name) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return model.Session{}, fmt.Errorf("no session matches %q (see pitwall session ls)", name)
	case 1:
		return found[0], nil
	}
	var names []string
	for _, s := range found {
		names = append(names, s.Name)
	}
	return model.Session{}, fmt.Errorf("ambiguous session %q; matches %s", name, strings.Join(names, ", "))
}

// resolveTab finds a tab of session by its id, then by its number in
// pitwall ls ("3" or "#3"), then by exact title, then by a unique title
// prefix. With no name it is the tab of $PITWALL_PANE, in whichever session.
func resolveTab(state model.State, session, name string) (model.Workspace, error) {
	if name == "" {
		pane := os.Getenv("PITWALL_PANE")
		for _, p := range state.Panes {
			if pane != "" && p.ID == pane {
				for _, w := range state.Workspaces {
					if w.ID == p.WorkspaceID {
						return w, nil
					}
				}
			}
		}
		if pane != "" {
			return model.Workspace{}, fmt.Errorf("pane %q has no tab in daemon state", pane)
		}
		return model.Workspace{}, errors.New("specify a tab name outside a pitwall pane (see pitwall ls)")
	}
	tabs := numbered(state, session)
	if i := slices.IndexFunc(tabs, func(w model.Workspace) bool { return w.ID == name }); i >= 0 {
		return tabs[i], nil
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(name, "#")); err == nil {
		if n < 1 || n > len(tabs) {
			return model.Workspace{}, fmt.Errorf("no tab #%d (see pitwall ls)", n)
		}
		return tabs[n-1], nil
	}
	var exact, prefixed []int
	for i, w := range tabs {
		switch t := tabTitle(w); {
		case t == name:
			exact = append(exact, i)
		case strings.HasPrefix(t, name):
			prefixed = append(prefixed, i)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = prefixed
	}
	switch len(matches) {
	case 0:
		return model.Workspace{}, fmt.Errorf("no tab matches %q (see pitwall ls)", name)
	case 1:
		return tabs[matches[0]], nil
	}
	var nums []string
	for _, i := range matches {
		nums = append(nums, "#"+strconv.Itoa(i+1))
	}
	return model.Workspace{}, fmt.Errorf("ambiguous tab %q; matches %s", name, strings.Join(nums, ", "))
}

// numbered is session's tabs as pitwall ls numbers them: the sidebar's
// order, then the detached tabs.
func numbered(state model.State, session string) []model.Workspace {
	all := state.Ordered(session)
	shown := slices.DeleteFunc(slices.Clone(all), func(w model.Workspace) bool { return w.Detached })
	return append(shown, slices.DeleteFunc(all, func(w model.Workspace) bool { return !w.Detached })...)
}

// tabTitle is what the sidebar shows for a tab: its chosen name, else its
// label.
func tabTitle(w model.Workspace) string {
	if w.NameSet {
		return w.Name
	}
	return cmp.Or(w.Label, w.Name)
}

// listTabs prints one row per tab of session: its number, title, state,
// folder and group.
func listTabs(out io.Writer, state model.State, session string) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	home, _ := os.UserHomeDir()
	if _, err := fmt.Fprintln(w, "#\tNAME\tSTATE\tFOLDER\tGROUP"); err != nil {
		return err
	}
	for i, tab := range numbered(state, session) {
		label := "idle"
		var activities []model.Activity
		for _, a := range state.Activities {
			if a.WorkspaceID == tab.ID {
				activities = append(activities, a)
			}
		}
		if a := model.Aggregate(activities); a != nil {
			label = model.PillLabel(*a)
		}
		group := "-"
		for _, p := range state.Projects {
			if p.ID == tab.ProjectID {
				group = p.Name
			}
		}
		folder := tab.Path
		if home != "" && (folder == home || strings.HasPrefix(folder, home+string(os.PathSeparator))) {
			folder = "~" + strings.TrimPrefix(folder, home)
		}
		marker := ""
		if tab.Detached {
			marker = " (detached)"
		}
		if _, err := fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s%s\n", i+1, tabTitle(tab), label, folder, group, marker); err != nil {
			return err
		}
	}
	return w.Flush()
}

func tabCommand(args []string) error {
	if len(args) == 0 || (args[0] != "new" && args[0] != "rename" && args[0] != "close") || (args[0] != "rename" && len(args) != 1) {
		return errors.New("usage: pitwall tab new|rename [name...]|close")
	}
	pane := os.Getenv("PITWALL_PANE")
	if pane == "" {
		return errors.New("tab commands must run inside a pitwall pane (PITWALL_PANE is not set)")
	}
	conn, err := dialCLI()
	if err != nil {
		return err
	}
	defer conn.Close()
	var request any
	switch args[0] {
	case "new":
		request = proto.NewTab{FromPane: pane}
	case "rename":
		request = proto.RenameTab{Pane: pane, Name: strings.Join(args[1:], " ")}
	case "close":
		state, err := syncCLI(conn)
		if err != nil {
			return err
		}
		w, err := resolveTab(state, "", "")
		if err != nil {
			return err
		}
		request = proto.CloseTab{WorkspaceID: w.ID}
	}
	_, err = syncCLI(conn, request)
	return err
}
