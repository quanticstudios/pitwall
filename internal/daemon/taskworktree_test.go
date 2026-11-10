package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestTaskWorktreeName(t *testing.T) {
	now := time.Date(2026, 11, 10, 2, 3, 4, 0, time.FixedZone("test", 3600))
	a := taskWorktreeName("Triage issues!", now, "abc")
	b := taskWorktreeName("Triage issues!", now, "def")
	if a != "triage-issues-20261110-010304-abc" || a == b || gitstat.Slug(a) != a {
		t.Fatalf("run names %q, %q", a, b)
	}
}

func taskRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	gitIn(t, repo, "init", "--initial-branch=main")
	must(t, os.WriteFile(filepath.Join(repo, "file"), []byte("base\n"), 0600))
	gitIn(t, repo, "add", "file")
	gitIn(t, repo, "commit", "-m", "base")
	return repo
}

func TestTaskWorktreeRemovable(t *testing.T) {
	for _, kind := range []string{"clean", "commit", "base moved", "untracked", "modified", "staged", "open PR", "closed PR", "PR error", "GitHub unavailable", "missing base"} {
		t.Run(kind, func(t *testing.T) {
			repo := taskRepo(t)
			path, _, err := gitstat.AddWorktree(t.Context(), repo, "run", model.WorktreeFrom{})
			must(t, err)
			base, err := gitstat.Head(t.Context(), path)
			must(t, err)
			ws := model.Workspace{Path: path, WorktreeRoot: repo, TaskRun: &model.TaskRun{Base: base}}
			d := &Daemon{o: Options{PR: func(context.Context, string) (*model.PR, error) { return nil, nil }}}
			switch kind {
			case "commit", "base moved":
				gitIn(t, path, "commit", "--allow-empty", "-m", "work")
				if kind == "base moved" {
					gitIn(t, repo, "merge", "--ff-only", "run")
				}
			case "untracked":
				must(t, os.WriteFile(filepath.Join(path, "new"), []byte("work"), 0600))
			case "modified", "staged":
				must(t, os.WriteFile(filepath.Join(path, "file"), []byte("work"), 0600))
				if kind == "staged" {
					gitIn(t, path, "add", "file")
				}
			case "open PR", "closed PR":
				state := model.PROpen
				if kind == "closed PR" {
					state = model.PRClosed
				}
				d.o.PR = func(context.Context, string) (*model.PR, error) { return &model.PR{State: state}, nil }
			case "PR error":
				d.o.PR = func(context.Context, string) (*model.PR, error) { return nil, errors.New("offline") }
			case "GitHub unavailable":
				gitIn(t, repo, "remote", "add", "origin", "https://github.com/example/demo.git")
				d.o.PR = func(context.Context, string) (*model.PR, error) { return nil, forge.ErrNoGH }
			case "missing base":
				ws.TaskRun.Base = ""
			}
			err = d.taskWorktreeRemovable(t.Context(), ws)
			wantRemove := kind == "clean" || kind == "closed PR"
			if (err == nil) != wantRemove {
				t.Fatalf("removable = %v, want %v: %v", err == nil, wantRemove, err)
			}
		})
	}
}

func freshTaskDaemon(t *testing.T) (*Daemon, *fakes, string) {
	t.Helper()
	repo := taskRepo(t)
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.RepoRoot, o.Branch = gitstat.RepoRoot, gitBranch
	o.AddWorktree, o.RemoveWorktree = gitstat.AddWorktree, gitstat.RemoveWorktree
	return newDaemon(t, o), f, repo
}

func TestRepeatFreshWorktree(t *testing.T) {
	d, f, repo := freshTaskDaemon(t)
	now := time.Date(2100, 1, 1, 2, 0, 0, 0, time.UTC)
	task := model.Task{Dir: repo, Worktree: "triage", FreshWorktree: true, Cmd: []string{"sh", "-c", "echo hi"}, NextStart: now, Repeat: 30 * time.Second}
	must(t, d.handle(t.Context(), proto.NewTask{Task: task, Queue: true}))
	d.checkQueue(now)
	waitUntil(t, "first run", func() bool { return len(d.state().Workspaces) == 1 })
	first := d.state().Workspaces[0]
	gitIn(t, first.Path, "commit", "--allow-empty", "-m", "keep this run")
	must(t, f.pane(0).Close())
	waitUntil(t, "committed run kept", func() bool {
		st := d.state()
		return len(st.Workspaces) == 1 && st.Workspaces[0].TaskRun == nil
	})
	d.checkQueue(now.Add(30 * time.Second))
	waitUntil(t, "second run", func() bool { return len(d.state().Workspaces) == 2 })
	second := d.state().Workspaces[1]
	if first.Path == second.Path || first.Branch == second.Branch || !strings.HasPrefix(second.Branch, "triage-") {
		t.Fatalf("runs share a worktree: %+v, %+v", first, second)
	}
	must(t, f.pane(1).Close())
	waitUntil(t, "clean run removed", func() bool { return len(d.state().Workspaces) == 1 })
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("committed run removed: %v", err)
	}
	if _, err := os.Stat(second.Path); !os.IsNotExist(err) {
		t.Fatalf("clean run remains: %v", err)
	}
	st := d.state()
	if len(st.Tasks) != 1 || !st.Tasks[0].FreshWorktree || st.Tasks[0].Worktree != "triage" || !st.Tasks[0].NextStart.Equal(now.Add(time.Minute)) {
		t.Fatalf("repeat changed: %+v", st.Tasks)
	}
}

func TestFreshTaskDoneClose(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "dirty"}[keep], func(t *testing.T) {
			d, _, repo := freshTaskDaemon(t)
			must(t, d.handle(t.Context(), proto.NewTask{Task: model.Task{Dir: repo, Worktree: "triage", FreshWorktree: true, Cmd: []string{"codex", "triage"}}}))
			ws := d.state().Workspaces[0]
			if keep {
				must(t, os.WriteFile(filepath.Join(ws.Path, "new"), []byte("keep"), 0600))
			}
			must(t, d.handle(t.Context(), proto.AgentEvent{Pane: ws.TaskRun.Pane, Provider: model.ProviderCodex, Payload: []byte(model.StateCompleted)}))
			if len(d.state().Workspaces) != 1 {
				t.Fatal("Done alone closed the tab")
			}
			must(t, d.handle(t.Context(), proto.CloseTab{WorkspaceID: ws.ID}))
			d.worktreeJobs.Wait()
			if st := d.state(); len(st.Workspaces) != 0 || keep && !strings.Contains(st.Notice, ws.Path) {
				t.Fatalf("closed run's state: %+v", st)
			}
			if _, err := os.Stat(ws.Path); (err == nil) != keep {
				t.Fatalf("kept worktree = %v, want %v: %v", err == nil, keep, err)
			}
		})
	}
}

func TestFreshTaskTrustedTeardown(t *testing.T) {
	for _, fromRepo := range []bool{false, true} {
		t.Run(map[bool]string{false: "trusted", true: "repo"}[fromRepo], func(t *testing.T) {
			d, f, repo := freshTaskDaemon(t)
			marker := filepath.Join(t.TempDir(), "teardown-ran")
			d.o.Worktrees = func(string) (config.WorktreeSettings, []config.Problem) {
				return config.WorktreeSettings{Teardown: "echo ran > '" + filepath.ToSlash(marker) + "'", TeardownFromRepo: fromRepo}, nil
			}
			must(t, d.handle(t.Context(), proto.NewTask{Task: model.Task{Dir: repo, Worktree: "triage", FreshWorktree: true, Cmd: []string{"codex", "triage"}}}))
			must(t, f.pane(0).Close())
			waitUntil(t, "cleanup", func() bool { return len(d.state().Workspaces) == 0 })
			_, err := os.Stat(marker)
			if (err == nil) == fromRepo {
				t.Fatalf("teardown ran = %v, from repo = %v", err == nil, fromRepo)
			}
		})
	}
}

func TestFreshTaskKeepsProtectedWorktrees(t *testing.T) {
	for _, kind := range []string{"pinned", "shared", "split", "teardown failed"} {
		t.Run(kind, func(t *testing.T) {
			d, f, repo := freshTaskDaemon(t)
			if kind == "teardown failed" {
				d.o.Worktrees = func(string) (config.WorktreeSettings, []config.Problem) {
					return config.WorktreeSettings{Teardown: "exit 1"}, nil
				}
			}
			must(t, d.handle(t.Context(), proto.NewTask{Task: model.Task{Dir: repo, Worktree: "triage", FreshWorktree: true, Cmd: []string{"codex", "triage"}}}))
			ws := d.state().Workspaces[0]
			switch kind {
			case "pinned":
				d.mu.Lock()
				d.workspace(ws.ID).Pinned = true
				d.mu.Unlock()
			case "shared":
				must(t, d.handle(t.Context(), proto.NewSession{Cwd: ws.Path}))
			case "split":
				must(t, d.handle(t.Context(), proto.OpenPane{WorkspaceID: ws.ID}))
			}
			must(t, f.pane(0).Close())
			waitUntil(t, "run kept", func() bool {
				st := d.state()
				return len(st.Workspaces) > 0 && st.Workspaces[0].TaskRun == nil
			})
			if _, err := os.Stat(ws.Path); err != nil {
				t.Fatalf("protected worktree removed: %v", err)
			}
		})
	}
}

func TestFreshTaskPreparation(t *testing.T) {
	d, f, repo := freshTaskDaemon(t)
	must(t, os.WriteFile(filepath.Join(repo, ".env"), []byte("local"), 0600))
	mkdir(t, filepath.Join(repo, "cache"))
	d.o.Worktrees = func(string) (config.WorktreeSettings, []config.Problem) {
		return config.WorktreeSettings{Copy: []string{".env"}, Link: []string{"cache"}, PortBase: 3000, PortStep: 10, Setup: "echo setup"}, nil
	}
	must(t, d.handle(t.Context(), proto.NewTask{Task: model.Task{Dir: repo, Worktree: "triage", FreshWorktree: true, Cmd: []string{"codex", "triage"}}}))
	ws := d.state().Workspaces[0]
	if ws.Ports.String() != "3010-3019" || ws.TaskRun.Pane != d.state().Panes[0].ID {
		t.Fatalf("run workspace: %+v", ws)
	}
	if data, err := os.ReadFile(filepath.Join(ws.Path, ".env")); err != nil || string(data) != "local" {
		t.Fatalf("copy: %q, %v", data, err)
	}
	link, err := os.Readlink(filepath.Join(ws.Path, "cache"))
	must(t, err)
	target, err := os.Stat(filepath.Join(ws.Path, "cache"))
	must(t, err)
	source, err := os.Stat(filepath.Join(repo, "cache"))
	must(t, err)
	if !os.SameFile(target, source) {
		t.Fatalf("link: %q points to another directory", link)
	}
	waitUntil(t, "setup typed", func() bool { return f.pane(1).got() == "echo setup\r" })
}

func TestFreshTaskCleanupAfterRestart(t *testing.T) {
	d, f, repo := freshTaskDaemon(t)
	must(t, d.handle(t.Context(), proto.NewTask{Task: model.Task{Dir: repo, Worktree: "triage", FreshWorktree: true, Cmd: []string{"codex", "triage"}}}))
	path := d.state().Workspaces[0].Path
	// Save an exit whose cleanup has not yet run, as at daemon shutdown.
	d.mu.Lock()
	d.st.Panes[0].Exited = true
	d.mu.Unlock()
	d.shutdown()
	o := f.options()
	o.RepoRoot, o.Branch = gitstat.RepoRoot, gitBranch
	o.AddWorktree, o.RemoveWorktree = gitstat.AddWorktree, gitstat.RemoveWorktree
	d = newDaemon(t, o)
	d.checkQueue(time.Now())
	waitUntil(t, "cleanup after restart", func() bool { return len(d.state().Workspaces) == 0 })
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clean worktree remains after restart: %v", err)
	}
}

func TestFreshTaskInterruptedRestart(t *testing.T) {
	d, f, repo := freshTaskDaemon(t)
	must(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("out/\n"), 0600))
	gitIn(t, repo, "add", ".gitignore")
	gitIn(t, repo, "commit", "-m", "ignore output")
	must(t, d.handle(t.Context(), proto.NewTask{Task: model.Task{Dir: repo, Worktree: "triage", FreshWorktree: true, Cmd: []string{"sh", "run.sh"}}}))
	ws := d.state().Workspaces[0]
	mkdir(t, filepath.Join(ws.Path, "out"))
	output := filepath.Join(ws.Path, "out", "log")
	must(t, os.WriteFile(output, []byte("interrupted run\n"), 0600))
	d.shutdown()
	o := f.options()
	o.RepoRoot, o.Branch = gitstat.RepoRoot, gitBranch
	o.AddWorktree, o.RemoveWorktree = gitstat.AddWorktree, gitstat.RemoveWorktree
	d = newDaemon(t, o)
	d.checkQueue(time.Now())
	d.worktreeJobs.Wait()
	st := d.state()
	if len(st.Workspaces) != 1 || len(st.Panes) != 1 || !st.Panes[0].Exited || !st.Panes[0].ExitUnknown || st.Workspaces[0].TaskRun != nil {
		t.Fatalf("interrupted run did not become a kept tab: %+v", st)
	}
	if data, err := os.ReadFile(output); err != nil || string(data) != "interrupted run\n" {
		t.Fatalf("interrupted output lost: %q, %v", data, err)
	}
}

func TestFreshTaskCloseBeforeChecks(t *testing.T) {
	for _, entry := range []string{"tab", "last pane", "exit cleanup"} {
		t.Run(entry, func(t *testing.T) {
			d, f, repo := freshTaskDaemon(t)
			must(t, d.handle(t.Context(), proto.NewTask{Task: model.Task{Dir: repo, Worktree: "triage", FreshWorktree: true, Cmd: []string{"codex", "triage"}}}))
			ws := d.state().Workspaces[0]
			entered, release := make(chan struct{}), make(chan struct{})
			d.o.PR = func(context.Context, string) (*model.PR, error) {
				close(entered)
				<-release
				return &model.PR{State: model.PROpen}, nil
			}
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
				d.worktreeJobs.Wait()
			})
			must(t, d.handle(t.Context(), proto.AgentEvent{Pane: ws.TaskRun.Pane, Provider: model.ProviderCodex, Payload: []byte(model.StateCompleted)}))
			if entry == "exit cleanup" {
				must(t, f.pane(0).Close())
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("exit cleanup did not check PRs")
				}
			}
			var closeRequest any = proto.CloseTab{WorkspaceID: ws.ID}
			if entry == "last pane" {
				closeRequest = proto.ClosePane{Pane: ws.TaskRun.Pane}
			}
			must(t, d.handle(t.Context(), closeRequest))
			if st := d.state(); len(st.Workspaces) != 0 || len(st.Panes) != 0 {
				t.Fatalf("close left the tab live while cleanup checks: %+v", st)
			}
			if err := d.handle(t.Context(), proto.Input{Pane: ws.TaskRun.Pane, Data: []byte("follow-up")}); err == nil {
				t.Fatal("closed task accepted input")
			}
			if err := d.handle(t.Context(), proto.OpenPane{WorkspaceID: ws.ID}); err == nil {
				t.Fatal("closed task accepted a new pane")
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("cleanup did not check PRs")
			}
			if !f.closed(0) {
				t.Fatal("PR check started before the agent stopped")
			}
			close(release)
			d.worktreeJobs.Wait()
			if st := d.state(); len(st.Workspaces) != 0 || !strings.Contains(st.Notice, ws.Path) {
				t.Fatalf("kept worktree was not named without reopening the tab: %+v", st)
			}
			if _, err := os.Stat(ws.Path); err != nil {
				t.Fatalf("worktree with an open PR removed: %v", err)
			}
		})
	}
}
