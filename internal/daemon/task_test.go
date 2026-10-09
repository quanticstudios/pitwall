package daemon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestQueueStarts(t *testing.T) {
	task := func(id, s string) model.Task { return model.Task{ID: id, SessionID: s} }
	ids := func(ts []model.Task) string {
		var out []string
		for _, t := range ts {
			out = append(out, t.ID)
		}
		return strings.Join(out, " ")
	}
	three := []model.Task{task("a", "s"), task("b", "s"), task("c", "s")}
	for _, c := range []struct {
		name                     string
		tasks                    []model.Task
		running, starting, freed map[string]int
		limit                    int
		start                    string
		freedLeft                int
	}{
		{"under the limit", three, map[string]int{"s": 1}, nil, nil, 3, "a b", 0},
		{"at the limit", three, map[string]int{"s": 2}, map[string]int{"s": 1}, nil, 3, "", 0},
		{"no limit, none running", three, nil, nil, nil, 0, "a", 0},
		{"no limit, one per finished agent", three, map[string]int{"s": 2}, nil, map[string]int{"s": 2}, 0, "a b", 0},
		{"no limit, a starting task runs", three, nil, map[string]int{"s": 1}, nil, 0, "", 0},
		{"sessions apart", []model.Task{task("a", "s"), task("b", "t")}, map[string]int{"s": 1}, nil, nil, 1, "b", 0},
		{"finished agents wait for a task", nil, nil, nil, map[string]int{"s": 1}, 0, "", 1},
	} {
		freed := c.freed
		if freed == nil {
			freed = map[string]int{}
		}
		start, wait := queueStarts(c.tasks, c.running, c.starting, freed, c.limit)
		if ids(start) != c.start || len(start)+len(wait) != len(c.tasks) || freed["s"] != c.freedLeft {
			t.Errorf("%s: start %q wait %q freed %d, want start %q freed %d", c.name, ids(start), ids(wait), freed["s"], c.start, c.freedLeft)
		}
	}
}

// A queued task waits while max_running agents run in its session and
// starts, in a tab running its command, once one of them is done.
func TestTaskQueue(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.MaxRunning = func() int { return 1 }
	d := newDaemon(t, o)
	ctx := context.Background()
	dir := t.TempDir()
	must(t, d.handle(ctx, proto.NewSession{Cwd: dir, Cmd: []string{"claude", "first"}}))
	must(t, d.handle(ctx, proto.NewTask{Task: model.Task{Dir: dir, Cmd: []string{"codex", "second\nmore"}}, Queue: true}))
	st := d.state()
	if len(st.Tasks) != 1 || len(st.Workspaces) != 1 || st.Tasks[0].SessionID != st.Workspaces[0].SessionID || st.Tasks[0].Title() != "second" {
		t.Fatalf("queued %+v beside %d tabs", st.Tasks, len(st.Workspaces))
	}
	must(t, d.handle(ctx, proto.AgentEvent{Pane: st.Panes[0].ID, Provider: model.ProviderClaude, Payload: []byte(model.StateWorking)}))
	if len(d.state().Tasks) != 1 {
		t.Fatal("started while the limit's agent works")
	}
	// The queue survives a restart, which resumes the agent.
	d.shutdown()
	d = newDaemon(t, o)
	if st = d.state(); len(st.Tasks) != 1 || len(st.Workspaces) != 1 {
		t.Fatalf("after a restart: %d queued, %d tabs", len(st.Tasks), len(st.Workspaces))
	}
	must(t, d.handle(ctx, proto.AgentEvent{Pane: st.Panes[0].ID, Provider: model.ProviderClaude, Payload: []byte(model.StateCompleted)}))
	deadline := time.Now().Add(5 * time.Second)
	for len(d.state().Workspaces) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("queued task never started: %+v", d.state().Tasks)
		}
		time.Sleep(10 * time.Millisecond)
	}
	st = d.state()
	if len(st.Tasks) != 0 || !slices.Equal(st.Panes[1].Cmd, []string{"codex", "second\nmore"}) || st.Workspaces[1].Path != dir {
		t.Fatalf("started %+v in %q, queue %+v", st.Panes[1].Cmd, st.Workspaces[1].Path, st.Tasks)
	}

	must(t, d.handle(ctx, proto.NewTask{Task: model.Task{Dir: dir, Cmd: []string{"pi", "third"}}, Queue: true}))
	id := d.state().Tasks[0].ID
	must(t, d.handle(ctx, proto.DropTask{ID: id}))
	if st := d.state(); len(st.Tasks) != 0 || len(st.Workspaces) != 2 {
		t.Fatalf("dropped task left %d queued, %d tabs", len(st.Tasks), len(st.Workspaces))
	}
}

// A task in a new worktree starts off its base or on an existing branch,
// with the worktree's ports, and with setup typed into a shell below it.
func TestTaskWorktree(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Worktrees = func(string) (config.WorktreeSettings, []config.Problem) {
		return config.WorktreeSettings{PortBase: 3000, PortStep: 10, Setup: "make"}, nil
	}
	d := newDaemon(t, o)
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	must(t, d.handle(ctx, proto.NewTask{Task: model.Task{Dir: repo, Worktree: "fix", Base: "dev", Cmd: []string{"claude", "fix it"}}}))
	must(t, d.handle(ctx, proto.NewTask{Task: model.Task{Dir: repo, Branch: "feature", Cmd: []string{"codex", "review"}}}))
	if !slices.Equal(f.worktrees, []string{"fix {Kind:0 Ref:dev PR:0}", "feature {Kind:1 Ref:feature PR:0}"}) {
		t.Fatalf("AddWorktree calls %q", f.worktrees)
	}
	st := d.state()
	w := st.Workspaces[0]
	if w.Path != filepath.Join(repo, ".worktrees", "fix") || w.WorktreeRoot != repo || w.Ports.String() != "3010-3019" {
		t.Fatalf("tab %+v", w)
	}
	if st.Workspaces[1].Path != filepath.Join(repo, ".worktrees", "feature") {
		t.Fatalf("existing branch tab at %q", st.Workspaces[1].Path)
	}
	if !slices.Equal(st.Panes[0].Cmd, []string{"claude", "fix it"}) || len(st.Panes[1].Cmd) != 0 || st.Panes[1].WorkspaceID != w.ID {
		t.Fatalf("panes %+v", st.Panes[:2])
	}
	if got := f.pane(1).got(); got != "make\r" {
		t.Fatalf("setup pane got %q", got)
	}
}
