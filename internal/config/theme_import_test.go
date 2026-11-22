package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGhosttyThemeFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "themes", "ocean-dusk.ghostty"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseGhosttyTheme(data)
	if err != nil {
		t.Fatal(err)
	}
	if s.Background.hex() != "#1b222c" || s.Foreground.hex() != "#d9e2ec" || s.Cursor.hex() != "#7cc4fa" {
		t.Fatalf("core colors = bg %s fg %s cur %s", s.Background.hex(), s.Foreground.hex(), s.Cursor.hex())
	}
	// Bare hex (palette 9) and 8-digit hex with alpha=ff (palette 10).
	if s.ANSI[9].hex() != "#ef4e4e" || s.ANSI[10].hex() != "#3ebd93" {
		t.Fatalf("ansi[9]=%s ansi[10]=%s", s.ANSI[9].hex(), s.ANSI[10].hex())
	}
	// Selection background had 8-digit alpha 0xcc (80% over #1b222c).
	wantSelBg := (rgb8{0x1b, 0x22, 0x2c}).mix(rgb8{0x3e, 0x4c, 0x59}, float64(0xcc)/255).hex()
	if !s.HasSelectionBg || s.SelectionBackground.hex() != wantSelBg {
		t.Fatalf("selection_background = %s (has=%v), want %s", s.SelectionBackground.hex(), s.HasSelectionBg, wantSelBg)
	}
	if !s.HasSelectionFg || s.SelectionForeground.hex() != "#f0f4f8" {
		t.Fatalf("selection_foreground = %s", s.SelectionForeground.hex())
	}
}

func TestParseGhosttyThemeEdgeCases(t *testing.T) {
	// Missing keys: only background + 2 ANSI colors, quoted values, 3-digit hex,
	// keywords, comments, CRLF, palette index >= 16 ignored.
	raw := strings.Join([]string{
		"# full-line comment",
		`background = "#123"`,
		`cursor-color = cell-foreground`,
		`selection-background = cell-background`,
		`palette = 1 = "#ff4444" # inline comment`,
		`palette = 4 = "#3399ff80"`,
		`palette = 200 = #abcdef`,
		`unknown-key = whatever`,
	}, "\r\n")
	s, err := ParseGhosttyTheme([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if s.Background.hex() != "#112233" {
		t.Fatalf("bg = %s, want #112233", s.Background.hex())
	}
	if !s.HasForeground || !s.HasCursor || s.Cursor != s.Foreground {
		t.Fatalf("fallback fg/cursor: fg=%s cur=%s", s.Foreground.hex(), s.Cursor.hex())
	}
	if s.HasSelectionBg || s.HasSelectionFg {
		t.Fatalf("keyword selection should stay unset: bg=%v fg=%v", s.HasSelectionBg, s.HasSelectionFg)
	}
	if s.ANSI[1].hex() != "#ff4444" || s.ANSI[9].hex() != "#ff4444" {
		t.Fatalf("ansi[1]=%s ansi[9]=%s (bright should fall back to normal)", s.ANSI[1].hex(), s.ANSI[9].hex())
	}
	// 8-digit palette 4 flattened over bg #112233 at 0x80/255.
	want4 := (rgb8{0x11, 0x22, 0x33}).mix(rgb8{0x33, 0x99, 0xff}, float64(0x80)/255).hex()
	if s.ANSI[4].hex() != want4 {
		t.Fatalf("ansi[4] = %s, want %s", s.ANSI[4].hex(), want4)
	}

	// Error cases.
	for name, bad := range map[string]string{
		"empty":           "# only comments\n",
		"no background":   "foreground = #ffffff\n",
		"bad hex":         "background = #zzzzzz\n",
		"bad palette":     "background = #000000\npalette = nope\n",
		"bad palette idx": "background = #000000\npalette = 999=#ffffff\n",
	} {
		if _, err := ParseGhosttyTheme([]byte(bad)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestParseITermColorsFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "themes", "Paper Warm.itermcolors"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseTerminalTheme("Paper Warm.itermcolors", data)
	if err != nil {
		t.Fatal(err)
	}
	if s.Background.hex() != "#f9f5ed" || s.Foreground.hex() != "#2b2825" || s.Cursor.hex() != "#2363af" {
		t.Fatalf("core = bg %s fg %s cur %s", s.Background.hex(), s.Foreground.hex(), s.Cursor.hex())
	}
	if s.ANSI[15].hex() != "#ffffff" {
		t.Fatalf("integer components: ansi[15] = %s, want #ffffff", s.ANSI[15].hex())
	}
	// Selection Color has Alpha Component 0.25 -> flattened over #f9f5ed.
	wantSelBg := (rgb8{0xf9, 0xf5, 0xed}).mix(rgb8{0x23, 0x63, 0xaf}, float64(64)/255).hex()
	if !s.HasSelectionBg || s.SelectionBackground.hex() != wantSelBg {
		t.Fatalf("selection_background = %s, want %s", s.SelectionBackground.hex(), wantSelBg)
	}
	if !s.HasSelectionFg || s.SelectionForeground.hex() != "#2b2825" {
		t.Fatalf("selection_foreground = %s", s.SelectionForeground.hex())
	}
}

func TestParseITermColorsEdgeCases(t *testing.T) {
	// Minimal plist with missing keys, clamping, comments, unknown element types.
	minimal := `<?xml version="1.0" encoding="UTF-8"?>
<!-- comment -->
<plist version="1.0"><dict>
	<key>Background Color</key>
	<dict>
		<key>Red Component</key><real>-0.1</real>
		<key>Green Component</key><real>0.1</real>
		<key>Blue Component</key><real>1.2</real>
	</dict>
	<key>Other Array</key><array><string>x</string></array>
	<key>Missing Blue</key><dict><key>Red Component</key><real>0.5</real></dict>
</dict></plist>`
	s, err := ParseITermColors([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if s.Background.hex() != "#001aff" {
		t.Fatalf("clamped bg = %s, want #001aff", s.Background.hex())
	}
	if !s.HasForeground || !s.HasCursor {
		t.Fatalf("missing fg/cursor should be filled")
	}

	for name, bad := range map[string]string{
		"empty":      "<plist><dict></dict></plist>",
		"malformed":  "<plist><dict><key>x</plist>",
		"no bg":      "<plist><dict><key>Foreground Color</key><dict><key>Red Component</key><real>1</real><key>Green Component</key><real>1</real><key>Blue Component</key><real>1</real></dict></dict></plist>",
		"not a list": "hello world",
	} {
		if _, err := ParseITermColors([]byte(bad)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestImportThemeFileDirAndGhosttyName(t *testing.T) {
	dir := t.TempDir()
	themesDir := filepath.Join(dir, "themes")

	// Import a Ghostty file directly.
	res, err := ImportTheme(filepath.Join("testdata", "themes", "ocean-dusk.ghostty"), "", themesDir)
	if err != nil || len(res) != 1 || res[0].Name != "ocean-dusk" {
		t.Fatalf("file import: %+v, err=%v", res, err)
	}

	// Import a directory containing both Ghostty and iTerm2 fixtures (and a README to skip).
	srcDir := t.TempDir()
	for _, fn := range []string{"ocean-dusk.ghostty", "Paper Warm.itermcolors"} {
		b, err := os.ReadFile(filepath.Join("testdata", "themes", fn))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(srcDir, fn), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("# themes\n"), 0o644)
	dirRes, err := ImportTheme(srcDir, "", themesDir)
	if err != nil || len(dirRes) != 2 {
		t.Fatalf("dir import: %+v, err=%v", dirRes, err)
	}

	// Import a Ghostty theme by name (exact, case-insensitive, and slug).
	ghosttyDir := t.TempDir()
	b, _ := os.ReadFile(filepath.Join("testdata", "themes", "ocean-dusk.ghostty"))
	os.WriteFile(filepath.Join(ghosttyDir, "Ocean Dusk"), b, 0o644)
	os.WriteFile(filepath.Join(ghosttyDir, "Dracula"), b, 0o644) // collides with builtin "dracula"
	t.Setenv(GhosttyThemesDirEnv, ghosttyDir)

	if list := ListGhosttyThemes(); len(list) != 2 || list[0] != "Dracula" || list[1] != "Ocean Dusk" {
		t.Fatalf("ListGhosttyThemes = %v", list)
	}
	for _, query := range []string{"Ocean Dusk", "ocean dusk", "ocean-dusk"} {
		p, err := FindGhosttyTheme(query)
		if err != nil || filepath.Base(p) != "Ocean Dusk" {
			t.Errorf("FindGhosttyTheme(%q) = %q, %v", query, p, err)
		}
	}
	// Importing a name that collides with a built-in gets "-imported" suffix.
	colRes, err := ImportTheme("Dracula", "", themesDir)
	if err != nil || len(colRes) != 1 || colRes[0].Name != "dracula-imported" {
		t.Fatalf("builtin collision import = %+v, err=%v", colRes, err)
	}
	// Explicit --name colliding with a built-in fails cleanly.
	if _, err := ImportTheme("Ocean Dusk", "dracula", themesDir); err == nil {
		t.Error("expected error when explicit custom name collides with built-in")
	}

	// Round-trip through config.LoadFile and AllThemes with zero problems.
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte("[theme]\nname = \"paper-warm\"\n"), 0o644)
	s, probs := LoadFile(cfgPath)
	if len(probs) > 0 {
		t.Fatalf("LoadFile problems: %v", probs)
	}
	if s.ThemeName != "paper-warm" || s.Theme.Terminal.Background != "#f9f5ed" || s.Theme.Terminal.SelectionBackground == "" {
		t.Fatalf("loaded imported theme: %+v", s.Theme)
	}
	all := AllThemes(themesDir)
	foundPaper := false
	for _, nt := range all {
		if nt.Name == "paper-warm" && nt.Custom {
			foundPaper = true
		}
	}
	if !foundPaper {
		t.Fatalf("AllThemes missing paper-warm")
	}
}
