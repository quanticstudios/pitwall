package agent

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// Claude Code 2.1.329's prompt for a PreToolUse hook that returns ask,
// under --dangerously-skip-permissions: the hook's reason, its source cut.
func TestHookReason(t *testing.T) {
	screen := ` Bash command
 Print asktest-one
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 echo asktest-one
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 │ Hook PreToolUse:Bash requires confirmation for this command:
 │ The ask-hook wants a human to approve asktest commands [settings]
 settings.json to update hooks

 Do you want to proceed?
 ❯ 1. Yes
   2. No

 Esc to cancel · Tab to amend`
	for _, tc := range []struct{ text, want string }{
		{screen, "PreToolUse hook: The ask-hook wants a human to approve asktest commands"},
		{strings.Replace(screen, " │ The ask-hook wants a human to approve asktest commands [settings]\n", "", 1), ""},
		{" Bash command\n echo hi\n Do you want to proceed?\n ❯ 1. Yes\n   2. No", ""},
	} {
		e := vt.New(100, 20, nil)
		e.Write([]byte(strings.ReplaceAll(tc.text, "\n", "\r\n")))
		if got := HookReason(e.Snapshot()); got != tc.want {
			t.Errorf("HookReason = %q, want %q", got, tc.want)
		}
	}
}
