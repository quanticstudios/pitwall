package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestPressFeedback: a reasoned denial moves the cursor onto the prompt's
// No, opens its field with Tab, types the reason on one line and sends
// it; a plan's "Tell Claude what to change" takes it without the Tab. A
// screen with no such option, or that moved on, presses nothing.
func TestPressFeedback(t *testing.T) {
	screen := func(file string) []string {
		b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	// The fake pane is 80 bytes wide: the plan's rows as Claude Code 2.1.329
	// draws them (claude-plan-feedback.txt), cut short.
	plan := []string{" Claude has written up a plan. Would you like to proceed?", " ❯ 1. Yes, and use auto mode",
		"   2. Yes, manually approve edits", "   3. Tell Claude what to change", "      shift+tab to approve with this feedback"}
	for _, tc := range []struct {
		name   string
		screen []string
		state  model.AgentState
		want   string
	}{
		{"permission", screen("claude-permission.txt"), model.StatePendingApproval, "\x1b[B\x1b[B\x1b[B\tuse echo instead\r"},
		{"plan", plan, model.StatePlanReady, "\x1b[B\x1b[Buse echo instead\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, f, _ := remoteDaemon(t)
			at := setForm(t, d, f, 0, "a", model.Activity{State: tc.state}, tc.screen...)
			if err := d.pressFeedback("a", at, " use echo\ninstead "); err != nil {
				t.Fatal(err)
			}
			if got := gotAfter(t, f.pane(0), 0, len(tc.want)); got != tc.want {
				t.Errorf("keys %q want %q", got, tc.want)
			}
		})
	}
	d, f, _ := remoteDaemon(t)
	at := setForm(t, d, f, 0, "a", model.Activity{State: model.StatePendingApproval}, screen("claude-idle.txt")...)
	if err := d.pressFeedback("a", at, "no"); err == nil || f.pane(0).got() != "" {
		t.Fatalf("an idle screen: %v, pressed %q", err, f.pane(0).got())
	}
	at = setForm(t, d, f, 0, "a", model.Activity{State: model.StatePendingApproval}, screen("claude-permission.txt")...)
	if err := d.pressFeedback("a", at-1, "no"); err == nil || f.pane(0).got() != "" {
		t.Fatalf("a prompt that moved on: %v, pressed %q", err, f.pane(0).got())
	}
}
