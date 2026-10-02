package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

type cliExchange struct {
	request any
	state   model.State
	error   string
}

// fakeCLI checks request ordering over the same unix transport as the daemon.
func fakeCLI(t *testing.T, exchanges ...cliExchange) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("PITWALL_PANE", "")
	path := filepath.Join(t.TempDir(), "cli.sock")
	t.Setenv("PITWALL_SOCKET", path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			nc, err := ln.Accept()
			if err != nil {
				return err
			}
			defer nc.Close()
			nc.SetDeadline(time.Now().Add(5 * time.Second))
			c := proto.NewConn(nc)
			want := proto.Hello{Version: proto.Version, Kind: "cli"}
			if got, err := c.Recv(); err != nil || got != want {
				return fmt.Errorf("Hello = %#v, %v", got, err)
			}
			for _, exchange := range exchanges {
				if exchange.request != nil {
					if got, err := c.Recv(); err != nil || !reflect.DeepEqual(got, exchange.request) {
						return fmt.Errorf("request = %#v, %v; want %#v", got, err, exchange.request)
					}
				}
				if got, err := c.Recv(); err != nil || got != (proto.Sync{}) {
					return fmt.Errorf("Sync = %#v, %v", got, err)
				}
				if exchange.error != "" {
					if err := c.Send(proto.Error{Message: exchange.error}); err != nil {
						return err
					}
				}
				if err := c.Send(proto.StateMsg{State: exchange.state}); err != nil {
					return err
				}
			}
			return nil
		}()
	}()
	t.Cleanup(func() {
		ln.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

func cliOutput(args ...string) (int, string, string) {
	var out, stderr bytes.Buffer
	code := runCLI(args, nil, &out, &stderr)
	return code, out.String(), stderr.String()
}

func cliState() model.State {
	return model.State{
		Projects: []model.Project{{ID: "g", Name: "agents"}},
		Workspaces: []model.Workspace{
			{ID: "a", Name: "alpha", Path: "/work/a", ProjectID: "g", Tabs: []model.Tab{{ID: "ta"}, {ID: "tb"}}},
			{ID: "b", Name: "alpine", Path: "/work/b", Detached: true, Tabs: []model.Tab{{ID: "tc"}}},
		},
		Panes: []model.Pane{{ID: "pa", WorkspaceID: "a"}, {ID: "pb", WorkspaceID: "b"}},
		Activities: []model.Activity{
			{WorkspaceID: "a", Provider: model.ProviderClaude, State: model.StateWorking},
			{WorkspaceID: "a", Provider: model.ProviderCodex, State: model.StateError},
		},
	}
}

func TestCLIList(t *testing.T) {
	for _, format := range []string{"table", "json", "empty json"} {
		t.Run(format, func(t *testing.T) {
			state := cliState()
			if format == "empty json" {
				state = model.State{}
			}
			fakeCLI(t, cliExchange{state: state})
			if format == "table" {
				state.Workspaces[0].Path = filepath.Join(os.Getenv("HOME"), "src")
				code, out, stderr := cliOutput("ls")
				if code != 0 || stderr != "" {
					t.Fatalf("%d: %s", code, stderr)
				}
				lines := strings.Split(strings.TrimSpace(out), "\n")
				if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "alpha Agent Error 2 ~/src agents" ||
					strings.Join(strings.Fields(lines[1]), " ") != "alpine idle 1 /work/b - (detached)" {
					t.Fatal(out)
				}
			} else {
				code, out, stderr := cliOutput("ls", "--json")
				var got []model.Workspace
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				if code != 0 || stderr != "" || (format == "json" && !reflect.DeepEqual(got, state.Workspaces)) || got == nil {
					t.Fatalf("%d: %s %s", code, out, stderr)
				}
			}
		})
	}
}

func TestCLINew(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(fmt.Sprint(detached), func(t *testing.T) {
			before := cliState()
			after := cliState()
			after.Workspaces = append(after.Workspaces, model.Workspace{ID: "new", Name: "build"})
			dir := t.TempDir()
			t.Chdir(dir)
			wantDir := filepath.Join(dir, "child")
			args := []string{"new", "-n", "build", "child"}
			exchanges := []cliExchange{{state: before}, {request: proto.NewSession{Name: "build", Cwd: wantDir}, state: after}}
			if detached {
				args = []string{"new", "-n", "build", "-d"}
				exchanges[1].request = proto.NewSession{Name: "build", Cwd: dir}
				exchanges = append(exchanges, cliExchange{request: proto.DetachSession{WorkspaceID: "new", Detached: true}, state: after})
			}
			fakeCLI(t, exchanges...)
			if code, out, stderr := cliOutput(args...); code != 0 || out != "build\n" || stderr != "" {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
		})
	}
}

func TestCLIResolve(t *testing.T) {
	state := cliState()
	t.Setenv("PITWALL_PANE", "pb")
	for _, tc := range []struct{ name, want, error string }{
		{"alpha", "a", ""}, {"alph", "a", ""}, {"", "b", ""},
		{"al", "", "ambiguous session \"al\"; candidates: alpha, alpine"},
		{"absent", "", "no session matches \"absent\"; candidates: alpha, alpine"},
	} {
		w, err := resolveSession(state, tc.name)
		if tc.error != "" {
			if err == nil || err.Error() != tc.error {
				t.Fatalf("%q: %v", tc.name, err)
			}
		} else if err != nil || w.ID != tc.want {
			t.Fatalf("%q: %+v, %v", tc.name, w, err)
		}
	}
	state.Workspaces = append(state.Workspaces, model.Workspace{ID: "exact", Name: "al"})
	if w, err := resolveSession(state, "al"); err != nil || w.ID != "exact" {
		t.Fatalf("exact: %+v %v", w, err)
	}
	t.Setenv("PITWALL_PANE", "")
	if _, err := resolveSession(state, ""); err == nil {
		t.Fatal("resolved no name outside pane")
	}
}

func TestCLISessionMutations(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		request any
		pane    string
	}{
		{[]string{"detach", "alph"}, proto.DetachSession{WorkspaceID: "a", Detached: true}, ""},
		{[]string{"detach"}, proto.DetachSession{WorkspaceID: "b", Detached: true}, "pb"},
		{[]string{"rename", "alpha", "renamed"}, proto.RenameWorkspace{WorkspaceID: "a", Name: "renamed"}, ""},
		{[]string{"rename", "renamed"}, proto.RenameWorkspace{WorkspaceID: "b", Name: "renamed"}, "pb"},
		{[]string{"kill", "-f", "alpha"}, proto.KillSession{WorkspaceID: "a"}, ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			fakeCLI(t, cliExchange{state: cliState()}, cliExchange{request: tc.request, state: cliState()})
			t.Setenv("PITWALL_PANE", tc.pane)
			if code, out, stderr := cliOutput(tc.args...); code != 0 || out != "" || stderr != "" {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
		})
	}
}

func TestCLINotRunning(t *testing.T) {
	t.Setenv("PITWALL_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	for _, args := range [][]string{{"ls"}, {"ls", "--json"}, {"new"}, {"detach", "alpha"}, {"kill", "-f", "alpha"}} {
		code, out, stderr := cliOutput(args...)
		if args[0] == "ls" {
			if code != 0 || out != "" || stderr != "" {
				t.Fatalf("%v: %d %s %s", args, code, out, stderr)
			}
		} else if code != 1 || !strings.Contains(stderr, "pitwall is not running") {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
	}
}

func TestCLIDaemonError(t *testing.T) {
	fakeCLI(t, cliExchange{state: cliState()}, cliExchange{request: proto.KillSession{WorkspaceID: "a"}, state: cliState(), error: "cannot kill"})
	if code, out, stderr := cliOutput("kill", "-f", "alpha"); code != 1 || out != "" || !strings.Contains(stderr, "cannot kill") {
		t.Fatalf("%d: %s %s", code, out, stderr)
	}
}

func TestCLIDaemonErrorExit(t *testing.T) {
	fakeCLI(t, cliExchange{error: "bad version"})
	cmd := exec.Command(os.Args[0], "ls")
	cmd.Env = append(os.Environ(), "PITWALL_TEST_MAIN=1")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "bad version") {
		t.Fatalf("%v: %s %s", err, out.String(), stderr.String())
	}
}

func TestCLIConfirmKill(t *testing.T) {
	for _, answer := range []string{"y\n", "\n", "n\n"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			exchanges := []cliExchange{{state: cliState()}}
			if answer == "y\n" {
				exchanges = append(exchanges, cliExchange{request: proto.KillSession{WorkspaceID: "a"}, state: cliState()})
			}
			fakeCLI(t, exchanges...)
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if _, err := master.Write([]byte(answer)); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			if code := runCLI([]string{"kill", "alpha"}, slave, &out, &stderr); code != 0 || out.Len() != 0 || stderr.String() != "kill alpha? [y/N] " {
				t.Fatalf("%d: %s %s", code, out.String(), stderr.String())
			}
		})
	}
}
