package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFollowSystemLightAndDark(t *testing.T) {
	dir := t.TempDir()
	// Import a custom theme to prove follow_system works with imported themes too.
	themesDir := filepath.Join(dir, "themes")
	if _, err := ImportTheme(filepath.Join("testdata", "themes", "Paper Warm.itermcolors"), "", themesDir); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.toml")
	cfg := `[appearance]
follow_system = true
theme_light = "paper-warm"
theme_dark = "nord"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(SystemAppearanceEnv, "light")
	s, probs := LoadFile(cfgPath)
	if len(probs) > 0 {
		t.Fatalf("light problems: %v", probs)
	}
	if !s.FollowSystem || s.SystemScheme != SchemeLight || s.ThemeName != "paper-warm" || s.ThemeLight != "paper-warm" || s.ThemeDark != "nord" {
		t.Fatalf("light state: follow=%v scheme=%v name=%q light=%q dark=%q", s.FollowSystem, s.SystemScheme, s.ThemeName, s.ThemeLight, s.ThemeDark)
	}
	if s.Theme.Terminal.Background != "#f9f5ed" {
		t.Fatalf("light bg = %s", s.Theme.Terminal.Background)
	}

	t.Setenv(SystemAppearanceEnv, "dark")
	s, probs = LoadFile(cfgPath)
	if len(probs) > 0 {
		t.Fatalf("dark problems: %v", probs)
	}
	if !s.FollowSystem || s.SystemScheme != SchemeDark || s.ThemeName != "nord" {
		t.Fatalf("dark state: follow=%v scheme=%v name=%q", s.FollowSystem, s.SystemScheme, s.ThemeName)
	}
	if s.Theme.Terminal.Background != "#2e3440" {
		t.Fatalf("dark bg = %s", s.Theme.Terminal.Background)
	}

	// [theme] name = "system" acts as an alias for follow_system = true.
	sysCfg := "[theme]\nname = \"system\"\n[appearance]\ntheme_light = \"catppuccin-latte\"\ntheme_dark = \"gruvbox-dark\"\n"
	os.WriteFile(cfgPath, []byte(sysCfg), 0o644)
	t.Setenv(SystemAppearanceEnv, "light")
	s, probs = LoadFile(cfgPath)
	if len(probs) > 0 || !s.FollowSystem || s.ThemeName != "catppuccin-latte" {
		t.Fatalf("system alias light: follow=%v name=%q probs=%v", s.FollowSystem, s.ThemeName, probs)
	}
	t.Setenv(SystemAppearanceEnv, "dark")
	s, probs = LoadFile(cfgPath)
	if len(probs) > 0 || !s.FollowSystem || s.ThemeName != "gruvbox-dark" {
		t.Fatalf("system alias dark: follow=%v name=%q probs=%v", s.FollowSystem, s.ThemeName, probs)
	}

	// Invalid theme_light / theme_dark report problems and fall back to defaults.
	badCfg := "[appearance]\nfollow_system = true\ntheme_light = \"nope-light\"\ntheme_dark = \"system\"\n"
	os.WriteFile(cfgPath, []byte(badCfg), 0o644)
	t.Setenv(SystemAppearanceEnv, "light")
	s, probs = LoadFile(cfgPath)
	if len(probs) != 2 || s.ThemeLight != "aide-light" || s.ThemeDark != "aide-dark" || s.ThemeName != "aide-light" {
		t.Fatalf("invalid light/dark: light=%q dark=%q name=%q probs=%v", s.ThemeLight, s.ThemeDark, s.ThemeName, probs)
	}
	if !strings.Contains(probs[0].Msg, "appearance.theme_light") || !strings.Contains(probs[1].Msg, "appearance.theme_dark") {
		t.Fatalf("unexpected problem messages: %v", probs)
	}
}
