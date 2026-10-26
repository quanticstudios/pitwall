// Command pitwall is the GUI client (default), the daemon (`pitwall daemon`)
// and the agent hook entry point (`pitwall hook <provider>`).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	gioapp "gioui.org/app"
	"github.com/charmbracelet/x/term"
	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const usage = `usage:
  pitwall --version          print the version
  pitwall                    open a window: on the most recent session no
                             window shows, else on a new session here
                             (starts the daemon if needed)
  pitwall -s <name>          open a window on session name, made if missing
  pitwall --host <host> [-s <name>]
                             open a window on the daemon of ssh host host,
                             a [[hosts]] name in config.toml or a destination
                             (starts the daemon there if needed)
  pitwall session <cmd>      ls, new, attach, rename, kill (see pitwall session)
  pitwall daemon             run the daemon in the foreground
  pitwall hook <provider>    forward an agent hook event (claude, codex, pi, omp, gemini, opencode)
  pitwall guard              exit 2 when the guard denies the command in {"tool_input":...} on stdin
  pitwall hooks              print the hook config for every agent
  pitwall hooks install      add hooks, pi's and omp's extensions, OpenCode's plugin and the
                             pitwall skill for Claude Code and Codex (--dry-run;
                             --statusline also runs Claude Code's status line through
                             pitwall statusline, to show its plan limits)
  pitwall hooks uninstall    remove this binary's hooks and status line (--dry-run)
  pitwall statusline [cmd]   Claude Code's status line: save its plan limits for
                             Settings, Usage, then run cmd, your own status line
  pitwall ls [--json] [--children] [--tree]
                             list the current session's tabs, numbered in sidebar order
                             (--json: one object per tab, see docs/agent-skill.md;
                             --children: only the tabs this pane's tab opened;
                             --tree: each tab under the tab that opened it)
  pitwall new [-n name] [-d] [--issue n] [--preset name] [--guard] [dir] [-- cmd args...]
                             open a tab (-d detaches it) running cmd, else a
                             shell; prints its #. --issue adds GitHub issue n
                             as cmd's last argument, its prompt. --preset runs a
                             [[agents.preset]] with cmd's args after its own;
                             --guard denies the agent pushes and merges (see
                             docs/usage.md). A cmd's pane stays after it
                             exits, showing its output, until closed. After a
                             daemon restart it shows a notice instead; a cmd
                             still running then is not run again (exit unknown).
                             Run inside a pane, the tab hangs under the pane's tab
  pitwall wait <tab> --until done|idle|blocked|exit [--timeout 10m]
                             block until the tab's agent is there (its first
                             live agent pane, else live pane). done: it
                             finished its turn; idle: done, or at its prompt;
                             blocked: it waits on a permission prompt or a
                             question; exit: its process ended. Without an agent, done means exit. Exit codes: 0 reached,
                             2 blocked instead, 3 the process exited, 124
                             timed out, 1 error; for exit, the process's code,
                             or 3 when a daemon restart lost it. --children in
                             place of <tab> waits on every tab this pane's tab
                             opened, printing one line each; one blocked ends it.
                             --done-line also wants a last reply line DONE or
                             BLOCKED: <why>, and nudges a worker once without one
  pitwall split [--down] [tab] [-- cmd args...]
                             open a pane running cmd, else a shell, right of
                             the tab's last pane (--down: below it); prints
                             its address, as #3.2
  pitwall prompt <pane> [--wait] [--timeout 10m] [-- text]
                             type text (- or none: stdin) into the pane's
                             agent once it is idle or done, and press Enter.
                             --wait waits for that turn and prints the
                             agent's reply. Exit codes as wait's
  pitwall send <pane> [-- text]
                             queue text (- or none: stdin) for the pane's
                             agent and return; pitwall types it, under a
                             header naming this pane, at the agent's next
                             prompt
  pitwall read <pane> [--screen]
                             print the last reply of the pane's agent
                             (--screen: the text on its screen)
  pitwall review [tab] [--agent claude|codex]
                             a second opinion: open the other agent, read
                             only, beside the tab's agent to review the
                             branch and uncommitted changes; prints its
                             address. Its first reply goes to the tab's agent
  pitwall status [pane] [-- text]
                             show text under the tab's row, such as "CI
                             3/7"; no text or --clear clears it
  pitwall attach [name]      show a tab in a window
  pitwall detach [name]      hide a tab, keeping its processes running
  pitwall kill [-f] <name>   close a tab and its processes
  pitwall rename [old] <new> rename a tab
  pitwall tab new            open a tab next to the calling pane's tab
  pitwall tab rename [name...]  name the calling pane's tab (empty clears)
  pitwall tab close          close the calling pane's tab
  pitwall queue <cmd>        ls, add, rm: tasks that start when a running
                             agent finishes (see pitwall queue)
  A tab is named by its # in pitwall ls, its id, its title, or a unique prefix
  of the title. Outside a pane, name it; inside, it defaults to the
  pane's own tab. Tab commands act on the calling pane's session, else
  the most recently used one; -s <session> picks another. A pane is
  TAB.N, the Nth pane of the tab in layout order (3.2, #3.2, review.2),
  or @ and its id from ls --json; wait, prompt, send and read take one,
  and a tab alone is its main pane.
  pitwall worktree <cmd>     ls, new, rm, prune: this repo's git worktrees (see pitwall worktree)
  pitwall completion <shell>  bash, zsh, fish: print the shell's completion script, e.g.
                             source <(pitwall completion bash)
  pitwall notify <text>      ring the calling pane, e.g. npm test && pitwall notify "tests passed"
  pitwall focus left|right|up|down
                             focus the pane next to the calling one, for
                             vim at the edge of its windows (see docs/keys.md)
  pitwall config <cmd>       path, default, init, check, schema (see pitwall config)
  pitwall doctor             check the install, the daemon, hooks and config; changes nothing
  pitwall jev <cmd>          login, status, logout, report: connect and measure TypeSafe's Jev
  pitwall logs [-f]          print the GUI and daemon log paths (-f: follow both)
  pitwall remote <cmd>       pair, devices, revoke: answer agents from a phone (see pitwall remote)
`

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	attachConsole(cmd)
	var err error
	switch cmd {
	case "--version", "-v", "version":
		fmt.Println("pitwall", versionString())
		return
	case "":
		bundleEnv()
		onMain(cmd, func() error { return runGUI("") })
		return
	case "-s":
		if len(os.Args) != 3 || os.Args[2] == "" {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		onMain(cmd, func() error { return runGUI(os.Args[2]) })
		return
	case "--host":
		host, session, ok := parseHost(os.Args[2:])
		if !ok {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		onMain(cmd, func() error { return runHostGUI(host, session) })
		return
	case "remote-start":
		err = runRemoteStart(os.Args[2:], os.Stdout)
	case "daemon":
		err = runDaemon()
	case "hook":
		// why: pi's final report runs detached, with no one left to kill a hook stuck on a busy daemon.
		time.AfterFunc(5*time.Second, func() { os.Exit(0) })
		os.Exit(runHook(os.Args[2:]))
	case "guard":
		os.Exit(runGuard(os.Stdin, os.Stdout, os.Stderr))
	case "hooks":
		err = runHooks(os.Args[2:], os.Stdout)
	case "statusline":
		os.Exit(runStatusline(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "config":
		os.Exit(runConfig(os.Args[2:], os.Stdout, os.Stderr))
	case "doctor":
		os.Exit(runDoctor(os.Stdout))
	case "notify":
		err = runNotify(os.Args[2:])
	case "focus":
		err = runFocus(os.Args[2:])
	case "jev":
		os.Exit(runJev(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "logs":
		err = runLogs(os.Args[2:], os.Stdout)
	case "remote":
		os.Exit(runRemote(os.Args[2:], os.Stdout, os.Stderr))
	case "worktree":
		os.Exit(runWorktree(os.Args[2:], os.Stdout, os.Stderr))
	case "completion":
		err = runCompletion(os.Args[2:], os.Stdout)
	case "ls", "new", "wait", "split", "prompt", "send", "read", "review", "attach", "detach", "kill", "rename", "tab", "session", "queue", "status":
		os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	exit(cmd, err)
}

// onMain runs a GUI command. macOS needs the main thread for [NSApp run]
// (gioapp.Main), or Gio's new window waits forever for the app to launch.
func onMain(cmd string, f func() error) {
	if runtime.GOOS != "darwin" {
		exit(cmd, f())
		return
	}
	go func() {
		exit(cmd, f())
		os.Exit(0)
	}()
	gioapp.Main()
}

func exit(cmd string, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "pitwall:", err)
		if cmd == "" || cmd == "-s" || cmd == "--host" {
			startFailed(err) // launched from a desktop launcher, stderr goes nowhere
		}
		os.Exit(1)
	}
}

// runDaemon holds an exclusive lock next to the socket for its lifetime, so
// of two daemons started at once only one restores panes and binds; the
// other exits cleanly.
func runDaemon() (err error) {
	// Started by a window, stderr is crash.log, which is for crash traces.
	w, _ := logs.Start(filepath.Join(stateDir(), "daemon.log"), "daemon", versionString(), term.IsTerminal(os.Stderr.Fd()))
	defer w.Close(time.Second)
	log.Printf("daemon starting")
	defer func() {
		log.Printf("daemon stopped: %s", outcome(err))
	}()
	path, err := proto.SocketPath()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if ok, err := tryLock(lock); err != nil {
		return err
	} else if !ok {
		log.Printf("a daemon is already running on %q", path)
		return nil
	}
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return errors.New("a daemon is already running on " + path)
	}
	if err := lock.Truncate(0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(lock, "%d\n", os.Getpid()); err != nil {
		return err
	}
	os.Remove(path) // stale socket from a crashed daemon: the lock holder owns it
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// Panes inherit this, so their hooks reach this daemon even when the
	// shell inside changes XDG_RUNTIME_DIR.
	os.Setenv("PITWALL_SOCKET", path)
	vt.DefaultPalette = config.Palette() // color queries answer with the theme the GUI draws
	if home, err := os.UserHomeDir(); err == nil {
		for _, p := range agent.RefreshSkill(home) {
			log.Printf("updated the pitwall skill at %s", p)
		}
	}
	daemon.Build = versionString()
	d, err := daemon.New()
	if err != nil {
		ln.Close()
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return d.Serve(ctx, ln)
}

// runHook never fails loudly: an agent must not stall or print errors because
// pitwall is down, and outside a pitwall pane it does nothing at all. It
// returns the exit status: 2 when the guard denies the tool call (guardHook),
// which then goes unreported, else 0.
func runHook(args []string) int {
	pane := os.Getenv("PITWALL_PANE")
	if pane == "" || len(args) == 0 {
		return 0
	}
	provider := model.Provider(args[0])
	var payload []byte
	if provider == model.ProviderCodex && len(args) > 1 {
		payload = []byte(args[len(args)-1]) // codex notify passes the JSON as the last argv
	} else {
		payload, _ = io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	}
	if code := guardHook(provider, payload, os.Stdout, os.Stderr); code != 0 {
		return code
	}
	path := os.Getenv("PITWALL_SOCKET")
	if path == "" {
		var err error
		if path, err = proto.SocketPath(); err != nil {
			return 0
		}
	}
	sendHook(path, pane, provider, payload)
	return 0
}

// sendHook forwards one hook event. It waits at most hookWait, for the
// daemon to read the event or refuse this proto.Version, and prints
// nothing, so a hook never stalls or decides an agent's permission prompt:
// recommendations show in pitwall only. A daemon from before hooks of any
// Version were served refuses one after an upgrade it was not restarted
// for; the event goes again at the Version it names.
func sendHook(path, pane string, provider model.Provider, payload []byte) {
	ev := proto.AgentEvent{Pane: pane, Provider: provider, Payload: payload}
	wait := hookWait
	if agent.Prompt(provider, payload) != "" {
		wait = promptWait // the daemon checkpoints the files first
	}
	deadline := time.Now().Add(wait)
	v, refused, reset := hookOnce(path, proto.Version, &ev, deadline)
	if reset {
		// why: Windows resets a socket closed with input unread, and the
		// reset can drop the refusal sent before it. A Hello alone leaves
		// nothing unread.
		v, refused, _ = hookOnce(path, proto.Version, nil, deadline)
	}
	if refused {
		hookOnce(path, v, &ev, deadline)
	}
}

// hookWait bounds sendHook. A daemon answers in a millisecond or two.
const hookWait = 500 * time.Millisecond

// promptWait bounds sendHook for a prompt, a little over the daemon's
// turnWait.
const promptWait = 2500 * time.Millisecond

// hookOnce sends ev, when set, after Hello{Version: v}. It reports the
// daemon's Version when the daemon refused v, and whether the connection
// ended in a reset rather than a close.
func hookOnce(path string, v int, ev *proto.AgentEvent, deadline time.Time) (dv int, refused, reset bool) {
	nc, err := net.Dial("unix", path)
	if err != nil {
		return 0, false, false
	}
	defer nc.Close()
	nc.SetDeadline(deadline)
	// why: a daemon closes as it refuses, and a Send that fails then
	// would close the conn before the refusal is read.
	conn := proto.NewConn(keepOpen{nc})
	conn.Send(proto.Hello{Version: v, Level: proto.Level, Kind: "hook"})
	if ev != nil {
		conn.Send(*ev)
	}
	if cw, ok := nc.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite() // the daemon closes once it has read the event
	}
	for {
		m, err := conn.Recv()
		if err != nil {
			return 0, false, !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrDeadlineExceeded)
		}
		if e, ok := m.(proto.Error); ok {
			if dv, refused := proto.RefusedVersion(e); refused && dv != v {
				return dv, true, false
			}
		}
	}
}

// keepOpen is a net.Conn whose Close does nothing.
type keepOpen struct{ net.Conn }

func (keepOpen) Close() error { return nil }

func printHooks() error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Printf("# ~/.claude/settings.json, merge into the top-level object:\n{\"hooks\": %s}\n\n", agent.ClaudeHooks(bin))
	fmt.Printf("# ~/.codex/hooks.json (then trust them once with /hooks inside codex):\n{\"hooks\": %s}\n\n", agent.CodexHooks(bin))
	fmt.Printf("# or, for finished turns only, ~/.codex/config.toml:\n%s\n\n", agent.CodexNotify(bin))
	fmt.Printf("# ~/.pi/agent/extensions/pitwall.ts ($PI_CODING_AGENT_DIR/extensions/pitwall.ts when set):\n%s\n", agent.PiExtension(bin))
	fmt.Printf("# ~/.omp/agent/extensions/pitwall.ts, for Oh My Pi (pi's above when $PI_CODING_AGENT_DIR is set):\n%s\n", agent.OmpExtension(bin))
	fmt.Printf("# ~/.gemini/settings.json, merge into the top-level object:\n{\"hooks\": %s}\n\n", agent.GeminiHooks(bin))
	fmt.Printf("# ~/.cursor/hooks.json, merge into the top-level object:\n{\"version\": 1, \"hooks\": %s}\n\n", agent.CursorHooks(bin))
	fmt.Printf("# ~/.config/opencode/plugins/pitwall.js and, for OpenCode 2, ~/.config/opencode/plugins/pitwall/tui.js ($XDG_CONFIG_HOME/opencode/plugins/ when set):\n%s\n", agent.OpenCodePlugin(bin))
	fmt.Printf("# ~/.claude/skills/pitwall/SKILL.md, and ~/.codex/skills/pitwall/SKILL.md ($CODEX_HOME/skills/pitwall/SKILL.md when set):\n%s", agent.Skill())
	return nil
}

// runGUI opens a window on the session named session, else the most
// recently used session no window shows, else a new session in the current
// folder. When a named session has a window already, it raises that window
// and exits instead.
func runGUI(session string) error {
	hideConsole()
	w, _ := logs.Start(filepath.Join(stateDir(), "gui.log"), "gui", versionString(), true)
	defer w.Close(time.Second)
	log.Printf("gui starting")
	if err := crashOutput(); err != nil {
		log.Printf("crash output: %q", err)
	}
	app.Version, app.Relaunch, app.InstallHooks = versionString(), launchGUI, installHooks
	app.NoteFirstRun() // before the daemon starts and saves its state
	conn, initial, err := guiDial(session, false)
	var b *backend
	var refused incompatible
	switch {
	case errors.As(err, &refused):
		// The window asks before the restart, which stops every program
		// in a pane.
		log.Printf("gui: %q; asking to restart it", err)
		b = newBackend(nil, "")
		b.link.State = app.LinkRestart
	case err != nil:
		log.Printf("gui: %q", err)
		return err
	default:
		defer conn.Close()
		log.Printf("connected to the daemon")
		target, raise := guiTarget(initial.State, session)
		if raise && os.Getenv("PITWALL_ATTACH") == "" {
			return conn.Send(proto.FocusSession{SessionID: target.ID})
		}
		b = newBackend(conn, target.ID)
		b.state, b.link.Level, b.link.Build = initial.State, initial.Level, initial.Build
		go b.recvLoop()
		go b.sendLoop()
	}
	b.name, b.redial = session, guiDial
	err = app.Run(b)
	log.Printf("window closed: %s", outcome(err))
	return err
}

// incompatible is a daemon's refusal of this client's proto.Version.
type incompatible struct{ version int }

func (e incompatible) Error() string {
	return fmt.Sprintf("the daemon speaks protocol version %d and this pitwall %d", e.version, proto.Version)
}

// dialOrStart completes the GUI handshake, starting a daemon when none
// runs. A daemon of another proto.Version keeps running, reported as
// incompatible, unless restart is set: then it gets one graceful restart,
// so it saves its state, and a daemon of this version replaces it.
func dialOrStart(session string, restart bool) (*proto.Conn, proto.StateMsg, error) {
	path, err := socketPath()
	if err != nil {
		return nil, proto.StateMsg{}, err
	}
	nc, err := net.Dial("unix", path)
	if err != nil {
		nc, err = startDaemon(path)
		if err != nil {
			return nil, proto.StateMsg{}, err
		}
	}
	conn, initial, err := guiHandshake(nc, session)
	switch {
	case err == nil && restart && app.Behind(initial.Level, initial.Build):
		// Same Version, older Level or release: the user asked to finish
		// an upgrade.
		conn.Close()
		err = fmt.Errorf("the daemon is %q at protocol level %d and this pitwall %q at %d", initial.Build, initial.Level, app.Version, proto.Level)
	case err == nil:
		return conn, initial, nil
	case !errors.As(err, new(incompatible)):
		return nil, proto.StateMsg{}, fmt.Errorf("daemon handshake failed: %w; see daemon.log (pitwall logs)", err)
	}
	if !restart {
		return nil, proto.StateMsg{}, err
	}
	log.Printf("restarting the daemon: %q", err)
	if stopErr := stopIncompatibleDaemon(path); stopErr != nil {
		return nil, proto.StateMsg{}, fmt.Errorf("daemon handshake failed: %v; %w", err, stopErr)
	}
	nc, err = startDaemon(path)
	if err != nil {
		return nil, proto.StateMsg{}, err
	}
	conn, initial, err = guiHandshake(nc, session)
	if err != nil {
		return nil, proto.StateMsg{}, fmt.Errorf("replacement daemon handshake failed: %w", err)
	}
	return conn, initial, nil
}

func guiHandshake(nc net.Conn, session string) (*proto.Conn, proto.StateMsg, error) {
	conn := proto.NewConn(nc)
	fail := func(err error) (*proto.Conn, proto.StateMsg, error) {
		conn.Close()
		return nil, proto.StateMsg{}, err
	}
	if err := nc.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return fail(err)
	}
	if err := conn.Send(proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: cwd(), Session: session}); err != nil {
		return fail(err)
	}
	for {
		msg, err := conn.Recv()
		if err != nil {
			return fail(err)
		}
		switch m := msg.(type) {
		case proto.StateMsg:
			if err := nc.SetDeadline(time.Time{}); err != nil {
				return fail(err)
			}
			return conn, m, nil
		case proto.Error:
			if v, ok := proto.RefusedVersion(m); ok {
				return fail(incompatible{v})
			}
			// The daemon could not open the session; its state follows.
			log.Printf("daemon: %q", m.Message)
		}
	}
}

func stopIncompatibleDaemon(path string) error {
	lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("cannot read old daemon pid: %w; stop the old daemon with kill <pid>, then retry", err)
	}
	defer lock.Close()
	data, err := io.ReadAll(io.LimitReader(lock, 64))
	if err != nil {
		return fmt.Errorf("cannot read old daemon pid: %w; stop the old daemon with kill <pid>, then retry", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		// Daemons from before the pid was written hold the lock all the same.
		pid, err = lockHolder(lock)
	}
	if err != nil || pid <= 1 || pid == os.Getpid() {
		return errors.New("old daemon lock has no valid daemon pid; stop the old daemon with kill <pid>, then retry")
	}
	// Another client may have stopped the daemon during our handshake.
	if ok, err := tryLock(lock); err != nil {
		return fmt.Errorf("inspect old daemon lock: %w; stop the old daemon with kill %d, then retry", err, pid)
	} else if ok {
		return nil
	}
	if err := terminate(pid); err != nil {
		return fmt.Errorf("stop old daemon: %w; stop the old daemon with kill %d, then retry", err, pid)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		ok, err := tryLock(lock)
		if err != nil {
			return fmt.Errorf("wait for old daemon lock: %w; stop the old daemon with kill %d, then retry", err, pid)
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("old daemon %d did not release its lock within 5s; stop the old daemon with kill %d, then retry", pid, pid)
}

// startDaemon launches a detached daemon. If another daemon wins the lock,
// the dial loop connects to that daemon.
func startDaemon(path string) (net.Conn, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	crashPath := filepath.Join(stateDir(), "crash.log")
	crash, err := logs.OpenFile(crashPath, logs.MaxSize)
	if err != nil {
		return nil, err
	}
	defer crash.Close()
	log.Printf("starting a daemon")
	cmd := exec.Command(bin, "daemon")
	// The daemon logs events to daemon.log itself; what it prints, a start
	// failure or a crash trace, goes to crash.log.
	cmd.Stdout, cmd.Stderr = crash, crash
	detach(cmd) // outlive the window
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go cmd.Wait() // reap it if it exits while the window is open
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err := net.Dial("unix", path); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("daemon did not come up; see %s and daemon.log next to it", crashPath)
}

func stateDir() string { return config.StateDir() }

// outcome is err quoted for a log line, or "ok".
func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	return strconv.Quote(err.Error())
}

// crashOutput sends the runtime's crash traces to crash.log as well as
// stderr, which a window started from a launcher has nowhere to show.
func crashOutput() error {
	f, err := logs.OpenFile(filepath.Join(stateDir(), "crash.log"), logs.MaxSize)
	if err != nil {
		return err
	}
	defer f.Close() // SetCrashOutput keeps a duplicate
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

// cwd is where the GUI was launched; the daemon opens the first session
// there. A host's daemon opens it in the home folder there.
func cwd() string {
	if app.Host != "" {
		return ""
	}
	d, _ := os.Getwd()
	return d
}
