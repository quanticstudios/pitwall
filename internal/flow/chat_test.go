package flow

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	managed "github.com/quanticstudios/pitwall/internal/session"
)

// TestChat: a snapshot's turns become the Feed's, with their calls and
// last reply, and the latest task list is the plan.
func TestChat(t *testing.T) {
	str := func(s string) *string { return &s }
	done := "2026-10-05T10:01:00.000Z"
	s := managed.Snapshot{Provider: model.ProviderClaude,
		Turns: []managed.TurnSummary{
			{ID: "t1", RequestText: "Fix the build\nplease", StartedAt: "2026-10-05T10:00:00.000Z", CompletedAt: &done, State: managed.TurnCompleted},
			{ID: "t2", StartedAt: "2026-10-05T10:02:00.000Z", State: managed.TurnRunning},
		},
		Content: []managed.Item{
			{ID: "p1", Kind: managed.KindUserPrompt, Text: "Fix the build\nplease", TurnID: str("t1")},
			{ID: "c1", Kind: managed.KindToolCall, TurnID: str("t1"), CreatedAt: "2026-10-05T10:00:05.000Z",
				Tool: &managed.ToolCall{ToolName: "Bash", Command: "go build ./...\necho", Status: "failed"}},
			{ID: "c2", Kind: managed.KindToolCall, TurnID: str("t1"), UpdatedAt: "2026-10-05T10:00:09.000Z",
				Tool: &managed.ToolCall{ToolName: "Read", Input: json.RawMessage(`{"file_path":"/a/b/main.go"}`), Status: "completed"}},
			{ID: "c3", Kind: managed.KindToolCall, TurnID: str("t1"), Tool: &managed.ToolCall{ToolName: "TodoWrite", Status: "completed",
				Tasks: []managed.Task{{Subject: "build", Status: "completed"}, {Subject: "test", Status: "in_progress"}}}},
			{ID: "a1", Kind: managed.KindAssistantText, Text: "First.", TurnID: str("t1")},
			{ID: "a2", Kind: managed.KindAssistantText, Text: "Fixed.", TurnID: str("t1")},
			{ID: "p2", Kind: managed.KindUserPrompt, Text: "now the tests", TurnID: str("t2")},
			{ID: "c4", Kind: managed.KindToolCall, TurnID: str("t2"), Tool: &managed.ToolCall{ToolName: "Edit", Status: "running",
				Files: []managed.ToolFile{{Path: `C:\src\x.go`}}}},
		},
	}
	f := Chat(s)
	if f.Provider != model.ProviderClaude || len(f.Turns) != 2 {
		t.Fatalf("feed %+v", f)
	}
	t1, t2 := f.Turns[0], f.Turns[1]
	if t1.Prompt != "Fix the build" || t1.Reply != "Fixed." || !t1.Start.Equal(time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)) || t1.End.Sub(t1.Start) != time.Minute {
		t.Fatalf("turn 1: %+v", t1)
	}
	if len(t1.Calls) != 3 || !t1.Calls[0].Time.Equal(time.Date(2026, 10, 5, 10, 0, 5, 0, time.UTC)) || !t1.Calls[1].Time.Equal(time.Date(2026, 10, 5, 10, 0, 9, 0, time.UTC)) {
		t.Fatalf("turn 1 calls: %+v", t1.Calls)
	}
	checkCalls(t, "turn 1", t1.Calls, []Call{
		{Tool: "Bash", Arg: "go build ./...", Failed: true},
		{Tool: "Read", Arg: "main.go"},
		{Tool: "TodoWrite"},
	})
	if t2.Prompt != "now the tests" || !t2.End.IsZero() {
		t.Fatalf("turn 2: %+v", t2)
	}
	checkCalls(t, "turn 2", t2.Calls, []Call{{Tool: "Edit", Arg: "x.go", Running: true}})
	if len(f.Plan) != 2 || f.Plan[0] != (Step{Text: "build", State: StepDone}) || f.Plan[1] != (Step{Text: "test", State: StepActive}) {
		t.Fatalf("plan %+v", f.Plan)
	}
	if Chat(managed.Snapshot{}).Plan != nil {
		t.Fatal("a chat with no task list has a plan")
	}
}
