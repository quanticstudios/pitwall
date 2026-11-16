//go:build !nogui

package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"time"

	gioapp "gioui.org/app"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// binName is this binary's name: the desktop pitwall, with its window.
const binName = "pitwall"

// canOpenWindow tells the commands that open a window that they can.
const canOpenWindow = true

// openURL opens a URL in the default browser; a var for tests.
var openURL = app.OpenURL

// runWindow runs the commands that open a window: none, -s and --host.
func runWindow(cmd string, args []string) {
	switch cmd {
	case "":
		bundleEnv()
		onMain(cmd, func() error { return runGUI("") })
	case "-s":
		if len(args) != 1 || args[0] == "" {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		onMain(cmd, func() error { return runGUI(args[0]) })
	case "--host":
		host, session, ok := parseHost(args)
		if !ok {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		onMain(cmd, func() error { return runHostGUI(host, session) })
	}
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
	if runtime.GOOS != "windows" { // Windows keeps the restart
		app.HandOff = handOffSelf
	}
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
	var ab app.Backend = b
	if s, _ := config.Load(); app.Host == "" {
		if hosts, closeHosts := windowHosts(s.Hosts); len(hosts) > 0 {
			defer closeHosts()
			ab = newMulti(b, hosts)
		}
	}
	err = app.Run(ab)
	log.Printf("window closed: %s", outcome(err))
	return err
}

// handOffSelf moves the daemon's panes to this binary's daemon, their
// programs still running; a var for tests.
var handOffSelf = func() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return handOff(exe)
}

// dialOrStart completes the GUI handshake, starting a daemon when none
// runs. A daemon of another proto.Version keeps running, reported as
// incompatible, unless restart is set: then it hands its panes to this
// binary's daemon, or when it cannot, gets one graceful restart, so it
// saves its state, and a daemon of this version replaces it.
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
	conn, initial, err := guiHandshake(nc, cwd(), session)
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
	log.Printf("replacing the daemon: %q", err)
	if hErr := handOffSelf(); hErr == nil {
		log.Printf("the daemon handed its panes over")
		nc, err = net.Dial("unix", path)
	} else {
		// The user chose Restart, whose text says programs in panes stop.
		log.Printf("handoff: %q; restarting the daemon", hErr)
		if stopErr := stopIncompatibleDaemon(path); stopErr != nil {
			return nil, proto.StateMsg{}, fmt.Errorf("daemon handshake failed: %v; %w", err, stopErr)
		}
		nc, err = startDaemon(path)
	}
	if err != nil {
		return nil, proto.StateMsg{}, err
	}
	conn, initial, err = guiHandshake(nc, cwd(), session)
	if err != nil {
		return nil, proto.StateMsg{}, fmt.Errorf("replacement daemon handshake failed: %w", err)
	}
	return conn, initial, nil
}

func guiHandshake(nc net.Conn, cwd, session string) (*proto.Conn, proto.StateMsg, error) {
	conn := proto.NewConn(nc)
	fail := func(err error) (*proto.Conn, proto.StateMsg, error) {
		conn.Close()
		return nil, proto.StateMsg{}, err
	}
	if err := nc.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return fail(err)
	}
	if err := conn.Send(proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: cwd, Session: session}); err != nil {
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
// there. A host's daemon, which gets "", opens it in the home folder there.
func cwd() string {
	d, _ := os.Getwd()
	return d
}

// guiDial is how a window connects and reconnects: dialOrStart, or a
// remote's dial for pitwall --host.
var guiDial = dialOrStart

// hostArgs open another window on the same host: --host <name>, or none.
var hostArgs []string

// runHostGUI is runGUI on the daemon of an ssh host, which it starts when
// none runs there.
func runHostGUI(host, session string) error {
	s, _ := config.Load()
	r, err := newSSHHost(host, s.Hosts)
	if err != nil {
		return err
	}
	defer r.close()
	app.Host, guiDial, hostArgs = host, r.dial, []string{"--host", host}
	return runGUI(session)
}

// dial is dialOrStart on the host: it makes sure a daemon runs there,
// forwards its socket here and completes the GUI handshake. After the link
// drops, the backend calls it again, which opens a new ssh connection when
// the old one died.
func (r *sshHost) dial(session string, restart bool) (*proto.Conn, proto.StateMsg, error) {
	nc, err := r.socket(restart)
	if err != nil {
		return nil, proto.StateMsg{}, err
	}
	conn, initial, err := guiHandshake(nc, "", session)
	if err != nil && !errors.As(err, new(incompatible)) {
		err = fmt.Errorf("daemon handshake on %s failed: %w; see daemon.log there (pitwall logs)", r.name, err)
	}
	return conn, initial, err
}

// launchGUI opens a window on session in a process of its own, showing tab
// workspaceID when set.
var launchGUI = func(session, workspaceID string) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, append(slices.Clone(hostArgs), "-s", session)...)
	if workspaceID != "" {
		cmd.Env = append(os.Environ(), "PITWALL_ATTACH="+workspaceID)
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
