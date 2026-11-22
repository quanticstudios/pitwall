package config

import (
	"os"
	"path/filepath"
	"slices"
)

func ansi(cs ...Color) []Color { return cs }

// builtins are the themes `theme.name` can pick without a file.
var builtins = map[string]Theme{
	// aide's "dark" palette (styles.css :root[data-palette="dark"]) and its
	// terminal colors.
	"aide-dark": {
		Colors: Colors{
			Bg: "#08090c", Sidebar: "#08090c", Surface: "#14161b", SurfaceSecondary: "#1c1f26",
			SurfaceElevated: "#252932", Border: "#191a1d", Fg: "#f2f3f5", Muted: "#8e939c",
			Primary: "#2997ff", OnPrimary: "#06121f", Red: "#ff6b6b", Yellow: "#ffc533",
			Green: "#59d499", Blue: "#57c1ff", Purple: "#bd93ff",
		},
		Terminal: Terminal{
			Foreground: "#f4f4f6", Background: "#08090c", Cursor: "#ffffff",
			ANSI: ansi("#0d0d0d", "#ff6161", "#59d499", "#ffc533", "#57c1ff", "#bb9af7", "#7dcfff", "#cdcdcd",
				"#242728", "#ff6161", "#59d499", "#ffc533", "#57c1ff", "#bb9af7", "#7dcfff", "#ffffff"),
		},
	},
	// aide ships no light palette; this follows its DESIGN.md light canvas
	// (parchment #f5f5f7, ink #1d1d1f, Action Blue #0066cc), with state
	// colors and ANSI from GitHub Light so colored text stays readable.
	"aide-light": {
		Colors: Colors{
			Bg: "#f5f5f7", Sidebar: "#f5f5f7", Surface: "#ebebef", SurfaceSecondary: "#e1e1e6",
			SurfaceElevated: "#d6d6dc", Border: "#dcdce0", Fg: "#1d1d1f", Muted: "#626267",
			Primary: "#0066cc", OnPrimary: "#ffffff", Red: "#cf222e", Yellow: "#9a6700",
			Green: "#1a7f37", Blue: "#0969da", Purple: "#8250df",
		},
		Terminal: Terminal{
			Foreground: "#1f2328", Background: "#ffffff", Cursor: "#1d1d1f",
			ANSI: ansi("#24292f", "#cf222e", "#116329", "#4d2d00", "#0969da", "#8250df", "#1b7c83", "#6e7781",
				"#57606a", "#a40e26", "#1a7f37", "#633c01", "#218bff", "#a475f9", "#3192aa", "#8c959f"),
		},
	},
	// Tokyo Night (folke/tokyonight.nvim "night" and its kitty theme).
	"tokyo-night": {
		Colors: Colors{
			Bg: "#16161e", Sidebar: "#16161e", Surface: "#1a1b26", SurfaceSecondary: "#232433",
			SurfaceElevated: "#292e42", Border: "#24253a", Fg: "#c0caf5", Muted: "#838ab4",
			Primary: "#7aa2f7", OnPrimary: "#16161e", Red: "#f7768e", Yellow: "#e0af68",
			Green: "#9ece6a", Blue: "#7dcfff", Purple: "#bb9af7",
		},
		Terminal: Terminal{
			Foreground: "#c0caf5", Background: "#1a1b26", Cursor: "#c0caf5",
			ANSI: ansi("#15161e", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#a9b1d6",
				"#414868", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#bb9af7", "#7dcfff", "#c0caf5"),
		},
	},
	// Catppuccin Mocha (catppuccin/palette and its kitty theme).
	"catppuccin-mocha": {
		Colors: Colors{
			Bg: "#181825", Sidebar: "#181825", Surface: "#1e1e2e", SurfaceSecondary: "#313244",
			SurfaceElevated: "#45475a", Border: "#28283a", Fg: "#cdd6f4", Muted: "#969cb5",
			Primary: "#89b4fa", OnPrimary: "#11111b", Red: "#f38ba8", Yellow: "#f9e2af",
			Green: "#a6e3a1", Blue: "#74c7ec", Purple: "#cba6f7",
		},
		Terminal: Terminal{
			Foreground: "#cdd6f4", Background: "#1e1e2e", Cursor: "#f5e0dc",
			ANSI: ansi("#45475a", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#bac2de",
				"#585b70", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#a6adc8"),
		},
	},
	// Dracula (dracula/dracula-theme, its VS Code and terminal themes). The
	// comment color is too dim for muted text, so muted is a lighter step of it.
	"dracula": {
		Colors: Colors{
			Bg: "#21222c", Sidebar: "#21222c", Surface: "#282a36", SurfaceSecondary: "#343746",
			SurfaceElevated: "#44475a", Border: "#191a21", Fg: "#f8f8f2", Muted: "#a2abd0",
			Primary: "#bd93f9", OnPrimary: "#21222c", Red: "#ff5555", Yellow: "#f1fa8c",
			Green: "#50fa7b", Blue: "#8be9fd", Purple: "#ff79c6",
		},
		Terminal: Terminal{
			Foreground: "#f8f8f2", Background: "#282a36", Cursor: "#f8f8f2",
			ANSI: ansi("#21222c", "#ff5555", "#50fa7b", "#f1fa8c", "#bd93f9", "#ff79c6", "#8be9fd", "#f8f8f2",
				"#6272a4", "#ff6e6e", "#69ff94", "#ffffa5", "#d6acff", "#ff92df", "#a4ffff", "#ffffff"),
		},
	},
}

// Themes are the built-in theme names, the default first.
var Themes = []string{
	"aide-dark", "aide-light",
	"tokyo-night", "catppuccin-mocha", "catppuccin-latte",
	"dracula", "nord", "gruvbox-dark", "rose-pine-dawn",
}

func init() {
	for name, spec := range bundledGhosttyThemes {
		scheme, err := ParseGhosttyTheme([]byte(spec))
		if err != nil {
			panic("config: bundled theme " + name + ": " + err.Error())
		}
		builtins[name] = MapTerminalScheme(scheme)
	}
}

// bundledGhosttyThemes are additional well-known MIT-licensed themes derived
// through pitwall's Ghostty importer so their terminal colors stay faithful
// and their UI tokens satisfy pitwall's contrast rules.
var bundledGhosttyThemes = map[string]string{
	// Catppuccin Latte (catppuccin/palette, MIT).
	"catppuccin-latte": `
palette = 0=#bcc0cc
palette = 1=#d20f39
palette = 2=#40a02b
palette = 3=#df8e1d
palette = 4=#1e66f5
palette = 5=#ea76cb
palette = 6=#179299
palette = 7=#5c5f77
palette = 8=#acb0be
palette = 9=#e7103f
palette = 10=#46b02f
palette = 11=#e49931
palette = 12=#3878f6
palette = 13=#ef95d7
palette = 14=#19a1a8
palette = 15=#6c6f85
background = #eff1f5
foreground = #4c4f69
cursor-color = #dc8a78
selection-background = #dc8a78
selection-foreground = #eff1f5
`,
	// Nord (nordtheme/nord, MIT).
	"nord": `
palette = 0=#3b4252
palette = 1=#bf616a
palette = 2=#a3be8c
palette = 3=#ebcb8b
palette = 4=#81a1c1
palette = 5=#b48ead
palette = 6=#88c0d0
palette = 7=#e5e9f0
palette = 8=#596377
palette = 9=#bf616a
palette = 10=#a3be8c
palette = 11=#ebcb8b
palette = 12=#81a1c1
palette = 13=#b48ead
palette = 14=#8fbcbb
palette = 15=#eceff4
background = #2e3440
foreground = #d8dee9
cursor-color = #eceff4
selection-background = #eceff4
selection-foreground = #4c566a
`,
	// Gruvbox Dark (morhetz/gruvbox, MIT).
	"gruvbox-dark": `
palette = 0=#282828
palette = 1=#cc241d
palette = 2=#98971a
palette = 3=#d79921
palette = 4=#458588
palette = 5=#b16286
palette = 6=#689d6a
palette = 7=#a89984
palette = 8=#928374
palette = 9=#fb4934
palette = 10=#b8bb26
palette = 11=#fabd2f
palette = 12=#83a598
palette = 13=#d3869b
palette = 14=#8ec07c
palette = 15=#ebdbb2
background = #282828
foreground = #ebdbb2
cursor-color = #ebdbb2
selection-background = #665c54
selection-foreground = #ebdbb2
`,
	// Rosé Pine Dawn (rose-pine/rose-pine-theme, MIT).
	"rose-pine-dawn": `
palette = 0=#f2e9e1
palette = 1=#b4637a
palette = 2=#286983
palette = 3=#ea9d34
palette = 4=#56949f
palette = 5=#907aa9
palette = 6=#d7827e
palette = 7=#575279
palette = 8=#9893a5
palette = 9=#b4637a
palette = 10=#286983
palette = 11=#ea9d34
palette = 12=#56949f
palette = 13=#907aa9
palette = 14=#d7827e
palette = 15=#575279
background = #faf4ed
foreground = #575279
cursor-color = #575279
selection-background = #dfdad9
selection-foreground = #575279
`,
}

// Builtin is a built-in theme with every color set, and whether it exists.
func Builtin(name string) (Theme, bool) {
	t, ok := builtins[name]
	t.Terminal.ANSI = slices.Clone(t.Terminal.ANSI)
	return t, ok
}

// NamedTheme is a theme the settings page offers, with every color set.
type NamedTheme struct {
	Name   string
	Custom bool // a themes/<name>.toml file
	Theme  Theme
}

// AllThemes lists the built-in themes, omarchy where Omarchy runs, then the custom ones in dir (the
// themes folder next to config.toml). A custom file with mistakes still
// shows, with whatever of it loads.
func AllThemes(dir string) []NamedTheme {
	var out []NamedTheme
	for _, n := range Themes {
		t, _ := Builtin(n)
		out = append(out, NamedTheme{Name: n, Theme: t})
	}
	if t, _, err := LoadOmarchy(); err == nil {
		out = append(out, NamedTheme{Name: OmarchyName, Theme: t})
	}
	for _, n := range customThemes(dir) {
		if _, ok := builtins[n]; ok || n == OmarchyName || n == SystemThemeName {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, n+".toml"))
		if err != nil {
			continue
		}
		var t Theme
		parse("", data, &t, nil)
		base, ok := builtins[t.Name]
		if !ok {
			base = builtins["aide-dark"]
		}
		th, _ := overlay(base, t, "")
		out = append(out, NamedTheme{Name: n, Custom: true, Theme: th})
	}
	return out
}
