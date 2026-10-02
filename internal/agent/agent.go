// Package agent maps coding-agent hook events to model.Activity.
package agent

import (
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Derive maps one hook payload to the pane's next activity. ok is false when
// the event changes nothing.
func Derive(prev *model.Activity, provider model.Provider, payload []byte, now time.Time) (next model.Activity, ok bool) {
	panic("unimplemented")
}

// SessionID returns the agent session id carried by the payload, or "".
func SessionID(provider model.Provider, payload []byte) string { panic("unimplemented") }

// ClaudeHooks returns the "hooks" object for ~/.claude/settings.json that
// runs `<bin> hook claude` on every event Derive reads.
func ClaudeHooks(bin string) []byte { panic("unimplemented") }

// CodexNotify returns the `notify = [...]` line for ~/.codex/config.toml.
func CodexNotify(bin string) string { panic("unimplemented") }
