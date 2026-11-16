package errlog

import (
	"strings"
	"testing"
)

func TestLikelyFix(t *testing.T) {
	for _, tc := range []struct {
		e    Event
		want string // a part of the fix, "" for none
	}{
		{Event{Kind: Command, Output: "zsh: no matches found: --include=*.go"}, "quote globs"},
		{Event{Kind: Command, Output: "(eval):1: no matches found: --include=*.go"}, "quote globs"},
		{Event{Kind: Command, Output: "(eval):1: ===== not found"}, "echo '====='"},
		{Event{Kind: Command, Output: "zsh: defining function based on alias `ls'"}, "other names"},
		{Event{Kind: Command, Output: "zsh:1: command not found: rg"}, "rg is not installed"},
		{Event{Kind: Command, Output: "bash: line 1: fd: command not found"}, "fd is not installed"},
		{Event{Kind: Tool, Tool: "Edit", Output: "String to replace not found in file.\nString: foo"}, "stale read"},
		{Event{Kind: Command, Output: "cat: /tmp/claude-1/scratch/x.py: No such file or directory"}, "scratch file under /tmp"},
		{Event{Kind: Command, Output: "npm error Missing script: \"lint\""}, "scripts in package.json"},
		{Event{Kind: API, Output: "API Error: 400 The long context beta is not yet available for this subscription."}, "plan does not include"},
		{Event{Kind: API, Output: "API Error: 529 Overloaded"}, "rate limit"},
		{Event{Kind: API, Output: "API Error: 429 rate_limit_error"}, "rate limit"},
		{Event{Kind: Command, Output: "zsh:1: can't create temp file for here document: read-only file system"}, "use printf"},
		{Event{Kind: Command, Output: "Traceback (most recent call last):\n  File \"<stdin>\", line 1, in <module>\nModuleNotFoundError: No module named 'PIL'"}, "module PIL is not installed"},
		{Event{Kind: Command, Output: "magick not found"}, "magick was not found"},
		{Event{Kind: Command, Output: "GraphQL: API rate limit already exceeded for user ID 1."}, "polling gh"},
		{Event{Kind: Command, Output: "GraphQL: Projects (classic) is being deprecated in favor of the new Projects experience, see: https://example.com/x. (repository.pullRequest.projectCards)"}, "classic project cards"},
		{Event{Kind: Tool, Tool: "WebFetch", Output: "Claude Code is unable to fetch from example.com"}, "two refusals"},
		{Event{Kind: Tool, Tool: "WebFetch", Output: "API Error: 400 The following domains are not accessible to our user agent: [\"example.com\"]."}, "two refusals"},
		{Event{Kind: Command, Tool: "Bash", Command: "pkill -f 'pitwall daemon'", Exit: 144}, "pgrep"},
		{Event{Kind: Command, Tool: "Bash", Command: "pkill --full worker", Exit: 144}, "pgrep"},
		{Event{Kind: Command, Tool: "Bash", Command: "pkill -fx worker", Exit: 144}, "pgrep"},
		// Lookalikes that must get no hint.
		{Event{Kind: Command, Tool: "Bash", Command: "pkill -f worker", Exit: 144, Output: "worker refused shutdown"}, ""},
		{Event{Kind: Command, Tool: "Bash", Command: "printf '%s' 'pkill -f worker'; exit 144", Exit: 144, Output: "pkill -f worker"}, ""},
		{Event{Kind: Command, Tool: "Bash", Command: "printf '%s' 'pkill -f worker' >/dev/null; exit 144", Exit: 144}, ""},
		{Event{Kind: Command, Tool: "Bash", Command: "pkill 'worker -f'", Exit: 144}, ""},
		{Event{Kind: Command, Tool: "Bash", Command: "cat >/dev/null <<'EOF'\npkill -f worker\nEOF\nexit 144", Exit: 144}, ""},
		{Event{Kind: Command, Tool: "Bash", Command: "pkill -f 'pitwall daemon'", Exit: 1}, ""},
		{Event{Kind: Command, Tool: "Bash", Command: "pkill pitwall", Exit: 144}, ""},
		{Event{Kind: Command, Output: "zsh:1: can't create temp file for here document: no space left on device"}, ""},
		{Event{Kind: Command, Output: "error: unknown module named foo"}, ""},
		{Event{Kind: Command, Output: "--- FAIL: TestRateLimit (0.00s)\nexit status 1"}, ""},
		{Event{Kind: Command, Output: "cat: /home/u/notes.md: No such file or directory"}, ""},
		{Event{Kind: Command, Output: "zsh: foo not found"}, ""},
		{Event{Kind: Command, Output: "error: model not found in registry"}, ""},
	} {
		got := LikelyFix(Groups([]Event{tc.e})[0])
		if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("LikelyFix(%q) = %q, want %q", tc.e.Output, got, tc.want)
		}
	}
}
