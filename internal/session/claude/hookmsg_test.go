package claude

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// TestHookMessage: a hook's systemMessage is a notice row, from the
// transcript's attachment and from stream-json's informational line.
func TestHookMessage(t *testing.T) {
	tr := NewTranscript()
	es := tr.Line([]byte(`{"type":"attachment","uuid":"h1","timestamp":"2026-11-20T17:41:00Z","isSidechain":false,"attachment":{"type":"hook_system_message","content":"Lint passed: 0 warnings","hookName":"PostToolUse:Bash","toolUseID":"toolu_1","hookEvent":"PostToolUse","level":"info"}}`))
	if len(es) != 1 || es[0].Phase != session.PhaseNotice || es[0].Text != "PostToolUse:Bash says: Lint passed: 0 warnings" {
		t.Errorf("transcript hook message %+v", es)
	}
	if es := tr.Line([]byte(`{"type":"attachment","uuid":"h2","timestamp":"2026-11-20T17:41:00Z","attachment":{"type":"hook_success","hookName":"PostToolUse:Bash","content":"","stdout":"{}","exitCode":0}}`)); len(es) != 0 {
		t.Errorf("a hook that only succeeded shows %+v", es)
	}
	s := newStream("")
	es = s.line([]byte(`{"type":"system","subtype":"informational","content":"PostToolUse:Bash says: Lint passed: 0 warnings","level":"notice","uuid":"i1","session_id":"S"}`))
	if len(es) != 1 || es[0].Phase != session.PhaseNotice || es[0].Text != "PostToolUse:Bash says: Lint passed: 0 warnings" {
		t.Errorf("stream-json hook message %+v", es)
	}
}
