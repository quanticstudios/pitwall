package panel

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// TestMonitorRow: a Monitor call is a watch its row tracks as a shell's,
// and shows the events it saw, not the line that started it.
func TestMonitorRow(t *testing.T) {
	c := &session.ToolCall{ToolName: "Monitor", Output: "Monitor started (task b3ac8u92s, expires in 30s). You will be notified on each event.\ntick 1\ntick 2"}
	if got := shellID(c); got != "b3ac8u92s" {
		t.Errorf("shellID = %q", got)
	}
	if got := monitorEvents(c.Output); got != "tick 1\ntick 2" {
		t.Errorf("monitorEvents = %q", got)
	}
	if shellID(&session.ToolCall{ToolName: "Read", Output: c.Output}) != "" {
		t.Error("a Read is no watch")
	}
}
