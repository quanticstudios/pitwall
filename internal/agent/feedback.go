package agent

import (
	"strings"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// Feedback finds where the Claude Code permission prompt or plan on g's
// screen takes a reason to deny it with: a permission prompt's No, which
// Tab turns into "No, and tell Claude what to do differently" with a field,
// or a plan's "Tell Claude what to change", a field already. It returns that
// option's index and the cursor's, and whether Tab opens the field; ok is
// false when the screen shows no such option under a cursor.
func Feedback(g vt.Grid) (target, cursor int, tab, ok bool) {
	opts := formOptions(screenRows(g))
	target, cursor = -1, -1
	for i, o := range opts {
		if o.cursor {
			if cursor >= 0 {
				return 0, 0, false, false
			}
			cursor = i
		}
		low := strings.ToLower(o.label)
		switch {
		case strings.HasPrefix(low, "tell claude"), strings.HasPrefix(low, "no") && strings.Contains(low, "tell claude"):
			target, tab = i, false
		case low == "no" || strings.HasPrefix(low, "no,") || strings.HasPrefix(low, "no "):
			target, tab = i, true
		}
	}
	if target < 0 || cursor < 0 {
		return 0, 0, false, false
	}
	return target, cursor, tab, true
}
