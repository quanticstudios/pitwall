package app

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Closing a pinned tab, or its last pane, asks first; anything else does not.
func TestPinnedClose(t *testing.T) {
	st := &model.State{
		Workspaces: []model.Workspace{{ID: "pin", Pinned: true}, {ID: "free"}, {ID: "split", Pinned: true}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "pin"}, {ID: "b", WorkspaceID: "free"}, {ID: "c", WorkspaceID: "split"}, {ID: "d", WorkspaceID: "split"}},
	}
	for _, tc := range []struct {
		msg  any
		asks bool
	}{
		{proto.CloseTab{WorkspaceID: "pin"}, true},
		{proto.KillSession{WorkspaceID: "pin"}, true},
		{proto.ClosePane{Pane: "a"}, true},
		{proto.ClosePane{Pane: "c"}, false}, // the tab keeps pane d
		{proto.CloseTab{WorkspaceID: "free"}, false},
		{proto.ClosePane{Pane: "b"}, false},
		{proto.RenameTab{WorkspaceID: "pin"}, false},
	} {
		if got := pinnedClose(st, tc.msg) != nil; got != tc.asks {
			t.Errorf("%#v: asks %v, want %v", tc.msg, got, tc.asks)
		}
	}
}
