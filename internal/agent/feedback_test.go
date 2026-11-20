package agent

import "testing"

// TestFeedback: a permission prompt takes its reason on No after a Tab, a
// plan on "Tell Claude what to change" as it is, an older plan's "No, keep
// planning" after a Tab; an idle screen or a question takes none.
func TestFeedback(t *testing.T) {
	for _, tc := range []struct {
		file           string
		target, cursor int
		tab, ok        bool
	}{
		{"claude-permission.txt", 3, 0, true, true},
		{"claude-permission-accept-edits.txt", 2, 0, true, true},
		{"claude-plan-feedback.txt", 2, 0, false, true},
		{"claude-plan.txt", 3, 0, true, true},
		{"claude-idle.txt", 0, 0, false, false},
		{"claude-question.txt", 0, 0, false, false},
	} {
		target, cursor, tab, ok := Feedback(screenFixture(t, tc.file))
		if target != tc.target || cursor != tc.cursor || tab != tc.tab || ok != tc.ok {
			t.Errorf("%s: got %d %d %v %v, want %d %d %v %v", tc.file, target, cursor, tab, ok, tc.target, tc.cursor, tc.tab, tc.ok)
		}
	}
	amended := gridOf(" Do you want to proceed?", "   1. Yes", " ❯ 2. No, and tell Claude what to do differently")
	if target, cursor, tab, ok := Feedback(amended); target != 1 || cursor != 1 || tab || !ok {
		t.Errorf("an open field: got %d %d %v %v", target, cursor, tab, ok)
	}
}
