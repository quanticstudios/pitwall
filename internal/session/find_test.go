package session

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Find looks in prompts, replies, reasoning and plans, folded, oldest
// first, and past its limit keeps the newest matches.
func TestFind(t *testing.T) {
	plan, _ := json.Marshal(map[string]string{"plan": "# Plan\nAdd the Needle flag"})
	items := []Item{
		{ID: "p", Kind: KindUserPrompt, Text: "find the NEEDLE"},
		{ID: "out", Kind: KindCommandOutput, Text: "needle in output"},
		{ID: "bash", Kind: KindToolCall, Tool: &ToolCall{ToolName: "Bash", Input: json.RawMessage(`{"command":"grep needle"}`)}},
		{ID: "plan", Kind: KindToolCall, Tool: &ToolCall{ToolName: "ExitPlanMode", Input: plan}},
		{ID: "think", Kind: KindReasoningText, Text: "needle? needle."},
		{ID: "a", Kind: KindAssistantText, Text: "Ünïcode NEEDLE, nEeDlE"},
	}
	hits, more := Find(items, Fold("needle"), 100)
	got := fmt.Sprint(hits)
	want := "[{p [9] 15} {plan [15] 26} {think [0 8] 15} {a [8 16] 22}]"
	if got != want || more {
		t.Fatalf("Find = %s, %v; want %s", got, more, want)
	}
	if Matches(hits) != 6 {
		t.Errorf("Matches = %d", Matches(hits))
	}
	// Past the limit: the newest items whole, the older left out.
	hits, more = Find(items, Fold("needle"), 4)
	if fmt.Sprint(hits) != "[{think [0 8] 15} {a [8 16] 22}]" || !more {
		t.Errorf("limit 4: %v, %v", hits, more)
	}
	// The newest item comes whole even past the limit.
	hits, more = Find(items, Fold("needle"), 1)
	if fmt.Sprint(hits) != "[{a [8 16] 22}]" || !more {
		t.Errorf("limit 1: %v, %v", hits, more)
	}
	if hits, more := Find(items, nil, 10); hits != nil || more {
		t.Errorf("empty query: %v, %v", hits, more)
	}
	if at := FindRunes(Fold("aaaa"), Fold("aa")); fmt.Sprint(at) != "[0 2]" {
		t.Errorf("overlapping: %v", at)
	}
}
