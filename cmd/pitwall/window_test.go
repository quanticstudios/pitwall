//go:build unix && !nogui

package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// setWindowSize sets the window's size, "WxH" in dp, for screenshots of
// the real window; "" leaves it.
func setWindowSize(size string) {
	if size != "" {
		fmt.Sscanf(size, "%dx%d", &app.WindowSize.X, &app.WindowSize.Y)
	}
}

// A daemon that refuses this client's proto.Version keeps running until
// the user chooses to restart it; one that does not answer the handshake
// is never restarted.
func TestDialOrStartAsksBeforeRestart(t *testing.T) {
	t.Setenv("PITWALL_SOCKET", "")
	for _, mode := range []string{"raw", "error", "silent", "empty", "stuck"} {
		t.Run(mode, func(t *testing.T) {
			handoffs := stubHandOff(t, errors.New("the old daemon cannot hand off"))
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("SHELL", "/bin/sh")
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("PITWALL_TEST_MAIN", "1")
			t.Chdir(home)
			path, err := proto.SocketPath()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), "PITWALL_TEST_OLD="+mode)
			ready, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(func() {
				cmd.Process.Kill()
				data, err := os.ReadFile(path + ".lock")
				pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
				if err == nil && parseErr == nil && pid != cmd.Process.Pid && pid > 1 {
					if err := stopIncompatibleDaemon(path); err != nil {
						t.Error(err)
					}
				}
			})
			started := make(chan error, 1)
			go func() {
				var data [6]byte
				_, err := io.ReadFull(ready, data[:])
				started <- err
			}()
			select {
			case err := <-started:
				if err != nil {
					t.Fatalf("start old daemon: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("old daemon did not start")
			}
			_, _, err = dialOrStart("", false)
			var refused incompatible
			if mode == "raw" || mode == "silent" {
				if err == nil || errors.As(err, &refused) || !strings.Contains(err.Error(), "daemon handshake failed") {
					t.Fatalf("a daemon that does not answer: %v", err)
				}
			} else if !errors.As(err, &refused) || refused.version != 15 {
				t.Fatalf("got %v, want a refusal of version 15", err)
			}
			select {
			case err := <-exited:
				t.Fatalf("the old daemon stopped before anyone chose to restart it: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if mode == "raw" || mode == "silent" {
				return
			}
			conn, initial, err := dialOrStart("", true)
			if *handoffs != 1 {
				t.Fatalf("restart tried %d handoffs, want 1 before the stop", *handoffs)
			}
			// "empty" is a daemon from before the pid was written: it is
			// found through /proc/locks and replaced like the others.
			if mode == "stuck" {
				if conn != nil {
					conn.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "stop the old daemon with kill ") {
					t.Fatalf("missing manual stop instruction: %v", err)
				}
				if mode == "stuck" && !strings.Contains(err.Error(), fmt.Sprintf("kill %d", cmd.Process.Pid)) {
					t.Fatalf("missing daemon pid: %v", err)
				}
				select {
				case err := <-exited:
					t.Fatalf("old daemon unexpectedly stopped: %v", err)
				default:
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if len(initial.State.Workspaces) == 0 {
				t.Fatal("replacement daemon did not return initial state")
			}
			select {
			case err := <-exited:
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
					t.Fatalf("old daemon was not SIGTERMed: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("old daemon did not stop")
			}
			data, err := os.ReadFile(path + ".lock")
			if err != nil || strings.TrimSpace(string(data)) == strconv.Itoa(cmd.Process.Pid) {
				t.Fatalf("replacement daemon did not write its pid: %q, %v", data, err)
			}
		})
	}
}

func TestDialOrStartHealthyDaemon(t *testing.T) {
	t.Setenv("PITWALL_SOCKET", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path, err := proto.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	served := make(chan error, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			served <- err
			return
		}
		conn := proto.NewConn(nc)
		defer conn.Close()
		msg, err := conn.Recv()
		if err != nil {
			served <- err
			return
		}
		hello, ok := msg.(proto.Hello)
		if !ok || hello.Version != proto.Version || hello.Kind != "gui" || hello.Cwd != cwd() || hello.Session != "work" {
			served <- fmt.Errorf("unexpected Hello: %#v", msg)
			return
		}
		if err := conn.Send(proto.StateMsg{State: model.State{Version: 42}}); err != nil {
			served <- err
			return
		}
		// The handshake deadline must not close a healthy connection later.
		time.Sleep(2100 * time.Millisecond)
		served <- conn.Send(proto.Frame{Pane: "healthy"})
	}()
	conn, initial, err := dialOrStart("work", false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if initial.State.Version != 42 {
		t.Fatalf("initial state lost: %+v", initial.State)
	}
	if msg, err := conn.Recv(); err != nil {
		t.Fatal(err)
	} else if frame, ok := msg.(proto.Frame); !ok || frame.Pane != "healthy" {
		t.Fatalf("got %#v, want healthy frame", msg)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir(), "daemon.log")); !os.IsNotExist(err) {
		t.Fatalf("healthy daemon triggered a start: %v", err)
	}
}

// A daemon of this Version at an older Level, or at this Level on an older
// release, serves the window as it is, and is replaced only when the user
// asks to restart it.
func TestDialOrStartRestartsOlderDaemon(t *testing.T) {
	t.Setenv("PITWALL_SOCKET", "")
	defer func(v string) { app.Version = v }(app.Version)
	app.Version = "v0.1.1"
	for _, mode := range []string{"behind", "older"} {
		t.Run(mode, func(t *testing.T) {
			stubHandOff(t, errors.New("the old daemon refused"))
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("SHELL", "/bin/sh")
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("PITWALL_TEST_MAIN", "1")
			t.Chdir(home)
			path, err := proto.SocketPath()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), "PITWALL_TEST_OLD="+mode)
			ready, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(func() {
				cmd.Process.Kill()
				if data, err := os.ReadFile(path + ".lock"); err == nil {
					if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid != cmd.Process.Pid && pid > 1 {
						stopIncompatibleDaemon(path)
					}
				}
			})
			var line [6]byte
			if _, err := io.ReadFull(ready, line[:]); err != nil {
				t.Fatalf("start old daemon: %v", err)
			}
			conn, initial, err := dialOrStart("", false)
			if err != nil || !app.Behind(initial.Level, initial.Build) {
				t.Fatalf("without restart: level %d, build %q, %v", initial.Level, initial.Build, err)
			}
			conn.Close()
			select {
			case err := <-exited:
				t.Fatalf("the old daemon stopped before anyone asked: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			conn, initial, err = dialOrStart("", true)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if len(initial.State.Workspaces) != 1 || initial.State.Workspaces[0].ID != "fake" {
				t.Fatalf("not the replacement daemon: %+v", initial.State)
			}
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				t.Fatal("the old daemon still runs")
			}
		})
	}
}

// stubHandOff makes the window's handoff return err and counts its calls.
func stubHandOff(t *testing.T, err error) *int {
	n := new(int)
	old := handOffSelf
	handOffSelf = func() error { *n++; return err }
	t.Cleanup(func() { handOffSelf = old })
	return n
}

// The window's Restart hands the daemon's panes over first, and stops the
// daemon only when that fails (TestDialOrStartRestartsOlderDaemon).
func TestRestartHandsOverFirst(t *testing.T) {
	t.Setenv("PITWALL_SOCKET", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	handoffs := stubHandOff(t, nil)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "PITWALL_TEST_OLD=behind")
	ready, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer cmd.Process.Kill()
	var line [6]byte
	if _, err := io.ReadFull(ready, line[:]); err != nil {
		t.Fatalf("start old daemon: %v", err)
	}
	conn, _, err := dialOrStart("", true)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if *handoffs != 1 {
		t.Fatalf("%d handoffs, want 1", *handoffs)
	}
	select {
	case err := <-exited:
		t.Fatalf("the restart stopped the daemon after a handoff: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
}

// pitwall attach opens a window on the tab's session only when no window
// shows it; one that does got the FocusSession and raises itself.
func TestCLIAttach(t *testing.T) {
	for _, shown := range []bool{false, true} {
		t.Run(fmt.Sprint(shown), func(t *testing.T) {
			after := cliState()
			if shown {
				after.Sessions[0].Windows = 1
			}
			fakeCLI(t, cliExchange{state: cliState()}, cliExchange{request: proto.FocusSession{WorkspaceID: "b"}, state: after})
			t.Setenv("PITWALL_PANE", "pb")
			previous := launchGUI
			var launched []string
			launchGUI = func(session, id string) error { launched = append(launched, session+"/"+id); return nil }
			t.Cleanup(func() { launchGUI = previous })
			args := []string{"attach"}
			if shown {
				args = append(args, "alpi")
			}
			if code, out, stderr := cliOutput(args...); code != 0 || out != "" || stderr != "" {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
			if shown && len(launched) != 0 {
				t.Fatal("launched a second window on a shown session")
			}
			if !shown && (len(launched) != 1 || launched[0] != "main/b") {
				t.Fatalf("launched: %v", launched)
			}
		})
	}
}

func TestCLIAttachErrors(t *testing.T) {
	for _, daemonError := range []bool{false, true} {
		t.Run(fmt.Sprint(daemonError), func(t *testing.T) {
			exchange := cliExchange{request: proto.FocusSession{WorkspaceID: "a"}, state: cliState()}
			if daemonError {
				exchange.error = "cannot focus"
			}
			fakeCLI(t, cliExchange{state: cliState()}, exchange)
			previous := launchGUI
			launched := false
			launchGUI = func(string, string) error { launched = true; return errors.New("cannot launch") }
			t.Cleanup(func() { launchGUI = previous })
			code, out, stderr := cliOutput("attach", "1")
			want := "pitwall: cannot launch\n"
			if daemonError {
				want = "pitwall: cannot focus\n"
			}
			if code != 1 || out != "" || stderr != want || launched == daemonError {
				t.Fatalf("%d: %s %s; launched %v", code, out, stderr, launched)
			}
		})
	}
}

func TestBackendFocus(t *testing.T) {
	t.Setenv("PITWALL_ATTACH", "startup")
	path := sockPath(t, "gui.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	served := make(chan error, 1)
	wanted := []proto.FocusSession{{WorkspaceID: "first", TabID: "tab"}, {WorkspaceID: "second"}}
	go func() {
		served <- func() error {
			nc, err := ln.Accept()
			if err != nil {
				return err
			}
			defer nc.Close()
			nc.SetDeadline(time.Now().Add(5 * time.Second))
			c := proto.NewConn(nc)
			for _, focus := range wanted {
				if err := c.Send(focus); err != nil {
					return err
				}
			}
			return c.Send(proto.StateMsg{State: model.State{Version: 99}})
		}()
	}()
	c, err := proto.Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b := newBackend(c, "first-session")
	if got := <-b.Focus(); got != (proto.FocusSession{WorkspaceID: "startup", SessionID: "first-session"}) {
		t.Fatalf("startup: %+v", got)
	}
	select {
	case got := <-b.Focus():
		t.Fatalf("startup repeats: %+v", got)
	default:
	}
	done := make(chan struct{})
	go func() { b.recvLoop(); close(done) }()
	// The newest request wins, so "first" may be dropped when "second" lands
	// before it is read; "second" always arrives, and nothing older after it.
	last := wanted[len(wanted)-1]
	for got := (proto.FocusSession{}); got != last; {
		select {
		case got = <-b.Focus():
			if got != wanted[0] && got != last {
				t.Fatalf("focus: %+v, want %+v or %+v", got, wanted[0], last)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("focus did not arrive")
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not stop")
	}
	select {
	case got := <-b.Focus():
		t.Fatalf("an older focus after the newest: %+v", got)
	default:
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if b.State().Version != 99 {
		t.Fatal("focus prevented state updates")
	}
	t.Setenv("PITWALL_ATTACH", "")
	select {
	case got := <-newBackend(nil, "").Focus():
		t.Fatalf("unexpected startup: %+v", got)
	default:
	}
}

// The window starts a daemon of its own binary, never pitwall-core: with
// nothing on PATH, dialOrStart still gets one.
func TestGUIStartsItsOwnDaemon(t *testing.T) {
	t.Setenv("PITWALL_SOCKET", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PITWALL_TEST_MAIN", "1") // the daemon it starts is fakeOldDaemon("healthy")
	t.Setenv("PATH", t.TempDir())
	path, err := proto.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	conn, initial, err := dialOrStart("", false)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	t.Cleanup(func() { stopIncompatibleDaemon(path) })
	if initial.State.Workspace("fake") == nil {
		t.Fatalf("not the test binary's daemon: %+v", initial.State)
	}
}

func TestShownOnlyActiveTabs(t *testing.T) {
	st := model.State{Workspaces: []model.Workspace{
		{SessionID: "s", ActiveTab: "a", Tabs: []model.Tab{{ID: "a", Layout: layout.Leaf("p1")}, {ID: "b", Layout: layout.Leaf("p2")}}},
		{SessionID: "s", Detached: true, ActiveTab: "c", Tabs: []model.Tab{{ID: "c", Layout: layout.Leaf("p3")}}},
		{SessionID: "other", ActiveTab: "d", Tabs: []model.Tab{{ID: "d", Layout: layout.Leaf("p4")}}},
	}}
	for pane, want := range map[string]bool{"p1": true, "p2": false, "p3": false, "p4": false, "gone": false} {
		if got := shown(&st, "s", pane); got != want {
			t.Errorf("shown(%s) = %v, want %v", pane, got, want)
		}
	}
}
