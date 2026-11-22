package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// TestPaneChatInterruptedCall: the machine shut down mid-call; after the
// reboot Claude Code resumed the session and appended the call's result as
// an interruption. The pane chat, reading the file again, ends the call.
func TestPaneChatInterruptedCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	write := func(lines ...string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			f.WriteString(l + "\n")
		}
		f.Close()
	}
	write(`{"type":"user","uuid":"p1","timestamp":"2026-11-21T23:29:50Z","origin":{"kind":"human"},"message":{"role":"user","content":"Shutdown this pc"}}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"p1","timestamp":"2026-11-21T23:29:59Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"systemctl poweroff","description":"Shut down the PC"}}]}}`)
	pc := &paneChat{id: "a", path: path, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(paneLive{p: &fakePane{}})
	// A new daemon after the reboot reads the file from the start.
	pc = &paneChat{id: "a", path: path, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.catchUp(paneLive{p: &fakePane{}})
	write(`{"type":"user","uuid":"r1","parentUuid":"a1","timestamp":"2026-11-22T16:21:30Z","toolDenialKind":"interrupted","sourceToolAssistantUUID":"a1","message":{"role":"user","content":[{"type":"tool_result","content":"[Tool call interrupted: the session ended before this call's result was recorded.]","is_error":true,"tool_use_id":"t1"}]},"toolUseResult":"[Tool call interrupted: the session ended before this call's result was recorded.]"}`,
		`{"type":"assistant","uuid":"a2","parentUuid":"r1","timestamp":"2026-11-22T16:21:30Z","message":{"id":"m2","model":"<synthetic>","content":[{"type":"text","text":"No response requested."}]}}`)
	pc.catchUp(paneLive{p: &fakePane{}})
	n := 0
	for _, it := range pc.snap.Content {
		if it.Tool != nil {
			n++
			if it.Tool.Status == "running" {
				t.Fatalf("call %s still running after its interrupted result", it.ID)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d calls, want 1", n)
	}
	for _, tu := range pc.snap.Turns {
		if tu.State == session.TurnRunning {
			t.Fatalf("turn %s still running after the resume", tu.ID)
		}
	}
}

// TestPaneChatCompacting: a /compact runs no turn and writes nothing until
// it ends; the pane chat shows Claude Code's "Compacting conversation…"
// from its spinner row, and drops it once the row goes.
func TestPaneChatCompacting(t *testing.T) {
	rule := "────────────────"
	fp := &fakePane{screen: []string{"❯ /compact", "", "✻ Compacting conversation… (5s)", "  ⎿  Tip: a tip", rule, "❯ ", rule}}
	pc := &paneChat{id: "a", prov: model.ProviderClaude, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	pc.live(paneLive{p: fp})
	if len(pc.snap.Live) != 1 || pc.snap.Live[0].Kind != session.KindSystem || pc.snap.Live[0].Text != "Compacting conversation…" {
		t.Fatalf("live %+v: want the compaction row", pc.snap.Live)
	}
	fp.mu.Lock()
	fp.screen = []string{"❯ /compact", "", rule, "❯ ", rule}
	fp.mu.Unlock()
	pc.live(paneLive{p: fp})
	if len(pc.snap.Live) != 0 {
		t.Fatalf("live %+v after the compaction: want none", pc.snap.Live)
	}
}
