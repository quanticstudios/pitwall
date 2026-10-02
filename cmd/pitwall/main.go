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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

const usage = `usage:
  pitwall --version          print the version
  pitwall                    open the window (starts the daemon if needed)
  pitwall daemon             run the daemon in the foreground
  pitwall hook <provider>    forward an agent hook event (claude, codex)
  pitwall hooks              print the Claude Code and Codex config that calls the hook
  pitwall hooks install      merge hooks into agent config files (--dry-run)
  pitwall hooks uninstall    remove this binary's hooks (--dry-run)
  pitwall ls [--json]        list sessions
  pitwall new [-n name] [-d] [dir]  create a session (-d detaches it)
  pitwall attach [name]      show a session in the window
  pitwall detach [name]      hide a session, keeping its processes running
  pitwall kill [-f] <name>   close a session and its processes
  pitwall rename [old] <new> rename a session
  pitwall tab new            open a tab in the calling pane's session
  pitwall tab rename [name...]  name the calling pane's tab (empty clears)
  pitwall tab close          close the calling pane's tab
`

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "--version", "-v", "version":
		fmt.Println("pitwall", versionString())
		return
	case "":
		err = runGUI()
	case "daemon":
		err = runDaemon()
	case "hook":
		runHook(os.Args[2:])
	case "hooks":
		err = runHooks(os.Args[2:], os.Stdout)
	case "ls", "new", "attach", "detach", "kill", "rename", "tab":
		os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pitwall:", err)
		if cmd == "" {
			// Launched from a desktop launcher, stderr goes nowhere.
			exec.Command("notify-send", "--app-name=pitwall", "--urgency=critical", "pitwall could not start", err.Error()).Run()
		}
		os.Exit(1)
	}
}

// lockHolder is the pid holding an flock on f, from /proc/locks, which
// lists each lock's owner and the device:inode it covers.
func lockHolder(f *os.File) (int, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	data, err := os.ReadFile("/proc/locks")
	if err != nil {
		return 0, err
	}
	suffix := ":" + strconv.FormatUint(st.Ino, 10)
	for _, line := range strings.Split(string(data), "\n") {
		// "1: FLOCK  ADVISORY  WRITE 4056327 00:3a:1234 0 EOF"
		f := strings.Fields(line)
		if len(f) >= 6 && f[1] == "FLOCK" && strings.HasSuffix(f[5], suffix) && devMatches(f[5], st.Dev) {
			return strconv.Atoi(f[4])
		}
	}
	return 0, errors.New("no process holds the daemon lock")
}

// devMatches compares /proc/locks' "MAJ:MIN:INODE" (hex major and minor) with
// a stat device number.
func devMatches(field string, dev uint64) bool {
	parts := strings.Split(field, ":")
	if len(parts) != 3 {
		return false
	}
	maj, err1 := strconv.ParseUint(parts[0], 16, 32)
	min, err2 := strconv.ParseUint(parts[1], 16, 32)
	return err1 == nil && err2 == nil && maj == uint64(devMajor(dev)) && min == uint64(devMinor(dev))
}

func devMajor(dev uint64) uint32 { return uint32((dev>>32)&0xfffff000) | uint32((dev>>8)&0x00000fff) }
func devMinor(dev uint64) uint32 { return uint32((dev>>12)&0xffffff00) | uint32(dev&0x000000ff) }

// runDaemon holds an exclusive lock next to the socket for its lifetime, so
// of two daemons started at once only one restores panes and binds; the
// other exits cleanly.
func runDaemon() error {
	fmt.Fprintln(os.Stderr, "pitwall daemon", versionString(), "starting")
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
	lock, err := guiLock()
	if err != nil || lock == nil {
		return err
	}
	defer lock.Close()
	conn, initial, err := dialOrStart()
	if err != nil {
		return err
	}
	defer conn.Close()
	b := newBackend(conn)
	b.state = initial.State
	go b.recvLoop()
	return app.Run(b)
}

// dialOrStart completes the GUI handshake before starting the window. An
// incompatible daemon gets one graceful restart so it can save its state.
func dialOrStart() (*proto.Conn, proto.StateMsg, error) {
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
	conn, initial, err := guiHandshake(nc)
	if err == nil {
		return conn, initial, nil
	}
	fmt.Fprintln(os.Stderr, "pitwall: restarting incompatible daemon")
	if stopErr := stopIncompatibleDaemon(path); stopErr != nil {
		return nil, proto.StateMsg{}, fmt.Errorf("daemon handshake failed: %v; %w", err, stopErr)
	}
	nc, err = startDaemon(path)
	if err != nil {
		return nil, proto.StateMsg{}, err
	}
	conn, initial, err = guiHandshake(nc)
	if err != nil {
		return nil, proto.StateMsg{}, fmt.Errorf("replacement daemon handshake failed: %w", err)
	}
	return conn, initial, nil
}

func guiHandshake(nc net.Conn) (*proto.Conn, proto.StateMsg, error) {
	conn := proto.NewConn(nc)
	fail := func(err error) (*proto.Conn, proto.StateMsg, error) {
		conn.Close()
		return nil, proto.StateMsg{}, err
	}
	if err := nc.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return fail(err)
	}
	if err := conn.Send(proto.Hello{Version: proto.Version, Kind: "gui", Cwd: cwd()}); err != nil {
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
			return fail(errors.New(m.Message))
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
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EWOULDBLOCK) {
		return fmt.Errorf("inspect old daemon lock: %w; stop the old daemon with kill %d, then retry", err, pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop old daemon: %w; stop the old daemon with kill %d, then retry", err, pid)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("wait for old daemon lock: %w; stop the old daemon with kill %d, then retry", err, pid)
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
		if c, err := net.Dial("unix", path); err == nil {
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

// cwd is where the GUI was launched; the daemon opens the first session
// there.
func cwd() string {
	d, _ := os.Getwd()
	return d
}
