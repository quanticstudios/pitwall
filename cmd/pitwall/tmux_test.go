//go:build unix

package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/tmuxshim"
)

// Adapted from tuios (MIT): internal/tmuxcompat/shim_test.go
// The recorded TmuxBackend sequence runs through the PATH wrapper and real PTYs.
const fakeTeam = `set -eu
[ "$(tmux -V)" = 'tmux 3.4' ]
window=$(tmux display-message -t "$TMUX_PANE" -p '#{window_id}')
before=$(tmux list-panes -t "$window" -F '#{pane_id}')
p=$(tmux -S "${TMUX%%,*}" split-window -d -t "$TMUX_PANE" -h -l 70% -P -F '#{pane_id}' -- cat)
tmux set-option -p -t "$p" window-style bg=default,fg=blue
tmux set-option -p -t "$p" pane-border-style fg=blue
tmux set-option -p -t "$p" pane-active-border-style fg=blue
tmux select-pane -t "$p" -T researcher
tmux set-option -p -t "$p" pane-border-format '#[fg=blue,bold] #{pane_title} #[default]'
tmux select-layout -t "$window" main-vertical
tmux resize-pane -t "$TMUX_PANE" -x 30%
tmux set-option -p -t "$p" remain-on-exit failed
tmux respawn-pane -k -t "$p" -- "sh '$TEAM_DIR/worker.sh'"
tmux send-keys -t "$p" 'hello team' Enter
tmux list-panes -t "$window" -F '#{pane_id} #{pane_title}' > "$TEAM_DIR/panes"
printf '%s\n' "$p" > "$TEAM_DIR/ready"
while [ ! -e "$TEAM_DIR/gate-sent" ]; do sleep 0.01; done
IFS= read -r release < "$TEAM_DIR/gate"
tmux capture-pane -p -t "$p" > "$TEAM_DIR/capture"
tmux kill-pane -t "$p"
[ "$(tmux list-panes -t "$window" -F '#{pane_id}')" = "$before" ]
exit 23
`

func TestTmuxShimTeam(t *testing.T) {
	dir := t.TempDir()
	// Race builds otherwise wait a second at the exit of every tmux call.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	for _, key := range []string{"XDG_RUNTIME_DIR", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(key, filepath.Join(dir, key))
	}
	for _, key := range []string{"PITWALL_SOCKET", "PITWALL_PANE", "PITWALL_ATTACH"} {
		t.Setenv(key, "")
	}
	t.Setenv("PITWALL_FAKE_PROVIDERS", "1")
	t.Setenv("PITWALL_TEST_MAIN", "1")
	t.Setenv("TEAM_DIR", dir)
	socket, err := proto.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	d, err := daemon.New()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("temp socket: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"team.sh": fakeTeam,
		"worker.sh": `set -eu
printf '%s\n' "$TMUX_PANE" > "$TEAM_DIR/worker-id"
tmux display-message -p '#{window_id}' > "$TEAM_DIR/worker-window"
IFS= read -r line
printf '%s\n' "$line" > "$TEAM_DIR/received"
printf 'worker received: %s\n' "$line"
exec cat
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "gate"), 0o600); err != nil {
		t.Fatal(err)
	}
	gate, err := os.OpenFile(filepath.Join(dir, "gate"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	conn, err := dialCLI()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st, err := syncCLI(conn, proto.NewSession{Name: "fake-team", Cwd: dir, Cmd: []string{exe, "tmux-shim", "--", "sh", filepath.Join(dir, "team.sh")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Panes) != 1 {
		t.Fatal(st.Panes)
	}
	lead := st.Panes[0]
	defer func() {
		if t.Failed() {
			screen, err := readPane(conn, proto.Read{Pane: lead.ID, Screen: true})
			t.Logf("fake team screen: %s\nread error: %v", screen, err)
		}
	}()
	// The team starts a new process for each tmux call, so allow CI startup time.
	waitTeam := func(what string, ready func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !ready(); time.Sleep(10 * time.Millisecond) {
			state, err := syncCLI(conn)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range state.Panes {
				if p.ID == lead.ID && p.Exited && !ready() {
					t.Fatalf("fake team exited with code %d while waiting for %s", p.ExitCode, what)
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	// A second tab must stay outside the shim's tmux window.
	st, err = syncCLI(conn, proto.NewSession{Name: "unrelated", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	var unrelated string
	for _, p := range st.Panes {
		if p.WorkspaceID != lead.WorkspaceID {
			unrelated = p.ID
		}
	}
	var worker string
	waitTeam("team panes", func() bool {
		data, e := os.ReadFile(filepath.Join(dir, "ready"))
		if e != nil {
			return false
		}
		worker = strings.TrimSpace(string(data))
		return worker != ""
	})
	st, err = syncCLI(conn)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, p := range st.Panes {
		if p.WorkspaceID == lead.WorkspaceID {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("team has %d panes, want 2", count)
	}
	read := func(name string) string {
		t.Helper()
		data, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		return strings.TrimSpace(string(data))
	}
	waitTeam("worker input", func() bool {
		data, err := os.ReadFile(filepath.Join(dir, "received"))
		return err == nil && strings.TrimSpace(string(data)) == "hello team"
	})
	if got := read("worker-id"); got != worker {
		t.Fatalf("worker identity %q, want %q", got, worker)
	}
	if got := read("worker-window"); got != tmuxshim.ID("@", lead.WorkspaceID) {
		t.Fatal(got)
	}
	if got := read("received"); got != "hello team" {
		t.Fatal(got)
	}
	if got := read("panes"); !strings.Contains(got, worker+" researcher") {
		t.Fatal(got)
	}
	if err := conn.Send(proto.Tmux{From: lead.ID, Args: []string{"kill-pane", "-t", tmuxshim.ID("%", unrelated)}}); err != nil {
		t.Fatal(err)
	}
	msg, err := conn.Recv()
	if err != nil {
		t.Fatal(err)
	}
	result, ok := msg.(proto.TmuxResult)
	if !ok || !strings.Contains(result.Error, "caller's tab") {
		t.Fatalf("foreign target: %#v", msg)
	}
	if _, err := gate.WriteString("go\n"); err != nil {
		t.Fatal(err)
	}
	// Keep the FIFO open until the late reader consumes the buffered release.
	if err := os.WriteFile(filepath.Join(dir, "gate-sent"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitTeam("team cleanup", func() bool {
		var e error
		st, e = syncCLI(conn)
		if e != nil {
			t.Fatal(e)
		}
		for _, p := range st.Panes {
			if p.ID == lead.ID {
				return p.Exited && p.ExitCode == 23 && len(st.Panes) == 2
			}
		}
		return false
	})
	if !strings.Contains(read("capture"), "worker received: hello team") {
		t.Fatal("capture lost worker output")
	}
	if got := os.Getenv("TMUX_PANE"); got != "" {
		t.Fatalf("launcher leaked TMUX_PANE %q", got)
	}
}

func TestTmuxShimRejects(t *testing.T) {
	t.Setenv("PITWALL_PANE", "")
	var out bytes.Buffer
	if code := runTmuxShim(nil, os.Stdin, &out, &out); code != 2 {
		t.Fatal(code)
	}
	if code := runTmuxShim([]string{"--", "sh"}, os.Stdin, &out, &out); code != 1 || !strings.Contains(out.String(), "inside a pitwall pane") {
		t.Fatalf("%d %s", code, &out)
	}
	if err := runTmux([]string{"-Q"}, &out); err == nil {
		t.Fatal("accepted unknown flag")
	}
	if err := runTmux([]string{"-S", "/different/server", "list-panes"}, &out); err == nil || !strings.Contains(err.Error(), "another server") {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "")
	if err := runTmux([]string{"list-panes"}, &out); err == nil || !strings.Contains(err.Error(), "TMUX") {
		t.Fatal(err)
	}
}

func TestTmuxShimOldDaemon(t *testing.T) {
	fakeCLI(t, cliExchange{level: proto.TmuxLevel - 1})
	t.Setenv("PITWALL_PANE", "lead")
	var out bytes.Buffer
	if code := runTmuxShim([]string{"--", "sh"}, os.Stdin, &out, &out); code != 1 || !strings.Contains(out.String(), "needs daemon protocol level") {
		t.Fatalf("%d %s", code, &out)
	}
}
