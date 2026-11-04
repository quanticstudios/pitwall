//go:build unix

package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func revDaemon(t *testing.T, dir string) {
	t.Helper()
	setHome(t, dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	d, err := daemon.New()
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

const revScript = "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FORK_LOG_DIR/$PITWALL_PANE.args\"\npwd > \"$FORK_LOG_DIR/$PITWALL_PANE.cwd\"\nexec cat\n"

// A shell tab where the user typed `cd sub && claude` on one line: the pane's
// stored Cwd never hears of sub, because detect only updates it at a prompt.
func TestRevStaleCwd(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	revDaemon(t, dir)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	fake := filepath.Join(dir, "claude")
	os.WriteFile(fake, []byte(revScript), 0o700)
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
func TestRevMenuForkDoesNotHoldChildren(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	revDaemon(t, dir)
	fake := filepath.Join(dir, "claude")
	os.WriteFile(fake, []byte(revScript), 0o700)
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
