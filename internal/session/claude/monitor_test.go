package claude

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// TestMonitorWatch: a Monitor call runs on as a background shell does:
// each event goes on its row once, whether it came mid-turn or started a
// turn, and its end notice ends it.
func TestMonitorWatch(t *testing.T) {
	event := func(ev string) string {
		return `<task-notification>\n<task-id>b3ac8u92s</task-id>\n<summary>Monitor event: \"ticks\"</summary>\n<event>` + ev + `</event>\n</task-notification>`
	}
	end := `<task-notification>\n<task-id>b3ac8u92s</task-id>\n<tool-use-id>mon</tool-use-id>\n<output-file>/tmp/x/tasks/b3ac8u92s.output</output-file>\n<status>completed</status>\n<summary>Monitor \"ticks\" stream ended</summary>\n</task-notification>`
	tr := NewTranscript()
	snap := base()
	for _, l := range []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-11-20T17:19:40Z","origin":{"kind":"human"},"message":{"role":"user","content":"watch"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-20T17:19:41Z","message":{"id":"m1","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"mon","name":"Monitor","input":{"description":"ticks","command":"for i in 1 2; do echo tick $i; sleep 1; done"}}]}}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-11-20T17:19:42Z","message":{"role":"user","content":[{"tool_use_id":"mon","type":"tool_result","content":"Monitor started (task b3ac8u92s, expires in 30s unless the source ends first). You will be notified on each event."}]},"toolUseResult":{"taskId":"b3ac8u92s","timeoutMs":30000,"persistent":false}}`,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-20T17:19:43Z","content":"` + event("tick 1") + `"}`,
		`{"type":"attachment","uuid":"q1","timestamp":"2026-11-20T17:19:43Z","attachment":{"type":"queued_command","prompt":"` + event("tick 1") + `"}}`,
		`{"type":"system","subtype":"turn_duration","uuid":"d1","timestamp":"2026-11-20T17:19:44Z","durationMs":3000}`,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-20T17:19:45Z","content":"` + event("tick 2") + `"}`,
		`{"type":"user","uuid":"n1","timestamp":"2026-11-20T17:19:45Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"` + event("tick 2") + `"}}`,
		`{"type":"user","uuid":"n2","timestamp":"2026-11-20T17:19:46Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"` + end + `"}}`,
	} {
		snap = apply(t, snap, tr.Line([]byte(l)))
	}
	var mon *session.ToolCall
	for i, it := range snap.Content {
		switch {
		case it.ID == "mon":
			mon = snap.Content[i].Tool
		case it.Kind == session.KindSystem:
			t.Errorf("a row of its own: %q", it.Text)
		}
	}
	if mon == nil || mon.Background != "completed" {
		t.Fatalf("Monitor row %+v: want it ended", mon)
	}
	if _, events, _ := strings.Cut(mon.Output, "\n"); events != "tick 1\ntick 2" {
		t.Errorf("Monitor output %q: want each event once after the start line", mon.Output)
	}
}
