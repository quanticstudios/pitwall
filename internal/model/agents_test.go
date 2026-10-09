package model

import (
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
)

// TestAgentPanes lists a tab's agent panes in layout order with their
// activities: one agent, two agents beside a shell, and none.
func TestAgentPanes(t *testing.T) {
	split := &layout.Node{Children: []*layout.Node{{Pane: "b"}, {Pane: "a"}, {Pane: "sh"}}}
	s := State{
		Workspaces: []Workspace{
			{ID: "one", Tabs: []Tab{{Layout: &layout.Node{Pane: "o"}}}},
			{ID: "two", Tabs: []Tab{{Layout: split}}},
			{ID: "none", Tabs: []Tab{{Layout: &layout.Node{Pane: "run"}}}},
		},
		Panes: []Pane{
			{ID: "o", WorkspaceID: "one", Provider: ProviderClaude},
			{ID: "a", WorkspaceID: "two", Provider: ProviderClaude},
			{ID: "sh", WorkspaceID: "two"},
			{ID: "b", WorkspaceID: "two"}, // known from its activity only
			{ID: "run", WorkspaceID: "none"},
		},
		Activities: []Activity{
			{PaneID: "a", WorkspaceID: "two", Provider: ProviderClaude, State: StatePendingApproval},
			{PaneID: "b", WorkspaceID: "two", Provider: ProviderCodex, State: StateWorking},
			{PaneID: "run", WorkspaceID: "none", Provider: ProviderTerminal, State: StateTerminalRunning},
		},
	}
	for i, want := range [][]string{{"o:"}, {"b:working", "a:pending-approval"}, nil} {
		var got []string
		for _, ap := range s.AgentPanes(s.Workspaces[i]) {
			st := AgentState("")
			if ap.Activity != nil {
				st = ap.Activity.State
			}
			got = append(got, ap.Pane.ID+":"+string(st))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", s.Workspaces[i].ID, got, want)
		}
	}
}
