package panel

import (
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/widget"

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

// newAsk is a question card's state as settle makes it.
func newAsk(qs []session.Question) *ask {
	a := &ask{moved: make([]bool, len(qs)), active: -1, other: make([]widget.Editor, len(qs))}
	for _, q := range qs {
		var m []bool
		if q.Multi {
			m = make([]bool, len(q.Options))
		}
		a.pick, a.multi = append(a.pick, -1), append(a.multi, m)
		a.opts = append(a.opts, make([]widget.Clickable, len(q.Options)+1))
	}
	return a
}

// TestMultiKeys: the number keys act on one question at a time. They
// toggle a multiSelect question's options until Enter moves on; a
// single-select pick moves them to the next question; a click makes its
// question theirs.
func TestMultiKeys(t *testing.T) {
	qs := []session.Question{
		{ID: "f", Multi: true, Options: []session.PromptOption{{Label: "Apple"}, {Label: "Banana"}, {Label: "Cherry"}}},
		{ID: "l", Options: []session.PromptOption{{Label: "Sidebar"}, {Label: "Top bar"}}},
		{ID: "s", Options: []session.PromptOption{{Label: "Small"}, {Label: "Large"}}},
	}
	tr := &Transcript{}
	tr.init()
	tr.askQs = map[string][]session.Question{"q": qs}
	a := newAsk(qs)
	tr.asks["q"], tr.keyAsk = a, "q"
	enter := string(key.NameReturn)
	for _, k := range []string{"1", "3"} {
		tr.Key(k)
	}
	if !a.multi[0][0] || a.multi[0][1] || !a.multi[0][2] || a.current() != 0 {
		t.Fatalf("checks %v, keys on %d: want Apple and Cherry, keys still on the first", a.multi[0], a.current())
	}
	tr.Key("3") // toggles Cherry off: the keys stay until Enter
	tr.Key("3")
	tr.Key(enter)
	if a.current() != 1 {
		t.Fatalf("keys on %d after Enter: want the second question", a.current())
	}
	tr.Key("2")
	if a.pick[1] != 1 || a.current() != 2 {
		t.Fatalf("pick %v, keys on %d: want Top bar, then the keys on the third", a.pick, a.current())
	}
	a.choose(1, 0) // a click on Sidebar: its question is answered, the keys go on
	if a.pick[1] != 0 || a.current() != 2 {
		t.Fatalf("after a click: pick %v, keys on %d", a.pick, a.current())
	}
	a.choose(0, 1) // a click into the multiSelect question takes the keys there
	if a.current() != 0 || !a.multi[0][1] {
		t.Fatalf("after a click into the first: keys on %d, checks %v", a.current(), a.multi[0])
	}
	tr.Key("2") // Banana off again
	tr.Key(enter)
	tr.Key("1")
	ans, ok := a.answers(qs)
	if !ok || len(ans["f"]) != 2 || ans["l"][0] != "Sidebar" || ans["s"][0] != "Small" {
		t.Fatalf("answers %v %v", ans, ok)
	}
	if keyFor(false, 0) != "" || keyFor(true, 0) != "1" || keyFor(true, 9) != "" {
		t.Error("keyFor")
	}
}

// TestQuietSettings: each change has its own divider with the model's
// name; a repeat of the same change, or one naming what the chat already
// runs, shows none.
func TestQuietSettings(t *testing.T) {
	sonnet, opus := "claude-sonnet-5-5", "claude-opus-5-5"
	set := func(text string) session.Item {
		return session.Item{Kind: session.KindSystem, Phase: session.PhaseSetting, Text: text}
	}
	c := []session.Item{
		{Kind: session.KindAssistantText, Model: &sonnet},
		set("Switched to claude-sonnet-5-5"), // 1: what it runs already
		set("Switched to claude-opus-5-5"),   // 2: a change
		set("Switched to Opus 5.5"),          // 3: the echo of the same change
		set("Plan mode"),                     // 4
		set("Plan mode"),                     // 5: again
		{Kind: session.KindAssistantText, Model: &opus},
		set("Switched to opus-5-5Switched to claude-opus-5-5"), // 7: glued, and no change
		set("Effort set to high"),                              // 8
	}
	q := quietSettings(c)
	for i, want := range map[int]bool{1: true, 2: false, 3: true, 4: false, 5: true, 7: true, 8: false} {
		if q[i] != want {
			t.Errorf("item %d %q: quiet %v, want %v", i, c[i].Text, q[i], want)
		}
	}
	if got := settingLabel("Switched to opus-5-5Switched to claude-opus-5-5"); got != "Switched to Opus 5.5" {
		t.Errorf("glued label %q", got)
	}
}
