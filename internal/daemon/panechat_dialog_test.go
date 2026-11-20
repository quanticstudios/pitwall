package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// /usage, /cost and /status sent from a pane chat become a chip and a
// card in the chat, and nothing is typed into Claude Code.
func TestPaneChatDialog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","uuid":"p1","timestamp":"2026-11-19T10:00:00Z","version":"2.1.329","cwd":"/tmp/demo","message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T10:00:01Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":4000,"cache_read_input_tokens":36000,"output_tokens":90}}}
{"type":"system","subtype":"turn_duration","uuid":"d1","timestamp":"2026-11-19T10:00:02Z"}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		m    proto.ChatSend
		want bool
	}{{proto.ChatSend{Text: " /usage "}, true}, {proto.ChatSend{Text: "/status"}, true}, {proto.ChatSend{Text: "/usage now"}, false}, {proto.ChatSend{Text: "/context"}, false}} {
		if got := dialogCommand(c.m); got != c.want {
			t.Errorf("dialogCommand(%q) = %v", c.m.Text, got)
		}
	}
	pc := &paneChat{id: "a", path: path, snap: session.Snapshot{SessionID: "a"}}
	pc.tr = pc.transcript()
	if !pc.dialog(paneLive{p: &fakePane{}}, "/cost") {
		t.Fatal("dialog: false, want a card")
	}
	n := len(pc.snap.Content)
	if n < 2 || pc.snap.Content[n-2].Phase != session.PhaseCommand || pc.snap.Content[n-1].Phase != session.PhaseOutput ||
		!strings.Contains(pc.snap.Content[n-1].Text, "claude-opus-5-5:  2 input, 90 output, 36k cache read, 4k cache write") {
		t.Fatalf("items %+v", pc.snap.Content)
	}
}
