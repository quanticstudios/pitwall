package app

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

func TestSidebarFork(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, cfg: config.Settings{Profiles: map[string]config.Profile{"work": {}}}}
	st := b.State()
	u.sidebarEvent(&st, sidebar.ForkPane{Pane: "remote-pane"})
	if got := b.Sent(); len(got) != 1 || got[0] != (proto.ForkPane{Pane: "remote-pane"}) || u.modal.kind != modalNone {
		t.Fatalf("fork messages %v, modal %v", got, u.modal.kind)
	}
}

func TestDeleteSharedWorktree(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b}
	b.tree = proto.WorktreeInfo{Query: proto.WorktreeQuery{WorkspaceID: "w4"}, UsedBy: []string{"side question"}}
	if !u.deleteForce("w4") {
		t.Fatal("shared folder needs confirmation")
	}
	st := b.State()
	u.modal.kind = modalDelete
	u.modal.ws = "w4"
	u.confirmModal(&st)
	if got := b.Sent(); len(got) != 1 || got[0] != (proto.DeleteWorkspace{WorkspaceID: "w4", Force: true}) {
		t.Fatalf("delete %v", got)
	}
}
