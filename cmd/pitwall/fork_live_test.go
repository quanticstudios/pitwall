//go:build unix

package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestForkLive(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			dir := t.TempDir()
			setHome(t, dir)
			for _, key := range []string{"PITWALL_SOCKET", "PITWALL_PANE", "PITWALL_ATTACH"} {
				t.Setenv(key, "")
			}
			t.Setenv("XDG_RUNTIME_DIR", filepath.Dir(sockPath(t, "runtime")))
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("PITWALL_FAKE_PROVIDERS", "1")
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
			go func() { done <- d.Serve(ctx, ln) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			if info, err := os.Stat(sock); err != nil || info.Mode()&os.ModeSocket == 0 || !strings.HasPrefix(sock, os.Getenv("XDG_RUNTIME_DIR")+string(os.PathSeparator)) {
				t.Fatalf("temp socket = %s, %v", sock, err)
			}
			fake := filepath.Join(dir, string(provider))
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$FORK_LOG_DIR/$PITWALL_PANE.args\"\nprintf 'fake agent ready\\n'\nexec cat\n"
			if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
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
				want = []string{"fork", "--model", "test-model", "source-session"}
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
