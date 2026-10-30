package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestProfiles(t *testing.T) {
	home, _ := os.UserHomeDir()
	path := write(t, t.TempDir(), "config.toml", `[profiles.work]
label = "Work plan"
color = "#abcdef"
claude_config_dir = "~/.claude-work"
codex_home = "~/.codex-work"
[profiles.personal]
codex_home = "~/.codex-personal"
`)
	s, problems := LoadFile(path)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	if len(s.Profiles) != 2 || s.Profiles["work"].ClaudeConfigDir != filepath.Join(home, ".claude-work") || s.Profiles["work"].CodexHome != filepath.Join(home, ".codex-work") || s.ProfileLabel("work") != "Work plan" || s.ProfileLabel("personal") != "personal" || s.ProfileLabel("removed") != "removed" {
		t.Fatalf("profiles: %+v", s.Profiles)
	}
	for _, input := range []string{"profiles = \"bad\"", "[profiles.work]\ncodex_home = \"relative\"", "[profiles.work]\nclaude_config_dir = \"relative\"", "[profiles.work]\ncolor = \"red\"", "[profiles.work]\nunknown = \"x\"", "[profiles.work]\ncodex_home = 1", "[profiles]\nwork = 1", "[profiles.\"\"]\nlabel = \"bad\""} {
		t.Run(input, func(t *testing.T) {
			_, probs := LoadFile(write(t, t.TempDir(), "config.toml", input))
			if len(probs) == 0 {
				t.Fatal("bad profile accepted")
			}
		})
	}
}

func TestProfileEnv(t *testing.T) {
	s := Settings{Profiles: map[string]Profile{"work": {ClaudeConfigDir: "/work/claude", CodexHome: "/work/codex"}, "personal": {CodexHome: "/personal/codex"}}}
	base := []string{"OTHER=keep", "CLAUDE_CONFIG_DIR=/old", "CODEX_HOME=/old", "PITWALL_PROFILE=old"}
	before := slices.Clone(base)
	got, err := s.ProfileEnv("work", base)
	want := []string{"OTHER=keep", "CLAUDE_CONFIG_DIR=/work/claude", "CODEX_HOME=/work/codex", "PITWALL_PROFILE=work"}
	if err != nil || !slices.Equal(got, want) || !slices.Equal(base, before) {
		t.Fatalf("env %q, err %v, original %q", got, err, base)
	}
	got, err = s.ProfileEnv("personal", nil)
	if err != nil || !slices.Equal(got, []string{"CODEX_HOME=/personal/codex", "PITWALL_PROFILE=personal"}) {
		t.Fatalf("personal env %q %v", got, err)
	}
	for _, vars := range [][]string{nil, base} {
		got, err := s.ProfileEnv("", vars)
		if err != nil || !slices.Equal(got, vars) || (got == nil) != (vars == nil) {
			t.Fatalf("default changed: %q %v", got, err)
		}
	}
	if _, err := s.ProfileEnv("missing", nil); err == nil {
		t.Fatal("unknown profile accepted")
	}
}
