package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
)

func TestRunThemeImportAndList(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)

	ghosttySrc := filepath.Join("..", "..", "internal", "config", "testdata", "themes", "ocean-dusk.ghostty")
	itermSrc := filepath.Join("..", "..", "internal", "config", "testdata", "themes", "Paper Warm.itermcolors")

	// Import a Ghostty file with --apply.
	var out, errOut bytes.Buffer
	if code := runTheme([]string{"import", "--apply", ghosttySrc}, &out, &errOut); code != 0 {
		t.Fatalf("import ghostty exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "imported ocean-dusk") || !strings.Contains(out.String(), "applied ocean-dusk") {
		t.Fatalf("unexpected stdout: %q", out.String())
	}
	s, probs := config.Load()
	if len(probs) > 0 || s.ThemeName != "ocean-dusk" {
		t.Fatalf("after --apply: name=%q probs=%v", s.ThemeName, probs)
	}

	// Import an iTerm2 file with --name.
	out.Reset()
	errOut.Reset()
	if code := runTheme([]string{"import", "--name", "warm-paper", itermSrc}, &out, &errOut); code != 0 {
		t.Fatalf("import iterm exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "imported warm-paper") {
		t.Fatalf("unexpected stdout: %q", out.String())
	}

	// Import by Ghostty theme name.
	gDir := t.TempDir()
	b, err := os.ReadFile(ghosttySrc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gDir, "My Ghostty Theme"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.GhosttyThemesDirEnv, gDir)
	out.Reset()
	errOut.Reset()
	if code := runTheme([]string{"import", "My Ghostty Theme"}, &out, &errOut); code != 0 {
		t.Fatalf("import by name exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "imported my-ghostty-theme") {
		t.Fatalf("unexpected stdout: %q", out.String())
	}

	// List shows built-ins and custom themes.
	out.Reset()
	if code := runTheme([]string{"list"}, &out, &errOut); code != 0 {
		t.Fatalf("list exit %d", code)
	}
	for _, want := range []string{"aide-dark", "nord", "ocean-dusk\tcustom", "warm-paper\tcustom", "my-ghostty-theme\tcustom"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list output missing %q:\n%s", want, out.String())
		}
	}

	// Missing argument returns usage code 2; missing source returns 1.
	if code := runTheme([]string{"import"}, &out, &errOut); code != 2 {
		t.Errorf("no args exit %d, want 2", code)
	}
	if code := runTheme([]string{"import", "nonexistent-theme-xyz"}, &out, &errOut); code != 1 {
		t.Errorf("missing theme exit %d, want 1", code)
	}
}
