package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// claudeAutoContinue reads the option behind Claude Code's /config row
// "Continue automatically at usage limit". Verified in CLI 2.1.314 and
// https://code.claude.com/docs/en/settings-reference#autocontinueatusagelimit.
// It lives in user settings, not the global .claude.json, and defaults on.
func claudeAutoContinue(p model.Pane) bool {
	dir := claudeConfigDir(p)
	if dir == "" {
		return true
	}
	return claudeAutoContinueFile(filepath.Join(dir, "settings.json"))
}

// claudeConfigDir is the config dir pane p's Claude Code reads
// (claude.ConfigDir).
func claudeConfigDir(p model.Pane) string { return claude.ConfigDir(p.Env, p.Cwd) }

// claudeSettingsFile is the model and effortLevel in Claude Code's user
// settings at path, "" for one unset, unknown or unreadable. Never writes.
func claudeSettingsFile(path string) (model, effort string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var s struct {
		Model  string `json:"model"`
		Effort string `json:"effortLevel"`
	}
	if json.Unmarshal(b, &s) != nil {
		return "", ""
	}
	switch s.Effort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		s.Effort = ""
	}
	if len(s.Model) > 80 || strings.ContainsAny(s.Model, " \n\t") {
		s.Model = ""
	}
	return s.Model, s.Effort
}

// Only an explicit false lets pitwall schedule a Claude resume. Missing
// settings use Claude's default; unreadable or invalid settings defer to
// Claude too, rather than risk typing a second continue. Never writes.
func claudeAutoContinueFile(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var s map[string]any
	if json.Unmarshal(b, &s) != nil {
		return true
	}
	on, ok := s["autoContinueAtUsageLimit"].(bool)
	return !ok || on
}

func (d *Daemon) claudeContinues(p model.Pane) bool {
	return p.Provider == model.ProviderClaude && d.o.ClaudeContinues != nil && d.o.ClaudeContinues(p)
}
