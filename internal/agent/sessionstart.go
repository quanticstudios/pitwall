package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// IsSessionStart reports whether payload is a SessionStart hook from Claude
// Code or Codex (excluding Claude Code /side forks, which carry null
// transcript_path).
func IsSessionStart(provider model.Provider, payload []byte) bool {
	if provider != model.ProviderClaude && provider != model.ProviderCodex {
		return false
	}
	var p struct {
		Event          string          `json:"hook_event_name"`
		TranscriptPath json.RawMessage `json:"transcript_path"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return false
	}
	return p.Event == "SessionStart" && string(p.TranscriptPath) != "null"
}

// SessionStartContext returns a concise single-line context note telling the
// agent its pitwall pane id, tab and session (when known), and that pitwall
// CLI commands work from its shell.
func SessionStartContext(paneID, tabAddr, tabTitle, sessionName string) string {
	var where []string
	if tabAddr != "" {
		tab := "tab " + tabAddr
		if t := strings.TrimSpace(tabTitle); t != "" {
			tab += fmt.Sprintf(" %q", t)
		}
		where = append(where, tab)
	}
	if s := strings.TrimSpace(sessionName); s != "" {
		where = append(where, fmt.Sprintf("session %q", s))
	}
	loc := "pitwall pane @" + paneID
	if len(where) > 0 {
		loc += " (" + strings.Join(where, ", ") + ")"
	}
	return fmt.Sprintf("Running in %s. pitwall commands (pitwall ls, new, split, wait, read, send, send-key, events, api) work directly from your shell.", loc)
}

// SessionStartOutput returns the JSON hookSpecificOutput line that Claude Code
// and Codex inject into the session context on SessionStart.
func SessionStartOutput(paneID, tabAddr, tabTitle, sessionName string) []byte {
	out, _ := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": SessionStartContext(paneID, tabAddr, tabTitle, sessionName),
		},
	})
	return out
}
