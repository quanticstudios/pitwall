// Command pitwall is the GUI client (default), the daemon (`pitwall daemon`)
// and the agent hook entry point (`pitwall hook <provider>`).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

const usage = `usage:
  pitwall                    open the window (starts the daemon if needed)
  pitwall daemon             run the daemon in the foreground
  pitwall hook <provider>    forward an agent hook event (claude, codex)
  pitwall hooks              print the Claude Code and Codex config that calls the hook
  pitwall hooks install      merge hooks into agent config files (--dry-run)
  pitwall hooks uninstall    remove this binary's hooks (--dry-run)
`

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "":
		err = runGUI()
	case "daemon":
		err = runDaemon()
	case "hook":
		runHook(os.Args[2:])
	case "hooks":
		err = runHooks(os.Args[2:], os.Stdout)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pitwall:", err)
		os.Exit(1)
	}
}

// runDaemon holds an exclusive lock next to the socket for its lifetime, so
// of two daemons started at once only one restores panes and binds; the
// other exits cleanly.
func runDaemon() error {
	path, err := proto.SocketPath()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errors.Is(err, syscall.EWOULDBLOCK) {
		fmt.Fprintln(os.Stderr, "pitwall: a daemon is already running on", path)
		return nil
	} else if err != nil {
		return err
	}
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return errors.New("a daemon is already running on " + path)
	}
	os.Remove(path) // stale socket from a crashed daemon: the lock holder owns it
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// Panes inherit this, so their hooks reach this daemon even when the
	// shell inside changes XDG_RUNTIME_DIR.
	os.Setenv("PITWALL_SOCKET", path)
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
// pitwall is down, and outside a pitwall pane it does nothing at all.
func runHook(args []string) {
	pane := os.Getenv("PITWALL_PANE")
	if pane == "" || len(args) == 0 {
		return
	}
	provider := model.Provider(args[0])
	var payload []byte
	if provider == model.ProviderCodex && len(args) > 1 {
		payload = []byte(args[len(args)-1]) // codex notify passes the JSON as the last argv
	} else {
		payload, _ = io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	}
	path := os.Getenv("PITWALL_SOCKET")
	if path == "" {
		var err error
		if path, err = proto.SocketPath(); err != nil {
			return
		}
	}
	conn, err := proto.Dial(path)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.Send(proto.Hello{Version: proto.Version, Kind: "hook"})
	conn.Send(proto.AgentEvent{Pane: pane, Provider: provider, Payload: payload})
}

func printHooks() error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Printf("# ~/.claude/settings.json, merge into the top-level object:\n{\"hooks\": %s}\n\n", agent.ClaudeHooks(bin))
	fmt.Printf("# ~/.codex/hooks.json (then trust them once with /hooks inside codex):\n{\"hooks\": %s}\n\n", agent.CodexHooks(bin))
	fmt.Printf("# or, for finished turns only, ~/.codex/config.toml:\n%s\n", agent.CodexNotify(bin))
	return nil
}

func runGUI() error {
	conn, err := dialOrStart()
	if err != nil {
		return err
	}
	b := newBackend(conn)
	if err := conn.Send(proto.Hello{Version: proto.Version, Kind: "gui"}); err != nil {
		return err
	}
	go b.recvLoop()
	return app.Run(b)
}

// dialOrStart connects to the daemon, launching a detached one first if
// nothing is listening. A daemon that loses the start race to another exits,
// and the loop below dials the winner.
func dialOrStart() (*proto.Conn, error) {
	path, err := proto.SocketPath()
	if err != nil {
		return nil, err
	}
	if c, err := proto.Dial(path); err == nil {
		return c, nil
	}
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(stateDir(), "daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd := exec.Command(bin, "daemon")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlive the window
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go cmd.Wait() // reap it if it exits while the window is open
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err := proto.Dial(path); err == nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("daemon did not come up; see %s", logPath)
}

func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "pitwall")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pitwall")
}
