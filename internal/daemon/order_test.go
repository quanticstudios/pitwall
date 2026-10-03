package daemon

import (
	"context"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestMoveSessionAndGroup(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 3 {
		must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	}
	a, b, c := d.st.Workspaces[0].ID, d.st.Workspaces[1].ID, d.st.Workspaces[2].ID
	ids := func() []string {
		var out []string
		for _, w := range d.st.Workspaces {
			out = append(out, w.ID)
		}
		return out
	}
	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: c, Before: a}))
	if got := ids(); got[0] != c || got[1] != a || got[2] != b {
		t.Fatalf("before a: %v", got)
	}
	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: c}))
	if got := ids(); got[2] != c {
		t.Fatalf("to end: %v", got)
	}
	must(t, d.handle(ctx, proto.NewGroup{Name: "one", WorkspaceIDs: []string{a}}))
	must(t, d.handle(ctx, proto.NewGroup{Name: "two", WorkspaceIDs: []string{b}}))
	g1, g2 := d.st.Projects[0].ID, d.st.Projects[1].ID
	must(t, d.handle(ctx, proto.MoveSession{WorkspaceID: c, GroupID: g2, Before: b}))
	if w := d.workspace(c); w.ProjectID != g2 || ids()[1] != c {
		t.Fatalf("into group: %+v order %v", w, ids())
	}
	must(t, d.handle(ctx, proto.MoveGroup{GroupID: g2, Before: g1}))
	if d.st.Projects[0].ID != g2 {
		t.Fatal("group not moved first")
	}
	if err := d.handle(ctx, proto.MoveSession{WorkspaceID: c, Before: "missing"}); err == nil {
		t.Fatal("moving before an unknown session succeeded")
	}
}
