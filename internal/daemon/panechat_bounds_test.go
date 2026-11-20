package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// paneTranscript points pane a of a remoteDaemon at a transcript holding
// s, and is its path and a ChatRead of the pane's chat.
func paneTranscript(t *testing.T, s string) (*Daemon, string, func(proto.ChatRead) proto.ChatDeltas) {
	t.Helper()
	d, _, _ := remoteDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()
	return d, path, func(m proto.ChatRead) proto.ChatDeltas {
		t.Helper()
		m.Chat = "a"
		r, _ := d.chatRequest(context.Background(), m)
		out, ok := r.(proto.ChatDeltas)
		if !ok {
			t.Fatalf("read %+v: %+v", m, r)
		}
		return out
	}
}

// bigTurn is one turn's prompt and n Bash calls with their results, each
// result out.
func bigTurn(n int, out string) string {
	o, _ := json.Marshal(out)
	var b strings.Builder
	b.WriteString(`{"type":"user","uuid":"p0","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"go"}}` + "\n")
	for i := range n {
		fmt.Fprintf(&b, `{"type":"assistant","uuid":"a%[1]d","timestamp":"2026-11-18T10:00:01Z","message":{"id":"m%[1]d","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t%[1]d","name":"Bash","input":{"command":"echo %[1]d"}}]}}
{"type":"user","uuid":"r%[1]d","timestamp":"2026-11-18T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%[1]d","content":%[2]s}]}}
`, i, o)
	}
	return b.String()
}

// TestPaneChatSubagentDeltas: a subagent's 600 calls leave deltas that
// stay small, each carrying a bounded share of the calls, and recent
// holds no more than read can serve; the Task still names every call.
func TestPaneChatSubagentDeltas(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	head := `{"type":"user","uuid":"p1","timestamp":"2026-11-19T20:40:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"count lines"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T20:40:01Z","message":{"id":"m1","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"ag","name":"Agent","input":{"description":"Count lines","subagent_type":"general-purpose"}}]}}
`
	os.WriteFile(path, []byte(head), 0o600)
	sub := filepath.Join(dir, "s", "subagents")
	os.MkdirAll(sub, 0o700)
	os.WriteFile(filepath.Join(sub, "agent-x1.meta.json"), []byte(`{"agentType":"general-purpose","toolUseId":"ag"}`), 0o600)
	var b strings.Builder
	pad := strings.Repeat("p", 400)
	for i := range 600 {
		fmt.Fprintf(&b, `{"type":"assistant","isSidechain":true,"agentId":"x1","timestamp":"2026-11-19T20:40:03Z","message":{"id":"s%[1]d","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"b%[1]d","name":"Bash","input":{"command":"wc -l notes%[1]d.txt %[2]s"}}]}}
{"type":"user","isSidechain":true,"agentId":"x1","timestamp":"2026-11-19T20:40:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"b%[1]d","content":"%[1]d notes.txt %[2]s"}]}}
`, i, pad)
	}
	os.WriteFile(filepath.Join(sub, "agent-x1.jsonl"), []byte(b.String()), 0o600)

	d, _, _ := remoteDaemon(t)
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()
	for range 2 { // the first read finds the Agent call, the second its subagent's file
		if r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a"}); r == nil {
			t.Fatal("no reply")
		}
	}
	pc, _, _ := d.paneChatOf("a")
	pc.mu.Lock()
	defer pc.mu.Unlock()
	total, biggest := 0, 0
	for _, dl := range pc.recent {
		j, _ := json.Marshal(dl)
		total, biggest = total+len(j), max(biggest, len(j))
	}
	if biggest > 64<<10 || total > 2*recentBytes+biggest {
		t.Fatalf("recent: %d deltas, %d bytes, the biggest %d bytes", len(pc.recent), total, biggest)
	}
	var task *session.ToolCall
	for _, it := range pc.snap.Content {
		if it.Tool != nil && it.Tool.ToolName == "Agent" {
			task = it.Tool
		}
	}
	if task == nil || len(task.SubagentTools) != 600 || len(task.SubagentCalls) == 0 || len(task.SubagentCalls) > session.SubagentCallsKept {
		t.Fatalf("task: %+v", task)
	}
	if last := task.SubagentCalls[len(task.SubagentCalls)-1]; last.CallID != "b599" || last.Status != "completed" {
		t.Fatalf("newest call %+v: want b599, completed", last)
	}
}

// TestChatRecentBytes: a managed chat's recent keeps what read can serve,
// by bytes as well as by count, and every delta a publish still owes.
func TestChatRecentBytes(t *testing.T) {
	c := &chat{id: "c", snap: session.Snapshot{SessionID: "c"}}
	big := strings.Repeat("x", 200<<10)
	add := func(i int) {
		c.reduce(session.Event{Type: session.EventContent, ItemID: fmt.Sprint("i", i), Kind: session.KindSystem, Text: big})
	}
	for i := range 100 {
		add(i)
		c.published = c.seq
	}
	if n := len(c.recent); n == 0 || n*len(big) > 2*recentBytes+len(big) {
		t.Fatalf("recent holds %d deltas of %d bytes", n, len(big))
	}
	for i := range 50 { // none published
		add(100 + i)
	}
	if int64(len(c.recent)) < c.seq-c.published {
		t.Fatalf("recent holds %d deltas, a publish owes %d", len(c.recent), c.seq-c.published)
	}
}
