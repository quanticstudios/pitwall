//go:build unix

package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/daemon"
)

// fakeClaude is an agent that reports through pitwall's hook as Claude
// Code does: idle at start, then for each line typed a turn that ends with
// a reply, or that asks permission for a line starting "block".
const fakeClaude = `#!/bin/sh
hook() { printf '%s' "$1" | PITWALL_TEST_MAIN=1 "$PITWALL_TEST_EXE" hook claude; }
hook '{"hook_event_name":"SessionStart","session_id":"s1"}'
while IFS= read -r line; do
	hook '{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"go"}'
	case $line in
	block*) hook '{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"description":"Run make"}}' ;;
	*) hook "{\"hook_event_name\":\"Stop\",\"session_id\":\"s1\",\"last_assistant_message\":\"critique of: $line\"}" ;;
	esac
done
`

// talkDaemon serves a real daemon in temp dirs, with fake Claude at
// <bin>/claude, and returns bin and run, which runs pitwall and checks
// its exit code and that its output has wantOut, and returns its stderr.
func talkDaemon(t *testing.T) (bin string, run func(wantCode int, wantOut string, args ...string) string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	setHome(t, home)
	for _, k := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(k, t.TempDir())
	}
	sock := sockPath(t, "d.sock")
	t.Setenv("PITWALL_SOCKET", sock)
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("PITWALL_TEST_EXE", exe)
	bin = filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte(fakeClaude), 0o755); err != nil {
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
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	})

	return bin, func(wantCode int, wantOut string, args ...string) string {
		t.Helper()
		code, out, stderr := cliOutput(args...)
		if code != wantCode || !strings.Contains(out, wantOut) {
			t.Fatalf("pitwall %s: %d %q %q; want %d and %q", strings.Join(args, " "), code, out, stderr, wantCode, wantOut)
		}
		return stderr
	}
}

// TestAgentTalk drives a real daemon the way one agent talks to another:
// split a fake Claude into a tab, prompt it and wait for its reply, read
// it again, and stop at a permission prompt.
func TestAgentTalk(t *testing.T) {
	bin, run := talkDaemon(t)
	claude := filepath.Join(bin, "claude")
	run(0, "#1\n", "new", "-n", "lead", "--", "sh")
	run(0, "#1.2\n", "split", "lead", "--", claude)
	run(0, "critique of: review plan.md\n", "prompt", "#1.2", "--wait", "--timeout", "30s", "--", "review", "plan.md")
	run(0, "critique of: review plan.md\n", "read", "lead.2")
	run(0, `"pane_list":[{"n":1,`, "ls", "--json")
	if stderr := run(1, "", "read", "1.1"); !strings.Contains(stderr, "no reply") {
		t.Fatalf("read of a shell: %q", stderr)
	}

	// prompt waits for the turn it typed, here into a permission prompt,
	// and never types into one.
	run(2, "blocked: Run make\n", "prompt", "1.2", "--wait", "--timeout", "30s", "block please")
	run(2, "blocked: Run make\n", "prompt", "1.2", "--", "anything")
	run(0, "block please", "read", "1.2", "--screen") // the terminal echoes what was typed

	// A Codex sends its first hook with its first prompt, so pitwall types
	// into one it saw start; this one never reports a turn.
	codex := filepath.Join(bin, "codex")
	if err := os.WriteFile(codex, []byte("#!/bin/sh\nwhile read -r line; do :; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := startWait
	startWait = 2 * time.Second
	defer func() { startWait = old }()
	run(0, "#1.3\n", "split", "--down", "lead", "--", codex)
	if stderr := run(1, "", "prompt", "1.3", "--wait", "--timeout", "30s", "hello"); !strings.Contains(stderr, "did not start a turn") {
		t.Fatalf("prompt into a silent codex: %q", stderr)
	}

	t.Setenv("PITWALL_PANE", paneID(t, "1.2"))
	if stderr := run(1, "", "prompt", "1.2", "--", "hi"); !strings.Contains(stderr, "this pane") {
		t.Fatalf("prompt into its own pane: %q", stderr)
	}
}

// fakePeer is a fake Claude that talks with send: on "start" it sends
// ping to its argument's address, and it answers a message ending in ping
// with pong to the address in its header. Each turn's reply is the line
// it got, without quotes.
const fakePeer = `#!/bin/sh
pw() { PITWALL_TEST_MAIN=1 "$PITWALL_TEST_EXE" "$@"; }
hook() { printf '%s' "$1" | pw hook claude; }
hook '{"hook_event_name":"SessionStart","session_id":"p"}'
while IFS= read -r line; do
	hook '{"hook_event_name":"UserPromptSubmit","session_id":"p","prompt":"go"}'
	case $line in
	start) pw send "$1" -- ping ;;
	*ping) to=${line#*reply with: pitwall send }; pw send "${to%% *}" -- pong ;;
	esac
	line=$(printf '%s' "$line" | tr -d '"\\')
	hook "{\"hook_event_name\":\"Stop\",\"session_id\":\"p\",\"last_assistant_message\":\"got: $line\"}"
done
`

// TestAgentSend has two fake agents message each other while both run a
// turn: neither waits on the other, and each reply reaches its sender
// once that sender's turn ends.
func TestAgentSend(t *testing.T) {
	bin, run := talkDaemon(t)
	peer := filepath.Join(bin, "peer")
	if err := os.WriteFile(peer, []byte(fakePeer), 0o755); err != nil {
		t.Fatal(err)
	}
	run(0, "#1\n", "new", "-n", "lead", "--", "sh")
	run(0, "#1.2\n", "split", "lead", "--", peer)
	b := paneID(t, "1.2")
	run(0, "#1.3\n", "split", "lead", "--", peer, "@"+b)
	a := paneID(t, "1.3")
	run(0, "idle\n", "wait", "1.2", "--until", "idle", "--timeout", "10s")
	run(0, "idle\n", "wait", "1.3", "--until", "idle", "--timeout", "10s")
	run(0, "got: start\n", "prompt", "@"+a, "--wait", "--timeout", "10s", "--", "start")

	// A's ping reached B, and B's pong came back to A as A's next prompt.
	want := map[string]string{
		b: "got: [pitwall: message from claude in #1.3; reply with: pitwall send @" + a + " ...] ping",
		a: "got: [pitwall: message from claude in #1.2; reply with: pitwall send @" + b + " ...] pong",
	}
	for pane, reply := range want {
		for start := time.Now(); ; time.Sleep(50 * time.Millisecond) {
			code, out, _ := cliOutput("read", "@"+pane)
			if code == 0 && strings.TrimSpace(out) == reply {
				break
			}
			if time.Since(start) > 15*time.Second {
				t.Fatalf("read @%s: %d %q, want %q", pane, code, out, reply)
			}
		}
	}

	// A message to an agent waiting on the user stays queued; a send from
	// a script needs no pane, and one to its own pane is refused.
	run(0, "#1.4\n", "split", "lead", "--", filepath.Join(bin, "claude"))
	run(0, "idle\n", "wait", "1.4", "--until", "idle", "--timeout", "10s")
	run(2, "blocked: Run make\n", "prompt", "1.4", "--wait", "--timeout", "10s", "block please")
	run(0, "", "send", "1.4", "later")
	run(0, `"queued":1}`, "ls", "--json")
	t.Setenv("PITWALL_PANE", a)
	if stderr := run(1, "", "send", "@"+a, "hi"); !strings.Contains(stderr, "this pane") {
		t.Fatalf("send to its own pane: %q", stderr)
	}
	if stderr := run(1, "", "send", "@gone", "hi"); !strings.Contains(stderr, "no pane @gone") {
		t.Fatalf("send to a closed pane: %q", stderr)
	}
}

// paneID is the id of the pane at addr, from pitwall ls --json.
func paneID(t *testing.T, addr string) string {
	t.Helper()
	conn, err := dialCLI()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	state, err := syncCLI(conn)
	if err != nil {
		t.Fatal(err)
	}
	s, err := currentSession(state, "")
	if err != nil {
		t.Fatal(err)
	}
	_, p, err := resolvePane(state, s.ID, addr)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}
