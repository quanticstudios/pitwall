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
		err = printHooks()
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pitwall:", err)
		os.Exit(1)
	}
}

func runDaemon() error {
	path := proto.SocketPath()
	if c, err := net.Dial("unix", path); err == nil {
		c.Close()
		return errors.New("a daemon is already running on " + path)
	}
	os.Remove(path) // stale socket from a crashed daemon
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
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
	conn, err := proto.Dial(proto.SocketPath())
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
// nothing is listening.
func dialOrStart() (*proto.Conn, error) {
	path := proto.SocketPath()
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
	cmd.Process.Release()
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
