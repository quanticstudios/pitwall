package daemon

import (
	"context"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestTopLevelOrder: groups and ungrouped tabs share one order, and every
// mutation keeps State.Order matching what the sidebar shows.
func TestTopLevelOrder(t *testing.T) {
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
	order := func() []string { return d.state().Order }
	want := func(what string, ids ...string) {
		t.Helper()
		if got := order(); !slices.Equal(got, ids) {
			t.Fatalf("%s: %v, want %v", what, got, ids)
		}
	}
	want("new tabs go last", a, b, c, e)

	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: c, Before: a}))
	want("tab before tab", c, a, b, e)
	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: c}))
	want("tab to the end", a, b, e, c)

	// A new group takes the place of its first tab.
	must(t, d.handle(ctx, proto.NewGroup{Name: "one", WorkspaceIDs: []string{b}}))
	g1 := d.st.Projects[0].ID
	want("new group", a, g1, e, c)
	must(t, d.handle(ctx, proto.NewGroup{Name: "two", WorkspaceIDs: []string{c}}))
	g2 := d.st.Projects[1].ID
	want("second group", a, g1, e, g2)

	// The user's case: a group above the ungrouped tabs.
	must(t, d.handle(ctx, proto.MoveGroup{GroupID: g2, Before: a}))
	want("group above a loose tab", g2, a, g1, e)
	must(t, d.handle(ctx, proto.MoveGroup{GroupID: g1}))
	want("group last", g2, a, e, g1)
	must(t, d.handle(ctx, proto.MoveGroup{GroupID: g1, Before: g2}))
	want("group before group", g1, g2, a, e)

	// A loose tab between two groups.
	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: e, Before: g2}))
	want("tab between groups", g1, e, g2, a)

	// Into a group it leaves the top level; out again it lands where asked.
	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: e, GroupID: g2, Before: c}))
	if w := d.workspace(e); w.ProjectID != g2 {
		t.Fatalf("into group: %+v", w)
	}
	want("into group", g1, g2, a)
	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: e, Before: g1}))
	want("out of a group", e, g1, g2, a)

	// Ungrouping lands right after the old group.
	must(t, d.handle(ctx, proto.SetSessionGroup{WorkspaceID: e, GroupID: g2}))
	must(t, d.handle(ctx, proto.SetSessionGroup{WorkspaceID: e}))
	want("ungroup", g1, g2, e, a)

	// A tab opened from a loose tab goes right after it; from a grouped
	// one it stays in the group.
	must(t, d.handle(ctx, proto.NewTab{WorkspaceID: e}))
	got := order()
	if len(got) != 5 || got[2] != e || got[4] != a || slices.Contains([]string{g1, g2, e, a}, got[3]) {
		t.Fatalf("new tab after its source: %v", got)
	}
	fresh := got[3]
	must(t, d.handle(ctx, proto.NewTab{WorkspaceID: c}))
	want("new tab in a group", g1, g2, e, fresh, a)

	// Deleting a group puts its tabs at its place, in their order.
	must(t, d.handle(ctx, proto.DeleteGroup{GroupID: g2}))
	got = order()
	if len(got) != 6 || got[0] != g1 || got[1] != c || got[3] != e || got[5] != a {
		t.Fatalf("delete group: %v", got)
	}

	// Detached tabs keep their slot; killed ones go.
	must(t, d.handle(ctx, proto.DetachSession{WorkspaceID: a, Detached: true}))
	if got := order(); got[len(got)-1] != a {
		t.Fatalf("detach moved the tab: %v", got)
	}
	must(t, d.handle(ctx, proto.KillSession{WorkspaceID: a}))
	if slices.Contains(order(), a) {
		t.Fatalf("killed tab still ordered: %v", order())
	}

	for _, bad := range []any{
		proto.MoveSession{WorkspaceID: e, Before: "missing"},
		proto.MoveSession{WorkspaceID: e, Before: b}, // b is in g1, not top level
		proto.MoveGroup{GroupID: g1, Before: "missing"},
	} {
		if err := d.handle(ctx, bad); err == nil {
			t.Fatalf("%+v succeeded", bad)
		}
	}
}
