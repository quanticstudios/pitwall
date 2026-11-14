package model

import "testing"

func TestFinishedWorkerAgents(t *testing.T) {
	w := Workspace{ID: "w", Parent: "parent"}
	s := State{
		Panes: []Pane{
			{ID: "agent", WorkspaceID: "w"},
			{ID: "shell", WorkspaceID: "w"},
		},
		Activities: []Activity{{PaneID: "agent", WorkspaceID: "w", Provider: ProviderCodex, State: StateCompleted}},
	}
	if !s.FinishedWorker(w) {
		t.Fatal("agent known through activity was not Done beside a shell")
	}
	s.Panes = append(s.Panes, Pane{ID: "new", WorkspaceID: "w", Provider: ProviderClaude})
	if s.FinishedWorker(w) {
		t.Fatal("agent without a final turn counted as Done")
	}
	s.Panes = s.Panes[:2]
	s.Activities = nil
	if s.FinishedWorker(w) {
		t.Fatal("tab without agents counted as Done")
	}
}
