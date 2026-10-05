package main

import (
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// fakeWatch serves one watch connection that receives msgs, then stays open
// until the client hangs up.
func fakeWatch(t *testing.T, msgs ...any) {
	t.Helper()
	t.Setenv("PITWALL_PANE", "")
	path := filepath.Join(t.TempDir(), "watch.sock")
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
			c := proto.NewConn(nc)
			if got, err := c.Recv(); err != nil || got != (proto.Hello{Version: proto.Version, Kind: "watch"}) {
				return fmt.Errorf("Hello = %#v, %v", got, err)
			}
			for _, m := range msgs {
				if err := c.Send(m); err != nil {
					return err
				}
			}
			c.Recv() // until the client hangs up
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

// driveState is one session with tab "build" (#1): pane p, running provider
// (none for a shell) in state, or exited with code.
func driveState(provider model.Provider, state model.AgentState, exited bool, code int) proto.StateMsg {
	s := model.State{
		Sessions:   []model.Session{{ID: "m", Name: "main", Order: []string{"w"}}},
		Projects:   []model.Project{{ID: "g", SessionID: "m", Name: "agents"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "m", Name: "build", NameSet: true, ProjectID: "g", Path: "/work/app", Branch: "main", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "p"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: "/work/app/src", Provider: provider, Exited: exited, ExitCode: code}},
	}
	if state != "" {
		s.Activities = []model.Activity{{PaneID: "p", WorkspaceID: "w", Provider: provider, State: state, Detail: "Run make?"}}
	}
	return proto.StateMsg{State: s}
}

func TestCLIWait(t *testing.T) {
	claude := model.ProviderClaude
	gone := driveState(claude, model.StateWorking, false, 0)
	gone.State.Panes = nil
	for _, tc := range []struct {
		name string
		args []string
		msgs []any
		code int
		out  string
	}{
		{"done", []string{"--until", "done"}, []any{driveState(claude, model.StateWorking, false, 0), driveState(claude, model.StateCompleted, false, 0)}, 0, "done\n"},
		{"done already", []string{"--until", "done"}, []any{driveState(claude, model.StateError, false, 0)}, 0, "done\n"},
		{"idle takes done", []string{"--until", "idle"}, []any{driveState(claude, model.StateCompleted, false, 0)}, 0, "done\n"},
		{"interrupted", []string{"--until", "done"}, []any{driveState(claude, model.StateWorking, false, 0), driveState(claude, "", false, 0)}, 0, "idle\n"},
		{"not started yet", []string{"--until", "done", "--timeout", "50ms"}, []any{driveState(claude, "", false, 0)}, 124, ""},
		{"blocked", []string{"--until", "done"}, []any{driveState(claude, model.StateWorking, false, 0), driveState(claude, model.StatePendingApproval, false, 0)}, 2, "blocked: Run make?\n"},
		{"until blocked", []string{"--until", "blocked"}, []any{driveState(claude, model.StateAwaitingInput, false, 0)}, 0, "blocked\n"},
		{"agent exited", []string{"--until", "done"}, []any{driveState(claude, model.StateWorking, false, 0), driveState(claude, "", true, 7)}, 3, "exit 7\n"},
		{"agent gone", []string{"--until", "exit"}, []any{driveState(claude, model.StateWorking, false, 0), gone, proto.PaneExited{Pane: "p", ExitCode: 5}}, 5, "exit 5\n"},
		{"shell exit", []string{"--until", "exit"}, []any{driveState("", model.StateTerminalRunning, false, 0), driveState("", "", true, 7)}, 7, "exit 7\n"},
		{"shell done is exit", []string{"--until", "done"}, []any{driveState("", model.StateTerminalRunning, false, 0), driveState("", "", true, 0)}, 0, "exit 0\n"},
		{"timeout", []string{"--until", "done", "--timeout", "50ms"}, []any{driveState(claude, model.StateWorking, false, 0)}, 124, ""},
		{"bad until", []string{"--until", "later"}, nil, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.code != 1 {
				fakeWatch(t, tc.msgs...)
			}
			// The tab first, flags after it, as scripts write it.
			code, out, stderr := cliOutput(append([]string{"wait", "build"}, tc.args...)...)
			if code != tc.code || out != tc.out {
				t.Fatalf("code %d out %q stderr %q; want %d %q", code, out, stderr, tc.code, tc.out)
			}
		})
	}
}

func TestCLIListJSON(t *testing.T) {
	st := driveState(model.ProviderCodex, model.StatePendingApproval, false, 0).State
	fakeCLI(t, cliExchange{state: st})
	code, out, stderr := cliOutput("ls", "--json")
	want := `[{"n":1,"id":"w","title":"build","group":"agents","cwd":"/work/app/src","branch":"main","agent":"codex","state":"blocked","question":"Run make?","exit_code":0,"panes":1,"detached":false}]` + "\n"
	if code != 0 || out != want || stderr != "" {
		t.Fatalf("%d %s\n%s\nwant %s", code, stderr, out, want)
	}
}

func TestPaneState(t *testing.T) {
	for _, tc := range []struct {
		pane  model.Pane
		state model.AgentState
		agent string
		want  string
	}{
		{model.Pane{ID: "p"}, "", "", "idle"},
		{model.Pane{ID: "p"}, model.StateTerminalRunning, "", "working"},
		{model.Pane{ID: "p", Cmd: []string{"/usr/bin/claude", "fix it"}}, "", "claude", ""},
		{model.Pane{ID: "p", Cmd: []string{"claude"}, Provider: model.ProviderClaude}, "", "claude", "idle"},
		{model.Pane{ID: "p", Provider: model.ProviderCodex}, model.StateConnecting, "codex", "working"},
		{model.Pane{ID: "p", Provider: model.ProviderCodex}, model.StatePlanReady, "codex", "blocked"},
		{model.Pane{ID: "p", Provider: model.ProviderCodex}, model.StateCompleted, "codex", "done"},
		{model.Pane{ID: "p", Provider: model.ProviderCodex, Exited: true}, model.StateWorking, "codex", "exited"},
	} {
		st := model.State{Panes: []model.Pane{tc.pane}}
		if tc.state != "" {
			st.Activities = []model.Activity{{PaneID: "p", State: tc.state}}
		}
		if agent, got, _ := paneState(st, tc.pane); agent != tc.agent || got != tc.want {
			t.Errorf("%+v %s: %q %q, want %q %q", tc.pane, tc.state, agent, got, tc.agent, tc.want)
		}
	}
}

func TestCLINewCommand(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	before, after := cliState(), cliState()
	after.Workspaces = append(after.Workspaces, model.Workspace{ID: "new", SessionID: "m"})
	dir := t.TempDir()
	t.Chdir(dir)
	fakeCLI(t, cliExchange{state: before}, cliExchange{request: proto.NewSession{Cwd: dir, SessionID: "m", Cmd: []string{sh, "-c", "go test -v"}}, state: after})
	if code, out, stderr := cliOutput("new", "--", "sh", "-c", "go test -v"); code != 0 || out != "#3\n" || stderr != "" {
		t.Fatalf("%d: %s %s", code, out, stderr)
	}
	if code, _, stderr := cliOutput("new", "--"); code != 1 || !strings.Contains(stderr, "usage") {
		t.Fatalf("new with an empty command: %d %s", code, stderr)
	}
}
