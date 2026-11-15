package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

type cliExchange struct {
	request any
	state   model.State
	error   string
	level   int  // the daemon's proto.Level in its StateMsg
	drop    bool // close the connection after the request, unanswered
}

// setHome makes dir the home folder, and on Windows the profile whose
// AppData holds pitwall's state and config without XDG variables.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("APPDATA", filepath.Join(dir, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "AppData", "Local"))
}

// sockPath is a socket path in a short new directory: a subtest's
// t.TempDir can pass the 104 bytes macOS and the 108 Windows allow.
func sockPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

// fakeCLI checks request ordering over the same unix transport as the daemon.
func fakeCLI(t *testing.T, exchanges ...cliExchange) {
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
	done := make(chan error, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		done <- serveCLI(nc, exchanges)
	}()
	t.Cleanup(func() {
		ln.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

// serveCLI is a fake daemon on nc: it takes a CLI's Hello, then answers
// each of exchanges in turn.
func serveCLI(nc net.Conn, exchanges []cliExchange) error {
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	c := proto.NewConn(nc)
	want := proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "cli"}
	if got, err := c.Recv(); err != nil || got != want {
		return fmt.Errorf("Hello = %#v, %v", got, err)
	}
	for _, exchange := range exchanges {
		if exchange.request != nil {
			got, err := c.Recv()
			matches := reflect.DeepEqual(got, exchange.request)
			if check, ok := exchange.request.(func(any) bool); ok && err == nil {
				matches = check(got)
			}
			if err != nil || !matches {
				return fmt.Errorf("request = %#v, %v; want %#v", got, err, exchange.request)
			}
		}
		if exchange.drop {
			return nil
		}
		if got, err := c.Recv(); err != nil || got != (proto.Sync{}) {
			return fmt.Errorf("Sync = %#v, %v", got, err)
		}
		if exchange.error != "" {
			if err := c.Send(proto.Error{Message: exchange.error}); err != nil {
				return err
			}
		}
		if err := c.Send(proto.StateMsg{State: exchange.state, Level: exchange.level}); err != nil {
			return err
		}
	}
	return nil
}

// fakeLocal is a CLI connection to a fake daemon that answers exchanges,
// for the calls that talk to this machine's daemon and a host's at once.
func fakeLocal(t *testing.T, exchanges ...cliExchange) *cliConn {
	t.Helper()
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveCLI(b, exchanges) }()
	c, err := helloCLI(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return c
}

func cliOutput(args ...string) (int, string, string) {
	var out, stderr bytes.Buffer
	code := runCLI(args, nil, &out, &stderr)
	return code, out.String(), stderr.String()
}

// cliState has two sessions: main, the most recently used, with three tabs
// and a group, and api with one tab.
func cliState() model.State {
	return model.State{
		Sessions: []model.Session{
			{ID: "m", Name: "main", Order: []string{"g", "c"}, UsedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
			{ID: "s2", Name: "api", UsedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
		Projects: []model.Project{{ID: "g", SessionID: "m", Name: "agents"}},
		Workspaces: []model.Workspace{
			{ID: "a", SessionID: "m", Label: "fix login", Path: "/work/a", ProjectID: "g", Tabs: []model.Tab{{ID: "ta"}}},
			{ID: "b", SessionID: "m", Name: "alpine", NameSet: true, Label: "alpine", Path: "/work/b", Detached: true, Tabs: []model.Tab{{ID: "tc"}}},
			{ID: "c", SessionID: "m", Label: "~", Path: "/work/c"},
			{ID: "z", SessionID: "s2", Label: "server", Path: "/work/z"},
		},
		Panes: []model.Pane{{ID: "pa", WorkspaceID: "a"}, {ID: "pb", WorkspaceID: "b"}, {ID: "pz", WorkspaceID: "z"}},
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
				state.Activities[1].UpdatedAt = time.Now().Add(-5*time.Minute - time.Second)
				code, out, stderr := cliOutput("ls")
				if code != 0 || stderr != "" {
					t.Fatalf("%d: %s", code, stderr)
				}
				lines := strings.Split(strings.TrimSpace(out), "\n")
				// Numbered in sidebar order (group g above tab c), detached last.
				if len(lines) != 4 || strings.Join(strings.Fields(lines[0]), " ") != "# NAME STATE SINCE FOLDER GROUP" ||
					strings.Join(strings.Fields(lines[1]), " ") != "1 fix login Error 5m ago "+filepath.Join("~", "src")+" agents" ||
					strings.Join(strings.Fields(lines[2]), " ") != "2 ~ idle - /work/c -" ||
					strings.Join(strings.Fields(lines[3]), " ") != "3 alpine idle - /work/b - (detached)" {
					t.Fatal(out)
				}
			} else {
				code, out, stderr := cliOutput("ls", "--json")
				var got []map[string]any
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				if code != 0 || stderr != "" || got == nil || (format == "json" && len(got) != 3) {
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
			after.Workspaces = append(after.Workspaces, model.Workspace{ID: "new", SessionID: "m", Name: "build", NameSet: true})
			dir := t.TempDir()
			t.Chdir(dir)
			wantDir := filepath.Join(dir, "child")
			args := []string{"new", "-n", "build", "child"}
			exchanges := []cliExchange{{state: before}, {request: proto.NewSession{Name: "build", Cwd: wantDir, SessionID: "m"}, state: after}}
			if detached {
				args = []string{"new", "-n", "build", "-d"}
				exchanges[1].request = proto.NewSession{Name: "build", Cwd: dir, SessionID: "m"}
				exchanges = append(exchanges, cliExchange{request: proto.DetachSession{WorkspaceID: "new", Detached: true}, state: after})
			}
			fakeCLI(t, exchanges...)
			want := "#3\n" // after fix login and ~, before the detached alpine
			if code, out, stderr := cliOutput(args...); code != 0 || out != want || stderr != "" {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
		})
	}
}

func TestCLIResolve(t *testing.T) {
	state := cliState()
	t.Setenv("PITWALL_PANE", "pb")
	for _, tc := range []struct{ name, want, error string }{
		{"1", "a", ""}, {"#2", "c", ""}, {"3", "b", ""}, {"", "b", ""},
		{"fix login", "a", ""}, {"fix", "a", ""}, {"alp", "b", ""},
		{"4", "", "no tab #4 (see pitwall ls)"},
		{"absent", "", "no tab matches \"absent\" (see pitwall ls)"},
		{"@a", "a", ""}, {"@pb", "b", ""},
		{"@gone", "", "no pane @gone: it closed (see pitwall ls --json)"},
	} {
		w, err := resolveTab(state, "m", tc.name)
		if tc.error != "" {
			if err == nil || err.Error() != tc.error {
				t.Fatalf("%q: %v", tc.name, err)
			}
		} else if err != nil || w.ID != tc.want {
			t.Fatalf("%q: %+v, %v", tc.name, w, err)
		}
	}
	// An exact title beats a prefix; two equal titles are ambiguous.
	state.Workspaces = append(state.Workspaces, model.Workspace{ID: "d", SessionID: "m", Label: "fix"})
	state.Sessions[0].Order = append(state.Sessions[0].Order, "d")
	if w, err := resolveTab(state, "m", "fix"); err != nil || w.ID != "d" {
		t.Fatalf("exact title: %+v %v", w, err)
	}
	state.Workspaces = append(state.Workspaces, model.Workspace{ID: "e", SessionID: "m", Label: "fix"})
	if _, err := resolveTab(state, "m", "fix"); err == nil || err.Error() != "ambiguous tab \"fix\"; matches #3, #4" {
		t.Fatalf("equal titles: %v", err)
	}
	t.Setenv("PITWALL_PANE", "")
	if _, err := resolveTab(state, "m", ""); err == nil {
		t.Fatal("resolved no name outside pane")
	}
	// #N counts in the session asked for.
	if w, err := resolveTab(state, "s2", "1"); err != nil || w.ID != "z" {
		t.Fatalf("#1 of api: %+v %v", w, err)
	}
}

// Tab commands act on the calling pane's session, else the most recently
// used one; -s picks one by name or unique prefix.
func TestCLICurrentSession(t *testing.T) {
	state := cliState()
	for _, tc := range []struct{ pane, flag, want, error string }{
		{"", "", "m", ""},
		{"pz", "", "s2", ""},
		{"pz", "main", "m", ""},
		{"", "ap", "s2", ""},
		{"gone", "", "m", ""},
		{"", "x", "", "no session matches \"x\" (see pitwall session ls)"},
	} {
		t.Setenv("PITWALL_PANE", tc.pane)
		s, err := currentSession(state, tc.flag)
		if tc.error != "" {
			if err == nil || err.Error() != tc.error {
				t.Errorf("%+v: %v", tc, err)
			}
		} else if err != nil || s.ID != tc.want {
			t.Errorf("%+v: %+v %v", tc, s, err)
		}
	}
	state.Sessions = append(state.Sessions, model.Session{ID: "s3", Name: "apex"})
	if _, err := resolveSession(state, "ap"); err == nil || err.Error() != "ambiguous session \"ap\"; matches api, apex" {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := currentSession(model.State{}, ""); err == nil {
		t.Fatal("a session out of nothing")
	}
}

// "ls -s api" lists api's tabs; "kill -s api 1" kills its first.
func TestCLISessionFlag(t *testing.T) {
	fakeCLI(t, cliExchange{state: cliState()})
	code, out, stderr := cliOutput("ls", "-s", "api")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); code != 0 || stderr != "" || len(lines) != 2 || !strings.HasPrefix(lines[1], "1  server") {
		t.Fatalf("%d: %q %s", code, out, stderr)
	}
	fakeCLI(t, cliExchange{state: cliState()}, cliExchange{request: proto.KillSession{WorkspaceID: "z"}, state: cliState()})
	if code, out, stderr := cliOutput("kill", "-f", "-s", "api", "1"); code != 0 || out != "" || stderr != "" {
		t.Fatalf("%d: %s %s", code, out, stderr)
	}
}

func TestCLISessionMutations(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		request any
		pane    string
	}{
		{[]string{"detach", "fix"}, proto.DetachSession{WorkspaceID: "a", Detached: true}, ""},
		{[]string{"detach"}, proto.DetachSession{WorkspaceID: "b", Detached: true}, "pb"},
		{[]string{"rename", "1", "renamed"}, proto.RenameWorkspace{WorkspaceID: "a", Name: "renamed"}, ""},
		{[]string{"rename", "renamed"}, proto.RenameWorkspace{WorkspaceID: "b", Name: "renamed"}, "pb"},
		{[]string{"kill", "-f", "#1"}, proto.KillSession{WorkspaceID: "a"}, ""},
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
			if want := map[bool]string{false: "", true: "[]\n"}[len(args) == 2]; code != 0 || out != want || stderr != "" {
				t.Fatalf("%v: %d %s %s", args, code, out, stderr)
			}
		} else if code != 1 || !strings.Contains(stderr, "pitwall is not running") {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
	}
}

func TestCLIDaemonError(t *testing.T) {
	fakeCLI(t, cliExchange{state: cliState()}, cliExchange{request: proto.KillSession{WorkspaceID: "a"}, state: cliState(), error: "cannot kill"})
	if code, out, stderr := cliOutput("kill", "-f", "1"); code != 1 || out != "" || !strings.Contains(stderr, "cannot kill") {
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
	if runtime.GOOS == "windows" {
		t.Skip("creack/pty has no pty on Windows")
	}
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
			if code := runCLI([]string{"kill", "1"}, slave, &out, &stderr); code != 0 || out.Len() != 0 || stderr.String() != "kill fix login? [y/N] " {
				t.Fatalf("%d: %s %s", code, out.String(), stderr.String())
			}
		})
	}
}

func TestCLITabCommands(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		request any
		initial bool
	}{
		{[]string{"tab", "new"}, proto.NewTab{FromPane: "pa"}, false},
		{[]string{"tab", "rename", "fix", "login"}, proto.RenameTab{Pane: "pa", Name: "fix login"}, false},
		{[]string{"tab", "rename"}, proto.RenameTab{Pane: "pa"}, false},
		{[]string{"tab", "rename", "--literal"}, proto.RenameTab{Pane: "pa", Name: "--literal"}, false},
		{[]string{"tab", "close"}, proto.CloseTab{WorkspaceID: "a"}, true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			state := cliState()
			exchanges := []cliExchange{}
			if tc.initial {
				exchanges = append(exchanges, cliExchange{state: state})
			}
			exchanges = append(exchanges, cliExchange{request: tc.request, state: state})
			fakeCLI(t, exchanges...)
			t.Setenv("PITWALL_PANE", "pa")
			if code, out, stderr := cliOutput(tc.args...); code != 0 || out != "" || stderr != "" {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
		})
	}
}

func TestCLITabOutsidePane(t *testing.T) {
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("PITWALL_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	for _, action := range []string{"new", "rename", "close"} {
		if code, out, stderr := cliOutput("tab", action); code != 1 || out != "" || !strings.Contains(stderr, "inside a pitwall pane") {
			t.Fatalf("%d: %s %s", code, out, stderr)
		}
	}
	cmd := exec.Command(os.Args[0], "tab", "rename", "work")
	cmd.Env = append(os.Environ(), "PITWALL_TEST_MAIN=1")
	if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 1 {
		t.Fatalf("outside pane: %v", err)
	}
}

func TestCLITabErrors(t *testing.T) {
	for _, cause := range []string{"daemon", "missing pane"} {
		t.Run(cause, func(t *testing.T) {
			state := cliState()
			exchange := cliExchange{state: state}
			args := []string{"tab", "close"}
			want := "has no tab"
			if cause == "missing pane" {
				state.Panes = nil
				exchange.state = state
			}
			if cause == "daemon" {
				args = []string{"tab", "rename", "work"}
				exchange.request = proto.RenameTab{Pane: "pa", Name: "work"}
				exchange.error = "cannot rename"
				want = "cannot rename"
			}
			fakeCLI(t, exchange)
			t.Setenv("PITWALL_PANE", "pa")
			if code, out, stderr := cliOutput(args...); code != 1 || out != "" || !strings.Contains(stderr, want) {
				t.Fatalf("%d: %s %s", code, out, stderr)
			}
		})
	}
}

func TestCLISessions(t *testing.T) {
	state := cliState()
	state.Sessions[0].Windows = 1
	state.Activities = append(state.Activities, model.Activity{WorkspaceID: "z", Provider: model.ProviderTerminal, State: model.StateAwaitingInput, Unseen: true})

	fakeCLI(t, cliExchange{state: state})
	code, out, stderr := cliOutput("session", "ls")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	field := func(i int) string { return strings.Join(strings.Fields(lines[i]), " ") }
	if code != 0 || stderr != "" || len(lines) != 3 || field(0) != "NAME TABS WORKING NEEDS YOU WINDOWS LAST USED" ||
		!strings.HasPrefix(field(1), "main * 2 (+1 detached) 1 1 1 ") || !strings.HasPrefix(field(2), "api 1 0 1 0 ") {
		t.Fatalf("%d: %q %s", code, out, stderr)
	}

	fakeCLI(t, cliExchange{state: state})
	_, out, _ = cliOutput("session", "ls", "--json")
	var rows []sessionJSON
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 || !rows[0].Current || rows[0].Windows != 1 ||
		rows[0].Tabs != 2 || rows[0].Working != 1 || rows[1].Name != "api" || rows[1].NeedsYou != 1 {
		t.Fatalf("json %q: %+v %v", out, rows, err)
	}

	// new prints the name and opens a window unless -d, which may follow
	// the name.
	for _, detached := range []bool{false, true} {
		after := cliState()
		after.Sessions = append(after.Sessions, model.Session{ID: "s3", Name: "docs"})
		dir := t.TempDir()
		t.Chdir(dir)
		args := []string{"session", "new", "docs"}
		if detached {
			args = append(args, "-d")
		}
		fakeCLI(t, cliExchange{state: cliState()}, cliExchange{request: proto.SessionNew{Name: "docs", Cwd: dir}, state: after})
		previous := launchGUI
		var launched []string
		launchGUI = func(session, id string) error { launched = append(launched, session+"/"+id); return nil }
		if code, out, stderr := cliOutput(args...); code != 0 || out != "docs\n" || stderr != "" {
			t.Fatalf("%v: %d %q %s", args, code, out, stderr)
		}
		launchGUI = previous
		if detached != (len(launched) == 0) || !detached && launched[0] != "docs/" {
			t.Fatalf("%v launched %v", args, launched)
		}
	}

	for _, tc := range []struct {
		args    []string
		pane    string
		request any
	}{
		{[]string{"session", "rename", "api", "server"}, "", proto.SessionRename{SessionID: "s2", Name: "server"}},
		{[]string{"session", "rename", "server"}, "pz", proto.SessionRename{SessionID: "s2", Name: "server"}},
		{[]string{"session", "rename", "work"}, "", proto.SessionRename{SessionID: "m", Name: "work"}},
		{[]string{"session", "kill", "-f", "ap"}, "", proto.SessionKill{SessionID: "s2"}},
		{[]string{"session", "attach", "main"}, "", proto.FocusSession{SessionID: "m"}},
	} {
		fakeCLI(t, cliExchange{state: state}, cliExchange{request: tc.request, state: state})
		t.Setenv("PITWALL_PANE", tc.pane)
		if code, out, stderr := cliOutput(tc.args...); code != 0 || out != "" || stderr != "" {
			t.Fatalf("%v: %d %s %s", tc.args, code, out, stderr)
		}
	}
	// attach to a session no window shows opens one.
	fakeCLI(t, cliExchange{state: state})
	previous := launchGUI
	var launched []string
	launchGUI = func(session, id string) error { launched = append(launched, session); return nil }
	t.Cleanup(func() { launchGUI = previous })
	if code, _, stderr := cliOutput("session", "attach", "api"); code != 0 || stderr != "" || !slices.Equal(launched, []string{"api"}) {
		t.Fatalf("%d %s %v", code, stderr, launched)
	}
}

// pitwall new --preset runs the preset's agent and args before cmd, with
// its variables and the guard's; a daemon below proto.EnvLevel is refused.
func TestCLINewPreset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "pitwall"), 0o755)
	os.WriteFile(filepath.Join(dir, "pitwall", "config.toml"), []byte(`[[agents.preset]]
name = "fast"
agent = "sh"
args = ["-c", "true"]
env = ["FOO=1"]
guard = true
`), 0o600)
	t.Chdir(dir)
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	sh, _ = filepath.Abs(sh)
	after := cliState()
	after.Workspaces = append(after.Workspaces, model.Workspace{ID: "new", SessionID: "m"})
	fakeCLI(t, cliExchange{state: cliState(), level: proto.Level},
		cliExchange{request: proto.NewSession{Cwd: dir, SessionID: "m", Cmd: []string{sh, "-c", "true", "hi"}, Env: []string{"FOO=1", "PITWALL_GUARD=1"}}, state: after})
	if code, out, stderr := cliOutput("new", "--preset", "fast", "--", "hi"); code != 0 || out != "#3\n" || stderr != "" {
		t.Fatalf("%d: %s %s", code, out, stderr)
	}
	fakeCLI(t, cliExchange{state: cliState(), level: proto.EnvLevel - 1})
	if code, _, stderr := cliOutput("new", "--guard", "--", "sh"); code == 0 || !strings.Contains(stderr, "older than this pitwall") {
		t.Fatalf("old daemon: %d %s", code, stderr)
	}
	fakeCLI(t, cliExchange{state: cliState(), level: proto.GuardGateLevel - 1})
	if code, _, stderr := cliOutput("new", "--guard", "--", "sh"); code == 0 || !strings.Contains(stderr, "without checking for pitwall's hooks") {
		t.Fatalf("daemon below GuardGateLevel: %d %s", code, stderr)
	}
	if code, _, stderr := cliOutput("new", "--preset", "none"); code == 0 || !strings.Contains(stderr, `no preset "none"`) {
		t.Fatalf("unknown preset: %d %s", code, stderr)
	}
	if code, _, stderr := cliOutput("new", "--guard"); code == 0 || !strings.Contains(stderr, "--guard needs an agent") {
		t.Fatalf("guard alone: %d %s", code, stderr)
	}
}

// pitwall new --host leaves the program for the host's daemon to find on
// its own PATH; a tab here gets this shell's absolute path.
func TestLaunchOnHost(t *testing.T) {
	cmd, _, err := launch("", false, []string{"pitwall-only-on-box", "x"}, false)
	if err != nil || !slices.Equal(cmd, []string{"pitwall-only-on-box", "x"}) {
		t.Fatalf("on a host: %v %v", cmd, err)
	}
	if _, _, err := launch("", false, []string{"pitwall-only-on-box"}, true); err == nil {
		t.Fatal("found a program that is not on this PATH")
	}
	cmd, _, err = launch("", false, []string{"sh"}, true)
	if err != nil || !filepath.IsAbs(cmd[0]) {
		t.Fatalf("here: %v %v", cmd, err)
	}
}

// TestNewOnHostRefuses checks that pitwall new --host refuses what the
// host would drop: --issue, read from this machine's checkout, and a
// guard or preset variables on a host daemon too old to keep them.
func TestNewOnHostRefuses(t *testing.T) {
	fakeCLI(t, cliExchange{state: model.State{}, level: proto.EnvLevel - 1})
	if code, _, errOut := cliOutput("new", "--host", "box", "--issue", "12", "--", "codex"); code == 0 || !strings.Contains(errOut, "--issue") {
		t.Fatalf("--host with --issue: %d %q", code, errOut)
	}
	prev := dialHost
	t.Cleanup(func() { dialHost = prev })
	dialHost = func(string, string) (*cliConn, func(), error) {
		c, err := dialCLI()
		return c, func() { c.Close() }, err
	}
	_, err := newOnHost(nil, "", &model.Session{ID: "s", Name: "main"}, "box", proto.NewSession{Cmd: []string{"codex"}, Env: []string{"PITWALL_GUARD=1"}}, false)
	if err == nil || !strings.Contains(err.Error(), "would drop") {
		t.Fatalf("an old host took a guarded tab: %v", err)
	}
	// A host that keeps the guard's variable but would not check its hooks.
	fakeCLI(t, cliExchange{state: model.State{}, level: proto.GuardGateLevel - 1})
	_, err = newOnHost(nil, "", &model.Session{ID: "s", Name: "main"}, "box", proto.NewSession{Cmd: []string{"codex"}, Env: []string{"FOO=1", "PITWALL_GUARD=1"}}, false)
	if err == nil || !strings.HasPrefix(err.Error(), "box: ") || !strings.Contains(err.Error(), "without checking for pitwall's hooks") {
		t.Fatalf("a host below GuardGateLevel took a guarded tab: %v", err)
	}
}

// A refused guarded tab removes the shell this call made for a new part.
// It neither returns that shell as a successful guarded start nor closes
// a tab that another caller added meanwhile.
func TestNewOnHostGuardRefusalCleanup(t *testing.T) {
	for _, cmd := range [][]string{nil, {"claude"}} {
		t.Run(strings.Join(cmd, " "), func(t *testing.T) {
			part := hostState("part", "shell", "pane")
			env := []string{"PITWALL_GUARD=1"}
			refused := part
			refused.Workspaces = append(slices.Clone(part.Workspaces), model.Workspace{ID: "other", SessionID: "part"})
			fakeCLI(t,
				cliExchange{state: model.State{}, level: proto.Level},
				cliExchange{request: proto.SessionNew{Name: "main"}, state: part, level: proto.Level},
				cliExchange{request: func(got any) bool {
					m, ok := got.(proto.NewSession)
					return ok && m.SessionID == "part" && slices.Equal(m.Cmd, cmd) && slices.Equal(m.Env, env) && len(m.ID) == 16
				}, state: refused, level: proto.Level, error: "guarded tab not started"},
				cliExchange{request: proto.CloseTab{WorkspaceID: "shell"}, state: refused, level: proto.Level})
			local := fakeLocal(t, cliExchange{request: proto.SessionLink{SessionID: "s", Host: "box", Remote: "part"}, level: proto.Level})
			prev := dialHost
			t.Cleanup(func() { dialHost = prev })
			dialHost = func(string, string) (*cliConn, func(), error) {
				c, err := dialCLI()
				return c, func() { c.Close() }, err
			}
			_, err := newOnHost(local, "", &model.Session{ID: "s", Name: "main"}, "box", proto.NewSession{Cmd: cmd, Env: env}, false)
			if err == nil || !strings.Contains(err.Error(), "guarded tab not started") {
				t.Fatalf("start = %v", err)
			}
		})
	}
}

// TestAgentHost: in a pitwall pane, --host takes only a [[hosts]] name; at
// a shell any ssh destination goes.
func TestAgentHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("--host needs ssh's ControlMaster")
	}
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	os.MkdirAll(filepath.Join(cfg, "pitwall"), 0o700)
	os.WriteFile(filepath.Join(cfg, "pitwall", "config.toml"), []byte("[[hosts]]\nname = \"box\"\n\n[[hosts]]\nname = \"local\"\n"), 0o600)
	t.Setenv("PITWALL_PANE", "p1")
	for _, name := range []string{"me@elsewhere", "local"} {
		if _, _, err := dialHost(name, "cli"); err == nil || !strings.Contains(err.Error(), "takes only a [[hosts]] name") {
			t.Fatalf("an agent pane reached %s: %v", name, err)
		}
	}
	hosts := []config.Host{{Name: "box"}}
	if err := agentHost("box", hosts); err != nil {
		t.Fatalf("a [[hosts]] name: %v", err)
	}
	t.Setenv("PITWALL_PANE", "")
	if err := agentHost("me@elsewhere", hosts); err != nil {
		t.Fatalf("a shell: %v", err)
	}
}

// TestNewOnHostAmbiguous checks that new -d --host detaches nothing when
// another client opened a tab in the same part at the same time on a host
// that keeps no ids: it cannot tell which of the two is its own.
func TestNewOnHostAmbiguous(t *testing.T) {
	part := []model.Session{{ID: "p", Name: "main"}}
	tab := proto.NewSession{SessionID: "p", Cmd: []string{"sh"}}
	old := proto.RemoteWorkerLevel - 1
	fakeCLI(t,
		cliExchange{state: model.State{Sessions: part, Workspaces: []model.Workspace{{ID: "a", SessionID: "p"}}}, level: old},
		cliExchange{request: tab, state: model.State{Sessions: part, Workspaces: []model.Workspace{{ID: "a", SessionID: "p"}, {ID: "b", SessionID: "p"}, {ID: "c", SessionID: "p"}}}, level: old},
	)
	prev := dialHost
	t.Cleanup(func() { dialHost = prev })
	dialHost = func(string, string) (*cliConn, func(), error) {
		c, err := dialCLI()
		return c, func() { c.Close() }, err
	}
	session := &model.Session{ID: "s", Name: "main", Remotes: []model.RemoteSession{{Host: "box", Session: "p"}}}
	_, err := newOnHost(nil, "", session, "box", proto.NewSession{Cmd: []string{"sh"}}, true)
	if err == nil || !strings.Contains(err.Error(), "not detached") {
		t.Fatalf("detached a tab it could not tell apart: %v", err)
	}
}

// TestPartOn: a host's part of a session is the linked one, else the one
// made for the session's origin, else, for a person only, a session of the
// same name made for none. A part made for another session never is.
func TestPartOn(t *testing.T) {
	ours := model.Origin{Daemon: "here", Session: "s"}
	other := model.Origin{Daemon: "there", Session: "s"}
	hs := model.State{Sessions: []model.Session{
		{ID: "plain", Name: "main"},
		{ID: "theirs", Name: "main-2", Origin: other},
		{ID: "mine", Name: "main-3", Origin: ours},
	}}
	linked := func(to string) *model.Session {
		return &model.Session{ID: "s", Name: "main", Remotes: []model.RemoteSession{{Host: "box", Session: to}}}
	}
	unlinked := &model.Session{ID: "s", Name: "main"}
	for _, c := range []struct {
		name    string
		session *model.Session
		origin  model.Origin
		byName  bool
		want    string
	}{
		{"linked, stamped", linked("mine"), ours, false, "mine"},
		{"linked before origins", linked("plain"), ours, false, "plain"},
		{"linked to another's part", linked("theirs"), ours, true, "mine"},
		{"by origin", unlinked, ours, false, "mine"},
		{"agent never by name", &model.Session{ID: "new", Name: "main"}, model.Origin{Daemon: "here", Session: "new"}, false, ""},
		{"person by name", &model.Session{ID: "new", Name: "main"}, model.Origin{Daemon: "here", Session: "new"}, true, "plain"},
		{"never another's by name", &model.Session{ID: "new", Name: "main-2"}, model.Origin{Daemon: "here", Session: "new"}, true, ""},
		{"old host, by name", unlinked, model.Origin{}, true, "plain"},
		{"old host, linked to a stamped part", linked("theirs"), model.Origin{}, false, ""},
	} {
		got := ""
		if p := partOn(hs, c.session, "box", c.origin, c.byName); p != nil {
			got = p.ID
		}
		if got != c.want {
			t.Errorf("%s: part %q, want %q", c.name, got, c.want)
		}
	}
}

// An agent pane's --host needs a host that keeps each session's part
// apart; a person's shell may still use an older one.
func TestNewOnHostOriginLevel(t *testing.T) {
	fakeCLI(t, cliExchange{state: model.State{}, level: proto.OriginLevel - 1})
	prev := dialHost
	t.Cleanup(func() { dialHost = prev })
	dialHost = func(string, string) (*cliConn, func(), error) {
		c, err := dialCLI()
		return c, func() { c.Close() }, err
	}
	t.Setenv("PITWALL_PANE", "p1")
	_, err := newOnHost(nil, "here", &model.Session{ID: "s", Name: "main"}, "box", proto.NewSession{Cmd: []string{"codex"}}, false)
	if err == nil || !strings.HasPrefix(err.Error(), "box: ") || !strings.Contains(err.Error(), "another session's of the same name") {
		t.Fatalf("an agent used a host below OriginLevel: %v", err)
	}
	t.Setenv("PITWALL_PANE", "")
	if err := hostLevel(nil, false, proto.OriginLevel-1); err != nil {
		t.Fatalf("a person's shell: %v", err)
	}
}

// Another client finished creating the part after our first state. Its
// sole worker is not our bootstrap shell, even though SessionNew succeeds.
func TestNewOnHostConcurrentPart(t *testing.T) {
	origin := model.Origin{Daemon: "here", Session: "s"}
	part := model.Session{ID: "p", Name: "main", Origin: origin}
	existing := model.Workspace{ID: "worker", SessionID: "p"}
	before := model.State{Sessions: []model.Session{part}, Workspaces: []model.Workspace{existing}}
	after := before
	after.Workspaces = []model.Workspace{existing, {ID: "new", SessionID: "p"}}
	old := proto.RemoteWorkerLevel - 1 // a host that keeps no ids
	fakeCLI(t,
		cliExchange{state: model.State{}, level: old},
		cliExchange{request: func(got any) bool {
			m, ok := got.(proto.SessionNew)
			return ok && m.Origin == origin && strings.HasPrefix(m.Name, "main")
		}, state: before, level: old},
		cliExchange{request: proto.NewSession{SessionID: "p", Cmd: []string{"sh"}}, state: after, level: old},
	)
	prev := dialHost
	t.Cleanup(func() { dialHost = prev })
	dialHost = func(string, string) (*cliConn, func(), error) {
		c, err := dialCLI()
		return c, func() { c.Close() }, err
	}
	session := &model.Session{ID: "s", Name: "main", Remotes: []model.RemoteSession{{Host: "box", Session: "p"}}}
	got, err := newOnHost(nil, "here", session, "box", proto.NewSession{Cmd: []string{"sh"}}, false)
	if err != nil || got != "box:2" {
		t.Fatalf("new tab %q, %v; existing worker must stay", got, err)
	}
}

// hostConns makes dialHost hand out a connection to a fake daemon per
// call, each answering its own exchanges, and an error once they run out.
func hostConns(t *testing.T, conns ...[]cliExchange) {
	t.Helper()
	prev, prevRetries := dialHost, startRetries
	t.Cleanup(func() { dialHost, startRetries = prev, prevRetries })
	startRetries = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	dialHost = func(string, string) (*cliConn, func(), error) {
		if len(conns) == 0 {
			return nil, nil, errors.New("ssh: connect to host box: Connection refused")
		}
		c := fakeLocal(t, conns[0]...)
		conns = conns[1:]
		return c, func() { c.Close() }, nil
	}
}

// From an agent pane, new --host records the worker here, then starts it
// with its id and parent; a connection that drops before the answer is
// dialled again and the same start sent, which the host runs once.
func TestNewOnHostWorkerRetries(t *testing.T) {
	t.Setenv("PITWALL_PANE", "lp")
	const id = "0123456789abcdef"
	origin := model.Origin{Daemon: "here", Session: "s"}
	part := model.State{Sessions: []model.Session{{ID: "p", Name: "main", Origin: origin}}}
	started := part
	started.Workspaces = []model.Workspace{{ID: "other", SessionID: "p"}, {ID: id, SessionID: "p", Parent: "lw", RemoteParent: true}}
	isTab := func(got any) bool {
		m, ok := got.(proto.NewSession)
		return ok && m.ID == id && m.ParentTab == "lw" && m.SessionID == "p"
	}
	hostConns(t,
		[]cliExchange{{state: part, level: proto.Level}, {request: isTab, drop: true}},
		[]cliExchange{{state: part, level: proto.Level}, {request: isTab, state: started, level: proto.Level}},
	)
	local := fakeLocal(t, cliExchange{request: func(got any) bool {
		m, ok := got.(proto.RemoteWorkers)
		w := model.RemoteWorker{ID: id, Host: "box", Parent: "lw"}
		return ok && m.SessionID == "s" && len(m.Add) == 1 && len(m.Set) == 1 && m.Add[0].ID == w.ID && m.Add[0].Host == w.Host && m.Add[0].Parent == w.Parent && !m.Add[0].Since.IsZero()
	}, level: proto.Level})
	session := &model.Session{ID: "s", Name: "main", Remotes: []model.RemoteSession{{Host: "box", Session: "p"}}}
	got, err := newOnHost(local, "here", session, "box", proto.NewSession{Cmd: []string{"claude"}, ID: id, ParentTab: "lw"}, false)
	if err != nil || got != "box:2" {
		t.Fatalf("start after a dropped connection: %q, %v", got, err)
	}
}

// A start whose host stays unreachable after the retries names the id to
// finish it with; one the host refuses leaves no record here.
func TestNewOnHostWorkerLostOrRefused(t *testing.T) {
	t.Setenv("PITWALL_PANE", "lp")
	const id = "0123456789abcdef"
	origin := model.Origin{Daemon: "here", Session: "s"}
	part := model.State{Sessions: []model.Session{{ID: "p", Name: "main", Origin: origin}}}
	session := &model.Session{ID: "s", Name: "main", Remotes: []model.RemoteSession{{Host: "box", Session: "p"}}}
	isTab := func(got any) bool { m, ok := got.(proto.NewSession); return ok && m.ID == id }
	isAdd := func(got any) bool { m, ok := got.(proto.RemoteWorkers); return ok && len(m.Add) == 1 }
	tab := proto.NewSession{Cmd: []string{"claude"}, ID: id, ParentTab: "lw"}

	hostConns(t, []cliExchange{{state: part, level: proto.Level}, {request: isTab, drop: true}})
	local := fakeLocal(t, cliExchange{request: isAdd, level: proto.Level})
	_, err := newOnHost(local, "here", session, "box", tab, false)
	if err == nil || !strings.Contains(err.Error(), "lost box while starting @"+id) || !strings.Contains(err.Error(), "--id "+id) {
		t.Fatalf("an unreachable host: %v", err)
	}

	hostConns(t, []cliExchange{{state: part, level: proto.Level}, {request: isTab, state: part, level: proto.Level, error: "guarded tab not started"}})
	local = fakeLocal(t,
		cliExchange{request: isAdd, level: proto.Level},
		cliExchange{request: proto.RemoteWorkers{SessionID: "s", Drop: []string{id}}, level: proto.Level})
	if _, err := newOnHost(local, "here", session, "box", tab, false); err == nil || !strings.Contains(err.Error(), "guarded tab not started") {
		t.Fatalf("a refused start: %v", err)
	}
}

// A replacement daemon must prove it can keep ids and check the guard
// before it receives a retry, even if the previous daemon could.
func TestNewOnHostRetryChecksLevel(t *testing.T) {
	t.Setenv("PITWALL_PANE", "lp")
	const id = "0123456789abcdef"
	part := model.State{Sessions: []model.Session{{ID: "p", Origin: model.Origin{Daemon: "here", Session: "s"}}}}
	isTab := func(got any) bool { m, ok := got.(proto.NewSession); return ok && m.ID == id }
	for _, level := range []int{proto.GuardGateLevel - 1, proto.RemoteWorkerLevel - 1} {
		t.Run(fmt.Sprint(level), func(t *testing.T) {
			hostConns(t,
				[]cliExchange{{state: part, level: proto.Level}, {request: isTab, drop: true}},
				[]cliExchange{{state: part, level: level}},
			)
			local := fakeLocal(t, cliExchange{request: func(got any) bool { _, ok := got.(proto.RemoteWorkers); return ok }, level: proto.Level})
			session := &model.Session{ID: "s", Remotes: []model.RemoteSession{{Host: "box", Session: "p"}}}
			_, err := newOnHost(local, "here", session, "box", proto.NewSession{ID: id, ParentTab: "lw", Cmd: []string{"claude"}, Env: []string{"PITWALL_GUARD=1"}}, false)
			if err == nil || !strings.Contains(err.Error(), "older") || !strings.Contains(err.Error(), "--id "+id) {
				t.Fatalf("retry to level %d: %v", level, err)
			}
		})
	}
}

// A start may succeed while its final detach loses the connection.
// The error still needs the id so the caller can retry without a new tab.
func TestNewOnHostFinalStepKeepsID(t *testing.T) {
	t.Setenv("PITWALL_PANE", "")
	const id = "0123456789abcdef"
	part := model.State{Sessions: []model.Session{{ID: "p", Name: "main"}}}
	started := part
	started.Workspaces = []model.Workspace{{ID: id, SessionID: "p"}}
	hostConns(t, []cliExchange{
		{state: part, level: proto.Level},
		{request: proto.NewSession{ID: id, SessionID: "p"}, state: started, level: proto.Level},
		{request: proto.DetachSession{WorkspaceID: id, Detached: true}, drop: true},
	})
	session := &model.Session{ID: "s", Name: "main", Remotes: []model.RemoteSession{{Host: "box", Session: "p"}}}
	_, err := newOnHost(nil, "", session, "box", proto.NewSession{ID: id}, true)
	if err == nil || !strings.Contains(err.Error(), "--id "+id) {
		t.Fatalf("failed detach lost the start id: %v", err)
	}
}

func TestNewOnHostRetryReparentsFirst(t *testing.T) {
	t.Setenv("PITWALL_PANE", "lp")
	const id = "0123456789abcdef"
	origin := model.Origin{Daemon: "here", Session: "s"}
	part := model.State{Sessions: []model.Session{{ID: "p", Origin: origin}},
		Workspaces: []model.Workspace{{ID: id, SessionID: "p", Parent: "old", RemoteParent: true}}}
	current := part
	current.Workspaces = slices.Clone(part.Workspaces)
	current.Workspaces[0].Parent = "new"
	tab := proto.NewSession{ID: id, SessionID: "p", ParentTab: "new"}
	hostConns(t, []cliExchange{
		{state: part, level: proto.Level},
		{request: proto.Reparent{WorkspaceID: id, ParentTab: "new", Origin: origin}, state: current, level: proto.Level},
		{request: tab, state: current, level: proto.Level},
	})
	local := fakeLocal(t, cliExchange{request: func(got any) bool { _, ok := got.(proto.RemoteWorkers); return ok }, level: proto.Level})
	session := &model.Session{ID: "s", Remotes: []model.RemoteSession{{Host: "box", Session: "p"}},
		Workers: []model.RemoteWorker{{ID: id, Host: "box", Parent: "new", Since: time.Now()}}}
	if _, err := newOnHost(local, "here", session, "box", tab, false); err != nil {
		t.Fatalf("retry after parent reopened without a window: %v", err)
	}
}

// A worker needs a host that keeps its id and parent.
func TestHostLevelWorker(t *testing.T) {
	t.Setenv("PITWALL_PANE", "")
	if err := hostLevel(nil, true, proto.RemoteWorkerLevel-1); err == nil || !strings.Contains(err.Error(), "without its id or its parent") {
		t.Fatalf("a worker on an old host: %v", err)
	}
	if err := hostLevel(nil, false, proto.RemoteWorkerLevel-1); err != nil {
		t.Fatalf("a person's tab on an old host: %v", err)
	}
}
