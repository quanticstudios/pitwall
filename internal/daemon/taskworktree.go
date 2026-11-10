package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/teardown"
)

func taskWorktreeName(name string, now time.Time, id string) string {
	return gitstat.Slug(name) + "-" + now.UTC().Format("20060102-150405") + "-" + id
}

// taskRunEnded requires an exit or Done before a close can clean up a task worktree. Callers hold mu.
func (d *Daemon) taskRunEnded(w *model.Workspace) bool {
	if w == nil || w.TaskRun == nil {
		return false
	}
	return slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == w.TaskRun.Pane && p.Exited }) ||
		slices.ContainsFunc(d.st.Activities, func(a model.Activity) bool { return a.PaneID == w.TaskRun.Pane && a.State == model.StateCompleted })
}

// queueTaskCleanup keeps the tab until the removal succeeds. Callers hold mu.
func (d *Daemon) queueTaskCleanup(id string) {
	w := d.workspace(id)
	if d.closing || d.frozen || w == nil || w.TaskRun == nil || d.queue.cleaning[id] {
		return
	}
	if d.queue.cleaning == nil {
		d.queue.cleaning = map[string]bool{}
	}
	d.queue.cleaning[id] = true
	ws := *w
	d.worktreeJobs.Go(func() { d.cleanupTaskWorktree(ws) })
}

func (d *Daemon) cleanupTaskWorktree(ws model.Workspace) {
	ctx, cancel := context.WithTimeout(context.Background(), teardown.Timeout+time.Minute)
	defer cancel()
	defer func() {
		d.mu.Lock()
		delete(d.queue.cleaning, ws.ID)
		d.mu.Unlock()
	}()
	err := d.taskWorktreeRemovable(ctx, ws)
	d.mu.Lock()
	w := d.workspace(ws.ID)
	if w == nil || w.TaskRun != ws.TaskRun || d.closing || d.frozen {
		d.mu.Unlock()
		return
	}
	if w.Pinned || len(d.st.WorktreeUsers(ws.Path, ws.ID)) > 0 {
		err = errors.New("the tab is pinned or another tab uses the worktree")
	}
	if !d.taskRunEnded(w) {
		err = errors.New("the agent has started another turn")
	}
	for _, p := range d.st.Panes {
		if p.WorkspaceID == ws.ID && p.ID != ws.TaskRun.Pane && !p.Exited {
			err = errors.New("another pane is open in the tab")
		}
	}
	d.mu.Unlock()
	if err == nil {
		err = d.deleteWorkspace(ctx, proto.DeleteWorkspace{WorkspaceID: ws.ID, RemoveBranch: true})
		if errors.Is(err, gitstat.ErrBranchKept) {
			err = nil
		}
	}
	if err != nil {
		d.mu.Lock()
		if w := d.workspace(ws.ID); w != nil && w.TaskRun == ws.TaskRun {
			// A kept run becomes an ordinary tab; later turns must not trigger cleanup.
			w.TaskRun = nil
			d.changed()
		}
		d.mu.Unlock()
		d.setWorktreeNotice(fmt.Sprintf("Task worktree kept at %s: %v", ws.Path, err))
	}
}

func (d *Daemon) taskWorktreeRemovable(ctx context.Context, ws model.Workspace) error {
	if ws.WorktreeRoot == "" || ws.TaskRun == nil || ws.TaskRun.Base == "" {
		return errors.New("the run has no recorded starting commit")
	}
	clean, err := gitstat.UnchangedSince(ctx, ws.Path, ws.TaskRun.Base)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("the run has commits or changed or untracked files")
	}
	if d.o.PR == nil {
		if gitstat.GitHub(ctx, ws.Path) {
			return errors.New("cannot check for an open PR")
		}
		return nil
	}
	pr, err := d.o.PR(ctx, ws.Path)
	if errors.Is(err, forge.ErrNoGH) && !gitstat.GitHub(ctx, ws.Path) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check for an open PR: %w", err)
	}
	if pr != nil && pr.State == model.PROpen {
		return errors.New("the run has an open PR")
	}
	return nil
}
