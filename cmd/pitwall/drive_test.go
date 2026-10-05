package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		// The pane's agent is gone with it, but it was an agent: exited, not a pass.
		{"agent gone done", []string{"--until", "done"}, []any{driveState(claude, model.StateWorking, false, 0), gone, proto.PaneExited{Pane: "p", ExitCode: 0}}, 3, "exit 0\n"},
		{"timeout before state", []string{"--until", "done", "--timeout", "50ms"}, nil, 124, ""},
		// A shell is no agent sitting idle.
		{"shell never idle", []string{"--until", "idle", "--timeout", "50ms"}, []any{driveState("", "", false, 0)}, 124, ""},
		{"stale done", []string{"--until", "done"}, []any{sentAfterDone(false), sentAfterDone(true)}, 0, "done\n"},
		{"stale done only", []string{"--until", "done", "--timeout", "50ms"}, []any{sentAfterDone(false)}, 124, ""},
		// Ready for input after a --no-enter send: freshness is for done only.
		{"idle after send", []string{"--until", "idle", "--timeout", "1s"}, []any{sentAfterDone(false)}, 0, "done\n"},
		// An OSC question before pitwall has seen the agent still blocks.
		{"notice before agent", []string{"--until", "done"}, []any{noticeBeforeAgent()}, 2, "blocked: approve?\n"},
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

// noticeBeforeAgent is tab build started with claude, not yet seen running,
// with an OSC notification asking for input.
func noticeBeforeAgent() proto.StateMsg {
	m := driveState("", "", false, 0)
	m.State.Panes[0].Cmd = []string{"claude"}
	m.State.Activities = []model.Activity{{PaneID: "p", WorkspaceID: "w", Provider: model.ProviderTerminal, State: model.StateAwaitingInput, Detail: "approve?"}}
	return m
}

// sentAfterDone is tab build's claude done with turn 1 while its last send
// submitted turn 2, or, with fresh, done after turn 2 ran. The stale done is
// newer by the clock: only the turn count tells them apart.
func sentAfterDone(fresh bool) proto.StateMsg {
	m := driveState(model.ProviderClaude, model.StateCompleted, false, 0)
	m.State.Activities[0].UpdatedAt = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.State.Panes[0].Turns, m.State.Panes[0].SentTurn = 1, 2
	if fresh {
		m.State.Panes[0].Turns = 2
	}
	return m
}

func TestCLIListJSON(t *testing.T) {
	st := driveState(model.ProviderCodex, model.StatePendingApproval, false, 0).State
	fakeCLI(t, cliExchange{state: st})
	code, out, stderr := cliOutput("ls", "--json")
	want := `[{"n":1,"id":"w","title":"build","group":"agents","cwd":"/work/app/src","branch":"main","agent":"codex","state":"blocked","question":"Run make?","exit_code":null,"panes":1,"detached":false}]` + "\n"
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
		{model.Pane{ID: "p"}, "", "", "running"},
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

// A relative command path is the caller's, not one in the tab's folder.
func TestCLINewRelativeCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	t.Chdir(dir)
	before, after := cliState(), cliState()
	after.Workspaces = append(after.Workspaces, model.Workspace{ID: "new", SessionID: "m"})
	fakeCLI(t, cliExchange{state: before}, cliExchange{request: proto.NewSession{Cwd: other, SessionID: "m", Cmd: []string{filepath.Join(dir, "tool"), "-v"}}, state: after})
	if code, out, stderr := cliOutput("new", other, "--", "./tool", "-v"); code != 0 || out != "#3\n" {
		t.Fatalf("%d: %s %s", code, out, stderr)
	}
}

// fakeSendDaemon serves send's two connections: a watch that gets state,
// then, after the Send on the cli connection, nothing (or a hang-up with
// hangUp).
func fakeSendDaemon(t *testing.T, state model.State, hangUp bool) {
	t.Helper()
	t.Setenv("PITWALL_PANE", "")
	path := filepath.Join(t.TempDir(), "send.sock")
	t.Setenv("PITWALL_SOCKET", path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			wc, err := ln.Accept()
			if err != nil {
				return err
			}
			defer wc.Close()
			watch := proto.NewConn(wc)
			if _, err := watch.Recv(); err != nil {
				return err
			}
			if err := watch.Send(proto.StateMsg{State: state}); err != nil {
				return err
			}
			cc, err := ln.Accept()
			if err != nil {
				return err
			}
			defer cc.Close()
			cli := proto.NewConn(cc)
			for range 3 { // Hello, Send, Sync
				if _, err := cli.Recv(); err != nil {
					return err
				}
			}
			if err := cli.Send(proto.StateMsg{State: state}); err != nil {
				return err
			}
			if hangUp {
				wc.Close()
			}
			watch.Recv() // until send hangs up
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

func TestCLISendUnconfirmed(t *testing.T) {
	sendSettle = 50 * time.Millisecond
	t.Cleanup(func() { sendSettle = 10 * time.Second })
	st := driveState(model.ProviderClaude, model.StateCompleted, false, 0).State
	t.Run("timeout warns", func(t *testing.T) {
		fakeSendDaemon(t, st, false)
		if code, _, stderr := cliOutput("send", "build", "go on"); code != 0 || !strings.Contains(stderr, "didn't change") {
			t.Fatalf("%d %q", code, stderr)
		}
	})
	t.Run("watch error fails", func(t *testing.T) {
		fakeSendDaemon(t, st, true)
		if code, _, stderr := cliOutput("send", "build", "go on"); code != 1 {
			t.Fatalf("%d %q", code, stderr)
		}
	})
}

// A live pane beats an exited one, and an agent beats a shell among each.
func TestMainPane(t *testing.T) {
	deadClaude := model.Pane{ID: "dead", Provider: model.ProviderClaude, Exited: true}
	shell := model.Pane{ID: "shell"}
	codex := model.Pane{ID: "codex", Provider: model.ProviderCodex}
	deadShell := model.Pane{ID: "deadshell", Exited: true}
	for _, tc := range []struct {
		panes []model.Pane
		want  string
	}{
		{[]model.Pane{deadClaude, shell, codex}, "codex"},
		{[]model.Pane{deadClaude, shell}, "shell"},
		{[]model.Pane{deadShell, deadClaude}, "dead"},
		{[]model.Pane{deadShell}, "deadshell"},
	} {
		if got := mainPane(tc.panes); got == nil || got.ID != tc.want {
			t.Errorf("%v: %v, want %s", tc.panes, got, tc.want)
		}
	}
	if mainPane(nil) != nil {
		t.Error("no panes, a pane")
	}
}
