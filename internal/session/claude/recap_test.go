package claude

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// The line shapes in these tests are Claude Code 2.1.329's, from runs in
// a throwaway repo: a /recap, a recap line, a Monitor watch, a hook's
// message.

// TestRecap: Claude Code's recap, as its away_summary system line or as
// /recap's output, is one recap item without the hint on turning recaps
// off; /recap keeps its chip, as a chat tab's prompt does.
func TestRecap(t *testing.T) {
	tr := NewTranscript()
	snap := base()
	for _, l := range []string{
		`{"type":"user","uuid":"r0","timestamp":"2026-11-20T17:20:37Z","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>The command below was run directly in Claude Code, not sent to you as a request.</local-command-caveat>"}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-11-20T17:20:37Z","message":{"role":"user","content":"<command-name>/recap</command-name>\n            <command-message>recap</command-message>\n            <command-args></command-args>"}}`,
		`{"type":"system","subtype":"local_command","uuid":"r2","timestamp":"2026-11-20T17:20:38Z","level":"info","content":"<local-command-stdout>You asked me to run a series of tool checks. All of them finished.</local-command-stdout>"}`,
		`{"type":"system","subtype":"away_summary","uuid":"r3","timestamp":"2026-11-20T17:40:00Z","isMeta":false,"content":"You asked for three echo calls. All three ran. (turn off recaps in /config)"}`,
	} {
		snap = apply(t, snap, tr.Line([]byte(l)))
	}
	var recaps []string
	for _, it := range snap.Content {
		switch {
		case it.Phase == session.PhaseRecap:
			recaps = append(recaps, it.Text)
		case it.Phase == session.PhaseCommand && it.Text == "/recap":
		case it.Kind == session.KindSystem:
			t.Errorf("a %q row besides the recaps: %q", it.Phase, it.Text)
		}
	}
	want := []string{"You asked me to run a series of tool checks. All of them finished.", "You asked for three echo calls. All three ran."}
	if strings.Join(recaps, "|") != strings.Join(want, "|") {
		t.Fatalf("recaps %q, want %q", recaps, want)
	}

	s := newStream("")
	es := s.line([]byte(`{"type":"system","subtype":"away_summary","content":"Waiting on CI. (turn off recaps in /config)","uuid":"u9","session_id":"S"}`))
	if len(es) != 1 || es[0].Phase != session.PhaseRecap || es[0].Text != "Waiting on CI." || es[0].ItemID != "u9" {
		t.Fatalf("stream-json recap %+v", es)
	}
}
