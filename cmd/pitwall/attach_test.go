package main

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestCLIAttach(t *testing.T) {
	for _, held := range []bool{false, true} {
		t.Run(fmt.Sprint(held), func(t *testing.T) {
			fakeCLI(t, cliExchange{state: cliState()}, cliExchange{request: proto.FocusSession{WorkspaceID: "b"}, state: cliState()})
			t.Setenv("PITWALL_PANE", "pb")
			lock, err := guiLock()
			if err != nil || lock == nil {
				t.Fatalf("lock: %v", err)
			}
			defer lock.Close()
			if !held {
				lock.Close()
			}
			previous := launchGUI
			var launched []string
			launchGUI = func(id string) error { launched = append(launched, id); return nil }
			t.Cleanup(func() { launchGUI = previous })
			args := []string{"attach"}
			if held {
				args = append(args, "alpi")
			}
			if code, out, stderr := cliOutput(args...); code != 0 || out != "" || stderr != "" {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
			if held && len(launched) != 0 {
				t.Fatal("launched despite a held GUI lock")
			}
			if !held && (len(launched) != 1 || launched[0] != "b") {
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
			launchGUI = func(string) error { launched = true; return errors.New("cannot launch") }
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

func TestGUILock(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	first, err := guiLock()
	if err != nil || first == nil {
		t.Fatalf("first: %v", err)
	}
	defer first.Close()
	second, err := guiLock()
	if err != nil || second != nil {
		t.Fatalf("second: %v, %v", second, err)
	}
	// A duplicate GUI exits before dialing or creating a window.
	t.Setenv("PITWALL_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	if err := runGUI(); err != nil {
		t.Fatal(err)
	}
	first.Close()
	third, err := guiLock()
	if err != nil || third == nil {
		t.Fatalf("released: %v", err)
	}
	third.Close()
}

func TestBackendFocus(t *testing.T) {
	t.Setenv("PITWALL_ATTACH", "startup")
	path := filepath.Join(t.TempDir(), "gui.sock")
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
	b := newBackend(c)
	if got := <-b.Focus(); got != (proto.FocusSession{WorkspaceID: "startup"}) {
		t.Fatalf("startup: %+v", got)
	}
	select {
	case got := <-b.Focus():
		t.Fatalf("startup repeats: %+v", got)
	default:
	}
	done := make(chan struct{})
	go func() { b.recvLoop(); close(done) }()
	for _, want := range wanted {
		select {
		case got := <-b.Focus():
			if got != want {
				t.Fatalf("focus: %+v, want %+v", got, want)
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
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if b.State().Version != 99 {
		t.Fatal("focus prevented state updates")
	}
	t.Setenv("PITWALL_ATTACH", "")
	select {
	case got := <-newBackend(nil).Focus():
		t.Fatalf("unexpected startup: %+v", got)
	default:
	}
}
