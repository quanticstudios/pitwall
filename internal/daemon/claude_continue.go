package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// claudeAutoContinue reads the option behind Claude Code's /config row
// "Continue automatically at usage limit". Verified in CLI 2.1.314 and
// https://code.claude.com/docs/en/settings-reference#autocontinueatusagelimit.
// It lives in user settings, not the global .claude.json, and defaults on.
func claudeAutoContinue(p model.Pane) bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	for _, e := range p.Env {
		if v, ok := strings.CutPrefix(e, "CLAUDE_CONFIG_DIR="); ok {
			dir = v
		}
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return true
		}
		dir = filepath.Join(home, ".claude")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(p.Cwd, dir)
	}
	return claudeAutoContinueFile(filepath.Join(dir, "settings.json"))
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
