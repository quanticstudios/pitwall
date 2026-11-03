package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
)

func restoreState(t *testing.T) model.State {
	t.Helper()
	dir := t.TempDir()
	s := model.State{Sessions: []model.Session{
		{ID: "old", UsedAt: time.Unix(1, 0)},
		{ID: "recent", UsedAt: time.Unix(2, 0)},
	}}
	for _, id := range []string{"old-a", "shell", "recent-a", "old-b", "recent-b", "command"} {
		session := "old"
		if id == "recent-a" || id == "recent-b" {
			session = "recent"
		}
		p := model.Pane{ID: id, WorkspaceID: id, Cwd: dir, Env: []string{"RESTORE_TEST=1"}}
		if id != "shell" && id != "command" {
			p.Provider, p.SessionID = model.ProviderClaude, id
			if id == "recent-b" {
				p.Provider = model.ProviderCodex
			}
		} else if id == "command" {
			p.Cmd = []string{"make"}
		} else {
			p.Provider = model.ProviderClaude // detected agent without a session to resume
		}
		s.Panes = append(s.Panes, p)
		s.Workspaces = append(s.Workspaces, model.Workspace{ID: id, SessionID: session, Path: dir,
			Tabs: []model.Tab{{ID: id, Layout: layout.Leaf(id)}}, ActiveTab: id})
	}
	return s
}

func TestStartupRestoreOrderAndGap(t *testing.T) {
	for _, gap := range []time.Duration{0, 5 * time.Millisecond} {
		t.Run(gap.String(), func(t *testing.T) {
			f := &fakes{saved: restoreState(t)}
			o := f.options()
			o.RestoreCmd = store.RestoreCmd
			o.StartupPerAgentDelay = func() time.Duration { return gap }
			type launch struct {
				id string
				at time.Time
			}
			started := make(chan launch, len(f.saved.Panes))
			start := o.StartPane
			o.StartPane = func(c pane.Config) (Pane, error) {
				started <- launch{c.ID, time.Now()}
				return start(c)
			}
			d, err := NewWith(o)
			if err != nil {
				t.Fatal(err)
			}
			defer d.shutdown()
			want := []string{"old-a", "shell", "recent-a", "old-b", "recent-b", "command"}
			if gap > 0 {
				want = []string{"shell", "command", "recent-a", "recent-b", "old-a", "old-b"}
			}
			var previous time.Time
			for i, id := range want {
				select {
				case got := <-started:
					if got.id != id {
						t.Fatalf("launch %d = %s, want %s", i, got.id, id)
					}
					if gap > 0 && i > 2 && got.at.Sub(previous) < gap {
						t.Fatalf("launch gap %s, want at least %s", got.at.Sub(previous), gap)
					}
					previous = got.at
				case <-time.After(time.Second):
					t.Fatalf("waiting for %s", id)
				}
			}
			if gap == 0 && len(started) != 0 {
				t.Fatal("extra launches")
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if len(d.restoring.pending) != 0 || d.restoring.timer != nil || d.restoring.saved != nil {
				t.Fatal("finished queue retains a timer or saved activities")
			}
		})
	}
}

func TestStartupRestoreCloseAndStop(t *testing.T) {
	f := &fakes{saved: restoreState(t)}
	f.saved.Activities = []model.Activity{
		{PaneID: "old-a", State: model.StateCompleted},
		{PaneID: "old-b", State: model.StateWorking},
		{PaneID: "recent-b", State: model.StateWorking},
	}
	d, _, stop := runDaemon(t, f, func(o *Options) {
		o.StartupPerAgentDelay = func() time.Duration { return time.Hour }
	})
	if err := d.closePane("recent-b"); err != nil {
		t.Fatal(err)
	}
	if err := d.handle(nil, proto.Resize{Pane: "old-a", Cols: 123, Rows: 45}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	snapshot := d.saveSnapshot()
	d.mu.Unlock()
	if !slices.Equal(snapshot.Activities, f.saved.Activities[:2]) {
		t.Fatalf("queued activities lost on save: %+v", snapshot.Activities)
	}
	d.mu.Lock()
	timer := d.restoring.timer
	timer.Stop()
	d.restoreNext()
	d.mu.Unlock()
	f.mu.Lock()
	got := f.panes[len(f.panes)-1].cfg
	f.mu.Unlock()
	if got.ID != "old-a" || got.Cols != 123 || got.Rows != 45 {
		t.Fatalf("queued launch = %+v", got)
	}
	stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.restoreNext() // a timer callback that was waiting when shutdown stopped the queue
	if d.restoring.timer != nil || len(d.restoring.pending) > 0 {
		t.Fatal("shutdown retains a restore timer or queue")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, p := range f.panes {
		ids = append(ids, p.cfg.ID)
	}
	if !slices.Equal(ids, []string{"shell", "command", "recent-a", "old-a"}) {
		t.Fatalf("launches = %v", ids)
	}
	if !slices.ContainsFunc(f.saved.Panes, func(p model.Pane) bool { return p.ID == "old-b" && !p.Exited }) {
		t.Fatal("shutdown lost the agent still waiting to launch")
	}
	if !slices.ContainsFunc(f.saved.Activities, func(a model.Activity) bool { return a.PaneID == "old-b" && a.State == model.StateWorking }) {
		t.Fatal("shutdown lost the queued agent's turn")
	}
	if slices.ContainsFunc(f.saved.Activities, func(a model.Activity) bool { return a.PaneID == "recent-b" }) {
		t.Fatal("shutdown saved the closed pane's turn")
	}
}

func TestStartupRestoreCloseLastQueuedPane(t *testing.T) {
	f := &fakes{saved: restoreState(t)}
	o := f.options()
	o.StartupPerAgentDelay = func() time.Duration { return time.Hour }
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	defer d.shutdown()
	d.mu.Lock()
	timer := d.restoring.timer
	d.mu.Unlock()
	for _, id := range []string{"recent-b", "old-a", "old-b"} {
		if err := d.closePane(id); err != nil {
			t.Fatal(err)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if timer.Stop() || d.restoring.timer != nil || d.restoring.saved != nil {
		t.Fatal("closing the last queued pane did not stop the timer")
	}
	d.restoreNext() // a callback that already waited for d.mu
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.panes) != 3 {
		t.Fatalf("closed panes launched: %d starts", len(f.panes))
	}
}

func TestStartupRestoreFailureKeepsGap(t *testing.T) {
	f := &fakes{saved: restoreState(t)}
	o := f.options()
	gap := 5 * time.Millisecond
	o.StartupPerAgentDelay = func() time.Duration { return gap }
	start := o.StartPane
	started := make(chan time.Time, 4)
	o.StartPane = func(c pane.Config) (Pane, error) {
		if len(c.Cmd) > 0 && c.Cmd[0] == "claude" {
			started <- time.Now()
			return nil, errors.New("cannot start")
		}
		return start(c)
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	defer d.shutdown()
	var previous time.Time
	for i := range 4 {
		select {
		case at := <-started:
			if i > 0 && at.Sub(previous) < gap {
				t.Fatalf("failed launch gap = %s, want at least %s", at.Sub(previous), gap)
			}
			previous = at
		case <-time.After(time.Second):
			t.Fatal("queue stopped after a failed launch")
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.st.Panes) != 2 {
		t.Fatalf("failed panes remain in state: %+v", d.st.Panes)
	}
}

func TestRestoreDropsScrollbackEditor(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "scrollback.txt")
			if present {
				if err := os.WriteFile(path, []byte("history\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f := &fakes{saved: model.State{Sessions: []model.Session{{ID: "s"}}, Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Path: dir, Tabs: []model.Tab{{ID: "t", Layout: layout.Split(layout.Leaf("shell"), "shell", "editor", layout.Horizontal)}}, ActiveTab: "t"}}, Panes: []model.Pane{{ID: "shell", WorkspaceID: "w", Cwd: dir}, {ID: "editor", WorkspaceID: "w", Cwd: dir, Cmd: []string{"nvim", path}, ScrollbackFile: path}}}}
			saved := filepath.Join(dir, "state.json")
			if err := store.Save(saved, f.saved); err != nil {
				t.Fatal(err)
			}
			o := f.options()
			o.Load = func() (model.State, error) { return store.Open(saved) }
			d, err := NewWith(o)
			if err != nil {
				t.Fatal(err)
			}
			defer d.shutdown()
			if len(f.panes) != 1 || f.panes[0].cfg.ID != "shell" {
				t.Fatalf("restored editor: %+v", f.panes)
			}
			if len(d.st.Panes) != 1 || !slices.Equal(layout.Panes(d.st.Workspaces[0].Tabs[0].Layout), []string{"shell"}) {
				t.Fatalf("editor stays in state: %+v", d.st.Panes)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("temporary file stays after restore: %v", err)
			}
		})
	}
}
