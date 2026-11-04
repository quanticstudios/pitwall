package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
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

func teardownRepo(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(repo, "repo")
	must(t, os.MkdirAll(repo, 0o755))
	gitIn(t, repo, "init", "--initial-branch=main")
	gitIn(t, repo, "commit", "--allow-empty", "-m", "init")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.AddWorktree, o.RemoveWorktree, o.Orphans = gitstat.AddWorktree, gitstat.RemoveWorktree, pruneOrphans
	o.Worktrees = func(root string) (config.WorktreeSettings, []config.Problem) { return config.LoadWorktrees(cfg, root) }
	d := newDaemon(t, o)
	must(t, d.handle(context.Background(), proto.NewSession{Cwd: t.TempDir()}))
	must(t, d.handle(context.Background(), proto.AddProject{Path: repo}))
	return d, repo, cfg
}

func TestTeardownRemoval(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		for _, mode := range []string{"user", "repo approved", "repo unattended", "repo changed", "failure", "timeout", "force"} {
			t.Run(fmt.Sprintf("orphan=%t/%s", orphan, mode), func(t *testing.T) {
				d, repo, cfg := teardownRepo(t)
				ctx := context.Background()
				cmd := `printf '%s %s %s' "$PORT" "$PITWALL_PORT_BASE" "$PITWALL_PORTS" > ../../ran; echo stopped`
				approved := ""
				repoCommand := strings.HasPrefix(mode, "repo")
				if mode == "failure" || mode == "force" {
					cmd = "echo teardown-failed; exit 9"
				}
				if mode == "timeout" {
					cmd = "echo waiting; sleep 10"
				}
				content := fmt.Sprintf("teardown = %q\n", cmd)
				if repoCommand {
					must(t, os.MkdirAll(filepath.Join(repo, ".pitwall"), 0o755))
					must(t, os.WriteFile(filepath.Join(repo, config.WorktreeFile), []byte(content), 0o600))
					if mode == "repo approved" {
						approved = cmd
					}
					if mode == "repo changed" {
						approved = "an earlier command"
					}
				} else {
					must(t, os.WriteFile(cfg, []byte("[worktrees]\n"+content), 0o600))
				}
				must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: d.state().Projects[0].ID, Name: "feature"}))
				ws := d.state().Workspaces[len(d.state().Workspaces)-1]
				if got, err := teardown.Ports(ws.Path); err != nil || got != ws.Ports {
					t.Fatalf("saved ports %v, %v", got, err)
				}
				if orphan {
					must(t, d.handle(ctx, proto.KillSession{WorkspaceID: ws.ID}))
				}
				if mode == "timeout" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
					defer cancel()
				}
				var request any = proto.DeleteWorkspace{WorkspaceID: ws.ID, Teardown: approved, Force: mode == "force", SkipTeardown: mode == "force"}
				if orphan {
					request = proto.DeleteWorktree{Root: repo, Path: ws.Path, Teardown: approved, Force: mode == "force", SkipTeardown: mode == "force"}
					info := d.worktreeQuery(ctx, proto.WorktreeQuery{Orphans: true})
					if repoCommand && (len(info.Orphans) != 1 || info.Orphans[0].Teardown != cmd) {
						t.Fatalf("preview %+v", info)
					}
				} else if repoCommand && d.worktreeQuery(ctx, proto.WorktreeQuery{WorkspaceID: ws.ID}).Teardown != cmd {
					t.Fatal("missing command preview")
				}
				err := d.handle(ctx, request)
				_, statErr := os.Stat(ws.Path)
				if mode == "failure" || mode == "timeout" {
					if err == nil || statErr != nil || !strings.Contains(d.state().Notice, "worktree kept") {
						t.Fatalf("failure removed worktree: %v %v %q", err, statErr, d.state().Notice)
					}
					if !orphan && len(d.state().Workspaces) != 2 {
						t.Fatal("failure closed tab")
					}
					return
				}
				if err != nil || !os.IsNotExist(statErr) {
					t.Fatalf("removal: %v %v", err, statErr)
				}
				data, readErr := os.ReadFile(filepath.Join(repo, "ran"))
				if mode == "user" || mode == "repo approved" {
					if readErr != nil || string(data) != "3010 3010 3010-3019" || !strings.Contains(d.state().Notice, "stopped") {
						t.Fatalf("teardown did not run before removal: %q %v, notice %q", data, readErr, d.state().Notice)
					}
				} else if readErr == nil {
					t.Fatal("skipped teardown ran")
				}
				if mode == "repo unattended" || mode == "repo changed" {
					if notice := d.state().Notice; !strings.Contains(notice, "Teardown skipped") || !strings.Contains(notice, "printf") {
						t.Fatalf("skip notice %q", notice)
					}
				}
			})
		}
	}
}

func TestTeardownArchiveOnMerge(t *testing.T) {
	for _, repoCommand := range []bool{false, true} {
		t.Run(fmt.Sprint(repoCommand), func(t *testing.T) {
			d, repo, cfg := teardownRepo(t)
			must(t, os.WriteFile(cfg, []byte("[worktrees]\nteardown = 'echo archive-failure; exit 7'\n"), 0o600))
			if repoCommand {
				must(t, os.MkdirAll(filepath.Join(repo, ".pitwall"), 0o755))
				must(t, os.WriteFile(filepath.Join(repo, config.WorktreeFile), []byte("teardown = 'touch should-not-run'\n"), 0o600))
			}
			ctx := context.Background()
			must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: d.state().Projects[0].ID, Name: "feature"}))
			ws := d.state().Workspaces[len(d.state().Workspaces)-1]
			d.o.ArchiveOnMerge = func() bool { return true }
			d.o.PR = func(context.Context, string) (*model.PR, error) {
				return &model.PR{Number: 7, State: model.PRMerged}, nil
			}
			d.mu.Lock()
			d.st.PRs[ws.ID] = model.PR{Number: 7, State: model.PROpen}
			d.mu.Unlock()
			d.pollPRs(ctx, time.Now())
			d.worktreeJobs.Wait()
			_, err := os.Stat(ws.Path)
			if repoCommand {
				if !os.IsNotExist(err) || !strings.Contains(d.state().Notice, "Teardown skipped") || !strings.Contains(d.state().Notice, "touch should-not-run") {
					t.Fatalf("archive: %v %q", err, d.state().Notice)
				}
			} else if err != nil || len(d.state().Workspaces) != 2 || !strings.Contains(d.state().Notice, "archive-failure") {
				t.Fatalf("failed archive: %v %q", err, d.state().Notice)
			}
		})
	}
}

func TestTeardownDoesNotHoldStateLock(t *testing.T) {
	d, repo, cfg := teardownRepo(t)
	must(t, os.WriteFile(cfg, []byte("[worktrees]\nteardown = 'touch ../../started; sleep 10'\n"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: d.state().Projects[0].ID, Name: "feature"}))
	ws := d.state().Workspaces[len(d.state().Workspaces)-1]
	done := make(chan error, 1)
	go func() { done <- d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: ws.ID}) }()
	for deadline := time.Now().Add(3 * time.Second); ; {
		if _, err := os.Stat(filepath.Join(repo, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("teardown did not start")
		}
		time.Sleep(time.Millisecond)
	}
	changed := make(chan error, 1)
	go func() {
		changed <- d.handle(context.Background(), proto.RenameWorkspace{WorkspaceID: ws.ID, Name: "still responsive"})
	}()
	select {
	case err := <-changed:
		must(t, err)
	case <-time.After(time.Second):
		t.Fatal("teardown holds the state lock")
	}
	if err := d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: ws.ID}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("duplicate removal: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled teardown removed the worktree")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("teardown did not stop")
	}
}

func TestTeardownGUIStaysResponsive(t *testing.T) {
	d, _, cfg := teardownRepo(t)
	must(t, os.WriteFile(cfg, []byte("[worktrees]\nteardown = 'sleep 10'\n"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: d.state().Projects[0].ID, Name: "feature"}))
	ws := d.state().Workspaces[len(d.state().Workspaces)-1]
	client, server := net.Pipe()
	defer client.Close()
	must(t, client.SetDeadline(time.Now().Add(3*time.Second)))
	done := make(chan struct{})
	go func() { defer close(done); d.serveConn(ctx, server) }()
	conn := proto.NewConn(client)
	must(t, conn.Send(proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui"}))
	for {
		msg, err := conn.Recv()
		must(t, err)
		if _, ok := msg.(proto.StateMsg); ok {
			break
		}
	}
	must(t, conn.Send(proto.DeleteWorkspace{WorkspaceID: ws.ID}))
	must(t, conn.Send(proto.RenameWorkspace{WorkspaceID: ws.ID, Name: "still responsive"}))
	must(t, conn.Send(proto.Sync{}))
	for {
		msg, err := conn.Recv()
		must(t, err)
		if st, ok := msg.(proto.StateMsg); ok {
			for _, w := range st.State.Workspaces {
				if w.ID == ws.ID && w.Name == "still responsive" {
					cancel()
					<-done
					d.worktreeJobs.Wait()
					return
				}
			}
		}
	}
}
