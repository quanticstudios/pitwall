package settings

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestFollowSystemPickersAndThemeImport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.SystemAppearanceEnv, "light")
	t.Setenv(config.GhosttyThemesDirEnv, t.TempDir())

	var p Page
	p.Show(path)
	p.s, _ = config.LoadFile(path)
	p.th = theme.Dark()
	var r input.Router
	var ops op.Ops
	frame := func() Result {
		ops.Reset()
		gtx := gl.Context{
			Ops:         &ops,
			Source:      r.Source(),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1000, 900)),
			Now:         time.Now(),
		}
		res := p.Layout(gtx, p.th, p.s, nil)
		r.Frame(&ops)
		if res == Saved {
			p.s, _ = config.LoadFile(path)
		}
		return res
	}

	// Initially follow_system is off, so Light theme and Dark theme rows are hidden.
	hasRow := func(label string) bool {
		for _, sec := range p.appearance() {
			for _, rw := range sec.rows {
				if rw.label == label {
					return true
				}
			}
		}
		return false
	}
	if hasRow("Light theme") || hasRow("Dark theme") {
		t.Fatal("light/dark pickers shown before follow_system is enabled")
	}

	// Toggle Follow system on.
	p.btn("appearance.follow_system").Click()
	if res := frame(); res != Saved || !p.s.FollowSystem {
		t.Fatalf("after follow_system click: res=%v follow=%v", res, p.s.FollowSystem)
	}
	if !hasRow("Light theme") || !hasRow("Dark theme") {
		t.Fatal("light/dark pickers not shown when follow_system is enabled")
	}

	// Open Light theme dropdown and pick catppuccin-latte.
	p.btn("dd:theme:theme_light").Click()
	frame()
	if p.dd != "theme:theme_light" {
		t.Fatalf("light dropdown did not open: dd=%q", p.dd)
	}
	p.btn("themeopt:theme_light:catppuccin-latte").Click()
	if res := frame(); res != Saved || p.s.ThemeLight != "catppuccin-latte" || p.s.ThemeName != "catppuccin-latte" {
		t.Fatalf("after picking light theme: res=%v light=%q active=%q", res, p.s.ThemeLight, p.s.ThemeName)
	}
	if p.dd != "" {
		t.Fatalf("dropdown stayed open: %q", p.dd)
	}

	// Open Dark theme dropdown and pick nord.
	p.btn("dd:theme:theme_dark").Click()
	frame()
	p.btn("themeopt:theme_dark:nord").Click()
	if res := frame(); res != Saved || p.s.ThemeDark != "nord" {
		t.Fatalf("after picking dark theme: res=%v dark=%q", res, p.s.ThemeDark)
	}

	// Flipping system appearance switches the active theme to nord.
	t.Setenv(config.SystemAppearanceEnv, "dark")
	p.s, _ = config.LoadFile(path)
	if p.s.ThemeName != "nord" {
		t.Fatalf("dark mode active theme = %q, want nord", p.s.ThemeName)
	}

	// Clicking a theme card turns off follow_system and applies that theme.
	p.btn("theme:dracula").Click()
	if res := frame(); res != Saved || p.s.FollowSystem || p.s.ThemeName != "dracula" {
		t.Fatalf("clicking card: res=%v follow=%v name=%q", res, p.s.FollowSystem, p.s.ThemeName)
	}

	// Import theme via the Settings UI ("Import theme" -> enter fixture path -> "Import").
	fixture := filepath.Join("..", "..", "config", "testdata", "themes", "ocean-dusk.ghostty")
	p.btn("theme-import-open").Click()
	frame()
	if !p.im.open {
		t.Fatal("import panel did not open")
	}
	p.im.src.SetText(fixture)
	p.btn("theme-import-submit").Click()
	if res := frame(); res != Saved || p.s.ThemeName != "ocean-dusk" || p.im.err != "" {
		t.Fatalf("import submit: res=%v name=%q err=%q", res, p.s.ThemeName, p.im.err)
	}
	if p.findNamedTheme("ocean-dusk") == nil {
		t.Fatal("imported theme card missing from p.themes")
	}

	// Import via mocked Browse… file dialog.
	itermFixture := filepath.Join("..", "..", "config", "testdata", "themes", "Paper Warm.itermcolors")
	oldPick := pickThemeFileFn
	pickThemeFileFn = func() (string, error) { return itermFixture, nil }
	t.Cleanup(func() { pickThemeFileFn = oldPick })

	p.btn("theme-import-open").Click()
	frame()
	p.btn("theme-import-browse").Click()
	frame()
	deadline := time.Now().Add(2 * time.Second)
	for p.im.picking && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		frame()
	}
	if p.findNamedTheme("paper-warm") == nil || p.s.ThemeName != "paper-warm" {
		t.Fatalf("browse import failed: active=%q err=%q", p.s.ThemeName, p.im.err)
	}
}
