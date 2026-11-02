package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestFreeze checks an agent that exits after Freeze, as the system stops it
// on the way down, keeps its tab in the state Freeze saved and in later
// saves, and Thaw closes it as its exit would have.
func TestFreeze(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Sessions: []model.Session{{ID: "s", Name: "main"}},
		Workspaces: []model.Workspace{
			{ID: "w1", SessionID: "s", Tabs: []model.Tab{{ID: "t1", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t1"},
			{ID: "w2", SessionID: "s", Tabs: []model.Tab{{ID: "t2", Layout: &layout.Node{Pane: "b"}}}, ActiveTab: "t2"},
		},
		Panes: []model.Pane{
			{ID: "a", WorkspaceID: "w1", Cmd: []string{"claude"}, Provider: model.ProviderClaude, SessionID: "x"},
			{ID: "b", WorkspaceID: "w2"},
		},
	}
	d, _, stop := runDaemon(t, f, func(*Options) {})
	defer stop()
	hasA := func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == "a" })
	}
	waitUntil(t, "both panes start", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.panes) == 2 })
	agent := f.pane(0)
	if agent.cfg.ID != "a" {
		agent = f.pane(1)
	}
	f.mu.Lock()
	f.saved = model.State{}
	f.mu.Unlock()

	d.Freeze()
	f.mu.Lock()
	saved := len(f.saved.Panes)
	f.mu.Unlock()
	if saved != 2 {
		t.Fatalf("Freeze saved %d panes, want 2", saved)
	}
	agent.Close()
	time.Sleep(100 * time.Millisecond) // the pane's exit reaches the daemon
	if !hasA() {
		t.Fatal("the agent's exit while frozen closed its pane")
	}
	d.Thaw()
	if hasA() {
		t.Fatal("Thaw kept the pane whose agent exited")
	}
}

// TestExitAfterStop checks an agent that exits once Serve's context has
// ended, as SIGTERM reaches it with the daemon, keeps its tab in the final
// save, however soon after the stop its exit is handled.
func TestExitAfterStop(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Sessions: []model.Session{{ID: "s", Name: "main"}},
		Workspaces: []model.Workspace{
			{ID: "w1", SessionID: "s", Tabs: []model.Tab{{ID: "t1", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t1"},
			{ID: "w2", SessionID: "s", Tabs: []model.Tab{{ID: "t2", Layout: &layout.Node{Pane: "b"}}}, ActiveTab: "t2"},
		},
		Panes: []model.Pane{
			{ID: "a", WorkspaceID: "w1", Cmd: []string{"claude"}, Provider: model.ProviderClaude, SessionID: "x"},
			{ID: "b", WorkspaceID: "w2"},
		},
	}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "d.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()
	waitUntil(t, "Serve starts", func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.served != nil })
	d.mu.Lock()
	p := d.panes["a"]
	d.mu.Unlock()
	cancel()
	d.exited("a", p) // before anything Serve runs on the stop can
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.ContainsFunc(f.saved.Panes, func(p model.Pane) bool { return p.ID == "a" }) {
		t.Fatalf("final save lost the agent's pane: %+v", f.saved.Panes)
	}
}
