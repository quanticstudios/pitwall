package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/teardown"
)

func TestWorktreeCLIConfigProblemsAllowRemoval(t *testing.T) {
	for _, file := range []string{"future_key = 1", `link = ["../x"]`, "port_base = 1", "link = [", `teardown = "unfinished`} {
		t.Run(file, func(t *testing.T) {
			repo := gitRepo(t)
			t.Chdir(repo)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("PITWALL_SOCKET", sockPath(t, "none.sock"))
			path, _, err := gitstat.AddWorktree(context.Background(), repo, "feature", model.WorktreeFrom{})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(repo, ".pitwall"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, config.WorktreeFile), []byte(file), 0o600); err != nil {
				t.Fatal(err)
			}
			code, _, stderr := worktreeOutput("rm", "feature")
			if code != 0 {
				t.Fatalf("config mistake blocks removal: %s", stderr)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("worktree kept: %v", err)
			}
			if strings.Contains(file, "unfinished") && !strings.Contains(stderr, "Teardown skipped") {
				t.Fatalf("missing notice: %s", stderr)
			}
		})
	}
}

func TestWorktreeOldDaemonGate(t *testing.T) {
	for _, owned := range []bool{false, true} {
		for _, command := range []string{"", "echo stopped"} {
			t.Run(fmt.Sprintf("owned=%t/command=%s", owned, command), func(t *testing.T) {
				repo := gitRepo(t)
				t.Chdir(repo)
				path, _, err := gitstat.AddWorktree(context.Background(), repo, "feature", model.WorktreeFrom{})
				if err != nil {
					t.Fatal(err)
				}
				before := model.State{}
				if owned {
					before.Workspaces = []model.Workspace{{ID: "w", Path: path, WorktreeRoot: repo}}
				}
				exchanges := []cliExchange{{state: before, level: proto.TeardownLevel - 1}}
				if owned && command == "" {
					exchanges = append(exchanges, cliExchange{request: proto.DeleteWorkspace{WorkspaceID: "w"}})
				}
				fakeCLI(t, exchanges...)
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				if command != "" {
					if err := os.MkdirAll(filepath.Dir(config.Path()), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(config.Path(), []byte(fmt.Sprintf("[worktrees]\nteardown = %q", command)), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				code, _, stderr := worktreeOutput("rm", "feature")
				if owned && command != "" {
					if code == 0 || !strings.Contains(stderr, "teardown needs a daemon") {
						t.Fatalf("old daemon accepted teardown: %d %s", code, stderr)
					}
				} else if code != 0 {
					t.Fatalf("old daemon blocks compatible removal: %d %s", code, stderr)
				}
			})
		}
	}
}

type worktreeDeadlineConn struct {
	net.Conn
	deadline time.Time
}

func (c *worktreeDeadlineConn) SetDeadline(at time.Time) error {
	c.deadline = at
	return c.Conn.SetDeadline(at)
}

func TestWorktreeCLISocketDeadline(t *testing.T) {
	for _, request := range []any{nil, proto.DeleteWorkspace{WorkspaceID: "w"}, proto.DeleteWorktree{Root: "/repo", Path: "/repo/.worktrees/w"}} {
		t.Run(fmt.Sprintf("%T", request), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			socket := &worktreeDeadlineConn{Conn: client}
			done := make(chan error, 1)
			go func() {
				peer := proto.NewConn(server)
				if request != nil {
					if _, err := peer.Recv(); err != nil {
						done <- err
						return
					}
				}
				if _, err := peer.Recv(); err != nil {
					done <- err
					return
				}
				done <- peer.Send(proto.StateMsg{})
			}()
			conn := &cliConn{Conn: proto.NewConn(client), socket: socket}
			args := []any{}
			if request != nil {
				args = append(args, request)
			}
			if _, err := syncMsg(conn, args...); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			remaining := time.Until(socket.deadline)
			if request != nil && remaining < teardown.Timeout+30*time.Second {
				t.Fatalf("socket expires before teardown and git removal: %s", remaining)
			}
			if request == nil && remaining > 6*time.Second {
				t.Fatalf("ordinary request has removal deadline: %s", remaining)
			}
		})
	}
}

func TestWorktreeFailurePrintedOnce(t *testing.T) {
	for _, command := range []string{"echo failure-marker; exit 7", "echo bye > stop.log"} {
		t.Run(command, func(t *testing.T) {
			repo := gitRepo(t)
			t.Chdir(repo)
			t.Setenv("PITWALL_SOCKET", sockPath(t, "none.sock"))
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if _, _, err := gitstat.AddWorktree(context.Background(), repo, "feature", model.WorktreeFrom{}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(config.Path()), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config.Path(), []byte(fmt.Sprintf("[worktrees]\nteardown = %q", command)), 0o600); err != nil {
				t.Fatal(err)
			}
			code, _, stderr := worktreeOutput("rm", "feature")
			if code != 1 || strings.Count(stderr, "Teardown log:") != 1 || strings.Count(stderr, "worktree kept") != 1 {
				t.Fatalf("failure repeated or unclear: %d %s", code, stderr)
			}
		})
	}
}

func TestWorktreeLocalSharedTeardown(t *testing.T) {
	repo := gitRepo(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, _, err := gitstat.AddWorktree(t.Context(), repo, "shared", model.WorktreeFrom{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("[worktrees]\nteardown = 'touch ../../stopped'"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := model.State{Workspaces: []model.Workspace{{ID: "owner", Path: path}, {ID: "fork", Path: filepath.Join(path, "sub"), Name: "side question", NameSet: true}}}
	var stderr strings.Builder
	if err := removeWorktree(t.Context(), nil, nil, &stderr, state, repo, "shared", false, true, proto.Level); err == nil || !strings.Contains(err.Error(), "side question") {
		t.Fatalf("shared removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "stopped")); !os.IsNotExist(err) {
		t.Fatal("teardown ran before shared-folder refusal", err)
	}
	fakeCLI(t, cliExchange{request: proto.KillSession{WorkspaceID: "owner"}, state: model.State{}})
	conn, err := dialCLI()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := removeWorktree(t.Context(), conn, nil, &stderr, state, repo, "shared", true, true, proto.Level); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("forced worktree remains", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "stopped")); !os.IsNotExist(err) {
		t.Fatal("force ran teardown", err)
	}
	if !strings.Contains(stderr.String(), "side question") || !strings.Contains(stderr.String(), "Teardown is skipped") {
		t.Fatalf("warning: %s", &stderr)
	}
}
