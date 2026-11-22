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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/agentenv"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/logind"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const usage = `usage:
  pitwall tmux-shim -- <cmd...>
                             run cmd with tmux calls mapped to this tab
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
  pitwall agent explain [pane | --file path] [--provider name] [--json]
                             explain the agent state and the screen patterns checked
  pitwall hook <provider>    forward an agent hook event (claude, codex, pi, omp, gemini, opencode)
  pitwall report-agent [options] [-- cmd...]  report this pane's agent state, name and resume command
  pitwall guard              exit 2 when the guard denies the command in {"tool_input":...} on stdin
  pitwall hooks              print the hook config for every agent
  pitwall hooks install      add hooks, pi's and omp's extensions, OpenCode's plugin and the
                             pitwall skill for Claude Code and Codex (--dry-run;
                             --statusline also runs Claude Code's status line through
                             pitwall statusline, to show its plan limits)
  pitwall hooks uninstall    remove this binary's hooks and status line (--dry-run)
  pitwall statusline [cmd]   Claude Code's status line: save its plan limits for
                             Settings, Usage, then run cmd, your own status line
  pitwall ls [--json] [--children] [--tree] | --orphans
                             list the current session's tabs, numbered in sidebar order
                             (--json: one object per tab, see docs/agent-skill.md;
                             --children: only the tabs this pane's tab opened,
                             here and on other machines, as box:3;
                             --tree: each tab under the tab that opened it;
                             --orphans: workers here or elsewhere whose parent
                             tab is gone)
  pitwall adopt @<id>... | --all
                             make this pane's tab the parent of those orphans
  pitwall new [-n name] [-d] [--host name [--id id]] [--issue n] [--preset name] [--model model] [--guard] [dir] [-- cmd args...]
                             open a tab (-d detaches it) running cmd, else a
                             shell; prints its #. --issue adds GitHub issue n
                             as cmd's last argument, its prompt. --preset runs a
                             [[agents.preset]] with cmd's args after its own;
                             --model selects a model, default leaves it unset;
                             --guard denies the agent pushes and merges (see
                             docs/usage.md). A cmd's pane stays after it
                             exits, showing its output, until closed. After a
                             daemon restart it shows a notice instead; a cmd
                             still running then is not run again (exit unknown).
                             Run inside a pane, the tab hangs under the pane's tab.
                             --host opens it on that machine and prints #box:N,
                             which read, send, prompt, wait and kill take;
                             --id finishes a start there whose connection dropped
  pitwall wait <tab> --until done|idle|blocked|exit [--timeout 10m]
                             block until the tab's agent is there (its first
                             live agent pane, else live pane). done: it
                             finished its turn, and its pull request, if open,
                             merged or closed; idle: done, a pull request open,
                             or at its prompt;
                             blocked: it waits on a permission prompt or a
                             question; exit: its process ended. Without an agent, done means exit. Exit codes: 0 reached,
                             2 blocked instead, 3 the process exited, 124
                             timed out, 1 error; for exit, the process's code,
                             or 3 when a daemon restart lost it. --children in
                             place of <tab> waits on every tab this pane's tab
                             opened, printing one line each; one blocked ends it.
                             --done-line also wants a last reply line DONE or
                             BLOCKED: <why>, and nudges a worker once without one.
                             It exits 1 when another machine with a worker
                             stays out of reach 5 minutes or loses a worker
  pitwall split [--down] [tab] [-- cmd args...]
                             open a pane running cmd, else a shell, right of
                             the tab's last pane (--down: below it); prints
                             its address, as #3.2
  pitwall popup [--width 80%] [--height 70%] [--title T] [--dir D] [--wait] [tab] [-- cmd args...]
                             run cmd, else a shell, in a floating pane over
                             the tab's splits, in the tab's folder or D; it
                             closes when cmd exits. Sizes are cells or a % of
                             the tab. --wait returns cmd's exit code, 130 when
                             the popup closed first
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
  pitwall scrollback <pane> [--lines N]
                             print scrollback and screen as plain text
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
  pitwall status <state> [--app name] [--progress N] [--message text]
                             report a script's state to its pane: working,
                             blocked, done, error or idle (see docs/agent-report.md)
  pitwall form [--id id] [file]
                             show a form or a dashboard (file, - or none:
                             stdin) over the calling pane's tab and print
                             the answers as JSON; exits 1 on cancel, 2 on
                             an error (see docs/usage.md, Forms)
  pitwall form --id id --update [file] | --close
                             change an open form, or close it
  pitwall attach [name]      show a tab in a window
  pitwall detach [name]      hide a tab, keeping its processes running
  pitwall kill [-f] <name>   close a tab and its processes
  --host <name>              ls, read, send, prompt, wait, kill and session ls
                             act on that [[hosts]] machine's daemon over ssh,
                             -s picking a session there; ls --json rows
                             carry "host" and an addr, box:3, they all take
  pitwall rename [old] <new> rename a tab
  pitwall tab new            open a tab next to the calling pane's tab
  pitwall tab rename [name...]  name the calling pane's tab (empty clears)
  pitwall tab close          close the calling pane's tab
  pitwall queue <cmd>        ls, add, rm: tasks that start when a running
                             agent finishes (see pitwall queue)
  pitwall chat <cmd>         new, ls, send, approve, interrupt, set, read, wait, archive: agents
                             pitwall drives over their own protocol, one tab each
                             (see pitwall chat)
  A tab is named by its # in pitwall ls, its id, its title, or a unique prefix
  of the title. Outside a pane, name it; inside, it defaults to the
  pane's own tab. Tab commands act on the calling pane's session, else
  the most recently used one; -s <session> picks another. A pane is
  TAB.N, the Nth pane of the tab in layout order (3.2, #3.2, review.2),
  or @ and its id from ls --json; wait, prompt, send and read take one,
  and a tab alone is its main pane.
  pitwall fork [tab|pane] [--worktree name] [-n name]
                            fork Claude Code or Codex into a new tab
  pitwall worktree <cmd>     ls, new, lanes, rm, prune: this repo's git worktrees and the fleet lanes (see pitwall worktree)
  pitwall completion <shell>  bash, zsh, fish: print the shell's completion script, e.g.
                             source <(pitwall completion bash)
  pitwall notify <text>      ring the calling pane, e.g. npm test && pitwall notify "tests passed"
  pitwall focus left|right|up|down
                             focus the pane next to the calling one, for
                             vim at the edge of its windows (see docs/keys.md)
  pitwall config <cmd>       path, default, init, check, schema (see pitwall config)
  pitwall theme <cmd>        import, list: import Ghostty or iTerm2 themes (see pitwall theme)
  pitwall doctor             check the install, the daemon, hooks, config and [[hosts]]; changes nothing
  pitwall host status [--json] [name]
                             check each [[hosts]] entry over ssh, never prompting: reachable,
                             its pitwall's version and protocol, its daemon, the time taken
  pitwall host login <name>  ssh to name at this terminal to answer a password, MFA or
                             Tailscale check, then check it
  pitwall update [--restart] [--version vX]
                             install the latest release, or release vX older or newer,
                             over this binary, then hand the running daemon's panes to
                             it, their programs still running; if that fails, the old
                             daemon keeps serving, and --restart restarts it instead
                             (see docs/install.md#updates)
  pitwall logs [-f]          print the GUI and daemon log paths (-f: follow both)
  pitwall snapshots [restore <name>]
                             list the saved tabs' snapshots, or stop the daemon
                             and put one back (see docs/state.md)
  pitwall remote <cmd>       pair, devices, revoke, tailscale: answer agents from a phone (see pitwall remote)
  pitwall web <cmd>          open: the web client in a browser; token: the token local web clients connect with (see pitwall web)
`

func main() {
	if filepath.Base(os.Args[0]) == "tmux" {
		exit("tmux", runTmux(os.Args[1:], os.Stdout))
		return
	}
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	attachConsole(cmd)
	var err error
	switch cmd {
	case "--version", "-v", "version":
		fmt.Println("pitwall", versionLine())
		return
	case "", "-s", "--host":
		runWindow(cmd, os.Args[min(2, len(os.Args)):])
		return
	case "remote-start":
		err = runRemoteStart(os.Args[2:], os.Stdout)
	case "remote-status":
		err = runRemoteStatus(os.Stdout)
	case "host":
		os.Exit(runHost(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "daemon":
		if len(os.Args) == 3 && os.Args[2] == "--handoff" {
			err = runHandoffDaemon(os.NewFile(3, "handoff answer"))
		} else {
			err = runDaemon()
		}
	case "update":
		err = runUpdate(os.Args[2:], os.Stdout)
	case "hook":
		// why: pi's final report runs detached, with no one left to kill a hook stuck on a busy daemon.
		time.AfterFunc(5*time.Second, func() { os.Exit(0) })
		os.Exit(runHook(os.Args[2:]))
	case "guard":
		os.Exit(runGuard(os.Stdin, os.Stdout, os.Stderr))
	case "hooks":
		err = runHooks(os.Args[2:], os.Stdout)
	case "report-agent":
		err = runReportAgent(os.Args[2:])
	case "statusline":
		os.Exit(runStatusline(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "config":
		os.Exit(runConfig(os.Args[2:], os.Stdout, os.Stderr))
	case "theme":
		os.Exit(runTheme(os.Args[2:], os.Stdout, os.Stderr))
	case "doctor":
		os.Exit(runDoctor(os.Stdout))
	case "notify":
		err = runNotify(os.Args[2:])
	case "focus":
		err = runFocus(os.Args[2:])
	case "logs":
		err = runLogs(os.Args[2:], os.Stdout)
	case "snapshots":
		err = runSnapshots(os.Args[2:], os.Stdout)
	case "remote":
		os.Exit(runRemote(os.Args[2:], os.Stdout, os.Stderr))
	case "web":
		os.Exit(runWeb(os.Args[2:], os.Stdout, os.Stderr))
	case "worktree":
		os.Exit(runWorktree(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "completion":
		err = runCompletion(os.Args[2:], os.Stdout)
	case "tmux-shim":
		os.Exit(runTmuxShim(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "ls", "adopt", "new", "fork", "wait", "split", "popup", "prompt", "send", "read", "scrollback", "review", "attach", "detach", "kill", "rename", "tab", "session", "queue", "status", "chat", "agent", "form":
		os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	exit(cmd, err)
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
	daemonSetup(path)
	d, err := daemon.New()
	if err != nil {
		ln.Close()
		return err
	}
	return serveDaemon(d, ln)
}

// serveDaemon serves d until a stop signal, and has logind wait for it to
// save before the system shuts down or sleeps. SIGHUP comes with SIGTERM when
// a login session ends; a daemon started with SIGHUP ignored, as under
// nohup, keeps ignoring it.
func serveDaemon(d *daemon.Daemon, ln net.Listener) error {
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()
	// Programs in panes get the new colors when Omarchy or the system switches theme.
	go config.WatchOmarchy(ctx.Done(), func() { d.SetPalette(config.Palette()) })
	go config.WatchSystemAppearance(ctx.Done(), func() { d.SetPalette(config.Palette()) })
	go func() {
		err := logind.Watch(ctx, func(shutdown bool) {
			if shutdown {
				log.Printf("the system is shutting down: saving tabs")
				d.Freeze()
			} else {
				d.Save()
			}
		}, d.Thaw)
		if err != nil && !errors.Is(err, errors.ErrUnsupported) {
			log.Printf("logind: %q; tabs are saved at shutdown only when the daemon gets SIGTERM or SIGHUP", err)
		}
	}()
	return d.Serve(ctx, ln)
}

// daemonSetup prepares this process to serve path as the daemon.
func daemonSetup(path string) {
	// Panes inherit this, so their hooks reach this daemon even when the
	// shell inside changes XDG_RUNTIME_DIR.
	os.Setenv("PITWALL_SOCKET", path)
	// Started from inside an agent, the daemon would hand that agent's
	// session markers to every pane and chat: Claude Code then saves no
	// transcript.
	agentenv.Unset()
	vt.SetPalette(config.Palette()) // color queries answer with the theme the GUI draws
	if home, err := os.UserHomeDir(); err == nil {
		for _, p := range agent.RefreshSkill(home) {
			log.Printf("updated the pitwall skill at %s", p)
		}
	}
	daemon.Build = versionString()
}

// runHandoffDaemon takes over the panes of the daemon running, as
// docs/handoff.md describes, and serves them. It writes "ok" or why it
// could not to answer, which handOff passes as fd 3, and closes it before
// any pane can start and inherit it.
func runHandoffDaemon(answer *os.File) (err error) {
	w, _ := logs.Start(filepath.Join(stateDir(), "daemon.log"), "daemon", versionString(), term.IsTerminal(os.Stderr.Fd()))
	defer w.Close(time.Second)
	log.Printf("daemon starting, taking over the running one")
	defer func() {
		log.Printf("daemon stopped: %s", outcome(err))
	}()
	path, err := proto.SocketPath()
	if err == nil {
		daemonSetup(path)
	}
	var d *daemon.Daemon
	var ln net.Listener
	if err == nil {
		d, ln, err = daemon.Takeover(path, daemon.Defaults())
	}
	msg := "ok"
	if err != nil {
		msg = err.Error()
	}
	fmt.Fprint(answer, msg)
	answer.Close()
	if err != nil {
		return err
	}
	// The old daemon holds the lock until it exits, right after the
	// handoff; until then a daemon starting finds this one on the socket.
	// Nothing here may fail the daemon now: the panes would go down with it.
	if lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600); err != nil {
		log.Printf("daemon lock: %q", err)
	} else {
		defer lock.Close()
		go func() {
			err := waitLock(lock)
			if err == nil {
				err = lock.Truncate(0)
			}
			if err == nil {
				_, err = fmt.Fprintf(lock, "%d\n", os.Getpid())
			}
			if err != nil {
				log.Printf("daemon lock: %q", err)
			}
		}()
	}
	return serveDaemon(d, ln)
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
	if provider == model.ProviderAntigravity {
		// why: the daemon and its helpers read hooks in Claude Code's vocabulary.
		if payload = agent.FromAntigravity(payload); payload == nil {
			return 0
		}
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
	fmt.Printf("# ~/.gemini/config/hooks.json, for Antigravity (agy), merge into the top-level object:\n%s\n\n", agent.AntigravityHooks(bin))
	fmt.Printf("# ~/.config/opencode/plugins/pitwall.js and, for OpenCode 2, ~/.config/opencode/plugins/pitwall/tui.js ($XDG_CONFIG_HOME/opencode/plugins/ when set):\n%s\n", agent.OpenCodePlugin(bin))
	fmt.Printf("# ~/.claude/skills/pitwall/SKILL.md, and ~/.codex/skills/pitwall/SKILL.md ($CODEX_HOME/skills/pitwall/SKILL.md when set):\n%s", agent.Skill())
	return nil
}

// incompatible is a daemon's refusal of this client's proto.Version.
type incompatible struct{ version int }

func (e incompatible) Error() string {
	return fmt.Sprintf("the daemon speaks protocol version %d and this pitwall %d", e.version, proto.Version)
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
	for deadline := time.Now().Add(stopWait); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		ok, err := tryLock(lock)
		if err != nil {
			return fmt.Errorf("wait for old daemon lock: %w; stop the old daemon with kill %d, then retry", err, pid)
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("old daemon %d did not release its lock within %s; stop the old daemon with kill %d, then retry", pid, stopWait, pid)
}

// stopWait is how long a stopped old daemon gets to release its lock, and
// handshakeWait how long a daemon gets to answer the window's Hello. Vars so
// tests wait out shorter ones.
var stopWait, handshakeWait = 5 * time.Second, 2 * time.Second

// startDaemon launches a detached daemon. If another daemon wins the lock,
// the dial loop connects to that daemon.
func startDaemon(path string) (net.Conn, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return startDaemonOf(path, bin)
}

// startDaemonOf is startDaemon running bin.
func startDaemonOf(path, bin string) (net.Conn, error) {
	log.Printf("starting a daemon")
	cmd, err := daemonCmd(bin)
	if err != nil {
		return nil, err
	}
	err = cmd.Start()
	cmd.Stdout.(io.Closer).Close()
	if err != nil {
		return nil, err
	}
	go cmd.Wait() // reap it if it exits while the window is open
	crashPath := filepath.Join(stateDir(), "crash.log")
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err := net.Dial("unix", path); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("daemon did not come up; see %s and daemon.log next to it", crashPath)
}

// daemonCmd runs bin's daemon with args, detached to outlive this process.
// The daemon logs events to daemon.log itself; what it prints, a start
// failure or a crash trace, goes to crash.log, open as Stdout until the
// caller closes it after Start.
func daemonCmd(bin string, args ...string) (*exec.Cmd, error) {
	crash, err := logs.OpenFile(filepath.Join(stateDir(), "crash.log"), logs.MaxSize)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, append([]string{"daemon"}, args...)...)
	cmd.Stdout, cmd.Stderr = crash, crash
	detach(cmd)
	return cmd, nil
}

func stateDir() string { return config.StateDir() }

// outcome is err quoted for a log line, or "ok".
func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	return strconv.Quote(err.Error())
}
