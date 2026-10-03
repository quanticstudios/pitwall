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

func dialCLI() (*cliConn, error) {
	path, err := socketPath()
	if err != nil {
		return nil, err
	}
	nc, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, errNotRunning
		}
		return nil, err
	}
	conn := proto.NewConn(nc)
	if err := nc.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Send(proto.Hello{Version: proto.Version, Kind: "cli"}); err != nil {
		conn.Close()
		return nil, err
	}
	return &cliConn{Conn: conn, socket: nc}, nil
}

// syncCLI acknowledges all requests before Sync, including daemon errors.
func syncCLI(conn *cliConn, requests ...any) (model.State, error) {
	// Confirmation can take longer than the socket timeout.
	if err := conn.socket.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
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
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var asJSON, detached, force bool
	var name string
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
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	args = flags.Args()
	if (command == "ls" && len(args) != 0) ||
		((command == "new" || command == "attach" || command == "detach") && len(args) > 1) ||
		(command == "kill" && len(args) != 1) ||
		(command == "rename" && (len(args) < 1 || len(args) > 2)) {
		return errors.New(usage)
	}
	conn, err := dialCLI()
	if errors.Is(err, errNotRunning) && command == "ls" {
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
	switch command {
	case "ls":
		if asJSON {
			sessions := state.Workspaces
			if sessions == nil {
				sessions = []model.Workspace{}
			}
			return json.NewEncoder(out).Encode(sessions)
		}
		return listTabs(out, state)
	case "new":
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		dir, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		after, err := syncCLI(conn, proto.NewSession{Name: name, Cwd: dir})
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
		n := slices.IndexFunc(numbered(after), func(w model.Workspace) bool { return w.ID == added[0].ID })
		_, err = fmt.Fprintf(out, "#%d\n", n+1)
		return err
	}
	old := ""
	if len(args) != 0 && (command != "rename" || len(args) == 2) {
		old = args[0]
	}
	w, err := resolveTab(state, old)
	if err != nil {
		return err
	}
	var request any
	switch command {
	case "attach":
		if _, err := syncCLI(conn, proto.FocusSession{WorkspaceID: w.ID}); err != nil {
			return err
		}
		return attachGUI(w.ID)
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

// resolveTab finds a tab by its number in pitwall ls ("3" or "#3"), then
// by exact title, then by a unique title prefix. With no name it is the tab
// of $PITWALL_PANE.
func resolveTab(state model.State, name string) (model.Workspace, error) {
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
	tabs := numbered(state)
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

// numbered is the tabs as pitwall ls numbers them: the sidebar's order,
// then the detached tabs.
func numbered(state model.State) []model.Workspace {
	all := state.Ordered()
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

// listTabs prints one row per tab: its number, title, state, folder and
// group.
func listTabs(out io.Writer, state model.State) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	home, _ := os.UserHomeDir()
	if _, err := fmt.Fprintln(w, "#\tNAME\tSTATE\tFOLDER\tGROUP"); err != nil {
		return err
	}
	for i, tab := range numbered(state) {
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
		w, err := resolveTab(state, "")
		if err != nil {
			return err
		}
		request = proto.CloseTab{WorkspaceID: w.ID}
	}
	_, err = syncCLI(conn, request)
	return err
}
