package session

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// step reduces e on s, applies the deltas as the chat loop would, and
// returns the result and the delta types.
func step(t *testing.T, s Snapshot, e Event) (Snapshot, []DeltaType) {
	t.Helper()
	var types []DeltaType
	seq := int64(1)
	for _, d := range Reduce(s, e) {
		types = append(types, d.Type)
		d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = DeltaVersion, s.SessionID, seq+1, s.Revision+1, e.CreatedAt
		if d.Snapshot != nil {
			d.Snapshot.Revision, d.Snapshot.UpdatedAt = d.Revision, d.UpdatedAt
		}
		n, err := Apply(&s, seq, d)
		if err != nil {
			t.Fatalf("%s: %v", d.Type, err)
		}
		s, seq = n, seq+1
	}
	return s, types
}

func base(p model.Provider) Snapshot {
	return Snapshot{SessionID: "c1", PaneID: "c1", WorkspaceID: "w1", Provider: p, SessionState: StateRunning, Running: true, RuntimeGeneration: 1}
}

func running(s Snapshot, id string) Snapshot {
	s.Turns = append(s.Turns, TurnSummary{ID: id, Provider: s.Provider, StartedAt: "t0", State: TurnRunning})
	return s
}

func TestReduceExit(t *testing.T) {
	for _, tc := range []struct {
		code      *int
		turnState string
		state     string
	}{
		{nil, TurnInterrupted, StateStopped},
		{ptr(0), TurnInterrupted, StateStopped},
		{ptr(1), TurnFailed, StateError},
	} {
		s := running(base(model.ProviderClaude), "t1")
		s.PendingApprovals = []Approval{{ID: "r1", RequestType: RequestPlan}}
		s.PlanApproval = &PlanApproval{ID: "r1"}
		n, _ := step(t, s, Event{Type: EventExited, CreatedAt: "t9", ExitCode: tc.code})
		if n.Turns[0].State != tc.turnState || n.Turns[0].CompletedAt == nil || *n.Turns[0].CompletedAt != "t9" {
			t.Errorf("exit %v: turn %+v, want %s at t9", tc.code, n.Turns[0], tc.turnState)
		}
		if n.SessionState != tc.state || n.Running || len(n.PendingApprovals) != 0 || n.PlanApproval != nil {
			t.Errorf("exit %v: state %s running %v approvals %v plan %v", tc.code, n.SessionState, n.Running, n.PendingApprovals, n.PlanApproval)
		}
	}
}

func TestReduceTurnStartedBindsPrompts(t *testing.T) {
	s := running(base(model.ProviderClaude), "t1")
	s.Content = []Item{
		{ID: "p1", Kind: KindUserPrompt, TurnID: ptr("t1"), UpdatedAt: "a"},
		{ID: "p2", Kind: KindUserPrompt, TurnID: ptr("t1"), UpdatedAt: "b"}, // sent mid-turn, held by the agent
		{ID: "p3", Kind: KindUserPrompt, UpdatedAt: "c"},                    // sent between turns
	}
	s.Turns[0].State = TurnCompleted
	n, _ := step(t, s, Event{Type: EventTurnStarted, CreatedAt: "t5", TurnID: "t2", QueuedSendCount: 1})
	var got []string
	for _, it := range n.Content {
		got = append(got, *it.TurnID+"@"+it.UpdatedAt)
	}
	if want := []string{"t1@a", "t2@b", "t2@t5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("prompts %v, want %v", got, want)
	}
	if n.SessionState != StateRunning || len(n.Turns) != 2 || n.Turns[1].State != TurnRunning {
		t.Errorf("state %s turns %+v", n.SessionState, n.Turns)
	}
}

func TestReduceCoalescesAssistantText(t *testing.T) {
	for _, tc := range []struct {
		provider model.Provider
		items    int
	}{
		{model.ProviderClaude, 1},
		{model.ProviderCodex, 2}, // Codex messages stay apart
	} {
		s := running(base(tc.provider), "t1")
		s, _ = step(t, s, Event{Type: EventContent, CreatedAt: "1", Kind: KindAssistantText, ItemID: "a", Text: "One."})
		s, types := step(t, s, Event{Type: EventContent, CreatedAt: "2", Kind: KindAssistantText, ItemID: "b", Text: " Two."})
		if len(s.Content) != tc.items {
			t.Errorf("%s: %d items (%v), want %d", tc.provider, len(s.Content), types, tc.items)
		}
		if tc.items == 1 && (s.Content[0].Text != "One. Two." || types[0] != DeltaItemTextAppend) {
			t.Errorf("%s: %q via %v", tc.provider, s.Content[0].Text, types)
		}
	}
}

func TestReduceTool(t *testing.T) {
	call := func(status string, exit *int, cmd string) *ToolCall {
		return &ToolCall{CallID: "c", Command: cmd, ExitCode: exit, Input: json.RawMessage(`{}`), Status: status, ToolName: "Bash"}
	}
	s := running(base(model.ProviderClaude), "t1")
	s, _ = step(t, s, Event{Type: EventContent, CreatedAt: "1", Kind: KindToolCall, ItemID: "c", Tool: call("running", nil, "ls")})
	for _, tc := range []struct {
		tool *ToolCall
		want []DeltaType
	}{
		{call("failed", nil, "ls"), []DeltaType{DeltaToolEnd}},
		{call("running", nil, "ls -a"), []DeltaType{DeltaItemPatch}},
		{call("completed", ptr(2), "ls -a"), []DeltaType{DeltaItemPatch}}, // with another change, one patch
	} {
		e := Event{Type: EventContent, CreatedAt: "2", Kind: KindToolCall, ItemID: "c", Tool: tc.tool}
		n, types := step(t, s, e)
		if !reflect.DeepEqual(types, tc.want) || !reflect.DeepEqual(n.Content[0].Tool, tc.tool) || n.Content[0].UpdatedAt != "2" {
			t.Errorf("%+v: %v gives %+v, want %v", tc.tool, types, n.Content[0], tc.want)
		}
	}
}

func TestReduceRequestReopened(t *testing.T) {
	s := running(base(model.ProviderCodex), "t1")
	s, _ = step(t, s, Event{Type: EventRequestOpened, CreatedAt: "1", RequestID: "r", Detail: "one"})
	n, types := step(t, s, Event{Type: EventRequestOpened, CreatedAt: "2", RequestID: "r", Detail: "two"})
	if !reflect.DeepEqual(types, []DeltaType{DeltaSnapshot}) || len(n.PendingApprovals) != 1 || *n.PendingApprovals[0].Detail != "two" {
		t.Errorf("%v: %+v", types, n.PendingApprovals)
	}
	n, _ = step(t, n, Event{Type: EventRequestResolved, CreatedAt: "3", RequestID: "r"})
	if n.SessionState != StateRunning || n.Detail != nil {
		t.Errorf("after resolve: %s %v", n.SessionState, n.Detail)
	}
}

func TestReduceIgnores(t *testing.T) {
	for _, e := range []Event{{Type: EventLimit, Limit: &Limit{}}, {Type: EventResumeID, ResumeID: "x"}, {Type: "something.new"}} {
		if ds := Reduce(base(model.ProviderClaude), e); ds != nil {
			t.Errorf("%s: %v", e.Type, ds)
		}
	}
}

func TestSummarize(t *testing.T) {
	m := func(model string, in int64, cost *float64) ModelUsage {
		return ModelUsage{Model: ptr(model), InputTokens: in, OutputTokens: 1, CachedInputTokens: ptr[int64](in), EstimatedCostUSD: cost}
	}
	reports := []UsageReport{
		// One scope reports cumulative totals: the larger counts once.
		{ID: "1", ScopeID: "q1", Provider: "claude", RuntimeGeneration: 1, Models: []ModelUsage{m("a", 10, ptr(0.1))}, EstimatedCostUSD: ptr(0.1)},
		{ID: "2", ScopeID: "q1", Provider: "claude", RuntimeGeneration: 1, Models: []ModelUsage{m("a", 30, ptr(0.3))}, EstimatedCostUSD: ptr(0.3)},
		// Another scope adds, and its unknown cost leaves the total incomplete.
		{ID: "3", ScopeID: "q2", Provider: "claude", RuntimeGeneration: 1, Models: []ModelUsage{m("a", 5, nil)}},
	}
	got := summarize(reports)
	if got.InputTokens != 35 || got.OutputTokens != 2 || *got.CachedInputTokens != 35 || got.EstimatedCostUSD != 0.3 || got.IsCostComplete || len(got.Models) != 1 || got.Models[0].EstimatedCostUSD != nil {
		b, _ := json.Marshal(got)
		t.Errorf("summary %s", b)
	}
}
