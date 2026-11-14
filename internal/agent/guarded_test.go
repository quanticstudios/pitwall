package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestGuardHooked: the guard holds only where the settings `pitwall hooks
// install` writes run an existing pitwall on every PreToolUse.
func TestGuardHooked(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "it's pitwall")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := func(hooks []byte, extra string) string {
		return `{` + extra + `"hooks": ` + string(hooks) + `}`
	}
	for _, c := range []struct {
		name     string
		provider model.Provider
		file     string // "" writes none
		body     string
		want     string // "" for nil
	}{
		{"claude", model.ProviderClaude, "settings.json", settings(ClaudeHooks(bin), ""), ""},
		{"codex", model.ProviderCodex, "hooks.json", settings(CodexHooks(bin), ""), ""},
		{"no file", model.ProviderClaude, "", "", "no settings.json"},
		{"codex hooks in claude's file", model.ProviderClaude, "settings.json", settings(CodexHooks(bin), ""), "no PreToolUse hook"},
		{"hooks off", model.ProviderClaude, "settings.json", settings(ClaudeHooks(bin), `"disableAllHooks": true, `), "disableAllHooks"},
		{"gone binary", model.ProviderClaude, "settings.json", settings(ClaudeHooks(filepath.Join(dir, "gone")), ""), "no PreToolUse hook"},
		{"some tools", model.ProviderClaude, "settings.json", strings.ReplaceAll(settings(ClaudeHooks(bin), ""), `"hooks": [`, `"matcher": "Bash", "hooks": [`), "no PreToolUse hook"},
		{"no hooks", model.ProviderClaude, "settings.json", `{"model": "opus"}`, "no PreToolUse hook"},
		{"broken", model.ProviderClaude, "settings.json", `{`, "settings.json"},
		{"pi", model.ProviderPi, "", "", "covers Claude Code and Codex"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := t.TempDir()
			if c.file != "" {
				if err := os.WriteFile(filepath.Join(cfg, c.file), []byte(c.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := GuardHooked(c.provider, cfg)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("GuardHooked = %v, want nil", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("GuardHooked = %v, want %q", err, c.want)
			}
		})
	}
}
