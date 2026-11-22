package config

import (
	"os"
	"strings"
)

// ColorScheme is the operating system's light or dark appearance.
type ColorScheme int

const (
	SchemeUnknown ColorScheme = iota
	SchemeDark
	SchemeLight
)

// String names s for diagnostics and tests.
func (s ColorScheme) String() string {
	switch s {
	case SchemeDark:
		return "dark"
	case SchemeLight:
		return "light"
	default:
		return "unknown"
	}
}

// SystemThemeName is the [theme] name value that follows the system's light
// and dark appearance, equivalent to [appearance] follow_system = true.
const SystemThemeName = "system"

// SystemAppearanceEnv, when set to "light" or "dark", overrides the detected
// system appearance, for tests and screenshots.
const SystemAppearanceEnv = "PITWALL_COLOR_SCHEME"

// SystemColorScheme reports whether the operating system is currently in light
// or dark mode, or SchemeUnknown when it cannot be determined.
func SystemColorScheme() ColorScheme {
	if s := parseColorSchemeEnv(os.Getenv(SystemAppearanceEnv)); s != SchemeUnknown {
		return s
	}
	return detectSystemColorScheme()
}

func parseColorSchemeEnv(v string) ColorScheme {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "light":
		return SchemeLight
	case "dark":
		return SchemeDark
	default:
		return SchemeUnknown
	}
}
