package main

import (
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
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestMain runs pitwall's main instead of the tests when a test re-executes
// this binary as pitwall.
func TestMain(m *testing.M) {
	if mode := os.Getenv("PITWALL_TEST_OLD"); mode != "" {
		if err := fakeOldDaemon(mode); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("PITWALL_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDaemonsRaceForLock(t *testing.T) {
	runDir, stateDir, home := t.TempDir(), t.TempDir(), t.TempDir()
	lockPath := filepath.Join(runDir, "pitwall", "pitwall.sock.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(strings.Repeat("9", 256)), 0o600); err != nil {
		t.Fatal(err)
	}
	exited := make(chan *exec.Cmd, 2)
	var cmds []*exec.Cmd
	for range 2 {
		cmd := exec.Command(os.Args[0], "daemon")
		cmd.Env = append(os.Environ(), "PITWALL_TEST_MAIN=1", "XDG_RUNTIME_DIR="+runDir, "XDG_STATE_HOME="+stateDir, "HOME="+home, "SHELL=/bin/sh")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill() })
		cmds = append(cmds, cmd)
		go func() { cmd.Wait(); exited <- cmd }()
	}

	var loser *exec.Cmd
	select {
	case loser = <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("both daemons kept running")
	}
	if code := loser.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("losing daemon exited %d, want 0", code)
	}
	winner := cmds[0]
	if winner == loser {
		winner = cmds[1]
	}

	var c *proto.Conn
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		c, err = proto.Dial(filepath.Join(runDir, "pitwall", "pitwall.sock"))
		if err == nil {
			break
		}
	}
	if c == nil {
		t.Fatalf("winner does not serve: %v", err)
	}
	defer c.Close()
	data, err := os.ReadFile(lockPath)
	if err != nil || string(data) != fmt.Sprintf("%d\n", winner.Process.Pid) {
		t.Fatalf("daemon did not truncate and write its pid: %q, %v", data, err)
	}
	if err := c.Send(proto.Hello{Version: proto.Version, Kind: "gui"}); err != nil {
		t.Fatal(err)
	}
	if m, err := c.Recv(); err != nil {
		t.Fatal(err)
	} else if _, ok := m.(proto.StateMsg); !ok {
		t.Fatalf("got %T, want StateMsg", m)
	}

	winner.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("winner did not stop on SIGTERM")
	}
	if code := winner.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("winner exited %d, want 0", code)
	}
}

// fakeOldDaemon holds the daemon lock until the test sends a signal.
func fakeOldDaemon(mode string) error {
	if mode == "stuck" {
		signal.Ignore(syscall.SIGTERM)
	}
	path, err := proto.SocketPath()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	if mode != "empty" {
		if _, err := fmt.Fprintf(lock, "%d\n", os.Getpid()); err != nil {
			return err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer ln.Close()
	fmt.Fprintln(os.Stdout, "ready")
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		if mode == "error" {
			conn := proto.NewConn(nc)
			if _, err := conn.Recv(); err != nil {
				conn.Close()
				return err
			}
			if err := conn.Send(proto.Frame{}); err != nil {
				conn.Close()
				return err
			}
			if err := conn.Send(proto.Error{Message: "protocol version mismatch"}); err != nil {
				conn.Close()
				return err
			}
		} else {
			var header [4]byte
			if _, err := io.ReadFull(nc, header[:]); err != nil {
				nc.Close()
				return err
			}
			if mode == "silent" {
				io.Copy(io.Discard, nc)
			}
		}
		nc.Close()
	}
}

func TestDialOrStartRestartsOldDaemon(t *testing.T) {
	for _, mode := range []string{"raw", "error", "silent", "empty", "stuck"} {
		t.Run(mode, func(t *testing.T) {
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
			conn, initial, err := dialOrStart()
			if mode == "empty" || mode == "stuck" {
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
		if !ok || hello.Version != proto.Version || hello.Kind != "gui" || hello.Cwd != cwd() {
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
	conn, initial, err := dialOrStart()
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
