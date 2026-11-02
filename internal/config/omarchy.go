package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The omarchy theme: the colors of the theme Omarchy has applied to the
// desktop. Omarchy 4 keeps it in ~/.local/state/omarchy/current/theme,
// Omarchy 3 in ~/.config/omarchy/current/theme; either way its palette is
// the colors.toml there. A theme switch replaces the theme folder and
// rewrites theme.name beside it, so WatchOmarchy watches the current folder.

// OmarchyName is the theme name that follows Omarchy.
const OmarchyName = "omarchy"

// OmarchyDirEnv, when set, is the one Omarchy current folder pitwall reads,
// for tests and screenshots.
const OmarchyDirEnv = "PITWALL_OMARCHY_DIR"

// OmarchyDirs are the folders Omarchy keeps the applied theme in, newest
// layout first. Omarchy uses $HOME/.local/state whatever XDG_STATE_HOME says.
func OmarchyDirs() []string {
	if d := os.Getenv(OmarchyDirEnv); d != "" {
		return []string{d}
	}
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".local", "state", "omarchy", "current"),
		filepath.Join(home, ".config", "omarchy", "current"),
	}
}

// OmarchyInstalled reports whether Omarchy has applied a theme here.
func OmarchyInstalled() bool { return omarchyDir() != "" }

// omarchyDir is the first current folder that holds a theme. A machine
// upgraded from Omarchy 3 can keep a stale old folder, so a newer one in use
// wins even while a switch has its theme folder away.
func omarchyDir() string {
	for _, d := range OmarchyDirs() {
		for _, f := range []string{"theme", "theme.name"} {
			if _, err := os.Lstat(filepath.Join(d, f)); err == nil {
				return d
			}
		}
	}
	return ""
}

// errNoOmarchy is LoadOmarchy's error on a machine without Omarchy.
var errNoOmarchy = errors.New("Omarchy has applied no theme on this machine")

// LoadOmarchy reads the theme Omarchy has applied, mapped onto pitwall's
// colors, and its name ("Tokyo Night").
func LoadOmarchy() (Theme, string, error) {
	dir := omarchyDir()
	if dir == "" {
		return Theme{}, "", errNoOmarchy
	}
	name := ""
	if b, err := readCapped(filepath.Join(dir, "theme.name"), 256); err == nil {
		name = omarchyDisplayName(strings.TrimSpace(string(b)))
	}
	t, err := loadOmarchyTheme(filepath.Join(dir, "theme"))
	return t, name, err
}

// loadOmarchyTheme maps the colors.toml in an Omarchy theme folder.
func loadOmarchyTheme(dir string) (Theme, error) {
	f := filepath.Join(dir, "colors.toml")
	data, err := readCapped(f, 64<<10)
	if err != nil {
		return Theme{}, err
	}
	_, light := os.Stat(filepath.Join(dir, "light.mode"))
	c := resolveOmarchy(data, light == nil)
	if _, ok := omarchyHex(c["background"]); !ok {
		// Empty, broken, or caught half written: keep the theme in force.
		return Theme{}, fmt.Errorf("%s names no background color", f)
	}
	return mapOmarchy(c), nil
}

func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return b, err
}

// omarchyDisplayName is the name Omarchy shows: "tokyo-night" is "Tokyo Night".
func omarchyDisplayName(id string) string {
	b := []byte(id)
	for i, c := range b {
		if c >= 'a' && c <= 'z' && (i == 0 || id[i-1] == '-') {
			b[i] = c - 'a' + 'A'
		}
	}
	return strings.ReplaceAll(string(b), "-", " ")
}

// The contrast floors the mapping holds Omarchy colors to, as tuios does:
// WCAG AA for text, and 3:1 for marks (status dots, the accent, icons).
const (
	textFloor = 4.5
	markFloor = 3.0
)

// mapOmarchy maps Omarchy's resolved keys onto pitwall's colors. The
// terminal gets the theme's own colors, the way Omarchy's terminal templates
// map them, so a pane looks like any terminal Omarchy themed. The window
// takes its grounds from the theme's background, text from its foreground
// and muted, the accent from accent and the status colors from the ANSI
// ones. A text color below 4.5:1, or a mark below 3:1, on the grounds it is
// drawn on is moved toward the theme's text end (white on a dark theme,
// black on a light one) in small steps until it reads.
func mapOmarchy(c map[string]string) Theme {
	col := func(keys ...string) rgb8 {
		for _, k := range keys {
			if v, ok := omarchyHex(c[k]); ok {
				return v
			}
		}
		return rgb8{}
	}
	light := c["mode"] == "light"
	bg, fg := col("background"), col("foreground")
	// pitwall's panes sit on Surface, a step off the sidebar's ground. On a
	// dark theme the sidebar is darker than the panes, on a light one a
	// shade toward the text.
	side := col("dark_background")
	if light {
		side = bg.mix(fg, 0.04)
	}
	end := rgb8{0xff, 0xff, 0xff}
	if light {
		end = rgb8{}
	}
	surf2, elev := bg.mix(fg, 0.07), bg.mix(fg, 0.12)
	grounds := []rgb8{side, bg, surf2}
	text := func(v rgb8) rgb8 { return readable(v, end, textFloor, append(grounds, elev)...) }
	mark := func(v rgb8) rgb8 { return readable(v, end, markFloor, grounds...) }
	fgUI := text(fg)
	primary := mark(col("accent", "blue"))
	onPrimary := side
	if contrast(fgUI, primary) > contrast(side, primary) {
		onPrimary = fgUI
	}
	t := Theme{
		Colors: Colors{
			Bg: side.hex(), Sidebar: side.hex(), Surface: bg.hex(),
			SurfaceSecondary: surf2.hex(), SurfaceElevated: elev.hex(),
			Border: side.mix(fg, 0.09).hex(), Fg: fgUI.hex(), Muted: text(col("muted")).hex(),
			Primary: primary.hex(), OnPrimary: onPrimary.hex(),
			Red: mark(col("red")).hex(), Yellow: mark(col("yellow")).hex(), Green: mark(col("green")).hex(),
			Blue: mark(col("blue")).hex(), Purple: mark(col("magenta")).hex(),
		},
		Terminal: Terminal{
			Background: bg.hex(),
			Foreground: readable(fg, end, textFloor, bg).hex(),
			Cursor:     col("cursor", "foreground").hex(),
		},
	}
	for i := range 16 {
		t.Terminal.ANSI = append(t.Terminal.ANSI, col("color"+strconv.Itoa(i)).hex())
	}
	return t
}

// rgb8 is an opaque 8-bit color.
type rgb8 [3]uint8

func (c rgb8) hex() Color { return Color(fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2])) }

// mix moves c toward d by a, 0 to 1, in sRGB, as theme.Mix does.
func (c rgb8) mix(d rgb8, a float64) rgb8 {
	var out rgb8
	for i := range c {
		out[i] = uint8(float64(c[i]) + (float64(d[i])-float64(c[i]))*a + 0.5)
	}
	return out
}

// readable is c moved toward end in 1/16 steps until it reaches floor on
// every ground, end when no step does. Adapted from tuios (MIT):
// internal/overlay/contrast.go (ReadableAt).
func readable(c, end rgb8, floor float64, grounds ...rgb8) rgb8 {
	for i := 0; i <= 16; i++ {
		m := c.mix(end, float64(i)/16)
		ok := true
		for _, g := range grounds {
			ok = ok && contrast(m, g) >= floor
		}
		if ok {
			return m
		}
	}
	return end
}

// contrast is the WCAG 2.1 contrast ratio, 1 to 21.
func contrast(a, b rgb8) float64 {
	lum := func(c rgb8) float64 {
		var l float64
		for i, w := range []float64{0.2126, 0.7152, 0.0722} {
			s := float64(c[i]) / 255
			if s <= 0.04045 {
				s /= 12.92
			} else {
				s = math.Pow((s+0.055)/1.055, 2.4)
			}
			l += w * s
		}
		return l
	}
	la, lb := lum(a), lum(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// omarchyHex reads #rrggbb or #rgb.
func omarchyHex(s string) (rgb8, bool) {
	s, ok := strings.CutPrefix(s, "#")
	if !ok || len(s) != 6 && len(s) != 3 {
		return rgb8{}, false
	}
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return rgb8{}, false
	}
	return rgb8{uint8(v >> 16), uint8(v >> 8), uint8(v)}, true
}

// The resolver below is a port of Omarchy's bin/omarchy-theme-color, which
// every Omarchy app reads its theme through, so a theme gives pitwall the
// colors it gives the rest of the desktop.
// Adapted from tuios (MIT): swatch/omarchy/colors.go

// resolveOmarchy returns every key omarchy-theme-color --all prints for a
// colors.toml. light says a light.mode file sits beside it.
func resolveOmarchy(data []byte, light bool) map[string]string {
	c := parseOmarchy(data)
	resolveOmarchyKeys(c, light)
	return c
}

// parseOmarchy reads key = value lines as the script's bash does.
func parseOmarchy(data []byte) map[string]string {
	c := map[string]string{}
	// bash's read loop skips a last line with no newline.
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return c
	}
	for line := range strings.SplitSeq(string(data[:end]), "\n") {
		line = strings.ReplaceAll(line, "\x00", "")
		key, value, found := strings.Cut(line, "=")
		if found && strings.HasSuffix(value, "=") && strings.Count(value, "=") == 1 {
			value = value[:len(value)-1]
		}
		key = strings.Map(func(r rune) rune {
			if r == '"' || r == '\'' || r == ' ' {
				return -1
			}
			return r
		}, key)
		if key == "" || key[0] == '#' {
			continue
		}
		if i := strings.IndexAny(value, `"'`); i >= 0 {
			value = value[i+1:]
			if j := strings.IndexAny(value, `"'`); j >= 0 {
				value = value[:j]
			}
		} else {
			value = strings.Trim(value, " \t\n\v\f\r")
		}
		if !allIn(key, keyChars) || !allIn(value, valueChars) {
			continue
		}
		c[key] = value
	}
	return c
}

const (
	alnum      = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	keyChars   = alnum + "_-"
	valueChars = alnum + "#(),._+/% -"
)

func allIn(s, set string) bool {
	for i := range len(s) {
		if strings.IndexByte(set, s[i]) < 0 {
			return false
		}
	}
	return true
}

// legacyKeys maps each ground and text key to the short name older themes used.
var legacyKeys = [][2]string{
	{"background", "bg"}, {"dark_background", "dark_bg"}, {"darker_background", "darker_bg"},
	{"lighter_background", "lighter_bg"}, {"foreground", "fg"}, {"dark_foreground", "dark_fg"},
	{"light_foreground", "light_fg"}, {"bright_foreground", "bright_fg"},
}

// semanticANSI is the ANSI slot behind each named color.
var semanticANSI = [][2]string{
	{"red", "color1"}, {"green", "color2"}, {"yellow", "color3"}, {"blue", "color4"},
	{"magenta", "color5"}, {"cyan", "color6"}, {"bright_red", "color9"}, {"bright_green", "color10"},
	{"bright_yellow", "color11"}, {"bright_blue", "color12"}, {"bright_magenta", "color13"}, {"bright_cyan", "color14"},
}

// ansiKeys is the key each ANSI slot takes, 0 to 15, as Omarchy's kitty,
// ghostty and foot templates map them.
var ansiKeys = [16]string{
	"background", "red", "green", "yellow", "blue", "magenta", "cyan", "foreground",
	"muted", "bright_red", "bright_green", "bright_yellow", "bright_blue", "bright_magenta", "bright_cyan", "bright_foreground",
}

// resolveOmarchyKeys is the script's resolve_theme_colors and
// resolve_theme_mode: aliases, then derived keys, then the mode.
func resolveOmarchyKeys(c map[string]string, lightFile bool) {
	alias := func(key, from string) {
		if c[key] == "" {
			c[key] = c[from]
		}
	}
	or := func(keys ...string) string {
		for _, k := range keys {
			if c[k] != "" {
				return c[k]
			}
		}
		return ""
	}
	set := func(key, v string) {
		if c[key] == "" {
			c[key] = v
		}
	}
	for _, p := range legacyKeys {
		alias(p[0], p[1])
	}
	set("background", c["color0"])
	set("foreground", c["color7"])
	if c["background"] != "" {
		c["color0"] = c["background"]
	}
	if c["foreground"] != "" {
		c["color7"] = c["foreground"]
	}
	for _, p := range semanticANSI {
		alias(p[0], p[1])
	}
	alias("magenta", "purple")
	alias("bright_magenta", "bright_purple")
	set("light_foreground", or("color7", "foreground"))
	set("bright_foreground", or("color15", "foreground"))
	c["cursor"] = c["bright_foreground"]
	set("lighter_background", or("color0", "background"))
	set("dark_foreground", or("color8", "foreground"))
	set("muted", or("color8", "dark_foreground"))
	set("selection", or("selection_background", "color8", "color0", "background"))
	set("selection_background", c["selection"])
	set("selection_foreground", c["bright_foreground"])
	set("orange", c["yellow"])
	set("dark_background", mixOmarchy(c["background"], "#000000", 0.25))
	set("darker_background", mixOmarchy(c["background"], "#000000", 0.5))
	for _, k := range []string{"red", "yellow", "green", "cyan", "blue", "magenta"} {
		set("bright_"+k, mixOmarchy(c[k], "#ffffff", 0.2))
	}
	for i, k := range ansiKeys {
		alias("color"+strconv.Itoa(i), k)
	}
	if c["mode"] == "" {
		c["mode"] = c["theme_type"]
	}
	if c["mode"] == "" {
		c["mode"] = "dark"
		if bg, ok := omarchyHex(c["background"]); lightFile || ok && len(c["background"]) == 7 && int(bg[0])+int(bg[1])+int(bg[2]) > 382 {
			c["mode"] = "light"
		}
	}
}

// mixOmarchy is the script's mix_color for #rrggbb colors; anything else
// gives "", which no color reader accepts.
func mixOmarchy(start, end string, a float64) string {
	s, ok1 := omarchyHex(start)
	e, ok2 := omarchyHex(end)
	if !ok1 || !ok2 {
		return ""
	}
	return string(s.mix(e, a).hex())
}
