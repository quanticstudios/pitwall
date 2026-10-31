package session

import (
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestReconcile: a snapshot cut off mid-turn, waiting on an approval and
// an input with a tool call running, comes out with nothing running or
// answerable, through deltas alone, and a second pass changes nothing.
func TestReconcile(t *testing.T) {
	s := running(base(model.ProviderClaude), "t1")
	s.Turns = append([]TurnSummary{{ID: "t0", Provider: s.Provider, StartedAt: "t", CompletedAt: ptr("t"), State: TurnCompleted}}, s.Turns...)
	s.Content = []Item{
		{ID: "a", Kind: KindAssistantText, Text: "done", TurnID: ptr("t0")},
		{ID: "b", Kind: KindToolCall, TurnID: ptr("t1"), Tool: &ToolCall{CallID: "b", ToolName: "Bash", Status: "running", Output: "half"}},
		{ID: "c", Kind: KindToolCall, TurnID: ptr("t1"), Tool: &ToolCall{CallID: "c", ToolName: "Read", Status: "completed"}},
	}
	s.PendingApprovals = []Approval{{ID: "r1", RequestType: RequestCommand}}
	s.PendingUserInputs = []InputRequest{{ID: "q1", Questions: []Question{}}}
	s.SessionState, s.Detail = StateWaiting, ptr("Run ls?")

	ds := Reconcile(s, "now")
	var types []DeltaType
	n := s
	for i, d := range ds {
		types = append(types, d.Type)
		d.Version, d.SessionID, d.Seq, d.Revision = DeltaVersion, s.SessionID, int64(i+1), 1
		var err error
		if n, err = Apply(&n, int64(i), d); err != nil {
			t.Fatalf("%s: %v", d.Type, err)
		}
	}
	if want := []DeltaType{DeltaToolEnd, DeltaApprovalResolve, DeltaInputResolve, DeltaTurn, DeltaStatus}; !slices.Equal(types, want) {
		t.Fatalf("deltas %v, want %v", types, want)
	}
	if n.Turns[0].State != TurnCompleted || n.Turns[1].State != TurnInterrupted || *n.Turns[1].CompletedAt != "now" {
		t.Fatalf("turns %+v", n.Turns)
	}
	if n.Content[1].Tool.Status != "failed" || n.Content[1].Tool.Output != "half" || n.Content[2].Tool.Status != "completed" {
		t.Fatalf("tools %+v, %+v", n.Content[1].Tool, n.Content[2].Tool)
	}
	if len(n.PendingApprovals)+len(n.PendingUserInputs) != 0 || n.SessionState != StateReady || n.Running || n.Detail != nil {
		t.Fatalf("after: pending %v %v, state %s, running %v, detail %v", n.PendingApprovals, n.PendingUserInputs, n.SessionState, n.Running, n.Detail)
	}
	if again := Reconcile(n, "later"); again != nil {
		t.Fatalf("a second pass: %+v", again)
	}
}

// TestReconcileStopped: a session that ended stopped or in error keeps its
// state; only what still runs changes.
func TestReconcileStopped(t *testing.T) {
	s := base(model.ProviderCodex)
	s.SessionState, s.Running = StateError, false
	if ds := Reconcile(s, "now"); ds != nil {
		t.Fatalf("idle session: %+v", ds)
	}
	ds := Reconcile(running(s, "t1"), "now")
	if len(ds) != 1 || ds[0].Type != DeltaTurn || ds[0].Turn.State != TurnInterrupted {
		t.Fatalf("deltas %+v, want the turn alone", ds)
	}
}
