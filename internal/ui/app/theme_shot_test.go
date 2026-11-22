package app

import (
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

type themeShotBackend struct {
	*limitBackend
	paneID string
	grid   vt.Grid
}

func (b *themeShotBackend) Frame(pane string) (vt.Grid, vt.Modes, bool) {
	if pane == b.paneID {
		return b.grid, vt.Modes{}, true
	}
	return b.limitBackend.Frame(pane)
}

// TestImportedThemeShots imports two themes (a dark Ghostty scheme and a light
// iTerm2 scheme) through config.ImportTheme and renders headless PNGs of the
// sidebar beside a chat and a terminal pane, plus the Settings Appearance page
// with Follow system and its Light/Dark pickers, to $PITWALL_THEME_SHOTS.
func TestImportedThemeShots(t *testing.T) {
	dir := os.Getenv("PITWALL_THEME_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_THEME_SHOTS is not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })

	cfgDir := t.TempDir()
	themesDir := filepath.Join(cfgDir, "themes")

	ghosttyPath := filepath.Join("..", "..", "config", "testdata", "themes", "ocean-dusk.ghostty")
	itermPath := filepath.Join("..", "..", "config", "testdata", "themes", "Paper Warm.itermcolors")
	darkImp, err := config.ImportTheme(ghosttyPath, "", themesDir)
	if err != nil {
		t.Fatal(err)
	}
	lightImp, err := config.ImportTheme(itermPath, "", themesDir)
	if err != nil {
		t.Fatal(err)
	}

	makeSplitGrid := func() vt.Grid {
		em := vt.New(56, 42, io.Discard)
		em.Write([]byte(
			"\x1b[1;32muser@box\x1b[0m:\x1b[1;34m~/pitwall\x1b[0m$ git status -sb\r\n" +
				"\x1b[32m## theme-import\x1b[0m\r\n" +
				"\x1b[33m M\x1b[0m internal/config/config.go\r\n" +
				"\x1b[32m??\x1b[0m internal/config/theme_import.go\r\n" +
				"\x1b[32m??\x1b[0m cmd/pitwall/theme.go\r\n\r\n" +
				"\x1b[1;32muser@box\x1b[0m:\x1b[1;34m~/pitwall\x1b[0m$ pitwall theme import ocean-dusk\r\n" +
				"\x1b[36mimported\x1b[0m ocean-dusk (themes/ocean-dusk.toml)\r\n\r\n" +
				"\x1b[1;32muser@box\x1b[0m:\x1b[1;34m~/pitwall\x1b[0m$ go test ./internal/config\r\n" +
				"\x1b[32mok  \x1b[0m\t.../pitwall/internal/config\t0.81s\r\n" +
				"\x1b[32mok  \x1b[0m\t.../pitwall/internal/ui/theme\t0.42s\r\n" +
				"\x1b[35mPASS\x1b[0m — 16 ANSI colors + UI tokens >= 4.5:1\r\n\r\n" +
				"\x1b[1;31merror\x1b[0m \x1b[33mwarn\x1b[0m \x1b[32mok\x1b[0m \x1b[34minfo\x1b[0m \x1b[35mplan\x1b[0m \x1b[36mhint\x1b[0m\r\n" +
				"\x1b[1;32muser@box\x1b[0m:\x1b[1;34m~/pitwall\x1b[0m$ ",
		))
		return em.Snapshot()
	}

	renderWindow := func(name string, size image.Point, th *theme.Theme, cfg config.Settings, openSettings bool) {
		lb := &limitBackend{FakeBackend: NewFakeBackend(), snap: sizeChat()}
		lb.snap.Provider = model.ProviderClaude
		b := &themeShotBackend{limitBackend: lb, paneID: "pt", grid: makeSplitGrid()}

		splitLayout := &layout.Node{
			Dir:    layout.Horizontal,
			Ratios: []float64{0.58, 0.42},
			Children: []*layout.Node{
				{Pane: "pc"},
				{Pane: "pt"},
			},
		}
		b.st.Workspaces = append(b.st.Workspaces, model.Workspace{
			ID: "wtheme", SessionID: "s1", Name: "theme-import", Label: "Theme import",
			Path: t.TempDir(), Branch: "theme-import",
			Tabs:      []model.Tab{{ID: "tt", Title: "Theme import", Layout: splitLayout}},
			ActiveTab: "tt",
		})
		s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
		s1.Order = append([]string{"wtheme"}, s1.Order...)
		b.st.Panes = append(b.st.Panes,
			model.Pane{ID: "pc", WorkspaceID: "wtheme", Provider: model.ProviderClaude, Transcript: "/tmp/s.jsonl"},
			model.Pane{ID: "pt", WorkspaceID: "wtheme"},
		)
		b.st.Activities = append(b.st.Activities,
			model.Activity{PaneID: "pc", WorkspaceID: "wtheme", Provider: model.ProviderClaude, State: model.StatePendingApproval, Detail: "Edit internal/ui/app/chat.go"},
		)

		u := &ui{b: b, th: th, cfg: cfg, panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, 13
		u.cfg.Chat.Claude, u.cfg.Chat.Codex, u.cfg.Chat.Antigravity = true, true, true
		u.cfg.Chat.ToolCalls, u.cfg.Chat.Width = config.ToolsAll, config.ChatComfortable
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, "wtheme", "")
		u.nav.setFocus("pt")
		if openSettings {
			u.openSettings()
		}

		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		var r input.Router
		var ops op.Ops
		for range 4 {
			ops.Reset()
			u.layout(gl.Context{
				Ops: &ops, Source: r.Source(),
				Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: gl.Exact(size),
				Now:         time.Now(),
			})
			r.Frame(&ops)
			time.Sleep(80 * time.Millisecond)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	cfgPath := filepath.Join(cfgDir, "config.toml")
	cfgBody := "[appearance]\nfollow_system = true\ntheme_light = \"" + lightImp[0].Name + "\"\ntheme_dark = \"" + darkImp[0].Name + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Dark imported Ghostty theme (Ocean Dusk): sidebar + chat + terminal.
	t.Setenv(config.SystemAppearanceEnv, "dark")
	sDark, probs := config.LoadFile(cfgPath)
	if len(probs) > 0 {
		t.Fatalf("dark config problems: %v", probs)
	}
	thDark, _ := theme.New(sDark.Theme, sDark.Font)
	renderWindow("imported-dark-ocean-dusk", image.Pt(1440, 960), thDark, sDark, false)

	// 2. Light imported iTerm2 theme (Paper Warm): sidebar + chat + terminal.
	t.Setenv(config.SystemAppearanceEnv, "light")
	sLight, probs := config.LoadFile(cfgPath)
	if len(probs) > 0 {
		t.Fatalf("light config problems: %v", probs)
	}
	thLight, _ := theme.New(sLight.Theme, sLight.Font)
	renderWindow("imported-light-paper-warm", image.Pt(1440, 960), thLight, sLight, false)

	// 3. Settings Appearance page with Follow system enabled and both imported themes as cards + pickers.
	renderWindow("settings-follow-system", image.Pt(1280, 960), thDark, sDark, true)
}
