package panel

import (
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

func TestSummaryCut(t *testing.T) {
	for _, c := range []struct {
		in, want string
		cut      bool
	}{
		{"", "", false},
		{"one line", "one line", false},
		{"a\nb\nc\nd", "a\nb\nc", true},
		{"```\n```", "", false},
		{strings.Repeat("x", 1000), strings.Repeat("x", 280) + "…", true},
	} {
		if got, cut := summaryCut(c.in, 3, 280); got != c.want || cut != c.cut {
			t.Errorf("summaryCut(%.20q) = %.30q, %v; want %.30q, %v", c.in, got, cut, c.want, c.cut)
		}
	}
}

func TestCompactParts(t *testing.T) {
	if ti, sum := compactParts("Conversation compacted"); ti != "Conversation compacted" || sum != "" {
		t.Errorf("no summary: %q %q", ti, sum)
	}
	if ti, sum := compactParts("Conversation compacted\n\n1. Did things."); ti != "Conversation compacted" || sum != "1. Did things." {
		t.Errorf("summary: %q %q", ti, sum)
	}
}

func TestTurnUsage(t *testing.T) {
	turn, other := "t1", "t2"
	m := "claude-sonnet-5-5"
	c1, c2 := 0.25, 0.5
	cached := int64(1000)
	s := &session.Snapshot{UsageReports: []session.UsageReport{
		{TurnID: &turn, EstimatedCostUSD: &c1, Models: []session.ModelUsage{{Model: &m, InputTokens: 10, OutputTokens: 20, CachedInputTokens: &cached, EstimatedCostUSD: &c1}}},
		{TurnID: &other, EstimatedCostUSD: &c2},
	}}
	u := turnUsage(s, "t1")
	if u.in != 1010 || u.out != 20 || !u.priced || u.cost != 0.25 || len(u.lines) != 1 || u.lines[0] != "Sonnet 5.5: 10 in · 20 out · 1.0K cached · $0.25" {
		t.Errorf("usage %+v", u)
	}
	if u := turnUsage(s, ""); u.priced || u.in != 0 {
		t.Errorf("no turn: %+v", u)
	}
	s.UsageReports[0].EstimatedCostUSD = nil // a model without a price
	if u := turnUsage(s, "t1"); u.priced {
		t.Errorf("unpriced turn says it is priced: %+v", u)
	}
	if dollars(0.001) != "<$0.01" || dollars(1.234) != "$1.23" || count(999) != "999" || count(12_400) != "12.4K" || count(3_500_000) != "3.5M" {
		t.Error("dollars or count")
	}
}

func TestThinkTook(t *testing.T) {
	at := func(s int) string { return session.Stamp(time.Date(2026, 11, 19, 10, 0, s, 0, time.UTC)) }
	s := &session.Snapshot{Content: []session.Item{
		{ID: "p", UpdatedAt: at(0)},
		{ID: "whole", CreatedAt: at(7), UpdatedAt: at(7)},      // came in one line: timed from the item before
		{ID: "streamed", CreatedAt: at(8), UpdatedAt: at(20)},  // first event to last
		{ID: "broken", CreatedAt: "not a time", UpdatedAt: ""}, // malformed
	}}
	if d := thinkTook(s, 1); d != 7*time.Second {
		t.Errorf("whole: %v", d)
	}
	if d := thinkTook(s, 2); d != 12*time.Second {
		t.Errorf("streamed: %v", d)
	}
	if d := thinkTook(s, 3); d != 0 {
		t.Errorf("malformed: %v", d)
	}
	if thoughtFor(0, false) != "Thought for 1s" || thoughtFor(5*time.Second, true) != "Thinking" {
		t.Error("thoughtFor")
	}
}

func TestLiveTodo(t *testing.T) {
	tasks := []session.Task{{Status: "completed", Subject: "a"}, {Status: "in_progress", Subject: "b", ActiveForm: "Doing b"}}
	s := &session.Snapshot{Running: true, SessionState: session.StateRunning, Content: []session.Item{
		{ID: "x", Tool: &session.ToolCall{ToolName: "TodoWrite", Tasks: tasks}},
		{ID: "y", Tool: &session.ToolCall{ToolName: "Bash"}},
	}}
	if got := liveTodo(s); len(got) != 2 || taskText(got[1]) != "Doing b" || taskText(got[0]) != "a" {
		t.Errorf("running: %+v", got)
	}
	s.SessionState = session.StateReady
	if liveTodo(s) != nil {
		t.Error("an idle chat shows the strip")
	}
	s.SessionState = session.StateRunning
	s.Content[0].Tool.Tasks = []session.Task{{Status: "completed"}}
	if liveTodo(s) != nil {
		t.Error("a finished list shows the strip")
	}
	if isTodo(&session.ToolCall{ToolName: "TodoWrite"}) || !isTodo(&session.ToolCall{Tasks: []session.Task{}}) || isTodo(nil) {
		t.Error("isTodo")
	}
}

func TestAgentTook(t *testing.T) {
	at := func(s int) string { return session.Stamp(time.Date(2026, 11, 19, 10, 0, s, 0, time.UTC)) }
	now := time.Date(2026, 11, 19, 10, 1, 0, 0, time.UTC)
	running := &session.Item{CreatedAt: at(0), Tool: &session.ToolCall{Status: "running", StartedAt: at(10)}}
	done := &session.Item{CreatedAt: at(0), UpdatedAt: at(30), Tool: &session.ToolCall{Status: "completed", StartedAt: at(10), EndedAt: at(25)}}
	bad := &session.Item{Tool: &session.ToolCall{Status: "completed"}}
	if agentTook(running, now) != 50*time.Second || agentTook(done, now) != 15*time.Second || agentTook(bad, now) != 0 {
		t.Errorf("agentTook: %v %v %v", agentTook(running, now), agentTook(done, now), agentTook(bad, now))
	}
	if agentCalls(&session.ToolCall{SubagentTools: []string{"Read", "Grep"}}) != 2 {
		t.Error("agentCalls")
	}
}

func TestSettingAndLimit(t *testing.T) {
	if settingLabel("Switched to claude-opus-5-5") != "Switched to Opus 5.5" || settingLabel("Plan mode") != "Plan mode" {
		t.Error("settingLabel")
	}
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.Local)
	soon := session.Stamp(now.Add(3 * time.Hour))
	if ti, msg := limitNotice(&session.UsageLimit{Label: "Weekly limit", ResetsAt: &soon}, now); ti != "Weekly limit reached" || msg != "Resets 13:00." {
		t.Errorf("limit: %q %q", ti, msg)
	}
	if _, msg := limitNotice(&session.UsageLimit{Label: "Usage limit"}, now); msg != "Claude stopped until the limit resets." {
		t.Errorf("no reset time: %q", msg)
	}
	if dec, ok := planKey("2"); !ok || dec != "deny" {
		t.Error("planKey 2")
	}
	if _, ok := planKey("3"); ok {
		t.Error("planKey 3")
	}
}

func TestTurnOf(t *testing.T) {
	t1 := "t1"
	s := &session.Snapshot{Content: []session.Item{{ID: "p", TurnID: &t1}, {ID: "a", Kind: session.KindAssistantText}, {ID: "b", Kind: session.KindAssistantText}}}
	if turnOf(s, &s.Content[2]) != "t1" || turnOf(s, &s.Content[0]) != "t1" {
		t.Error("turnOf")
	}
	if lastReply(s, &s.Content[1]) || !lastReply(s, &s.Content[2]) {
		t.Error("lastReply")
	}
	if turnOf(&session.Snapshot{Content: []session.Item{{ID: "x"}}}, &session.Item{}) != "" {
		t.Error("an item not in the chat has a turn")
	}
}
