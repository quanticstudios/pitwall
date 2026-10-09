package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// taskQueue is what the queue (State.Tasks) keeps between changes. Guarded
// by d.mu.
type taskQueue struct {
	running  map[string]string // pane: session, of the panes running at the last change
	freed    map[string]int    // session: agents that finished since, which queued tasks may take
	starting map[string]int    // session: tasks taken off the queue whose tab is not open yet
}

// newTask starts a task, or queues it (see proto.NewTask). A failed start
// shows as State.Notice: a GUI shows no request's error.
func (d *Daemon) newTask(ctx context.Context, m proto.NewTask) error {
	t := m.Task
	if len(t.Cmd) == 0 {
		return errors.New("a task needs a command")
	}
	dir, err := filepath.Abs(t.Dir)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dir); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	t.Dir = dir
	d.mu.Lock()
	if t.SessionID, err = d.targetSession(t.SessionID, m.FromPane); err != nil {
		d.mu.Unlock()
		return err
	}
	t.ID = newID()
	if t.GroupID == "" {
		t.GroupID = d.projectAt(t.SessionID, dir)
	}
	if m.Queue {
		d.st.Tasks = append(d.st.Tasks, t)
		d.changed()
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	return d.tryTask(ctx, t)
}

// dropTask takes a task off its queue, and starts it with m.Start.
func (d *Daemon) dropTask(ctx context.Context, m proto.DropTask) error {
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Tasks, func(t model.Task) bool { return t.ID == m.ID })
	if i < 0 {
		d.mu.Unlock()
		return fmt.Errorf("no queued task %s", m.ID)
	}
	t := d.st.Tasks[i]
	d.st.Tasks = slices.Delete(d.st.Tasks, i, i+1)
	d.changed()
	d.mu.Unlock()
	if !m.Start {
		return nil
	}
	return d.tryTask(ctx, t)
}

// tryTask is startTask, with its error in State.Notice too.
func (d *Daemon) tryTask(ctx context.Context, t model.Task) error {
	err := d.startTask(ctx, t)
	if err != nil {
		log.Printf("task %s: %q", t.ID, err)
		d.mu.Lock()
		d.st.Notice = "Task not started: " + err.Error()
		d.changed()
		d.mu.Unlock()
	}
	return err
}

// startTask opens t's tab, in a worktree it makes first when t asks for
// one, with t.Cmd in its pane. A worktree's ports and setup come as
// NewWorkspace's do: setup, and anything that went wrong after git made
// the worktree, show in a shell pane below the agent.
func (d *Daemon) startTask(ctx context.Context, t model.Task) error {
	dir := t.Dir
	var root string
	var wt config.WorktreeSettings
	var problems []error
	if t.Worktree != "" || t.Branch != "" {
		var ok bool
		if root, ok = d.o.RepoRoot(ctx, t.Dir); !ok {
			return fmt.Errorf("%s is not in a git repository", t.Dir)
		}
		from := model.WorktreeFrom{Ref: t.Base}
		if t.Branch != "" {
			from = model.WorktreeFrom{Kind: model.FromBranch, Ref: t.Branch}
		}
		var err error
		if dir, _, wt, problems, err = d.worktree(ctx, root, t.Worktree, from); err != nil {
			return err
		}
	}
	typed, err := setupInput(wt)
	if err != nil {
		problems = append(problems, err)
	}
	d.mu.Lock()
	if t.GroupID != "" && d.project(t.GroupID) == nil {
		t.GroupID = "" // ungrouped since; the folder's group, if any, takes it
	}
	d.mu.Unlock()
	var id string
	err = d.addSession(ctx, proto.NewSession{Cwd: dir, SessionID: t.SessionID, GroupID: t.GroupID, Cmd: t.Cmd}, "", nil, func(w *model.Workspace) {
		id = w.ID
		if root != "" {
			w.WorktreeRoot, w.Ports = root, portBlock(d.st.Workspaces, wt.PortBase, wt.PortStep)
		}
	})
	if err != nil || typed == nil && len(problems) == 0 {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(id)
	if w == nil {
		return nil // closed already
	}
	ti := tabIndex(w, "")
	pid := newID()
	if err := d.start(pid, nil, w.Path, w.Ports); err != nil {
		return err
	}
	d.st.Panes = append(d.st.Panes, model.Pane{ID: pid, WorkspaceID: w.ID, Cwd: w.Path})
	tab := &w.Tabs[ti]
	tab.Layout = d.o.Split(tab.Layout, layout.Panes(tab.Layout)[0], pid, layout.Vertical)
	if typed != nil {
		d.inputs[pid] <- typed
	}
	if len(problems) > 0 {
		d.showNotice(pid, d.attnOf(pid), vt.Notification{Title: "Worktree setup", Body: errors.Join(problems...).Error()})
	}
	d.changed()
	return nil
}

// worktree makes a worktree of the repo at root, as gitstat.AddWorktree
// does with name (from.Name() when "") and from, and copies and links
// [worktrees] files into it. problems are what went wrong once git had
// made it.
func (d *Daemon) worktree(ctx context.Context, root, name string, from model.WorktreeFrom) (path, branch string, wt config.WorktreeSettings, problems []error, err error) {
	if path, branch, err = d.o.AddWorktree(ctx, root, cmp.Or(name, from.Name()), from); err != nil || d.o.Worktrees == nil {
		return path, branch, wt, nil, err
	}
	wt, probs := d.o.Worktrees(root)
	for _, pr := range probs {
		problems = append(problems, errors.New(pr.String()))
	}
	if err := prepareWorktree(root, path, wt); err != nil {
		problems = append(problems, err)
	}
	return path, branch, wt, problems, nil
}

// agentProviders are the providers whose panes count as running agents.
var agentProviders = []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderPi, model.ProviderGemini, model.ProviderOpenCode}

// runningPanes maps each pane that runs an agent or a command to its
// session: a pane opened with a command (Held) or one an agent reported
// from, until it exits or its agent is done or failed.
func runningPanes(st *model.State) map[string]string {
	ended := map[string]bool{}
	for _, a := range st.Activities {
		if a.State == model.StateCompleted || a.State == model.StateError {
			ended[a.PaneID] = true
		}
	}
	out := map[string]string{}
	for _, p := range st.Panes {
		if p.Exited || ended[p.ID] || !p.Held && !slices.Contains(agentProviders, p.Provider) {
			continue
		}
		if s := st.SessionOf(p.WorkspaceID); s != "" {
			out[p.ID] = s
		}
	}
	return out
}

// queueStarts splits tasks into the ones to start now, first first, and
// the ones that wait, given each session's running agents and starting
// tasks, the agents that finished since tasks last started (freed, which
// it takes from) and the [agents] max_running limit. Under a limit, a
// session's task starts while fewer than limit agents run there. With
// limit 0 it starts for each agent that finished, or when none runs.
func queueStarts(tasks []model.Task, running, starting, freed map[string]int, limit int) (start, wait []model.Task) {
	busy := map[string]int{}
	for _, t := range tasks {
		s := t.SessionID
		n := running[s] + starting[s] + busy[s]
		ok := limit > 0 && n < limit || limit == 0 && n == 0
		if !ok && limit == 0 && freed[s] > 0 {
			freed[s]--
			ok = true
		}
		if ok {
			start = append(start, t)
			busy[s]++
		} else {
			wait = append(wait, t)
		}
	}
	return start, wait
}

// pumpQueue starts the queued tasks queueStarts lets start, each on a
// goroutine of its own, and notes which agents finished since the last
// change. changed calls it; callers hold d.mu.
func (d *Daemon) pumpQueue() {
	q := &d.queue
	now := runningPanes(&d.st)
	if q.freed == nil {
		q.freed, q.starting = map[string]int{}, map[string]int{}
	}
	for id, s := range q.running {
		if _, ok := now[id]; !ok {
			q.freed[s]++
		}
	}
	q.running = now
	queued := map[string]bool{}
	for _, t := range d.st.Tasks {
		queued[t.SessionID] = true
	}
	for s := range q.freed {
		if !queued[s] {
			delete(q.freed, s) // only queued tasks take a finished agent's place
		}
	}
	if len(d.st.Tasks) == 0 || d.closing {
		return
	}
	running := map[string]int{}
	for _, s := range now {
		running[s]++
	}
	limit := 0
	if d.o.MaxRunning != nil {
		limit = d.o.MaxRunning()
	}
	var start []model.Task
	start, d.st.Tasks = queueStarts(d.st.Tasks, running, q.starting, q.freed, limit)
	for _, t := range start {
		q.starting[t.SessionID]++
		go d.runQueued(t)
	}
}

// queuedTimeout is how long a queued task's worktree and tab may take.
const queuedTimeout = time.Minute

// runQueued starts a task pumpQueue took off the queue.
func (d *Daemon) runQueued(t model.Task) {
	log.Printf("task %s: starting from the queue", t.ID)
	ctx, cancel := context.WithTimeout(context.Background(), queuedTimeout)
	defer cancel()
	d.tryTask(ctx, t)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.queue.starting[t.SessionID]--; d.queue.starting[t.SessionID] <= 0 {
		delete(d.queue.starting, t.SessionID)
	}
	if !d.closing {
		d.changed() // its slot counts as its pane now, or is free again
	}
}
