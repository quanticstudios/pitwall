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
