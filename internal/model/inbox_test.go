package model

import "testing"

func TestOrchestratorSkipsWorkers(t *testing.T) {
	if !Orchestrator(Workspace{}) {
		t.Error("a tab the user opened is an orchestrator")
	}
	if Orchestrator(Workspace{Parent: "tab-1"}) {
		t.Error("a tab a pane started is a worker, not an orchestrator")
	}
	if Orchestrator(Workspace{Parent: "tab-1", RemoteParent: true}) {
		t.Error("a remote worker is not an orchestrator")
	}
}

func TestInboxDoneStates(t *testing.T) {
	for _, s := range []AgentState{StateCompleted, StatePROpen} {
		if !InboxDone(Activity{State: s}) {
			t.Errorf("%v should list in the inbox", s)
		}
	}
	for _, s := range []AgentState{StateWorking, StateAwaitingInput, StateError} {
		if InboxDone(Activity{State: s}) {
			t.Errorf("%v should not list in the inbox", s)
		}
	}
}
