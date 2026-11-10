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

// taskRunEnded allows Done only for an explicit close. An unknown exit never qualifies. Callers hold mu.
func (d *Daemon) taskRunEnded(w *model.Workspace, closing bool) bool {
	if w == nil || w.TaskRun == nil {
		return false
	}
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == w.TaskRun.Pane })
	return i >= 0 && !d.st.Panes[i].ExitUnknown && (d.st.Panes[i].Exited || closing &&
		slices.ContainsFunc(d.st.Activities, func(a model.Activity) bool { return a.PaneID == w.TaskRun.Pane && a.State == model.StateCompleted }))
}

// taskCleanup tracks a close requested while an exit's cleanup is already running. Guarded by mu.
type taskCleanup struct {
	closed  bool
	stopped chan struct{}
	keep    error
}

// queueTaskCleanup removes an explicitly closed tab before starting any checks. Callers hold mu.
func (d *Daemon) queueTaskCleanup(id string, closeTab bool) {
	w := d.workspace(id)
	if d.closing || d.frozen || w == nil || w.TaskRun == nil {
		return
	}
	if d.queue.cleaning == nil {
		d.queue.cleaning = map[string]*taskCleanup{}
	}
	ws := *w
	job, running := d.queue.cleaning[id]
	if !running {
		job = &taskCleanup{}
		d.queue.cleaning[id] = job
	}
	if closeTab && !job.closed {
		job.keep = d.taskWorktreeKeepReason(w, true)
		job.closed, job.stopped = true, make(chan struct{})
		closing := d.removeWorkspace(id)
		d.changed()
		d.worktreeJobs.Go(func() {
			for _, p := range closing {
				if p != nil {
					p.Close()
				}
			}
			close(job.stopped)
		})
	}
	if !running {
		d.worktreeJobs.Go(func() { d.cleanupTaskWorktree(ws, job) })
	}
}

func (d *Daemon) cleanupTaskWorktree(ws model.Workspace, job *taskCleanup) {
	ctx, cancel := context.WithTimeout(context.Background(), teardown.Timeout+time.Minute)
	defer cancel()
	defer func() {
		d.mu.Lock()
		delete(d.queue.cleaning, ws.ID)
		d.mu.Unlock()
	}()
	d.mu.Lock()
	stopped := job.stopped
	d.mu.Unlock()
	if stopped != nil {
		<-stopped
	}
	err := d.taskWorktreeRemovable(ctx, ws)
	d.mu.Lock()
	w := d.workspace(ws.ID)
	stopped = job.stopped
	if d.closing || d.frozen || !job.closed && (w == nil || w.TaskRun != ws.TaskRun) {
		d.mu.Unlock()
		if stopped != nil {
			<-stopped
		}
		return
	}
	closed := job.closed
	if closed {
		if job.keep != nil {
			err = job.keep
		}
		if len(d.st.WorktreeUsers(ws.Path, "")) > 0 {
			err = errors.New("another tab uses the worktree")
		}
	} else if reason := d.taskWorktreeKeepReason(w, false); reason != nil {
		err = reason
	}
	d.mu.Unlock()
	if stopped != nil {
		<-stopped
	}
	if err == nil {
		if closed {
			err = d.removeWorktree(ctx, ws.WorktreeRoot, ws.Path, ws.Ports, true, false, false, "")
		} else {
			err = d.deleteWorkspace(ctx, proto.DeleteWorkspace{WorkspaceID: ws.ID, RemoveBranch: true})
		}
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

// taskWorktreeKeepReason protects live panes and other users of a run's folder. Callers hold mu.
func (d *Daemon) taskWorktreeKeepReason(w *model.Workspace, closing bool) error {
	if w.Pinned || len(d.st.WorktreeUsers(w.Path, w.ID)) > 0 {
		return errors.New("the tab is pinned or another tab uses the worktree")
	}
	if !d.taskRunEnded(w, closing) {
		return errors.New("the agent has not finished")
	}
	for _, p := range d.st.Panes {
		if p.WorkspaceID == w.ID && p.ID != w.TaskRun.Pane && !p.Exited {
			return errors.New("another pane is open in the tab")
		}
	}
	return nil
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
