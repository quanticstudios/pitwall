package model

import (
	"slices"
	"testing"
	"time"

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

func TestReportedAgentSummary(t *testing.T) {
	s := State{
		Workspaces: []Workspace{{ID: "w", SessionID: "s", Tabs: []Tab{{Layout: &layout.Node{Pane: "p"}}}}},
		Panes:      []Pane{{ID: "p", WorkspaceID: "w", Provider: "agent:My agent"}},
		Activities: []Activity{{PaneID: "p", WorkspaceID: "w", Provider: "agent:My agent", State: StateWorking}},
	}
	if !IsAgent("agent:My agent") || IsAgent("agent:") || IsAgent(ProviderTerminal) || ReportedName(ProviderCodex) != "" {
		t.Fatal("incorrect provider classification")
	}
	if panes := s.AgentPanes(s.Workspaces[0]); len(panes) != 1 || panes[0].Activity == nil {
		t.Fatalf("agent panes: %+v", panes)
	}
	if summary := s.Summary("s"); summary.Working != 1 || !slices.Equal(summary.Agents, []Provider{"agent:My agent"}) {
		t.Fatalf("summary: %+v", summary)
	}
	now := time.Unix(100, 0)
	s.Activities[0].UpdatedAt = now
	prev, _ := DecideNotifications(Notifications{}, s.Activities, false, "", now)
	s.Activities[0].State, s.Activities[0].Unseen = StateAwaitingInput, true
	s.Activities[0].UpdatedAt = now.Add(time.Second)
	if summary := s.Summary("s"); summary.NeedsYou != 1 {
		t.Fatalf("attention: %+v", summary)
	}
	if _, deliveries := DecideNotifications(prev, s.Activities, false, "", now.Add(5*time.Second)); len(deliveries) != 1 {
		t.Fatalf("notifications: %+v", deliveries)
	}
}
