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
	if es := r.Ask("1", "Run rm -rf build?"); len(es) != 1 || es[0].Type != session.EventRequestOpened || es[0].Request != nil {
		t.Errorf("ask: %+v", es)
	}
	// A hook's "tool\ncommand" is a command request the card shows whole.
	es = r.Ask("2", "Bash\ncurl -sI https://example.com")
	if len(es) != 2 || es[1].RequestType != session.RequestCommand || es[1].Request.Command != "curl -sI https://example.com" || es[1].Detail != "Bash: curl -sI https://example.com" {
		t.Errorf("ask a command: %+v", es)
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

// TestRolloutCompactResetsContext checks that Codex compaction events
// (ContextCompaction item, context_compacted, compacted, and zeroed
// last_token_usage) clear ContextTokens while keeping the window size and
// totals, rate-limit-only token_count (info: null) leaves context intact,
// and the next token_count restores the real count.
func TestRolloutCompactResetsContext(t *testing.T) {
	feed := func(s session.Snapshot, r *Rollout, line string) session.Snapshot {
		for _, e := range r.Line([]byte(line)) {
			s = applyEvent(t, s, e)
		}
		return s
	}
	seq = 0
	r := NewRollout()
	s := session.Snapshot{SessionID: "p", Provider: "codex", SessionState: session.StateReady}

	s = feed(s, r, `{"timestamp":"2026-11-22T16:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":410000,"cached_input_tokens":300000,"output_tokens":3000,"total_tokens":413000},"last_token_usage":{"input_tokens":410000,"cached_input_tokens":300000,"output_tokens":3000,"total_tokens":413000},"model_context_window":475000}}}`)
	if u := s.Usage; u == nil || u.ContextTokens == nil || *u.ContextTokens != 413000 || u.MaxTokens == nil || *u.MaxTokens != 475000 {
		t.Fatalf("before compact: %+v", u)
	}

	// Rate-limit-only token_count (info: null) must not clear context.
	s = feed(s, r, `{"timestamp":"2026-11-22T16:00:02Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":10}}}}`)
	if u := s.Usage; u == nil || u.ContextTokens == nil || *u.ContextTokens != 413000 {
		t.Fatalf("after info:null token_count: %+v", u)
	}

	// ContextCompaction item emits the divider and clears ContextTokens while
	// keeping totals and MaxTokens.
	s = feed(s, r, `{"timestamp":"2026-11-22T16:00:03Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"ContextCompaction","id":"cmp-1"}}}`)
	if u := s.Usage; u == nil || u.ContextTokens != nil || u.MaxTokens == nil || *u.MaxTokens != 475000 || u.InputTokens != 410000 {
		t.Fatalf("after ContextCompaction: %+v", u)
	}
	if n := len(s.Content); n == 0 || s.Content[n-1].Phase != session.PhaseCompact {
		t.Fatalf("content %+v: want compaction divider", s.Content)
	}

	// Next token_count restores the post-compaction usage.
	s = feed(s, r, `{"timestamp":"2026-11-22T16:00:05Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":435000,"cached_input_tokens":300000,"output_tokens":3500,"total_tokens":438500},"last_token_usage":{"input_tokens":25000,"cached_input_tokens":0,"output_tokens":500,"total_tokens":25500},"model_context_window":475000}}}`)
	if u := s.Usage; u == nil || u.ContextTokens == nil || *u.ContextTokens != 25500 || u.MaxTokens == nil || *u.MaxTokens != 475000 {
		t.Fatalf("after post-compact token_count: %+v", u)
	}

	// event_msg/context_compacted clears context too.
	s = feed(s, r, `{"timestamp":"2026-11-22T16:00:06Z","type":"event_msg","payload":{"type":"context_compacted"}}`)
	if u := s.Usage; u == nil || u.ContextTokens != nil || u.MaxTokens == nil || *u.MaxTokens != 475000 {
		t.Fatalf("after context_compacted: %+v", u)
	}

	// A zeroed last_token_usage in token_count also clears context.
	s = feed(s, r, `{"timestamp":"2026-11-22T16:00:07Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":460000,"cached_input_tokens":300000,"output_tokens":4000,"total_tokens":464000},"last_token_usage":{"input_tokens":25000,"cached_input_tokens":0,"output_tokens":500,"total_tokens":25500},"model_context_window":475000}}}`)
	s = feed(s, r, `{"timestamp":"2026-11-22T16:00:08Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":460000,"cached_input_tokens":300000,"output_tokens":4000,"total_tokens":464000},"last_token_usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0,"total_tokens":0},"model_context_window":475000}}}`)
	if u := s.Usage; u == nil || u.ContextTokens != nil || u.MaxTokens == nil || *u.MaxTokens != 475000 {
		t.Fatalf("after zeroed last_token_usage: %+v", u)
	}

	// Top-level compacted rollout record clears context too.
	s = feed(s, r, `{"timestamp":"2026-11-22T16:00:09Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":485000,"cached_input_tokens":300000,"output_tokens":4500,"total_tokens":489500},"last_token_usage":{"input_tokens":25000,"cached_input_tokens":0,"output_tokens":500,"total_tokens":25500},"model_context_window":475000}}}`)
	s = feed(s, r, `{"timestamp":"2026-11-22T16:00:10Z","type":"compacted","payload":{"message":"compacted"}}`)
	if u := s.Usage; u == nil || u.ContextTokens != nil || u.MaxTokens == nil || *u.MaxTokens != 475000 {
		t.Fatalf("after compacted record: %+v", u)
	}
}

// A prompt's pasted pictures come with it, and a picture sent alone is a
// prompt too.
func TestRolloutPromptImages(t *testing.T) {
	pic := t.TempDir() + "/pasted.png"
	os.WriteFile(pic, []byte("\x89PNG\r\n\x1a\n0000"), 0o600)
	for _, c := range []struct{ text, want string }{{"look", "look"}, {"", ""}} {
		content := []map[string]string{{"type": "local_image", "path": pic}, {"type": "local_image", "path": "/gone.png"}}
		if c.text != "" {
			content = append(content, map[string]string{"type": "text", "text": c.text})
		}
		line, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "turn_id": "t1",
			"item": map[string]any{"type": "UserMessage", "id": "u", "content": content}}})
		es := NewRollout().Line(line)
		if len(es) != 2 || es[1].Text != c.want || len(es[1].Attachments) != 1 || es[1].Attachments[0].MimeType != "image/png" {
			t.Errorf("%q: %+v", c.text, es)
		}
	}
}
