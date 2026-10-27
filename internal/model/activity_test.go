package model

import "testing"

// A finished turn reads PR open only while its tab's pull request is
// open, and sorts above a plain finished turn.
func TestAwaitPR(t *testing.T) {
	s := State{PRs: map[string]PR{"open": {State: PROpen}, "merged": {State: PRMerged}}}
	for _, tc := range []struct {
		ws         string
		state, out AgentState
	}{
		{"open", StateCompleted, StatePROpen},
		{"open", StateWorking, StateWorking},
		{"open", StateError, StateError},
		{"merged", StateCompleted, StateCompleted},
		{"none", StateCompleted, StateCompleted},
	} {
		if got := s.AwaitPR(Activity{WorkspaceID: tc.ws, State: tc.state}).State; got != tc.out {
			t.Errorf("%s %s: %s, want %s", tc.ws, tc.state, got, tc.out)
		}
	}
	a := []Activity{{State: StateCompleted}, {State: StatePROpen}}
	if Aggregate(a).State != StatePROpen || !NeedsYou(StatePROpen) || Tier(&a[1]) != TierActive || PillLabel(a[1]) != "PR open" {
		t.Error("PR open: want above Done, needs you, active, labeled PR open")
	}
}
