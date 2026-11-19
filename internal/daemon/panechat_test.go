package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// paneRead reads pane chat id after seq after and applies it to snap.
func paneRead(t *testing.T, d *Daemon, id string, snap session.Snapshot, after int64) (session.Snapshot, int64) {
	t.Helper()
	r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: id, After: after})
	out, ok := r.(proto.ChatDeltas)
	if !ok {
		t.Fatalf("read %s: %+v", id, r)
	}
	if out.Snapshot != nil {
		snap = session.Snapshot{}
		if err := json.Unmarshal(out.Snapshot, &snap); err != nil {
			t.Fatal(err)
		}
	}
	seq := out.Seq
	for _, b := range out.Deltas {
		var dl session.Delta
		if err := json.Unmarshal(b, &dl); err != nil {
			t.Fatal(err)
		}
		n, err := session.Apply(&snap, seq, dl)
		if err != nil {
			t.Fatal(err)
		}
		snap, seq = n, dl.Seq
	}
	return snap, seq
}

func TestPaneChat(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"list files"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-18T10:00:01Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	// Pane b runs Codex: no pane chat, so the read falls through to the chats.
	if r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "b"}); r.(proto.ChatResult).Code != "not-found" {
		t.Fatalf("codex pane read: %+v", r)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.st.Panes[0].AgentMode = "bypassPermissions" // cdang's: the prompt on screen hides the footer
	at := d.st.Activities[d.activityIndex("a")].UpdatedAt.UnixNano()
	d.mu.Unlock()

	// Attaching mid-session: the history so far, and the permission prompt
	// on screen as a card for the Bash call it asks about.
	snap, seq := paneRead(t, d, "a", session.Snapshot{}, 0)
	if len(snap.Content) < 2 || snap.Content[0].Kind != session.KindUserPrompt || snap.Content[0].Text != "list files" {
		t.Fatalf("content %+v: want the prompt first", snap.Content)
	}
	if len(snap.PendingApprovals) != 1 || snap.PendingApprovals[0].ID != strconv.FormatInt(at, 10) ||
		snap.PendingApprovals[0].Request == nil || snap.PendingApprovals[0].Request.ToolName != "Bash" {
		t.Fatalf("approvals %+v: want the Bash call's, under the activity's time", snap.PendingApprovals)
	}

	if snap.PermissionMode != "bypassPermissions" {
		t.Errorf("mode %q: want the hooks' bypassPermissions until the footer shows", snap.PermissionMode)
	}

	// Allow presses 1, Deny Esc, on the pane's prompt.
	before := len(f.pane(0).got())
	if r, _ := d.chatRequest(context.Background(), proto.ChatApprove{Chat: "a", Request: snap.PendingApprovals[0].ID, Decision: "allow"}); r.(proto.ChatResult).Code != "" {
		t.Fatalf("allow: %+v", r)
	}
	if got := gotAfter(t, f.pane(0), before, 1); got != "1" {
		t.Errorf("allow typed %q, want 1", got)
	}
	before = len(f.pane(0).got())
	d.chatRequest(context.Background(), proto.ChatApprove{Chat: "a", Request: snap.PendingApprovals[0].ID, Decision: "deny"})
	if got := gotAfter(t, f.pane(0), before, 1); got != "\x1b" {
		t.Errorf("deny typed %q, want Esc", got)
	}

	// The tool finishes and the turn ends: deltas after seq bring it in.
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateWorking
	d.mu.Unlock()
	more := `{"type":"user","uuid":"r1","timestamp":"2026-11-18T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a.go"}]}}
{"type":"system","subtype":"turn_duration","timestamp":"2026-11-18T10:00:03Z"}
`
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(more)
	fh.Close()
	snap, seq = paneRead(t, d, "a", snap, seq)
	if len(snap.PendingApprovals) != 0 || len(snap.Turns) != 1 || snap.Turns[0].State != session.TurnCompleted {
		t.Fatalf("after the turn: approvals %+v, turns %+v", snap.PendingApprovals, snap.Turns)
	}
	if snap2, seq2 := paneRead(t, d, "a", snap, seq); seq2 != seq || len(snap2.Content) != len(snap.Content) {
		t.Errorf("a read with nothing new moved seq %d to %d", seq, seq2)
	}

	// Esc stops a running turn; idle, it presses nothing.
	before = len(f.pane(0).got())
	d.chatRequest(context.Background(), proto.ChatInterrupt{Chat: "a"})
	if got := gotAfter(t, f.pane(0), before, 1); got != "\x1b" {
		t.Errorf("interrupt typed %q, want Esc", got)
	}
	// Sent mid-turn, a message waits in the chat's queue until the next
	// prompt line takes it.
	if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "then this"}); r.(proto.ChatResult).Message != "queued" {
		t.Fatalf("busy send: %+v", r)
	}
	if snap, seq = paneRead(t, d, "a", snap, seq); len(snap.QueuedInputs) != 1 || snap.QueuedInputs[0].Text != "then this" {
		t.Fatalf("queue %+v: want the busy send", snap.QueuedInputs)
	}
	fh, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"type":"user","uuid":"p2","timestamp":"2026-11-18T10:01:00Z","message":{"role":"user","content":"then this"}}` + "\n")
	fh.Close()
	if snap, seq = paneRead(t, d, "a", snap, seq); len(snap.QueuedInputs) != 0 || len(snap.Turns) != 2 {
		t.Fatalf("after its prompt: queue %+v, %d turns", snap.QueuedInputs, len(snap.Turns))
	}

	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateCompleted
	d.mu.Unlock()
	before = len(f.pane(0).got())
	d.chatRequest(context.Background(), proto.ChatInterrupt{Chat: "a"})
	if got := f.pane(0).got()[before:]; got != "" {
		t.Errorf("idle interrupt typed %q", got)
	}

	// Shift+Tab in the chat is one Shift+Tab in the TUI, whatever mode the
	// chat guessed comes next: the TUI's own cycle decides.
	before = len(f.pane(0).got())
	d.chatRequest(context.Background(), proto.ChatSet{Chat: "a", Mode: "default"})
	if got := gotAfter(t, f.pane(0), before, 3); got != "\x1b[Z" {
		t.Errorf("mode press typed %q, want Shift+Tab", got)
	}

	// Claude exits: its hooks' process leaves, the provider goes, and so
	// does the pane chat.
	d.mu.Lock()
	d.setProvider("a", "")
	d.mu.Unlock()
	if r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a"}); r.(proto.ChatResult).Code != "not-found" {
		t.Fatalf("read after exit: %+v", r)
	}
	d.mu.Lock()
	_, kept := d.paneChats["a"]
	d.mu.Unlock()
	if kept {
		t.Error("the pane chat outlived its claude")
	}
}

// TestPaneChatSubagents: a subagent's calls come from its own transcript,
// which its meta file ties to the Agent call, even once it has ended.
func TestPaneChatSubagents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	pc := &paneChat{id: "a", path: path, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	for _, l := range []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-11-19T20:40:00Z","origin":{"kind":"human"},"message":{"role":"user","content":"count lines"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T20:40:01Z","message":{"id":"m1","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"ag","name":"Agent","input":{"description":"Count lines","subagent_type":"general-purpose"}}]}}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-11-19T20:40:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"ag","content":[{"type":"text","text":"Async agent launched successfully."}]}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"x1"}}`,
		// It ended before the chat read its transcript, as on a replay.
		`{"type":"user","uuid":"n1","timestamp":"2026-11-19T20:40:09Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<task-id>x1</task-id>\n<tool-use-id>ag</tool-use-id>\n<status>completed</status>\n<summary>Agent finished</summary>\n<result>2 notes.txt</result>\n</task-notification>"}}`,
	} {
		pc.reduce(pc.tr.Line([]byte(l)))
	}
	sub := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sub, "agent-x1.meta.json"), []byte(`{"agentType":"general-purpose","toolUseId":"ag"}`), 0o600)
	os.WriteFile(filepath.Join(sub, "agent-x1.jsonl"), []byte(
		`{"type":"user","isSidechain":true,"agentId":"x1","timestamp":"2026-11-19T20:40:02Z","message":{"role":"user","content":"Run wc -l notes.txt"}}
{"type":"assistant","isSidechain":true,"agentId":"x1","timestamp":"2026-11-19T20:40:03Z","message":{"id":"s1","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"wc -l notes.txt"}}]}}
{"type":"user","isSidechain":true,"agentId":"x1","timestamp":"2026-11-19T20:40:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"b1","content":"2 notes.txt"}]}}
`), 0o600)
	pc.subagents()
	var calls []session.ToolCall
	for _, it := range pc.snap.Content {
		if it.Tool != nil && it.Tool.ToolName == "Agent" {
			calls = it.Tool.SubagentCalls
		}
	}
	if len(calls) != 1 || calls[0].ToolName != "Bash" || calls[0].Status != "completed" {
		t.Fatalf("subagent calls %+v: want its Bash call, done", calls)
	}
	pc.subagents() // read again: nothing new, nothing twice
	for _, it := range pc.snap.Content {
		if it.Tool != nil && it.Tool.ToolName == "Agent" && len(it.Tool.SubagentCalls) != 1 {
			t.Fatalf("a second read added calls: %+v", it.Tool.SubagentCalls)
		}
	}
}
