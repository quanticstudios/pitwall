package config

import "slices"

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
			SurfaceElevated: "#d6d6dc", Border: "#dcdce0", Fg: "#1d1d1f", Muted: "#6e6e73",
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
			SurfaceElevated: "#292e42", Border: "#24253a", Fg: "#c0caf5", Muted: "#737aa2",
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
			SurfaceElevated: "#45475a", Border: "#28283a", Fg: "#cdd6f4", Muted: "#9399b2",
			Primary: "#89b4fa", OnPrimary: "#11111b", Red: "#f38ba8", Yellow: "#f9e2af",
			Green: "#a6e3a1", Blue: "#74c7ec", Purple: "#cba6f7",
		},
		Terminal: Terminal{
			Foreground: "#cdd6f4", Background: "#1e1e2e", Cursor: "#f5e0dc",
			ANSI: ansi("#45475a", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#bac2de",
				"#585b70", "#f38ba8", "#a6e3a1", "#f9e2af", "#89b4fa", "#f5c2e7", "#94e2d5", "#a6adc8"),
		},
	},
}

// Themes are the built-in theme names, the default first.
var Themes = []string{"aide-dark", "aide-light", "tokyo-night", "catppuccin-mocha"}

// Builtin is a built-in theme with every color set, and whether it exists.
func Builtin(name string) (Theme, bool) {
	t, ok := builtins[name]
	t.Terminal.ANSI = slices.Clone(t.Terminal.ANSI)
	return t, ok
}
