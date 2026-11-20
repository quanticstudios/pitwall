package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// A Codex pane is a pane chat once its hooks name its rollout: the
// rollout's turns and calls, its model and effort, its approval as a
// request, and sends typed into the TUI.
func TestPaneChatCodex(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	rollout, err := os.ReadFile("../session/codex/testdata/rollout.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rollout-2026-11-20T18-42-57-01a1fa0d.jsonl")
	if err := os.WriteFile(path, rollout, 0o600); err != nil {
		t.Fatal(err)
	}
	// No hook named a rollout: no chat, whatever rollouts the folder holds.
	if r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "b"}); r.(proto.ChatResult).Code != "not-found" {
		t.Fatalf("codex pane without a transcript: %+v", r)
	}
	d.mu.Lock()
	d.st.Panes[1].Transcript = path
	d.mu.Unlock()

	snap, seq := paneRead(t, d, "b", session.Snapshot{}, 0)
	if snap.Provider != model.ProviderCodex || snap.Model == nil || *snap.Model != "gpt-6.2-sol" {
		t.Fatalf("provider %q, model %v", snap.Provider, snap.Model)
	}
	if len(snap.Turns) != 2 || snap.Turns[1].State != session.TurnInterrupted {
		t.Fatalf("turns %+v", snap.Turns)
	}
	var bash, edits int
	for _, it := range snap.Content {
		if it.Tool != nil {
			switch it.Tool.ToolName {
			case "Bash":
				bash++
			case "Edit", "Write":
				edits++
			}
		}
	}
	if bash < 2 || edits != 2 {
		t.Errorf("%d commands, %d edits in %d items", bash, edits, len(snap.Content))
	}
	// Codex asks on screen: a request that says what the hook said, and
	// Allow presses Codex's Y.
	if len(snap.PendingApprovals) != 1 {
		t.Fatalf("approvals %+v", snap.PendingApprovals)
	}
	before := len(f.pane(1).got())
	if r, _ := d.chatRequest(context.Background(), proto.ChatApprove{Chat: "b", Request: snap.PendingApprovals[0].ID, Decision: "allow"}); r.(proto.ChatResult).Code != "" {
		t.Fatalf("allow: %+v", r)
	}
	if got := gotAfter(t, f.pane(1), before, 1); !strings.EqualFold(got, "y") {
		t.Errorf("allow typed %q, want Y", got)
	}
	// Model, effort and mode stay in Codex's own terminal.
	if r, _ := d.chatRequest(context.Background(), proto.ChatSet{Chat: "b", Model: "gpt-6.2-sol"}); r.(proto.ChatResult).Code != "unsupported" {
		t.Errorf("set: %+v, want unsupported", r)
	}

	d.mu.Lock()
	d.st.Activities[d.activityIndex("b")].State = model.StateCompleted
	d.mu.Unlock()
	f.pane(1).mu.Lock()
	f.pane(1).screen = []string{"› "}
	f.pane(1).mu.Unlock()
	snap, seq = paneRead(t, d, "b", snap, seq)
	if len(snap.PendingApprovals) != 0 {
		t.Errorf("approvals after the prompt closed: %+v", snap.PendingApprovals)
	}
	before = len(f.pane(1).got())
	if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "b", Text: "run the tests"}); r.(proto.ChatResult).Code != "" {
		t.Fatalf("send: %+v", r)
	}
	if got := gotAfter(t, f.pane(1), before, len("run the tests")+1); !strings.Contains(got, "run the tests") || !strings.HasSuffix(got, "\r") {
		t.Errorf("send typed %q", got)
	}

	// The next turn's lines come in as deltas.
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"timestamp":"2026-11-20T17:50:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t3"}}
{"timestamp":"2026-11-20T17:50:00.100Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t3","item":{"type":"UserMessage","id":"u3","content":[{"type":"text","text":"run the tests"}]}}}
{"timestamp":"2026-11-20T17:50:02.000Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t3","item":{"type":"AgentMessage","id":"m3","content":[{"type":"Text","text":"All pass."}],"phase":"final_answer"}}}
{"timestamp":"2026-11-20T17:50:02.100Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t3"}}
`)
	fh.Close()
	snap, _ = paneRead(t, d, "b", snap, seq)
	if n := len(snap.Content); len(snap.Turns) != 3 || snap.Turns[2].State != session.TurnCompleted || snap.Content[n-1].Text != "All pass." {
		t.Errorf("after the third turn: turns %+v, last %+v", snap.Turns, snap.Content[len(snap.Content)-1])
	}
}

// When the agent leaves the pane's foreground, its transcript goes with
// it: a Codex started after it shows no chat until its own hook names one.
func TestPaneChatForgetsTranscript(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	d.mu.Lock()
	d.st.Panes[1].Transcript = filepath.Join(t.TempDir(), "rollout-old.jsonl")
	d.setProvider("b", "")
	d.setProvider("b", model.ProviderCodex)
	got := d.st.Panes[1].Transcript
	d.mu.Unlock()
	if got != "" {
		t.Errorf("transcript %q after codex left and came back", got)
	}
	if r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "b"}); r.(proto.ChatResult).Code != "not-found" {
		t.Errorf("read: %+v, want no chat", r)
	}
}
