package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func reviewTree(t *testing.T, d *Daemon, name string) model.Workspace {
	t.Helper()
	must(t, d.handle(context.Background(), proto.NewWorkspace{ProjectID: d.state().Projects[0].ID, Name: name}))
	return d.state().Workspaces[len(d.state().Workspaces)-1]
}

func mergeReviewTrees(d *Daemon, ctx context.Context) {
	d.o.ArchiveOnMerge = func() bool { return true }
	d.o.PR = func(context.Context, string) (*model.PR, error) {
		return &model.PR{Number: 7, State: model.PRMerged}, nil
	}
	d.mu.Lock()
	for _, w := range d.st.Workspaces {
		if w.WorktreeRoot != "" {
			d.st.PRs[w.ID] = model.PR{Number: 7, State: model.PROpen}
		}
	}
	d.mu.Unlock()
	d.pollPRs(ctx, time.Now())
}

func TestTeardownConfigProblemsAllowRemoval(t *testing.T) {
	for _, file := range []string{"future_key = 1", `link = ["../x"]`, "port_base = 1", "link = [", `teardown = "unfinished`} {
		for _, archive := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/archive=%t", file, archive), func(t *testing.T) {
				d, repo, _ := teardownRepo(t)
				w := reviewTree(t, d, "feature")
				must(t, os.MkdirAll(filepath.Join(repo, ".pitwall"), 0o755))
				must(t, os.WriteFile(filepath.Join(repo, config.WorktreeFile), []byte(file), 0o600))
				if archive {
					mergeReviewTrees(d, context.Background())
					d.worktreeJobs.Wait()
				} else {
					must(t, d.handle(context.Background(), proto.DeleteWorkspace{WorkspaceID: w.ID}))
				}
				if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
					t.Fatalf("config mistake kept worktree: %v", err)
				}
				if strings.Contains(file, "unfinished") && !strings.Contains(d.state().Notice, "Teardown skipped") {
					t.Fatalf("missing parse skip notice: %q", d.state().Notice)
				}
			})
		}
	}
	// Validation errors elsewhere do not suppress a resolved command.
	d, repo, cfg := teardownRepo(t)
	w := reviewTree(t, d, "feature")
	must(t, os.WriteFile(cfg, []byte("[worktrees]\nteardown = 'touch ../../ran'"), 0o600))
	must(t, os.MkdirAll(filepath.Join(repo, ".pitwall"), 0o755))
	must(t, os.WriteFile(filepath.Join(repo, config.WorktreeFile), []byte("future_key = 1"), 0o600))
	must(t, d.handle(context.Background(), proto.DeleteWorkspace{WorkspaceID: w.ID}))
	if _, err := os.Stat(filepath.Join(repo, "ran")); err != nil {
		t.Fatal("resolved command did not run", err)
	}
}

func TestTeardownDirtyWorktreeNotStopped(t *testing.T) {
	for _, dirtyBefore := range []bool{false, true} {
		for _, archive := range []bool{false, true} {
			t.Run(fmt.Sprintf("before=%t/archive=%t", dirtyBefore, archive), func(t *testing.T) {
				d, repo, cfg := teardownRepo(t)
				w := reviewTree(t, d, "feature")
				command := "echo bye > stop.log"
				if dirtyBefore {
					command = "touch ../../ran"
					must(t, os.WriteFile(filepath.Join(w.Path, "keep.txt"), []byte("work"), 0o600))
				}
				must(t, os.WriteFile(cfg, []byte(fmt.Sprintf("[worktrees]\nteardown = %q", command)), 0o600))
				if archive {
					mergeReviewTrees(d, context.Background())
					d.worktreeJobs.Wait()
				} else if err := d.handle(context.Background(), proto.DeleteWorkspace{WorkspaceID: w.ID}); err == nil {
					t.Fatal("dirty worktree removed")
				}
				if _, err := os.Stat(w.Path); err != nil {
					t.Fatal("worktree lost", err)
				}
				if !strings.Contains(d.state().Notice, "worktree kept") {
					t.Fatalf("no visible failure: %q", d.state().Notice)
				}
				if dirtyBefore {
					if _, err := os.Stat(filepath.Join(repo, "ran")); !os.IsNotExist(err) {
						t.Fatal("dirty worktree's stack stopped")
					}
				} else if _, err := os.Stat(filepath.Join(w.Path, "stop.log")); err != nil {
					t.Fatal("teardown did not run", err)
				}
			})
		}
	}
}

func TestArchiveNoticesKeepFailuresFirst(t *testing.T) {
	d, _, cfg := teardownRepo(t)
	first, second := reviewTree(t, d, "first"), reviewTree(t, d, "second")
	must(t, os.WriteFile(cfg, []byte(`[worktrees]
teardown = 'case "$PWD" in */second) echo SECOND-FAILED; exit 7;; *) echo FIRST-OK;; esac'
`), 0o600))
	mergeReviewTrees(d, context.Background())
	d.worktreeJobs.Wait()
	notice := d.state().Notice
	if !strings.HasPrefix(notice, "teardown failed; worktree kept") || !strings.Contains(notice, first.Path) || !strings.Contains(notice, second.Path) {
		t.Fatalf("batch notice: %q", notice)
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); !os.IsNotExist(err) {
		t.Fatal("first tree remains", err)
	}
}

func TestArchiveNoticesSummarizeEveryFailure(t *testing.T) {
	d, _, cfg := teardownRepo(t)
	first, second := reviewTree(t, d, "first"), reviewTree(t, d, "second")
	must(t, os.WriteFile(cfg, []byte("[worktrees]\nteardown = 'echo stopped; exit 7'"), 0o600))
	mergeReviewTrees(d, context.Background())
	d.worktreeJobs.Wait()
	lines := strings.Split(d.state().Notice, "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], first.Name) || !strings.Contains(lines[1], second.Name) {
		t.Fatalf("failures do not lead the notice: %q", d.state().Notice)
	}
	for _, line := range lines[:2] {
		if !strings.Contains(line, "worktree kept") || !strings.Contains(line, "exit status 7") {
			t.Fatalf("missing failure summary: %q", line)
		}
	}
}

func TestArchiveKeptBranch(t *testing.T) {
	for _, remote := range []string{"never pushed", "no upstream", "pruned upstream", "upstream"} {
		for _, command := range []string{"", "echo stopped"} {
			t.Run(remote+"/"+command, func(t *testing.T) {
				d, repo, cfg := teardownRepo(t)
				w := reviewTree(t, d, "feature")
				must(t, os.WriteFile(filepath.Join(w.Path, "feature.txt"), []byte("feature"), 0o600))
				gitIn(t, w.Path, "add", "feature.txt")
				gitIn(t, w.Path, "commit", "-m", "feature")
				if remote != "never pushed" {
					origin := filepath.Join(t.TempDir(), "origin.git")
					gitIn(t, repo, "init", "--bare", origin)
					gitIn(t, repo, "remote", "add", "origin", origin)
					if remote == "no upstream" {
						gitIn(t, repo, "push", "origin", "feature")
					} else {
						gitIn(t, repo, "push", "-u", "origin", "feature")
					}
					if remote == "pruned upstream" {
						gitIn(t, repo, "push", "origin", "--delete", "feature")
						gitIn(t, repo, "fetch", "--prune")
					}
				}
				gitIn(t, repo, "merge", "--squash", "feature")
				gitIn(t, repo, "commit", "-m", "squash feature")
				must(t, os.WriteFile(cfg, []byte(fmt.Sprintf("[worktrees]\nteardown = %q", command)), 0o600))
				mergeReviewTrees(d, context.Background())
				d.worktreeJobs.Wait()
				if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
					t.Fatalf("worktree remains: %v", err)
				}
				for _, live := range d.state().Workspaces {
					if live.ID == w.ID {
						t.Fatal("archived tab remains")
					}
				}
				branches := strings.Fields(gitIn(t, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/feature"))
				if (len(branches) > 0) != (remote != "upstream") {
					t.Fatalf("branches: %v", branches)
				}
				notice := d.state().Notice
				if command == "" && notice != "" {
					t.Fatalf("no teardown, new notice: %q", notice)
				}
				if command != "" && (!strings.Contains(notice, "stopped") || strings.Contains(notice, "branch kept")) {
					t.Fatalf("successful teardown notice: %q", notice)
				}
			})
		}
	}
}

func TestArchiveKeptBranchDoesNotHideFailure(t *testing.T) {
	d, _, cfg := teardownRepo(t)
	first, second := reviewTree(t, d, "first"), reviewTree(t, d, "second")
	gitIn(t, first.Path, "commit", "--allow-empty", "-m", "unmerged feature")
	must(t, os.WriteFile(cfg, []byte(`[worktrees]
teardown = 'case "$PWD" in */second) echo SECOND-FAILED; exit 7;; *) echo FIRST-OK;; esac'
`), 0o600))
	mergeReviewTrees(d, context.Background())
	d.worktreeJobs.Wait()
	firstLine, _, _ := strings.Cut(d.state().Notice, "\n")
	if !strings.Contains(firstLine, second.Name) || !strings.Contains(firstLine, "exit status 7") {
		t.Fatalf("branch kept hides teardown failure: %q", d.state().Notice)
	}
	if !strings.Contains(d.state().Notice, "FIRST-OK") || strings.Contains(d.state().Notice, "branch kept") {
		t.Fatalf("successful teardown notice: %q", d.state().Notice)
	}
}

func TestArchiveDoesNotBlockPolling(t *testing.T) {
	d, repo, cfg := teardownRepo(t)
	reviewTree(t, d, "feature")
	must(t, os.WriteFile(cfg, []byte("[worktrees]\nteardown = 'touch ../../archive-started; sleep 10'"), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); d.worktreeJobs.Wait() }()
	returned := make(chan struct{})
	go func() { mergeReviewTrees(d, ctx); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("teardown blocks PR polling")
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		if _, err := os.Stat(filepath.Join(repo, "archive-started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("archive teardown did not start")
		}
		time.Sleep(time.Millisecond)
	}
	// Another poll can finish while the removal is pending.
	returned = make(chan struct{})
	go func() { d.pollPRs(ctx, time.Now().Add(time.Hour)); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("second poll blocked")
	}
}

func TestOrphanTeardownDropsReassignedPorts(t *testing.T) {
	d, repo, cfg := teardownRepo(t)
	a := reviewTree(t, d, "a")
	must(t, d.handle(context.Background(), proto.KillSession{WorkspaceID: a.ID}))
	b := reviewTree(t, d, "b")
	if a.Ports != b.Ports {
		t.Fatalf("fixture needs reused ports: %v %v", a.Ports, b.Ports)
	}
	must(t, os.WriteFile(cfg, []byte(`[worktrees]
teardown = 'printf "port=<%s>" "$PORT" > ../../ports-seen'
`), 0o600))
	t.Setenv("PORT", "inherited-stack")
	must(t, d.handle(context.Background(), proto.DeleteWorktree{Root: repo, Path: a.Path}))
	data, err := os.ReadFile(filepath.Join(repo, "ports-seen"))
	if err != nil || string(data) != "port=<>" {
		t.Fatalf("used live tab ports: %q %v", data, err)
	}
	if _, err := os.Stat(b.Path); err != nil {
		t.Fatal("live tab lost", err)
	}
}

func TestGoneWorktreeCanBeDeletedWithTeardown(t *testing.T) {
	d, _, cfg := teardownRepo(t)
	w := reviewTree(t, d, "feature")
	must(t, os.WriteFile(cfg, []byte("[worktrees]\nteardown = 'true'"), 0o600))
	must(t, os.RemoveAll(w.Path))
	must(t, d.handle(context.Background(), proto.DeleteWorkspace{WorkspaceID: w.ID}))
	if !strings.Contains(d.state().Notice, "folder is gone") {
		t.Fatalf("notice %q", d.state().Notice)
	}
}
