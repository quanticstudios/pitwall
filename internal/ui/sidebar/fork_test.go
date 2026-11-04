package sidebar

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestForkPaneSelection(t *testing.T) {
	ws := model.Workspace{ID: "w", Tabs: []model.Tab{{Layout: &layout.Node{Children: []*layout.Node{{Pane: "first"}, {Pane: "second"}}}}}}
	for _, tc := range []struct {
		name, focus, session string
		provider             model.Provider
		want                 string
	}{
		{"first agent", "", "s", model.ProviderClaude, "first"},
		{"focused agent", "second", "s", model.ProviderClaude, "second"},
		{"unsupported", "first", "s", model.ProviderPi, ""},
		{"no session", "first", "", model.ProviderCodex, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := model.State{Workspaces: []model.Workspace{ws}, Panes: []model.Pane{
				{ID: "first", WorkspaceID: "w", Provider: tc.provider, SessionID: tc.session},
				{ID: "second", WorkspaceID: "w", Provider: model.ProviderCodex, SessionID: "second-session"},
			}}
			s := Sidebar{Pane: tc.focus}
			if got := s.forkPane(&st, ws); got != tc.want {
				t.Fatalf("fork pane = %q, want %q", got, tc.want)
			}
		})
	}
}
