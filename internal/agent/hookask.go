package agent

import (
	"regexp"
	"strings"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// hookAsk is Claude Code's line over a permission prompt a PreToolUse
// hook asked for: "Hook PreToolUse:Bash requires confirmation for this
// command:", the hook's reason on the │ rows under it.
var hookAsk = regexp.MustCompile(`^│\s*Hook (\w+):\S+ requires confirmation`)

// hookSource is the settings file a hook came from, which Claude Code
// puts after its reason: [settings], [project], [user].
var hookSource = regexp.MustCompile(`\s*\[[a-z ]+\]$`)

// HookReason is why a hook made Claude Code ask on g's permission prompt,
// "PreToolUse hook: <its reason>", "" when no hook asked or it gave none.
// It is how a bypass session that asks says why.
func HookReason(g vt.Grid) string {
	rows := screenRows(g)
	for i, r := range rows {
		m := hookAsk.FindStringSubmatch(strings.TrimSpace(r))
		if m == nil {
			continue
		}
		var why []string
		for _, n := range rows[i+1:] {
			t, ok := strings.CutPrefix(strings.TrimSpace(n), "│")
			if !ok {
				break
			}
			if t = strings.TrimSpace(t); t != "" {
				why = append(why, t)
			}
		}
		reason := hookSource.ReplaceAllString(strings.Join(why, " "), "")
		if reason == "" {
			return ""
		}
		return m[1] + " hook: " + reason
	}
	return ""
}
