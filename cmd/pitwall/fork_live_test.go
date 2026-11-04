//go:build unix

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestForkLive(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			dir := t.TempDir()
			forkTestDaemon(t, dir)
			fake := filepath.Join(dir, string(provider))
			if code, _, stderr := cliOutput("new", "-n", "source", dir, "--", fake, "--model", "test-model", "old prompt"); code != 0 {
				t.Fatalf("new: %d %s", code, stderr)
			}
			conn, err := dialCLI()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			st, err := syncCLI(conn)
			if err != nil || len(st.Panes) != 1 {
				t.Fatalf("source: %+v, %v", st.Panes, err)
			}
			id := st.Panes[0].ID
			_, err = syncCLI(conn, proto.AgentEvent{Pane: id, Provider: provider, Payload: []byte(`{"hook_event_name":"SessionStart"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if code, _, stderr := cliOutput("fork", "source"); code != 1 || !strings.Contains(stderr, "no stored session id yet") {
				t.Fatalf("no-session fork: %d %s", code, stderr)
			}
			_, err = syncCLI(conn, proto.AgentEvent{Pane: id, Provider: provider, Payload: []byte(`{"hook_event_name":"SessionStart","session_id":"source-session"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if code, out, stderr := cliOutput("fork", "source", "-n", "second"); code != 0 || out != "#2\n" {
				t.Fatalf("fork: %d %s %s", code, out, stderr)
			}
			st, err = syncCLI(conn)
			if err != nil || len(st.Panes) != 2 || st.Panes[0].SessionID != "source-session" || st.Panes[0].Exited {
				t.Fatalf("fork state: %+v, %v", st.Panes, err)
			}
			child := st.Panes[1]
			want := []string{"--model", "test-model", "--fork-session", "--resume", "source-session"}
			if provider == model.ProviderCodex {
				want = []string{"fork", "--model", "test-model", "--cd", dir, "source-session"}
			}
			if !slices.Equal(child.Cmd, append([]string{fake}, want...)) || child.Cwd != dir {
				t.Fatalf("child %+v", child)
			}
			waitFor(t, "fake fork argv", func() bool {
				b, err := os.ReadFile(filepath.Join(dir, child.ID+".args"))
				return err == nil && string(b) == strings.Join(want, "\n")+"\n"
			})
			if _, err := syncCLI(conn, proto.Input{Pane: id, Data: []byte("source still runs\r")}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "source still responds", func() bool {
				text, err := readPane(conn, proto.Read{Pane: id, Screen: true})
				return err == nil && strings.Contains(text, "source still runs")
			})
		})
	}
}

func forkTestDaemon(t *testing.T, dir string) {
	t.Helper()
	setHome(t, dir)
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(forkTestScript), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"cat", "git"} {
		found, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(found, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	for _, key := range []string{"PITWALL_SOCKET", "PITWALL_PANE", "PITWALL_ATTACH"} {
		t.Setenv(key, "")
	}
	t.Setenv("XDG_RUNTIME_DIR", filepath.Dir(sockPath(t, "runtime")))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PITWALL_FAKE_PROVIDERS", "1")
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("FORK_LOG_DIR", dir)
	sock, err := proto.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	o := daemon.Defaults()
	start := o.StartPane
	o.StartPane = func(c pane.Config) (daemon.Pane, error) {
		if len(c.Cmd) > 0 {
			name := filepath.Base(c.Cmd[0])
			found, err := exec.LookPath(c.Cmd[0])
			if err != nil || name != "claude" && name != "codex" || found != filepath.Join(dir, name) {
				return nil, fmt.Errorf("refusing non-fake agent %q", c.Cmd[0])
			}
		}
		return start(c)
	}
	d, err := daemon.NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if info, err := os.Stat(sock); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("temp socket: %v", err)
	}
	go func() { done <- d.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
}

const forkTestScript = "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FORK_LOG_DIR/$PITWALL_PANE.args\"\npwd > \"$FORK_LOG_DIR/$PITWALL_PANE.cwd\"\nexec cat\n"

// A shell tab where the user typed `cd sub && claude` on one line: the pane's
// stored Cwd never hears of sub, because detect only updates it at a prompt.
func TestForkLiveDirectory(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	forkTestDaemon(t, dir)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	fake := filepath.Join(dir, "claude")
	if code, _, stderr := cliOutput("new", "-n", "source", dir); code != 0 {
		t.Fatal(stderr)
	}
	conn, err := dialCLI()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, _ := syncCLI(conn)
	id := st.Panes[0].ID
	if _, err := syncCLI(conn, proto.Input{Pane: id, Data: []byte("cd sub && " + fake + "\r")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fake agent start", func() bool {
		b, err := os.ReadFile(filepath.Join(dir, id+".cwd"))
		return err == nil && len(b) > 0
	})
	agentCwd, _ := os.ReadFile(filepath.Join(dir, id+".cwd"))
	syncCLI(conn, proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte(`{"hook_event_name":"SessionStart","session_id":"src"}`)})
	code, out, stderr := cliOutput("fork", "source", "-n", "second")
	t.Logf("fork: code=%d out=%q err=%q", code, out, stderr)
	st, _ = syncCLI(conn)
	for _, p := range st.Panes {
		t.Logf("pane %s cwd=%s cmd=%q session=%q", p.ID, p.Cwd, p.Cmd, p.SessionID)
	}
	t.Logf("source agent really runs in %s", agentCwd)
	if len(st.Panes) != 2 || st.Panes[1].Cwd != sub {
		t.Errorf("fork opened in %q, the source agent runs in %q", st.Panes[len(st.Panes)-1].Cwd, sub)
	}
}

// An orchestrator waiting on its children: a user forks a finished worker.
func TestForkMenuDoesNotHoldChildren(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	forkTestDaemon(t, dir)
	fake := filepath.Join(dir, "claude")
	if code, _, stderr := cliOutput("new", "-n", "orch", dir); code != 0 {
		t.Fatal(stderr)
	}
	conn, err := dialCLI()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, _ := syncCLI(conn)
	orch := st.Panes[0].ID
	t.Setenv("PITWALL_PANE", orch)
	if code, _, stderr := cliOutput("new", "-n", "worker", dir, "--", fake); code != 0 {
		t.Fatal(stderr)
	}
	st, _ = syncCLI(conn)
	worker := st.Panes[1].ID
	ev := func(p, payload string) {
		t.Helper()
		if _, err := syncCLI(conn, proto.AgentEvent{Pane: p, Provider: model.ProviderClaude, Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
	}
	ev(worker, `{"hook_event_name":"SessionStart","session_id":"w1"}`)
	ev(worker, `{"hook_event_name":"UserPromptSubmit","session_id":"w1","prompt":"go"}`)
	ev(worker, `{"hook_event_name":"Stop","session_id":"w1","last_assistant_message":"ok"}`)
	code, out, stderr := cliOutput("wait", "--children", "--until", "done", "--timeout", "3s")
	t.Logf("before fork: code=%d out=%q err=%q", code, out, stderr)
	if code != 0 {
		t.Fatalf("baseline wait failed")
	}
	// The window's menu: proto.ForkPane on the worker.
	if _, err := syncCLI(conn, proto.ForkPane{Pane: worker}); err != nil {
		t.Fatal(err)
	}
	st, _ = syncCLI(conn)
	fork := st.Panes[2].ID
	ev(fork, `{"hook_event_name":"SessionStart","session_id":"f1"}`)
	code, lsOut, _ := cliOutput("ls", "--children")
	t.Logf("children after fork: %d\n%s", code, lsOut)
	code, out, stderr = cliOutput("wait", "--children", "--until", "done", "--timeout", "3s")
	t.Logf("after fork: code=%d out=%q err=%q", code, out, stderr)
	if code == 0 {
		t.Log("wait returned 0: fork does not hold the parent")
	} else {
		t.Errorf("parent's wait --children no longer returns once the worker is done: code %d", code)
	}
}

func TestForkLiveRejectsNonFakeCommands(t *testing.T) {
	dir := t.TempDir()
	forkTestDaemon(t, dir)
	for _, name := range []string{"claude", "codex"} {
		outside := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(outside, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if code, _, stderr := cliOutput("new", dir, "--", outside); code != 1 || !strings.Contains(stderr, "refusing non-fake agent") {
			t.Fatalf("outside command %d %s", code, stderr)
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		if code, _, stderr := cliOutput("new", dir, "--", name); code != 1 || !strings.Contains(stderr, name) {
			t.Fatalf("missing fake command %d %s", code, stderr)
		}
	}
}
