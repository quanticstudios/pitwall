package daemon

import (
	"context"
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
