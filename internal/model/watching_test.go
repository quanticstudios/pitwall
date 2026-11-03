package model

import "testing"

func TestWatchingSummary(t *testing.T) {
	for _, tt := range []struct {
		name               string
		root, worker       AgentState
		rootOff, workerOff bool
		watching           bool
	}{
		{"working worker", StateCompleted, StateWorking, false, false, true},
		{"connecting worker", StateCompleted, StateConnecting, false, false, true},
		{"done worker", StateCompleted, StateCompleted, false, false, false},
		{"detached worker", StateCompleted, StateWorking, false, true, false},
		{"detached root", StateCompleted, StateWorking, true, false, false},
		{"root error", StateError, StateWorking, false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := State{
				Workspaces: []Workspace{{ID: "o", SessionID: "s", Detached: tt.rootOff}, {ID: "w", SessionID: "s", Parent: "o", Detached: tt.workerOff}},
				Activities: []Activity{
					{WorkspaceID: "o", State: tt.root, Unseen: true},
					{WorkspaceID: "w", Provider: ProviderCodex, State: tt.worker},
				},
			}
			if got := st.Watching()["o"]; got != tt.watching {
				t.Fatalf("Watching = %v, want %v", got, tt.watching)
			}
			if sum := st.Summary("s"); tt.watching && (sum.NeedsYou != 0 || sum.Unseen != 0 || sum.Working != 1) {
				t.Fatalf("Watching summary: %+v", sum)
			}
		})
	}
}
