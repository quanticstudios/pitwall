package daemon

import (
	"context"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// A pinned tab refuses every close until it is unpinned; its color stays.
func TestPinnedTab(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.AddProject{Path: t.TempDir()}))
	ws := d.st.Workspaces[0].ID
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: ws}))
	pane := d.st.Panes[0].ID
	must(t, d.handle(ctx, proto.SetTabOptions{WorkspaceID: ws, Pinned: true, Color: "teal"}))
	for _, m := range []any{proto.CloseTab{WorkspaceID: ws}, proto.KillSession{WorkspaceID: ws}, proto.ClosePane{Pane: pane}} {
		if err := d.handle(ctx, m); err == nil {
			t.Fatalf("%T closed a pinned tab", m)
		}
	}
	if w := d.st.Workspace(ws); w == nil || w.Color != "teal" {
		t.Fatalf("tab after refused closes: %+v", w)
	}
	must(t, d.handle(ctx, proto.SetTabOptions{WorkspaceID: ws, Color: "teal"}))
	must(t, d.handle(ctx, proto.CloseTab{WorkspaceID: ws}))
	if d.st.Workspace(ws) != nil {
		t.Fatal("unpinned tab did not close")
	}
}

// Pinning moves a tab to the end of the pinned tabs at the top, of the
// session or of its group; unpinning puts it right after them.
func TestPinMovesTop(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 4 {
		must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	}
	a, b, c, e := d.st.Workspaces[0].ID, d.st.Workspaces[1].ID, d.st.Workspaces[2].ID, d.st.Workspaces[3].ID
	must(t, d.handle(ctx, proto.NewGroup{Name: "g", WorkspaceIDs: []string{e}}))
	g := d.st.Projects[0].ID
	pin := func(id string, on bool) { must(t, d.handle(ctx, proto.SetTabOptions{WorkspaceID: id, Pinned: on})) }
	want := func(what string, ids ...string) {
		t.Helper()
		if got := d.state().Sessions[0].Order; !slices.Equal(got, ids) {
			t.Fatalf("%s: %v, want %v", what, got, ids)
		}
	}
	pin(c, true)
	want("pinned to the top", c, a, b, g)
	pin(b, true)
	want("after the other pinned tab", c, b, a, g)
	pin(c, false)
	want("unpinned, right after the pinned", b, c, a, g)

	// In a group, among the group's tabs.
	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: a, GroupID: g}))
	pin(a, true)
	var in []string
	for _, w := range d.state().Workspaces {
		if w.ProjectID == g {
			in = append(in, w.ID)
		}
	}
	if !slices.Equal(in, []string{a, e}) {
		t.Fatalf("group order %v, want %v", in, []string{a, e})
	}
}
