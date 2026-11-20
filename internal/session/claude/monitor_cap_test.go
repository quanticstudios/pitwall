package claude

import (
	"fmt"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// A long watch keeps its newest events, not every event it ever saw: each
// event's delta carries the whole call.
func TestMonitorOutputCapped(t *testing.T) {
	s := newStream("")
	s.tasks, s.bg = map[string]string{}, map[string]session.ToolCall{}
	s.tasks["t1"] = "mon"
	s.bg["mon"] = session.ToolCall{ToolName: "Monitor", Output: "Monitor started"}
	pad := strings.Repeat("x", 90)
	var last []session.Event
	for i := range 20000 {
		last = s.monitorEvent("t1", fmt.Sprintf("event %05d %s", i, pad))
	}
	out := last[0].Tool.Output
	if len(out) > monitorKeep {
		t.Errorf("output is %d bytes after 20,000 events, want at most %d", len(out), monitorKeep)
	}
	if !strings.HasSuffix(out, "event 19999 "+pad) || !strings.HasPrefix(out, "event ") {
		t.Errorf("output does not keep whole newest lines: starts %q, ends %q", out[:20], out[len(out)-20:])
	}
}
