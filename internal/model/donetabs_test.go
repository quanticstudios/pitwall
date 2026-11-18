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

func TestTurnDoneStates(t *testing.T) {
	for _, s := range []AgentState{StateCompleted, StatePROpen} {
		if !TurnDone(Activity{State: s}) {
			t.Errorf("%v should show a notice", s)
		}
	}
	for _, s := range []AgentState{StateWorking, StateAwaitingInput, StateError} {
		if TurnDone(Activity{State: s}) {
			t.Errorf("%v should not show a notice", s)
		}
	}
}

func TestWaitsOnUserAndNoticeBody(t *testing.T) {
	for _, s := range []AgentState{StateAwaitingInput, StatePendingApproval, StatePlanReady, StateError} {
		if !WaitsOnUser(Activity{State: s}) {
			t.Errorf("%v waits on the user", s)
		}
	}
	if WaitsOnUser(Activity{State: StateCompleted}) || WaitsOnUser(Activity{State: StateWorking}) {
		t.Error("done and working do not wait on the user")
	}
	if got := NoticeBody(Activity{State: StatePendingApproval, Detail: "Bash: rm -rf build\nmore"}); got != "Approval needed · Bash: rm -rf build" {
		t.Errorf("body = %q", got)
	}
	if got := NoticeBody(Activity{State: StateCompleted}); got != "Done" {
		t.Errorf("body = %q", got)
	}
}
