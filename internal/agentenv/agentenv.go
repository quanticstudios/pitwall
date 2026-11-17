// Package agentenv removes the session markers an agent or a terminal sets
// for its own children, so an agent pitwall starts runs as a session of its
// own instead of a child of whatever started the daemon.
package agentenv

import (
	"os"
	"slices"
	"strings"
)

// Markers name one session each, never a setting. Claude Code sets the
// first seven on every command it runs and strips them itself before it
// starts a new session; CLAUDE_CODE_CHILD_SESSION turns off transcript
// saving. CLAUDE_CODE_ENTRYPOINT says how the parent was started and
// CLAUDE_CODE_SSE_PORT points at the parent's IDE connection. Codex sets
// its four on the commands it runs. The rest are terminal session ids.
var Markers = []string{
	"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_PID", "CLAUDE_EFFORT", "AI_AGENT",
	"CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SSE_PORT",
	"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CODEX_CI",
	"TERM_SESSION_ID", "ITERM_SESSION_ID", "WEZTERM_PANE", "KITTY_WINDOW_ID", "WT_SESSION",
}

// Marker reports whether name is in Markers or names Ghostty's surface.
// Windows names are case-insensitive, so the match is too.
func Marker(name string) bool {
	name = strings.ToUpper(name)
	return slices.Contains(Markers, name) || strings.HasPrefix(name, "GHOSTTY_")
}

// Clean copies env without the markers.
func Clean(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return Marker(name)
	})
}

// Unset removes the markers from this process's environment, so every
// child it starts from then on goes without them.
func Unset() {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); Marker(name) {
			os.Unsetenv(name)
		}
	}
}
