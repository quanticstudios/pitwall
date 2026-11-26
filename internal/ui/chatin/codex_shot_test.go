package chatin

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/codex"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// shotProvider is the chat's agent in the composer shootComposer draws.
var shotProvider model.Provider = "claude"

// TestCodexSkillShots draws a Codex chat's composer into
// PITWALL_SLASH_SHOTS with its skill menu open: on a $word mid-draft,
// on $ alone, and under the / menu at the draft's start.
func TestCodexSkillShots(t *testing.T) {
	dir := os.Getenv("PITWALL_SLASH_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_SLASH_SHOTS is not set")
	}
	shotProvider = model.ProviderCodex
	defer func() { shotProvider = "claude" }()
	skills := []session.Command{
		{Name: "release", Description: "Cut a release from main and write its notes", Source: "project"},
		{Name: "review-agent", Description: "Find actionable bugs in code changes", Source: "system"},
		{Name: "pages:write-page", Description: "Write or rewrite a page in a space", Source: "plugin"},
		{Name: "pr-review", Description: "Review a pull request against the checklist", Source: "user"},
		{Name: "imagegen", Description: "Generate or edit images for websites, games, and more", Source: "system"},
	}
	cmds := append(slices.Clone(codex.Builtins), skills...)
	for _, s := range []struct{ name, text string }{
		{"codex-skill-mid.png", "fix the flaky test, then follow $re"},
		{"codex-skill-all.png", "$"},
		{"codex-slash-start.png", "/re"},
	} {
		shootComposer(t, filepath.Join(dir, s.name), theme.Dark(), cmds, s.text, 0, false)
	}
}
