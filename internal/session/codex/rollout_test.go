package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// readRollout reduces a rollout's lines as a pane chat does.
func readRollout(t *testing.T, b []byte) (*Rollout, session.Snapshot) {
	t.Helper()
	seq = 0
	r, s := NewRollout(), session.Snapshot{SessionID: "p", Provider: "codex", SessionState: session.StateReady}
	for line := range bytes.Lines(b) {
		for _, e := range r.Line(line) {
			s = applyEvent(t, s, e)
		}
	}
	return r, s
}

func applyEvent(t *testing.T, s session.Snapshot, e session.Event) session.Snapshot {
	t.Helper()
	if e.CreatedAt == "" {
		e.CreatedAt = session.Stamp(now())
	}
	rev := s.Revision + 1
	for _, d := range session.Reduce(s, e) {
		d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, s.SessionID, seq+1, rev, e.CreatedAt
		n, err := session.Apply(&s, seq, d)
		if err != nil {
			t.Fatalf("apply %s: %v", d.Type, err)
		}
		s, seq = n, d.Seq
	}
	return s
}

// seq is the last delta applyEvent applied; tests here run one at a time.
var seq int64

// TestRollout reads a real Codex 0.171 rollout, its personal lines cut,
// with an update_plan, an MCP call and an interrupted turn added in the
// shapes Codex logs them.
func TestRollout(t *testing.T) {
	b, err := os.ReadFile("testdata/rollout.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	r, s := readRollout(t, b)
	if r.Running() {
		t.Error("a turn still runs after the rollout's last turn was aborted")
	}
	if m, e := r.Settings(); m != "gpt-6.2-sol" || s.Model == nil || *s.Model != m {
		t.Errorf("model %q (snapshot %v), effort %q", m, s.Model, e)
	}
	if len(s.Turns) != 2 || s.Turns[0].State != session.TurnCompleted || s.Turns[1].State != session.TurnInterrupted {
		t.Fatalf("turns %+v", s.Turns)
	}
	if u := s.Usage; u == nil || u.ContextTokens == nil || *u.ContextTokens == 0 || u.MaxTokens == nil || *u.MaxTokens != 475000 {
		t.Errorf("usage %+v", s.Usage)
	}
	tools := map[string]*session.ToolCall{}
	var prompts, replies []string
	for _, it := range s.Content {
		switch it.Kind {
		case session.KindUserPrompt:
			prompts = append(prompts, it.Text)
		case session.KindAssistantText:
			replies = append(replies, it.Text)
		case session.KindToolCall:
			tools[it.Tool.ToolName+" "+it.Tool.Command+toolFile(it.Tool)] = it.Tool
		}
	}
	if len(prompts) != 2 || !strings.HasPrefix(prompts[0], "Do these in order.") || prompts[1] != "Now run the tests forever" {
		t.Errorf("prompts %q", prompts)
	}
	if len(replies) == 0 || !strings.Contains(replies[len(replies)-1], "- Ran `ls`") {
		t.Errorf("replies %q", replies)
	}
	if c := tools["Bash ls"]; c == nil || c.Status != "completed" || c.Output != "README.md\ncalc.py\n" || c.ExitCode == nil || *c.ExitCode != 0 {
		t.Errorf("ls: %+v", c)
	}
	if c := tools["Bash false"]; c == nil || c.Status != "failed" || c.ExitCode == nil || *c.ExitCode != 1 {
		t.Errorf("false: %+v", c)
	}
	if c := tools["Edit /tmp/pwcx/repo/calc.py"]; c == nil || !strings.Contains(c.Diff, "+    return a + b") || c.Files[0].Status != "update" {
		t.Errorf("edit: %+v", c)
	}
	if c := tools["Write /tmp/pwcx/repo/notes.txt"]; c == nil || !strings.Contains(string(c.Input), "returns the sum") {
		t.Errorf("new file: %+v", c)
	}
	if c := tools["WebSearch "]; c == nil || !strings.HasPrefix(c.Output, "Links: [") || !strings.Contains(c.Output, "https://go.dev/") {
		t.Errorf("web search: %+v", c)
	}
	if c := tools["mcp__docs__lookup "]; c == nil || c.Status != "failed" || !strings.Contains(c.Output, "not found") || c.StartedAt == c.EndedAt {
		t.Errorf("mcp: %+v", c)
	}
	plan := tools["Plan "]
	if plan == nil || len(plan.Tasks) != 3 || plan.Tasks[1].Status != "in_progress" || plan.Tasks[2].Subject != "Search the web" {
		t.Errorf("plan: %+v", plan)
	}
}

func toolFile(c *session.ToolCall) string {
	var in struct {
		Path string `json:"file_path"`
	}
	_ = json.Unmarshal(c.Input, &in)
	return in.Path
}

// TestRolloutEdges: empty, malformed and unknown lines show nothing, a
// prompt with no task_started starts a turn, and a huge output stays whole.
func TestRolloutEdges(t *testing.T) {
	r := NewRollout()
	for _, l := range []string{"", "{", `{"type":"event_msg","payload":{"type":"what"}}`, `{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"Reasoning","id":"r","summary_text":[],"raw_content":[]}}}`} {
		if es := r.Line([]byte(l)); len(es) != 0 {
			t.Errorf("%q: %+v", l, es)
		}
	}
	es := r.Line([]byte(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"u","content":[{"type":"text","text":"hi"}]}}}`))
	if len(es) != 2 || es[0].Type != session.EventTurnStarted || es[0].TurnID != "t1" || es[1].Kind != session.KindUserPrompt || !r.Running() {
		t.Errorf("bare prompt: %+v", es)
	}
	big := strings.Repeat("x", 1<<20)
	line, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed",
		"item": map[string]any{"type": "CommandExecution", "id": "c", "command": []string{"sh", "-c", "yes"}, "aggregated_output": big, "exit_code": 0, "status": "completed"}}})
	es = r.Line(line)
	if len(es) != 1 || es[0].Tool.Output != big || es[0].Tool.Command != "yes" {
		t.Errorf("huge output: %d events", len(es))
	}
	if es := r.Ask("1", "Run rm -rf build?"); len(es) != 1 || es[0].Type != session.EventRequestOpened {
		t.Errorf("ask: %+v", es)
	}
	if es := r.End(session.TurnCompleted); len(es) != 2 || es[0].Type != session.EventRequestResolved || es[1].Type != session.EventTurnCompleted {
		t.Errorf("end: %+v", es)
	}
}

func TestRolloutMarks(t *testing.T) {
	for l, want := range map[string]claude.Mark{
		`{"type":"event_msg","payload":{"type":"task_started"}}`:                           claude.MarkPrompt,
		`{"type":"event_msg","payload":{"type":"item_completed","item":{}}}`:               claude.MarkItem,
		`{"type":"response_item","payload":{"type":"function_call","name":"update_plan"}}`: claude.MarkItem,
		`{"type":"response_item","payload":{"type":"message"}}`:                            claude.MarkNone,
		`not json`: claude.MarkNone,
	} {
		if got := MarkOf([]byte(l)); got != want {
			t.Errorf("%s: %d, want %d", l, got, want)
		}
	}
}
