//go:build nogui

package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// setWindowSize does nothing: pitwall-core has no window.
func setWindowSize(string) {}

// serveDrill is drill_test.go's, which builds without nogui only.
func serveDrill(string) int { return 2 }

// coreDaemon is a fake daemon on PITWALL_SOCKET that answers every Sync
// with st, or after once the CLI sent a request. requests returns what the
// CLI sent besides its Hello and Syncs, once the CLI has closed the
// connection.
func coreDaemon(t *testing.T, st, after model.State) (requests func() []any) {
	t.Helper()
	setHome(t, t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("PITWALL_PANE", "")
	path := sockPath(t, "cli.sock")
	t.Setenv("PITWALL_SOCKET", path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan []any, 1)
	go func() {
		var sent []any
		defer func() { got <- sent }()
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		nc.SetDeadline(time.Now().Add(5 * time.Second))
		c := proto.NewConn(nc)
		if _, err := c.Recv(); err != nil { // Hello
			return
		}
		for {
			m, err := c.Recv()
			if err != nil {
				return
			}
			if _, ok := m.(proto.Sync); !ok {
				sent, st = append(sent, m), after
				continue
			}
			if c.Send(proto.StateMsg{State: st}) != nil {
				return
			}
		}
	}()
	return func() []any {
		select {
		case sent := <-got:
			return sent
		case <-time.After(5 * time.Second):
			t.Fatal("the CLI kept its connection open")
			return nil
		}
	}
}

// pitwall-core refuses the commands that need a window it cannot open
// before it sends the daemon anything, and keeps the ones that work
// without one: session new -d, and attach to a session a window shows.
func TestCoreNoWindow(t *testing.T) {
	t.Run("no daemon", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
		t.Setenv("PITWALL_SOCKET", "")
		t.Setenv("PITWALL_PANE", "")
		if code, _, stderr := cliOutput("attach", "1"); code != 1 || stderr != "pitwall: "+errNotRunning.Error()+"\n" {
			t.Fatalf("%d %q", code, stderr)
		}
	})
	shown := cliState()
	shown.Sessions[0].Windows = 1 // main shows; api does not
	for _, tc := range []struct {
		args   []string
		hidden bool // no window shows main either
		code   int
		out    string
		stderr string
		sent   []any
	}{
		{args: []string{"session", "new", "-d", "docs"}, out: "docs\n", sent: []any{proto.SessionNew{Name: "docs"}}},
		{args: []string{"session", "new", "docs"}, code: 2, stderr: "pitwall: pitwall-core has no window; add -d to create the session detached\n"},
		{args: []string{"session", "attach", "main"}, sent: []any{proto.FocusSession{SessionID: "m"}}},
		{args: []string{"attach", "1"}, sent: []any{proto.FocusSession{WorkspaceID: "a"}}},
		{args: []string{"session", "attach", "api"}, code: 1, stderr: "pitwall: no window shows session api, and pitwall-core cannot open one; open it with the desktop pitwall\n"},
		{args: []string{"attach", "1"}, hidden: true, code: 1, stderr: "pitwall: no window shows session main, and pitwall-core cannot open one; open it with the desktop pitwall\n"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			st := shown
			if tc.hidden {
				st = cliState()
			}
			after := st
			if len(tc.sent) > 0 {
				if _, ok := tc.sent[0].(proto.SessionNew); ok {
					after = cliState()
					after.Sessions = append(after.Sessions, model.Session{ID: "s3", Name: "docs"})
				}
			}
			requests := coreDaemon(t, st, after)
			t.Chdir(t.TempDir())
			previous := launchGUI
			launched := false
			launchGUI = func(string, string) error { launched = true; return nil }
			t.Cleanup(func() { launchGUI = previous })
			code, out, stderr := cliOutput(tc.args...)
			if code != tc.code || out != tc.out || stderr != tc.stderr || launched {
				t.Fatalf("%d %q %q, launched %v", code, out, stderr, launched)
			}
			if c, err := net.Dial("unix", os.Getenv("PITWALL_SOCKET")); err == nil {
				c.Close() // ends the fake daemon when the CLI never dialed
			}
			sent := requests()
			for i, m := range sent {
				if n, ok := m.(proto.SessionNew); ok {
					n.Cwd = "" // the temp folder
					sent[i] = n
				}
			}
			if !reflect.DeepEqual(sent, tc.sent) {
				t.Fatalf("sent %#v, want %#v", sent, tc.sent)
			}
		})
	}
}

// pitwall-core's update refuses before it asks for a release: the one it
// would install is the desktop binary.
func TestCoreUpdateRefuses(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked++ }))
	defer srv.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "update")
	cmd.Env = append(os.Environ(), "PITWALL_TEST_MAIN=1", "PITWALL_UPDATE_URL="+srv.URL)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	err = cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 ||
		stderr.String() != "pitwall: pitwall-core update is not available in this build; reinstall with get.sh --core\n" {
		t.Fatalf("%v: %q", err, stderr.String())
	}
	after, err := os.Stat(exe)
	if err != nil || asked != 0 || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("asked %d times; binary %v -> %v, %v", asked, before, after, err)
	}
}

// pitwall-core says so in --version after the version, and with no
// command, -s or --host, which open a window, it exits 2.
func TestCoreMain(t *testing.T) {
	run := func(args ...string) (int, string, string) {
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), "PITWALL_TEST_MAIN=1")
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		err := cmd.Run()
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), out.String(), stderr.String()
		} else if err != nil {
			t.Fatal(err)
		}
		return 0, out.String(), stderr.String()
	}
	if code, out, _ := run("--version"); code != 0 || out != "pitwall "+versionString()+" (core)\n" {
		t.Fatalf("--version: %d %q", code, out)
	}
	for _, args := range [][]string{{}, {"-s", "work"}, {"--host", "box"}} {
		if code, out, stderr := run(args...); code != 2 || out != "" || !strings.HasPrefix(stderr, "pitwall: pitwall-core has no window") {
			t.Fatalf("%v: %d %q %q", args, code, out, stderr)
		}
	}
}
