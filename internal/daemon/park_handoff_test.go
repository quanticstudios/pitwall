//go:build linux || darwin

package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// An update's handoff carries the keys a resuming pane holds and its
// resume, so they still wait for the agent in the new daemon.
func TestParkedHandoffKeepsHeldKeys(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Sessions:   []model.Session{{ID: "s", Name: "work"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Name: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Cmd: []string{"sh"}}},
	}
	real := func(o *Options) {
		o.NewVT = vt.New
		o.StartPane = func(c pane.Config) (Pane, error) { return pane.Start(c) }
	}
	old, sock, stopOld := runDaemon(t, f, real)
	defer stopOld()
	held := [][]byte{[]byte("x"), []byte("y")}
	old.mu.Lock()
	old.park.keys = map[string][][]byte{"a": held}
	old.park.waking = map[string]time.Time{"a": time.Now()}
	old.mu.Unlock()

	o := f.options()
	real(&o)
	d, ln, err := Takeover(sock, o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	d.mu.Lock()
	defer d.mu.Unlock()
	_, waking := d.park.waking["a"]
	if !waking || !slices.EqualFunc(d.park.keys["a"], held, slices.Equal) {
		t.Fatalf("waking %v, keys %q; want the resume and both held keys", waking, d.park.keys["a"])
	}
}
