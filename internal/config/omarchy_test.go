package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// omarchyAt makes a current folder with fixture applied, as Omarchy leaves
// it, and points pitwall at it.
func omarchyAt(t *testing.T, fixture string) string {
	t.Helper()
	cur := filepath.Join(t.TempDir(), "current")
	applyOmarchy(t, cur, fixture)
	t.Setenv(OmarchyDirEnv, cur)
	return cur
}

// applyOmarchy switches cur to fixture the way omarchy-theme-set does: the
// new theme folder renamed over the old one, then theme.name written.
func applyOmarchy(t *testing.T, cur, fixture string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "omarchy", fixture, "colors.toml"))
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(cur, "next-theme")
	if err := os.MkdirAll(next, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(next, "colors.toml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(cur, "theme"))
	if err := os.Rename(next, filepath.Join(cur, "theme")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cur, "theme.name"), []byte(fixture+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadEmpty(t *testing.T, config string) Settings {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	s, probs := LoadFile(path)
	if len(probs) > 0 {
		t.Logf("problems: %v", probs)
	}
	return s
}

// TestOmarchyMapping maps three shipped Omarchy themes: the terminal gets
// the theme's own colors, the window its grounds, text and accent, and every
// text color reads at 4.5:1 and every mark at 3:1 on the grounds under it.
func TestOmarchyMapping(t *testing.T) {
	for _, c := range []struct {
		fixture, name string
		want          Theme
		adjusted      []string // window colors the contrast floors moved
	}{
		{"tokyo-night", "Tokyo Night", Theme{
			Colors: Colors{Bg: "#13141c", Sidebar: "#13141c", Surface: "#1a1b26", Fg: "#a9b1d6", Primary: "#7aa2f7", Red: "#f7768e", Purple: "#ad8ee6"},
			Terminal: Terminal{Foreground: "#a9b1d6", Background: "#1a1b26", Cursor: "#c0caf5",
				ANSI: ansi("#1a1b26", "#f7768e", "#9ece6a", "#e0af68", "#7aa2f7", "#ad8ee6", "#449dab", "#a9b1d6",
					"#414868", "#ff7a93", "#b9f27c", "#ff9e64", "#7da6ff", "#bb9af7", "#0db9d7", "#c0caf5")},
		}, []string{"muted"}},
		{"flexoki-light", "Flexoki Light", Theme{
			Colors: Colors{Surface: "#fffcf0", Fg: "#100f0f"},
			Terminal: Terminal{Foreground: "#100f0f", Background: "#fffcf0", Cursor: "#100f0f",
				ANSI: ansi("#fffcf0", "#d14d41", "#879a39", "#d0a215", "#205ea6", "#ce5d97", "#3aa99f", "#100f0f",
					"#b7b5ac", "#d14d41", "#879a39", "#d0a215", "#4385be", "#ce5d97", "#3aa99f", "#100f0f")},
		}, []string{"muted", "yellow", "green"}},
		{"hackerman", "Hackerman", Theme{
			Colors:   Colors{Surface: "#0b0c16", Primary: "#82fb9c"},
			Terminal: Terminal{Background: "#0b0c16", Foreground: "#ddf7ff"},
		}, []string{"muted"}},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			omarchyAt(t, c.fixture)
			s := loadEmpty(t, "")
			if s.ThemeName != OmarchyName || s.OmarchyTheme != c.name {
				t.Fatalf("theme %q (%q), want omarchy (%q)", s.ThemeName, s.OmarchyTheme, c.name)
			}
			checkSet(t, reflect.ValueOf(s.Theme.Colors), reflect.ValueOf(c.want.Colors), "colors")
			checkSet(t, reflect.ValueOf(s.Theme.Terminal), reflect.ValueOf(c.want.Terminal), "terminal")

			k := s.Theme.Colors
			raw := resolveOmarchy(mustRead(t, c.fixture), false)
			grounds := []Color{k.Bg, k.Sidebar, k.Surface, k.SurfaceSecondary}
			for _, f := range []struct {
				name, key string
				got       Color
				floor     float64
			}{
				{"fg", "foreground", k.Fg, textFloor}, {"muted", "muted", k.Muted, textFloor},
				{"primary", "accent", k.Primary, markFloor}, {"red", "red", k.Red, markFloor},
				{"yellow", "yellow", k.Yellow, markFloor}, {"green", "green", k.Green, markFloor},
				{"blue", "blue", k.Blue, markFloor}, {"purple", "magenta", k.Purple, markFloor},
			} {
				for _, g := range grounds {
					if r := contrast(hex8(t, f.got), hex8(t, g)); r < f.floor {
						t.Errorf("%s %s on %s: %.2f:1, want %.1f", f.name, f.got, g, r, f.floor)
					}
				}
				moved := !strings.EqualFold(string(f.got), raw[f.key])
				if want := contains(c.adjusted, f.name); moved != want {
					t.Errorf("%s: %s from %s, adjusted %v, want %v", f.name, f.got, raw[f.key], moved, want)
				}
			}
			if r := contrast(hex8(t, k.OnPrimary), hex8(t, k.Primary)); r < markFloor {
				t.Errorf("on_primary on primary: %.2f:1", r)
			}
		})
	}
}

// checkSet compares the fields want sets.
func checkSet(t *testing.T, got, want reflect.Value, path string) {
	t.Helper()
	for i := range want.NumField() {
		w := want.Field(i).Interface()
		if reflect.ValueOf(w).IsZero() {
			continue
		}
		if g := got.Field(i).Interface(); !reflect.DeepEqual(g, w) {
			t.Errorf("%s.%s = %v, want %v", path, want.Type().Field(i).Name, g, w)
		}
	}
}

func mustRead(t *testing.T, fixture string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "omarchy", fixture, "colors.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hex8(t *testing.T, c Color) rgb8 {
	t.Helper()
	v, ok := omarchyHex(string(c))
	if !ok {
		t.Fatalf("bad color %q", c)
	}
	return v
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestOmarchyChoice: omarchy is the default only where Omarchy runs and the
// config names no theme; a theme the user named wins, and omarchy named on
// a machine without it falls back to aide-dark with a problem.
func TestOmarchyChoice(t *testing.T) {
	t.Setenv(OmarchyDirEnv, filepath.Join(t.TempDir(), "none"))
	if s := loadEmpty(t, ""); s.ThemeName != "aide-dark" {
		t.Fatalf("no Omarchy: %q", s.ThemeName)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[theme]\nname = \"omarchy\"\n"), 0o644)
	s, probs := LoadFile(path)
	if s.ThemeName != "aide-dark" || len(probs) != 1 || !strings.Contains(probs[0].Msg, "omarchy") {
		t.Fatalf("omarchy named without Omarchy: %q, %v", s.ThemeName, probs)
	}

	omarchyAt(t, "tokyo-night")
	if s := loadEmpty(t, "[theme]\nname = \"dracula\"\n"); s.ThemeName != "dracula" || s.Theme.Colors.Fg != "#f8f8f2" {
		t.Fatalf("explicit dracula: %q %s", s.ThemeName, s.Theme.Colors.Fg)
	}
	s = loadEmpty(t, "[theme]\nname = \"omarchy\"\n[theme.colors]\nprimary = \"#ff0000\"\n")
	if s.ThemeName != OmarchyName || s.Theme.Colors.Primary != "#ff0000" || s.Theme.Colors.Fg != "#a9b1d6" {
		t.Fatalf("omarchy with an override: %q %+v", s.ThemeName, s.Theme.Colors)
	}
	names := []string{}
	for _, n := range AllThemes(t.TempDir()) {
		names = append(names, n.Name)
	}
	if !contains(names, OmarchyName) {
		t.Fatalf("AllThemes lacks omarchy: %v", names)
	}
}

// TestOmarchyHalfWritten: a colors.toml with no background, as one caught
// mid-write reads, is an error rather than a black and white theme.
func TestOmarchyHalfWritten(t *testing.T) {
	cur := omarchyAt(t, "tokyo-night")
	os.WriteFile(filepath.Join(cur, "theme", "colors.toml"), []byte("accent = \"#ffffff\"\n"), 0o644)
	if _, _, err := LoadOmarchy(); err == nil {
		t.Fatal("no error for a theme without a background")
	}
}
