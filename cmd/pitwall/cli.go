package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/teardown"
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
	return hello(nc, kind)
}

// helloCLI says Hello on nc as a CLI client.
func helloCLI(nc net.Conn) (*cliConn, error) { return hello(nc, "cli") }

func hello(nc net.Conn, kind string) (*cliConn, error) {
	conn := proto.NewConn(nc)
	if err := nc.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Send(proto.Hello{Version: proto.Version, Level: proto.Level, Kind: kind}); err != nil {
		conn.Close()
		return nil, err
	}
	return &cliConn{Conn: conn, socket: nc}, nil
}

// syncCLI acknowledges all requests before Sync, including daemon errors.
func syncCLI(conn *cliConn, requests ...any) (model.State, error) {
	m, err := syncMsg(conn, requests...)
	return m.State, err
}

// syncMsg is syncCLI with the daemon's whole StateMsg, its Level too.
func syncMsg(conn *cliConn, requests ...any) (proto.StateMsg, error) {
	// Confirmation can take longer than the socket timeout.
	wait := 5 * time.Second
	for _, request := range requests {
		switch request.(type) {
		case proto.DeleteWorkspace, proto.DeleteWorktree:
			wait = teardown.Timeout + time.Minute
		case proto.NewWorkspace:
			wait = max(wait, time.Minute) // git worktree add of a large repository
		}
	}
	if err := conn.socket.SetDeadline(time.Now().Add(wait)); err != nil {
		return proto.StateMsg{}, err
	}
	for _, request := range append(requests, proto.Sync{}) {
		if err := conn.Send(request); err != nil {
			return proto.StateMsg{}, err
		}
	}
	var daemonErr error
	for {
		msg, err := conn.Recv()
		if err != nil {
			if daemonErr != nil {
				return proto.StateMsg{}, daemonErr
			}
			return proto.StateMsg{}, err
		}
		switch m := msg.(type) {
		case proto.Error:
			daemonErr = errors.Join(daemonErr, errors.New(m.Message))
		case proto.StateMsg:
			return m, daemonErr
		}
	}
}

// versionRefusal explains the daemon's refusal of this proto.Version: it
// speaks v. update brings this binary to the latest release and moves the
// daemon onto it.
func versionRefusal(v int) error {
	return fmt.Errorf("the daemon speaks protocol version %d and this %s %s speaks %d; run %s update to bring both to one version", v, binName, versionString(), proto.Version, binName)
}

func runCLI(args []string, in *os.File, out, errOut io.Writer) int {
	var err error
	switch {
	case len(args) > 0 && args[0] == "agent":
		err = agentCommand(args[1:], out)
	case len(args) > 0 && args[0] == "wait":
		return waitCommand(args[1:], out, errOut)
	case len(args) > 0 && args[0] == "chat":
		return chatCommand(args[1:], out, errOut)
	case len(args) > 0 && args[0] == "prompt":
		var stdin io.Reader
		if in != nil {
			stdin = in // why: a nil *os.File in an io.Reader is not nil
		}
		return promptCommand(args[1:], stdin, out, errOut)
	case len(args) > 0 && args[0] == "popup":
		return popupCommand(args[1:], errOut)
	case len(args) > 0 && args[0] == "split":
		err = splitCommand(args[1:], out)
	case len(args) > 0 && args[0] == "review":
		err = reviewCommand(args[1:], out)
	case len(args) > 0 && args[0] == "status":
		err = statusCommand(args[1:])
	case len(args) > 0 && args[0] == "form":
		var stdin io.Reader
		if in != nil {
			stdin = in
		}
		return formCommand(args[1:], stdin, out, errOut)
	case len(args) > 0 && args[0] == "scrollback":
		err = scrollbackCommand(args[1:], out)
	case len(args) > 0 && args[0] == "read":
		err = readCommand(args[1:], out)
	case len(args) > 0 && args[0] == "send":
		var stdin io.Reader
		if in != nil {
			stdin = in
		}
		err = sendCommand(args[1:], stdin)
	default:
		err = sessionCommand(args, in, out, errOut)
	}
	if err != nil {
		if v, refused := proto.RefusedVersion(proto.Error{Message: err.Error()}); refused {
			err = versionRefusal(v)
		}
		fmt.Fprintln(errOut, "pitwall:", err)
		var c codeError
		if errors.As(err, &c) {
			return c.code
		}
		return 1
	}
	return 0
}

// codeError is an error a command exits with code for, not 1.
type codeError struct {
	code int
	error
}

// withIssue is cmd with GitHub issue n of the repository at dir as its
// last argument, the prompt, or cmd as it is for n 0.
func withIssue(cmd []string, dir string, n int) ([]string, error) {
	if n == 0 {
		return cmd, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	i, err := forge.IssueView(ctx, dir, n)
	if err != nil {
		return nil, fmt.Errorf("issue %d: %w", n, err)
	}
	return append(cmd, i.Prompt()), nil
}

// newOnHost opens tab on host, in that host's part of the shared session
// here (see partOn), else a new part made for it, which then joins
// session. daemon is this machine's State.Daemon. A detached tab is
// hidden there, as new -d hides one here. It returns the tab's number
// there, "box:3".
//
// The tab gets tab.ID there, a new one when it is "", on a host that keeps
// ids. A tab with a ParentTab is recorded in session's Workers before it
// starts, and a start whose connection drops is sent again, which the
// host's daemon runs once.
func newOnHost(local *cliConn, daemon string, session *model.Session, host string, tab proto.NewSession, detached bool) (address string, startErr error) {
	return startOnHost(local, daemon, session, host, tab, detached, nil)
}

// hostWorktree is what worktree new --host sends in place of NewSession:
// msg, with the tab's id, name, command, parent and part from it, after
// push, when set, has put the worktree's commit on the host.
type hostWorktree struct {
	msg   proto.NewWorkspace
	push  func(id string) error
	clone string // origin's URL here, for the host check's clone command
}

// startOnHost is newOnHost, starting a worktree's tab when wt is set. When
// the host makes the tab but reports a problem, as a worktree's setup can,
// it returns both the address and the problem.
func startOnHost(local *cliConn, daemon string, session *model.Session, host string, tab proto.NewSession, detached bool, wt *hostWorktree) (address string, startErr error) {
	hc, done, err := dialHost(host, "cli")
	if err != nil {
		return "", err
	}
	defer func() { done() }()
	m, err := syncMsg(hc)
	if err != nil {
		return "", err
	}
	// Every connection checks the level before it sends what can start a process.
	check := func(level int) error {
		if err := hostLevel(tab.Env, tab.ID != "" || tab.ParentTab != "" || wt != nil, level); err != nil {
			return err
		}
		if wt != nil && level < proto.WorktreeHostLevel {
			return errors.New("the pitwall daemon running there is older than this pitwall and cannot make a worktree for a worker started from another machine; update pitwall there or restart its daemon")
		}
		return nil
	}
	if err := check(m.Level); err != nil {
		return "", fmt.Errorf("%s: %w", host, err)
	}
	// The host checks the start before anything is pushed, made or recorded.
	// An existing id is reconciled by the daemon without starting a process.
	// Its executable or credentials may have changed since the original start.
	knownID := tab.ID != "" && (m.State.Workspace(tab.ID) != nil || slices.ContainsFunc(m.State.Closed, func(c model.ClosedTab) bool { return c.ID == tab.ID || c.Tab.ID == tab.ID }))
	if (len(tab.Cmd) > 0 || wt != nil) && m.Level >= proto.HostCheckLevel && !knownID {
		req := proto.HostCheck{Cmd: tab.Cmd, Env: tab.Env, Cwd: tab.Cwd}
		if wt != nil {
			req.Repo, req.Clone = wt.msg.Root, wt.clone
			req.Cwd = filepath.Join(req.Repo, ".worktrees", gitstat.Slug(tab.Name))
		}
		if err := hostCheck(hc, host, req); err != nil {
			return "", err
		}
	}
	if tab.ID == "" && m.Level >= proto.RemoteWorkerLevel {
		tab.ID = newTabID()
	}
	// The commit goes first: a host without the repository gets no part,
	// record or tab. A retry pushes the same commit to the same ref.
	if wt != nil && wt.push != nil && !knownID {
		if err := wt.push(tab.ID); err != nil {
			return "", err
		}
	}
	hs := m.State
	origin := model.Origin{Daemon: daemon, Session: session.ID}
	if daemon == "" || m.Level < proto.OriginLevel {
		origin = model.Origin{} // a person's shell, with a daemon too old on one side
	}
	// why: an agent never joins by name, so it cannot take over the tabs of
	// another session that happens to have its session's name.
	part := partOn(hs, session, host, origin, os.Getenv("PITWALL_PANE") == "")
	shell := "" // a new part's first tab, a shell
	if part == nil {
		name := session.Name
		if origin != (model.Origin{}) {
			// A repeated SessionNew can return a part another client made.
			// Only our unique creation name proves its shell is ours to close.
			name += "-" + rand.Text()
		}
		if hs, err = syncCLI(hc, proto.SessionNew{Name: name, Cwd: tab.Cwd, Origin: origin}); err != nil {
			return "", err
		}
		if part = hs.PartOf(origin); origin == (model.Origin{}) {
			part = hs.SessionNamed(session.Name)
		}
		if part == nil {
			return "", fmt.Errorf("%s made no part of session %s", host, session.Name)
		}
		if part.Name == name {
			if len(hs.Numbered(part.ID)) != 1 {
				return "", fmt.Errorf("%s's new part of session %s changed before its initial shell could be identified", host, session.Name)
			}
			shell = hs.Numbered(part.ID)[0].ID
			if origin != (model.Origin{}) {
				id := part.ID
				for tries := 0; ; tries++ {
					name = session.Name
					for n := 2; hs.SessionNamed(name) != nil && hs.SessionNamed(name).ID != id; n++ {
						name = session.Name + "-" + strconv.Itoa(n)
					}
					hs, err = syncCLI(hc, proto.SessionRename{SessionID: id, Name: name})
					if err == nil {
						break
					}
					if taken := hs.SessionNamed(name); tries >= 2 || taken == nil || taken.ID == id {
						return "", err
					}
				}
			}
		}
	} else if part.Origin != origin {
		if hs, err = syncCLI(hc, proto.SetOrigin{SessionID: part.ID, Origin: origin}); err != nil {
			return "", err
		}
	}
	partID, id := part.ID, shell
	// The link comes first, so a part a lost connection leaves is known here.
	if r := session.Remote(host); r == nil || r.Session != partID {
		if _, err := syncCLI(local, proto.SessionLink{SessionID: session.ID, Host: host, Remote: partID}); err != nil {
			return "", err
		}
	}
	// Reconcile before a retry: after its parent reopened, the host must
	// learn the new parent before it can recognize the same start.
	hs = reconcileWorkers(local, hc, *session, host, hs, origin, m.Level)
	recorded := slices.ContainsFunc(session.Workers, func(w model.RemoteWorker) bool { return w.ID == tab.ID })
	if tab.ParentTab != "" {
		// A retry restarts the record's clock and clears a Lost.
		w := model.RemoteWorker{ID: tab.ID, Host: host, Parent: tab.ParentTab, Since: time.Now()}
		if _, err := syncCLI(local, proto.RemoteWorkers{SessionID: session.ID, Add: []model.RemoteWorker{w}, Set: []model.RemoteWorker{w}}); err != nil {
			return "", err
		}
	}
	var problem error // the host made the tab, with a problem
	if shell == "" || len(tab.Cmd) > 0 || tab.Name != "" || len(tab.Env) > 0 || tab.ID != "" {
		before := hs
		tab.SessionID = partID
		var start any = tab
		if wt != nil {
			w := wt.msg
			w.ID, w.Name, w.Cmd, w.Env, w.ParentTab, w.SessionID = tab.ID, tab.Name, tab.Cmd, tab.Env, tab.ParentTab, partID
			start = w
		}
		// Later steps can fail after the start succeeded, such as hiding
		// its tab or closing the bootstrap shell. Keep the retry id there too.
		defer func() {
			if startErr != nil && startErr != problem && tab.ID != "" && !strings.Contains(startErr.Error(), "--id "+tab.ID) {
				startErr = fmt.Errorf("%w; tab @%s on %s; retry the same command with --id %s", startErr, tab.ID, host, tab.ID)
			}
		}()
		sm, err := syncMsg(hc, start)
		for _, wait := range startRetries {
			if err == nil || sm.Level != 0 || tab.ID == "" {
				break // the daemon answered, or a start without an id would run twice
			}
			time.Sleep(wait)
			done()
			var next *cliConn
			if next, done, err = dialHost(host, "cli"); err == nil {
				hc = next
				// Each connection can reach a different daemon. Check its
				// level before it receives anything that can start a process.
				if sm, err = syncMsg(hc); err == nil {
					if err = check(sm.Level); err != nil {
						return "", fmt.Errorf("lost %s while starting @%s; it may be running. Retry with --id %s after updating the host: %w", host, tab.ID, tab.ID, err)
					}
					sm, err = syncMsg(hc, start)
				} else {
					sm = proto.StateMsg{} // no answer to a start on this connection
				}
			} else {
				done = func() {}
			}
		}
		hs = sm.State
		if err != nil && sm.Level == 0 && tab.ID != "" {
			return "", fmt.Errorf("lost %s while starting @%s; it may be running. Run the same command with --id %s to finish starting it: %w", host, tab.ID, tab.ID, err)
		}
		if w := hs.Workspace(tab.ID); err != nil && wt != nil && w != nil && w.SessionID == partID && w.Parent == tab.ParentTab {
			problem, err = err, nil // our worktree, made with a setup problem: the tab stays, as here
		}
		if err != nil {
			if tab.ParentTab != "" && !recorded {
				syncCLI(local, proto.RemoteWorkers{SessionID: session.ID, Drop: []string{tab.ID}}) // the host refused it: nothing started
			}
			// A state accompanying the refusal proves the daemon answered.
			// Remove only our bootstrap shell; an uncertain connection must
			// leave any worker it may have started alone.
			if shell != "" && hs.Session(partID) != nil {
				if _, closeErr := syncCLI(hc, proto.CloseTab{WorkspaceID: shell}); closeErr != nil {
					return "", fmt.Errorf("%w; could not close the initial shell @%s on %s: %v", err, shell, host, closeErr)
				}
			}
			return "", err
		}
		id = tab.ID
		if id == "" { // a host that keeps no ids: the tab new in the part
			var made []string
			for _, w := range hs.Workspaces {
				if w.SessionID == partID && !slices.ContainsFunc(before.Workspaces, func(o model.Workspace) bool { return o.ID == w.ID }) {
					made = append(made, w.ID)
				}
			}
			if len(made) == 1 { // more when another client opened one there meanwhile
				id = made[0]
			}
		} else if hs.Workspace(id) == nil {
			return "", fmt.Errorf("@%s started on %s and has closed already", id, host)
		}
		if shell != "" { // the asked-for tab replaces it
			if hs, err = syncCLI(hc, proto.CloseTab{WorkspaceID: shell}); err != nil {
				return "", err
			}
		}
	}
	if id == "" && detached {
		return "", errors.New("cannot identify the new tab on " + host + ", so it is not detached")
	}
	if id == "" {
		return "", errors.New("cannot identify the new tab on " + host)
	}
	if detached {
		if hs, err = syncCLI(hc, proto.DetachSession{WorkspaceID: id, Detached: true}); err != nil {
			return "", err
		}
	}
	return tabNumber(host, hs.Numbered(partID), id), problem
}

// hostWarnings receives what a host check says a worker would start
// without. Tests replace it.
var hostWarnings io.Writer = os.Stderr

// hostCheck asks host's daemon, on hc, whether the worker req describes can
// start there. It prints warnings to hostWarnings, and returns the
// problems that stop it, each with its fix, as one error.
func hostCheck(hc *cliConn, host string, req proto.HostCheck) error {
	// The host runs up to three 10-second calls to origin and GitHub.
	if err := hc.socket.SetDeadline(time.Now().Add(time.Minute)); err != nil {
		return err
	}
	if err := hc.Send(req); err != nil {
		return err
	}
	for {
		msg, err := hc.Recv()
		if err != nil {
			return err
		}
		switch r := msg.(type) {
		case proto.Error:
			return errors.New(r.Message)
		case proto.HostCheckResult:
			var b strings.Builder
			for _, p := range r.Problems {
				line := "\n  " + p.What
				if p.Fix != "" {
					line += "\n    fix on " + host + ": " + p.Fix
				}
				if p.Warn {
					fmt.Fprintf(hostWarnings, "pitwall: %s: %s\n", host, strings.TrimPrefix(line, "\n  "))
				} else {
					b.WriteString(line)
				}
			}
			if b.Len() > 0 {
				return fmt.Errorf("%s cannot start this worker; nothing was started:%s", host, b.String())
			}
			return nil
		}
	}
}

// startRetries are the waits before each new try of a start on a host
// whose connection dropped.
var startRetries = []time.Duration{500 * time.Millisecond, 2 * time.Second, 5 * time.Second}

// newTabID is an id as a daemon makes one, for a tab the CLI starts on
// another host.
func newTabID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func sessionCommand(args []string, in *os.File, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	command := args[0]
	if command == "fork" {
		return forkCommand(args[1:], out)
	}
	if command == "tab" {
		return tabCommand(args[1:])
	}
	if command == "queue" {
		return queueCommand(args[1:], out)
	}
	if command == "session" {
		return sessionsCommand(args[1:], in, out, errOut)
	}
	args = args[1:]
	var cmd []string
	dashes := false
	if i := slices.Index(args, "--"); i >= 0 && command == "new" {
		args, cmd, dashes = args[:i], args[i+1:], true
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var asJSON, detached, force, children, tree, guarded, orphans, all bool
	var name, sessionName, preset, modelName, hostName, tabID string
	var issue int
	flags.StringVar(&sessionName, "s", "", "")
	switch command {
	case "ls":
		flags.BoolVar(&asJSON, "json", false, "")
		flags.BoolVar(&children, "children", false, "")
		flags.BoolVar(&tree, "tree", false, "")
		flags.BoolVar(&orphans, "orphans", false, "")
	case "adopt":
		flags.BoolVar(&all, "all", false, "")
	case "new":
		flags.StringVar(&name, "n", "", "")
		flags.StringVar(&modelName, "model", "", "")
		flags.BoolVar(&detached, "d", false, "")
		flags.IntVar(&issue, "issue", 0, "")
		flags.StringVar(&preset, "preset", "", "")
		flags.BoolVar(&guarded, "guard", false, "")
		flags.StringVar(&hostName, "host", "", "")
		flags.StringVar(&tabID, "id", "", "")
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
	if (command == "ls" && (len(args) != 0 || orphans && (children || tree))) ||
		(command == "adopt" && (len(args) == 0) == !all) ||
		((command == "new" || command == "attach" || command == "detach") && len(args) > 1) ||
		(command == "kill" && len(args) != 1) ||
		(command == "rename" && (len(args) < 1 || len(args) > 2)) {
		return errors.New(usage)
	}
	if dashes && len(cmd) == 0 && preset == "" {
		return errors.New(usage)
	}
	var vars []string
	if cmd, vars, err = launch(preset, guarded, cmd, hostName == ""); err != nil {
		return err
	}
	if cmd, err = withModel(cmd, modelName); err != nil {
		return err
	}
	if issue != 0 && cmd == nil {
		return errors.New("--issue needs an agent to run: pitwall new --issue 12 -- claude")
	}
	if tabID != "" && hostName == "" {
		return errors.New("--id names a tab on another host and goes with --host")
	}
	if issue != 0 && hostName != "" {
		return errors.New("--issue reads the issue from this machine's checkout and cannot be used with --host; pass the prompt after the agent instead")
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
	m, err := syncMsg(conn)
	if err != nil {
		return err
	}
	state := m.State
	if err := varsLevel(vars, m.Level); err != nil {
		return err
	}
	session, err := currentSession(state, sessionName)
	if err != nil && (sessionName != "" || command != "ls" && command != "new") {
		return err // with no session, ls lists nothing and new makes one
	}
	switch command {
	case "ls":
		if orphans || children && workersOf(state, callerPaneTab(state)) != nil {
			r := newRemotes(conn, state, session)
			defer r.close()
			if orphans {
				return listOrphans(out, errOut, r, asJSON)
			}
			return listChildren(out, r, asJSON, tree)
		}
		var only map[string]bool
		if children {
			if only, err = childTabs(state); err != nil {
				return err
			}
		}
		if asJSON {
			return json.NewEncoder(out).Encode(tabsJSON(state, session.ID, only))
		}
		return listTabs(out, state, session.ID, only, tree, nil, nil)
	case "adopt":
		r := newRemotes(conn, state, session)
		defer r.close()
		return adopt(out, r, args, all)
	case "new":
		if hostName != "" {
			if err := sharesHere(m, err); err != nil {
				return err
			}
			dir := ""
			if len(args) == 1 {
				dir = args[0] // a folder on the host
			}
			parent := parentHere(state, session.ID)
			num, err := newOnHost(conn, state.Daemon, &session, hostName, proto.NewSession{Name: name, Cwd: dir, Cmd: cmd, Env: vars, ID: tabID, ParentTab: parent}, detached)
			if err != nil {
				return err
			}
			if parent != "" && !shownInWindows(hostName) {
				fmt.Fprintf(errOut, "pitwall: %s is not shown in windows; set window = true under its [[hosts]] entry to see its workers in the sidebar\n", hostName)
			}
			_, err = fmt.Fprintf(out, "#%s\n", num)
			return err
		}
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		dir, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if cmd, err = withIssue(cmd, dir, issue); err != nil {
			return err
		}
		after, err := syncCLI(conn, proto.NewSession{Name: name, Cwd: dir, SessionID: session.ID, Cmd: cmd, Env: vars, ParentPane: os.Getenv("PITWALL_PANE")})
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
	target := conn
	if command == "kill" {
		r := newRemotes(conn, state, session)
		defer r.close()
		var h *hostPart
		if h, w, err = r.tab(old); h != nil {
			target = h.conn
		}
	}
	if err != nil {
		return err
	}
	var request any
	switch command {
	case "attach":
		if err := needsWindow(state, w.SessionID); err != nil {
			return err
		}
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
	_, err = syncCLI(target, request)
	return err
}

// sharesHere refuses --host when there is no session here (sessionErr) or
// this machine's daemon, m, cannot link a part of it on another host, or
// record an agent's worker there.
func sharesHere(m proto.StateMsg, sessionErr error) error {
	if sessionErr != nil {
		return errors.New("--host opens a tab in a session here, and there is none: open a tab here first")
	}
	if m.Level < proto.SharedLevel || os.Getenv("PITWALL_PANE") != "" && (m.State.Daemon == "" || m.Level < proto.RemoteWorkerLevel) {
		return errors.New("pitwall's background service here is older than this pitwall and cannot share a session; restart it")
	}
	return nil
}

// parentHere is the tab of the calling pane when it is in session: the
// parent of a tab it opens on another host, as here.
func parentHere(state model.State, session string) string {
	pane := os.Getenv("PITWALL_PANE")
	if i := slices.IndexFunc(state.Panes, func(p model.Pane) bool { return p.ID == pane }); pane != "" && i >= 0 && state.SessionOf(state.Panes[i].WorkspaceID) == session {
		return state.Panes[i].WorkspaceID
	}
	return ""
}

// callerPaneTab is the tab of $PITWALL_PANE in state, "" outside a pane.
func callerPaneTab(state model.State) string {
	tab, _ := callerTab(state)
	return tab
}

// shownInWindows reports whether host's [[hosts]] entry has window = true.
func shownInWindows(host string) bool {
	s, _ := config.Load()
	return slices.ContainsFunc(s.Hosts, func(h config.Host) bool { return h.Name == host && h.Window })
}

// launch is the command and environment of a tab that new, worktree new
// or queue add opens: preset's agent and args before rest, else rest, its
// program made absolute; preset's variables, and guard.Env when guarded or
// the preset asks for the guard. With here false the program is left as
// it is: it runs on another host, whose daemon finds it on its own PATH.
func launch(preset string, guarded bool, rest []string, here bool) (cmd, vars []string, err error) {
	cmd = rest
	if preset != "" {
		s, _ := config.Load()
		p, ok := s.FindPreset(preset)
		if !ok {
			return nil, nil, fmt.Errorf("no preset %q in %s", preset, config.Path())
		}
		cmd = append(append([]string{p.Agent}, p.Args...), rest...)
		vars, guarded = slices.Clone(p.Env), guarded || p.Guard
	}
	if guarded {
		if len(cmd) == 0 {
			return nil, nil, errors.New("--guard needs an agent to run: --guard -- claude")
		}
		vars = append(vars, guard.Env+"=1")
	}
	if len(cmd) > 0 && here {
		err = absCmd(cmd)
	}
	return cmd, vars, err
}

// withModel applies a CLI model override after resolving a preset.
func withModel(cmd []string, selection string) ([]string, error) {
	if strings.TrimSpace(selection) == "" {
		return cmd, nil
	}
	if len(cmd) == 0 {
		return nil, errors.New("--model needs an agent to run: --model opus -- claude")
	}
	return append([]string{cmd[0]}, config.WithModel(cmd[0], cmd[1:], selection)...), nil
}

// hostLevel is varsLevel for a tab on another host. From a pitwall pane
// the host must also keep the part of each session apart (OriginLevel),
// and a worker, a tab with an id or a parent here, needs both kept
// (RemoteWorkerLevel).
func hostLevel(vars []string, worker bool, level int) error {
	if err := varsLevel(vars, level); err != nil {
		return err
	}
	if os.Getenv("PITWALL_PANE") != "" && level < proto.OriginLevel {
		return errors.New("the pitwall daemon running there is older than this pitwall and cannot tell this session's tabs from another session's of the same name; update pitwall there or restart its daemon")
	}
	if worker && level < proto.RemoteWorkerLevel {
		return errors.New("the pitwall daemon running there is older than this pitwall and would start this tab without its id or its parent here; update pitwall there or restart its daemon")
	}
	return nil
}

// varsLevel refuses vars, a preset's or the guard's, for a daemon that
// would drop them, and the guard for one that would not check its hooks.
// Every path that starts a tab calls it.
func varsLevel(vars []string, level int) error {
	if len(vars) > 0 && level < proto.EnvLevel {
		return errors.New("the pitwall daemon running is older than this pitwall and would drop the preset's variables and the guard; they work once it restarts")
	}
	if level < proto.GuardGateLevel && slices.ContainsFunc(vars, guard.EnvOn) {
		return errors.New("the pitwall daemon running is older than this pitwall and would start a guarded tab without checking for pitwall's hooks; update pitwall there or restart its daemon")
	}
	return nil
}

// absCmd makes cmd's program an absolute path, found on this shell's
// PATH. why: the daemon runs cmd in another folder with its own PATH.
func absCmd(cmd []string) error {
	path, err := exec.LookPath(cmd[0])
	if err != nil && !errors.Is(err, exec.ErrDot) {
		return err // ErrDot: a relative PATH entry, which this shell would run too
	}
	cmd[0], err = filepath.Abs(path)
	return err
}

// envCaller is the caller this process is: session as -s named it, in
// the pane $PITWALL_PANE names.
func envCaller(session string) control.Caller {
	return control.Caller{Session: session, Pane: os.Getenv("PITWALL_PANE")}
}

// currentSession is control.CurrentSession for the calling pane: the
// session named name, else the pane's, else the most recently used one.
func currentSession(state model.State, name string) (model.Session, error) {
	return control.CurrentSession(state, envCaller(name))
}

// resolveSession is control.ResolveSession.
var resolveSession = control.ResolveSession

// resolveTab is control.ResolveTab for the calling pane: with no name it
// is that pane's tab.
func resolveTab(state model.State, session, name string) (model.Workspace, error) {
	return control.ResolveTab(state, envCaller(""), session, name)
}

// numbered is model.State.Numbered.
func numbered(state model.State, session string) []model.Workspace { return state.Numbered(session) }

// tabTitle is control.Title.
var tabTitle = control.Title

// childTabs is control.ChildTabs for the calling pane.
func childTabs(state model.State) (map[string]bool, error) {
	return control.ChildTabs(state, envCaller(""))
}

// listTabs prints one row per tab of session, or of only when it is not
// nil: its number, title, state and since when, folder and group. With
// tree, a tab's workers follow it, indented under it.
//
// where, when not nil, names a tab's machine and its number there, for a
// state with other machines' tabs, whose rows read box:3; stale are
// workers on hosts out of reach, listed last with their state unknown.
func listTabs(out io.Writer, state model.State, session string, only map[string]bool, tree bool, where func(model.Workspace) (string, int), stale []model.RemoteWorker) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	home, _ := os.UserHomeDir()
	if _, err := fmt.Fprintln(w, "#\tNAME\tSTATE\tSINCE\tFOLDER\tGROUP"); err != nil {
		return err
	}
	tabs := numbered(state, session)
	var rows []int             // indexes into tabs, in print order
	prefix := map[int]string{} // tree connectors before a tab's name
	listed := func(i int) bool { return only == nil || only[tabs[i].ID] }
	if tree {
		// walk adds tab i and, below it, the listed tabs it opened.
		var walk func(i int, indent string)
		walk = func(i int, indent string) {
			rows = append(rows, i)
			var kids []int
			for k, t := range tabs {
				if t.Parent == tabs[i].ID && listed(k) {
					kids = append(kids, k)
				}
			}
			for n, k := range kids {
				branch, next := "├─ ", "│  "
				if n == len(kids)-1 {
					branch, next = "└─ ", "   "
				}
				prefix[k] = indent + branch
				walk(k, indent+next)
			}
		}
		for i, t := range tabs {
			if p := slices.IndexFunc(tabs, func(p model.Workspace) bool { return p.ID == t.Parent }); listed(i) && (p < 0 || !listed(p)) {
				walk(i, "")
			}
		}
	} else {
		for i := range tabs {
			if listed(i) {
				rows = append(rows, i)
			}
		}
	}
	var chats []model.Chat
	for _, i := range rows {
		tab := tabs[i]
		if c := slices.IndexFunc(state.Chats, func(c model.Chat) bool { return c.ID == tab.Chat }); tab.Chat != "" {
			if c >= 0 {
				chats = append(chats, state.Chats[c])
			}
			continue // listed under chats
		}
		label, since := "idle", "-"
		var activities []model.Activity
		for _, a := range state.Activities {
			if a.WorkspaceID == tab.ID {
				activities = append(activities, a)
			}
		}
		if a := model.Aggregate(activities); a != nil {
			label = model.PillLabel(*a)
			if !a.UpdatedAt.IsZero() {
				since = ago(time.Since(a.UpdatedAt))
			}
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
		num := strconv.Itoa(i + 1)
		if where != nil {
			if host, n := where(tab); host != "" {
				num = hostAddr(host, n)
			} else {
				num = strconv.Itoa(n)
			}
		}
		if _, err := fmt.Fprintf(w, "%s\t%s%s\t%s\t%s\t%s\t%s%s\n", num, prefix[i], tabTitle(tab), label, since, folder, group, marker); err != nil {
			return err
		}
	}
	for _, r := range stale {
		if _, err := fmt.Fprintf(w, "%s:@%s\t?\tunknown (%s unreachable)\t-\t-\t-\n", r.Host, r.ID, r.Host); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil || len(chats) == 0 {
		return err
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	return listChats(out, state, session, chats)
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
