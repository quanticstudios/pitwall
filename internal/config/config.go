// Package config reads pitwall's config.toml: keybindings, theme and fonts.
// The structs below are the single source of truth; the JSON Schema, the
// commented default file and the settings dialog's labels all come from
// them and their doc tags.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// Config is config.toml.
type Config struct {
	Keys      Keys      `toml:"keys" doc:"Keybindings: a preset, then single actions overriding it. A value is a chord (\"Ctrl+Shift+T\"), an array of chords, or [] to unbind."`
	Theme     Theme     `toml:"theme" doc:"Colors."`
	Font      Font      `toml:"font" doc:"Fonts: any installed family (see fc-list : family)."`
	Layout    Layout    `toml:"layout" doc:"Spacing around panes, in dp."`
	Term      Term      `toml:"terminal" doc:"How panes behave. Terminal colors are under [theme.terminal]."`
	Updates   Updates   `toml:"updates" doc:"Release updates."`
	Decisions Decisions `toml:"decisions" doc:"A decision model, such as TypeSafe's Jev, answering quick questions: approval recommendations, attention triage, status for agents without hooks, turn checks. Off until provider is set; see the README for what each feature sends."`
}

// Keys is [keys]. Every Binding field is an action.
type Keys struct {
	Preset           string  `toml:"preset" enum:"conventional,aide" doc:"conventional follows Linux terminals (Ghostty, kitty, GNOME Terminal); aide is aide's Alt-key layout."`
	SwitcherModifier *string `toml:"switcher_modifier" enum:"Alt,Super,Ctrl," doc:"Holding this modifier shows the tab switcher; \"\" for none. aide uses Alt, conventional none."`

	NextTab         Binding `toml:"next_tab" doc:"Next tab in sidebar order. With a switcher_modifier it stays in the tab's group until the switcher shows"`
	PrevTab         Binding `toml:"prev_tab" doc:"Previous tab in sidebar order. With a switcher_modifier it stays in the tab's group until the switcher shows"`
	NextGroup       Binding `toml:"next_group" doc:"First tab of the next group"`
	PrevGroup       Binding `toml:"prev_group" doc:"First tab of the previous group"`
	GotoTab1        Binding `toml:"goto_tab_1" doc:"Go to tab 1"`
	GotoTab2        Binding `toml:"goto_tab_2" doc:"Go to tab 2"`
	GotoTab3        Binding `toml:"goto_tab_3" doc:"Go to tab 3"`
	GotoTab4        Binding `toml:"goto_tab_4" doc:"Go to tab 4"`
	GotoTab5        Binding `toml:"goto_tab_5" doc:"Go to tab 5"`
	GotoTab6        Binding `toml:"goto_tab_6" doc:"Go to tab 6"`
	GotoTab7        Binding `toml:"goto_tab_7" doc:"Go to tab 7"`
	GotoTab8        Binding `toml:"goto_tab_8" doc:"Go to tab 8"`
	GotoTab9        Binding `toml:"goto_tab_9" doc:"Go to tab 9"`
	JumpAttention   Binding `toml:"jump_attention" doc:"Go to the tab that needs you, newest first"`
	NewTab          Binding `toml:"new_tab" doc:"New tab below this one, in its folder"`
	CloseTab        Binding `toml:"close_tab" doc:"Close the tab and all its panes"`
	NextPane        Binding `toml:"next_pane" doc:"Next pane"`
	PrevPane        Binding `toml:"prev_pane" doc:"Previous pane"`
	SplitRight      Binding `toml:"split_right" doc:"Split the pane to the right"`
	SplitDown       Binding `toml:"split_down" doc:"Split the pane below"`
	ClosePane       Binding `toml:"close_pane" doc:"Close the pane (the tab with its last pane)"`
	Switcher        Binding `toml:"switcher" doc:"Show or hide the tab switcher"`
	PinSwitcher     Binding `toml:"pin_switcher" doc:"Keep the switcher open after the hold modifier is released"`
	SessionSwitcher Binding `toml:"session_switcher" doc:"Show the session switcher: every session, live, to switch to, make, rename or kill one"`
	SessionNew      Binding `toml:"session_new" doc:"New session, named in the session switcher"`
	SessionNext     Binding `toml:"session_next" doc:"Next session in the switcher's order"`
	SessionPrev     Binding `toml:"session_prev" doc:"Previous session in the switcher's order"`
	SessionRename   Binding `toml:"session_rename" doc:"Rename this session in the session switcher"`
	Copy            Binding `toml:"copy" doc:"Copy the selection"`
	Paste           Binding `toml:"paste" doc:"Paste"`
	ScrollPageUp    Binding `toml:"scroll_page_up" doc:"Scroll back a page"`
	ScrollPageDown  Binding `toml:"scroll_page_down" doc:"Scroll forward a page"`
	ToggleSidebar   Binding `toml:"toggle_sidebar" doc:"Show or hide the sidebar. aide's Ctrl+B then never reaches the shell (readline's backward-char, the tmux prefix); set toggle_sidebar = \"Ctrl+Shift+B\" or [] to give it back"`
	TogglePanel     Binding `toml:"toggle_panel" doc:"Show or hide the agent panel. It follows the agent in the focused pane"`
	OpenSettings    Binding `toml:"open_settings" doc:"Show or hide the settings page"`
	TabPrefix       Binding `toml:"tab_prefix" doc:"Tab mode: the next key runs a [keys.tab] action. Pressed twice it sends its control character to the pane"`
	PanePrefix      Binding `toml:"pane_prefix" doc:"Pane mode: [keys.pane] keys act on panes until Esc or Enter. Pressed twice it sends its control character to the pane"`

	Tab TabKeys `toml:"tab" doc:"Keys in tab mode, after the tab prefix."`

	Pane PaneKeys `toml:"pane" doc:"Keys in pane mode, after the pane prefix. The mode stays on after each one."`
}

// TabKeys is [keys.tab].
type TabKeys struct {
	New        Binding `toml:"new" doc:"New tab outside every group, in this tab's folder"`
	NewInGroup Binding `toml:"new_in_group" doc:"New tab in this tab's group, right after it"`
	Close      Binding `toml:"close" doc:"Close the tab"`
	Rename     Binding `toml:"rename" doc:"Rename the tab"`
	Prev       Binding `toml:"prev" doc:"Previous tab"`
	Next       Binding `toml:"next" doc:"Next tab"`
	Attention  Binding `toml:"attention" doc:"Go to the tab that needs you, newest first"`
	Sessions   Binding `toml:"sessions" doc:"Show the session switcher"`
	Goto1      Binding `toml:"goto_1" doc:"Go to tab 1"`
	Goto2      Binding `toml:"goto_2" doc:"Go to tab 2"`
	Goto3      Binding `toml:"goto_3" doc:"Go to tab 3"`
	Goto4      Binding `toml:"goto_4" doc:"Go to tab 4"`
	Goto5      Binding `toml:"goto_5" doc:"Go to tab 5"`
	Goto6      Binding `toml:"goto_6" doc:"Go to tab 6"`
	Goto7      Binding `toml:"goto_7" doc:"Go to tab 7"`
	Goto8      Binding `toml:"goto_8" doc:"Go to tab 8"`
	Goto9      Binding `toml:"goto_9" doc:"Go to tab 9"`
}

// PaneKeys is [keys.pane].
type PaneKeys struct {
	New        Binding `toml:"new" doc:"New pane, split along the focused pane's longer side"`
	SplitDown  Binding `toml:"split_down" doc:"Split the pane below"`
	SplitRight Binding `toml:"split_right" doc:"Split the pane to the right"`
	Close      Binding `toml:"close" doc:"Close the pane"`
	FocusLeft  Binding `toml:"focus_left" doc:"Focus the pane to the left"`
	FocusDown  Binding `toml:"focus_down" doc:"Focus the pane below"`
	FocusUp    Binding `toml:"focus_up" doc:"Focus the pane above"`
	FocusRight Binding `toml:"focus_right" doc:"Focus the pane to the right"`
	Fullscreen Binding `toml:"fullscreen" doc:"Let the pane fill the tab, or give the others back"`
	Next       Binding `toml:"next" doc:"Next pane"`
}

// Color is "#rrggbb" or "#rrggbbaa".
type Color string

// Theme is [theme], and the whole of a themes/<name>.toml file.
type Theme struct {
	Name     string   `toml:"name" enum:"@themes" doc:"A built-in theme, or a custom one from themes/<name>.toml next to this file. In a theme file: the built-in it starts from (default aide-dark)."`
	Colors   Colors   `toml:"colors" doc:"Window colors; each replaces the theme's."`
	Terminal Terminal `toml:"terminal" doc:"Terminal colors. The daemon answers programs' color queries from them; a running daemon picks changes up on its next start."`
}

// Colors are the window's colors.
type Colors struct {
	Bg               Color `toml:"bg" doc:"Window background"`
	Sidebar          Color `toml:"sidebar" doc:"Sidebar background"`
	Surface          Color `toml:"surface" doc:"Pane canvas, dialogs, cards"`
	SurfaceSecondary Color `toml:"surface_secondary" doc:"Selected rows, active tab, fields"`
	SurfaceElevated  Color `toml:"surface_elevated" doc:"Hovered and floating surfaces, badges"`
	Border           Color `toml:"border" doc:"Hairlines"`
	Fg               Color `toml:"fg" doc:"Text"`
	Muted            Color `toml:"muted" doc:"Secondary text"`
	Primary          Color `toml:"primary" doc:"Accent: buttons, focus, the tab-mode chip"`
	OnPrimary        Color `toml:"on_primary" doc:"Text on primary buttons"`
	Red              Color `toml:"red" doc:"Errors"`
	Yellow           Color `toml:"yellow" doc:"Waiting for you"`
	Green            Color `toml:"green" doc:"Done, idle"`
	Blue             Color `toml:"blue" doc:"Working"`
	Purple           Color `toml:"purple" doc:"Plan ready"`
}

// Terminal is the terminal palette.
type Terminal struct {
	Foreground Color   `toml:"foreground" doc:"Default text"`
	Background Color   `toml:"background" doc:"Default background"`
	Cursor     Color   `toml:"cursor" doc:"Cursor"`
	ANSI       []Color `toml:"ansi" doc:"The 16 ANSI colors: black, red, green, yellow, blue, magenta, cyan, white, then the bright ones"`
}

// Font is [font].
type Font struct {
	UIFamily     string   `toml:"ui_family" doc:"Sidebar and dialog text. Geist is the copy bundled with pitwall"`
	UISize       float64  `toml:"ui_size" min:"6" max:"48" doc:"UI text size"`
	MonoFamily   string   `toml:"mono_family" doc:"Terminal font"`
	MonoSize     float64  `toml:"mono_size" min:"6" max:"48" doc:"Terminal text size"`
	LineHeight   float64  `toml:"line_height" min:"0.8" max:"3" doc:"Terminal line height as a multiple of the font's"`
	MonoFallback []string `toml:"mono_fallback" doc:"Families tried, in order, for characters the terminal font lacks, before any monospace font and color emoji"`
}

// Layout is [layout].
type Layout struct {
	PaneGap    *float64 `toml:"pane_gap" min:"0" max:"64" doc:"Space between split panes"`
	PaneMargin *float64 `toml:"pane_margin" min:"0" max:"64" doc:"Space between the panes and the window edges and sidebar"`
}

// Term is [terminal].
type Term struct {
	CopyOnSelect *bool `toml:"copy_on_select" doc:"Copy text to the clipboard as soon as you select it with the mouse, as zellij and Warp do. The copy key works either way"`
	Links        *bool `toml:"links" doc:"Underline web and file links in panes, plain URLs and OSC 8 hyperlinks alike, and open them with Ctrl+click"`
}

// Updates is [updates].
type Updates struct {
	Check *bool `toml:"check" doc:"Check GitHub for a newer release at start and every 6 hours, and offer it with an Update button at the bottom of the sidebar. Release builds on Linux and macOS only"`
}

// Font defaults.
const (
	DefaultUIFamily   = "Geist"
	DefaultMonoFamily = "JetBrainsMono Nerd Font"
	DefaultUISize     = 13
	DefaultMonoSize   = 13
	DefaultPaneGap    = 4
	DefaultPaneMargin = 4
)

// Settings is a loaded config with every value filled in.
type Settings struct {
	Path       string
	Keys       *Bindings
	ThemeName  string
	Theme      Theme  // every color set
	ThemeFile  string // the custom theme file, "" for a built-in
	Font       Font   // every field but MonoFallback set
	PaneGap    float64
	PaneMargin float64
	// CopyOnSelect copies a mouse selection when it is made.
	CopyOnSelect bool
	// Links underlines links in panes and opens them on Ctrl+click.
	Links bool
	// CheckUpdates looks for a newer release on GitHub.
	CheckUpdates bool
	// Decisions is [decisions] resolved.
	Decisions DecideSettings
	// Notes are things that work but should change, like an action under
	// its old name. They are not problems: the GUI stays quiet about them.
	Notes []Problem
}

// Problem is one thing wrong with a config or theme file. The entry it is
// about falls back to its default.
type Problem struct {
	File string // as shown: "config.toml", "themes/x.toml"
	Line int    // 0 when unknown
	Msg  string
}

func (p Problem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Msg)
	}
	return p.File + ": " + p.Msg
}

type issue struct{ path, msg string }

// Dir is $XDG_CONFIG_HOME/pitwall, ~/.config/pitwall without it, and
// %APPDATA%\pitwall on Windows.
func Dir() string {
	return filepath.Join(baseDir("XDG_CONFIG_HOME", ".config", os.UserConfigDir), "pitwall")
}

// StateDir is $XDG_STATE_HOME/pitwall, ~/.local/state/pitwall without it,
// and %LOCALAPPDATA%\pitwall on Windows.
func StateDir() string {
	return filepath.Join(baseDir("XDG_STATE_HOME", filepath.Join(".local", "state"), os.UserCacheDir), "pitwall")
}

// baseDir is $name, else the Windows folder win names, else ~/rel. macOS
// keeps the XDG layout, like the agent CLIs pitwall runs.
func baseDir(name, rel string, win func() (string, error)) string {
	if d := os.Getenv(name); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if d, err := win(); err == nil {
			return d
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, rel)
}

// Path is config.toml in Dir.
func Path() string { return filepath.Join(Dir(), "config.toml") }

// SchemaPath is where the GUI and `config init` keep the schema.
func SchemaPath() string { return filepath.Join(Dir(), "schema.json") }

// ThemeSchemaPath is the schema for custom theme files.
func ThemeSchemaPath() string { return filepath.Join(Dir(), "theme.schema.json") }

// Load reads Path. A missing file is the defaults.
func Load() (Settings, []Problem) { return LoadFile(Path()) }

// LoadFile reads the config at path and the custom theme it names.
func LoadFile(path string) (Settings, []Problem) {
	var c Config
	var probs []Problem
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		probs = append(probs, Problem{File: filepath.Base(path), Msg: err.Error()})
	}
	s := Settings{Path: path}
	var renamed []issue
	probs = append(probs, parse("config.toml", data, &c, &renamed)...)
	s.Notes = locate("config.toml", data, renamed)
	sort.SliceStable(s.Notes, func(i, j int) bool { return s.Notes[i].Line < s.Notes[j].Line })
	var issues []issue
	s.Keys, issues = resolveKeys(c.Keys)
	probs = append(probs, locate("config.toml", data, issues)...)

	s.ThemeName = c.Theme.Name
	if s.ThemeName == "" {
		s.ThemeName = "aide-dark"
	}
	base, ok := builtins[s.ThemeName]
	if !ok {
		base = builtins["aide-dark"]
		dir := filepath.Join(filepath.Dir(path), "themes")
		f := filepath.Join(dir, s.ThemeName+".toml")
		td, err := os.ReadFile(f)
		switch {
		case errors.Is(err, os.ErrNotExist) || strings.ContainsAny(s.ThemeName, `/\`):
			probs = append(probs, locate("config.toml", data, []issue{{"theme.name",
				fmt.Sprintf("no theme %q: not built in (%s) and no %s%s", s.ThemeName, strings.Join(Themes, ", "), f, suggest(s.ThemeName, append(slices.Clone(Themes), customThemes(dir)...)))}})...)
			s.ThemeName = "aide-dark"
		case err != nil:
			probs = append(probs, Problem{File: "themes/" + s.ThemeName + ".toml", Msg: err.Error()})
		default:
			s.ThemeFile = f
			name := "themes/" + s.ThemeName + ".toml"
			var t Theme
			probs = append(probs, parse(name, td, &t, nil)...)
			if t.Name != "" {
				if b, ok := builtins[t.Name]; ok {
					base = b
				} else {
					probs = append(probs, locate(name, td, []issue{{"name", fmt.Sprintf("%q is not a built-in theme%s", t.Name, suggest(t.Name, Themes))}})...)
				}
			}
			var ti []issue
			base, ti = overlay(base, t, "")
			probs = append(probs, locate(name, td, ti)...)
		}
	}
	var ti []issue
	s.Theme, ti = overlay(base, c.Theme, "theme.")
	probs = append(probs, locate("config.toml", data, ti)...)

	s.Font = c.Font
	var fi []issue
	for _, f := range []struct {
		v      *float64
		name   string
		def    float64
		lo, hi float64
	}{
		{&s.Font.UISize, "font.ui_size", DefaultUISize, 6, 48},
		{&s.Font.MonoSize, "font.mono_size", DefaultMonoSize, 6, 48},
		{&s.Font.LineHeight, "font.line_height", 1, 0.8, 3},
		{or(c.Layout.PaneGap, &s.PaneGap), "layout.pane_gap", DefaultPaneGap, 0, 64},
		{or(c.Layout.PaneMargin, &s.PaneMargin), "layout.pane_margin", DefaultPaneMargin, 0, 64},
	} {
		unset := *f.v == 0 && !strings.HasPrefix(f.name, "layout.")
		if f.name == "layout.pane_gap" && c.Layout.PaneGap == nil || f.name == "layout.pane_margin" && c.Layout.PaneMargin == nil {
			unset = true
		}
		if !unset && (*f.v < f.lo || *f.v > f.hi) {
			fi = append(fi, issue{f.name, fmt.Sprintf("%g is outside %g-%g", *f.v, f.lo, f.hi)})
			unset = true
		}
		if unset {
			*f.v = f.def
		}
	}
	s.PaneGap, s.PaneMargin = *or(c.Layout.PaneGap, &s.PaneGap), *or(c.Layout.PaneMargin, &s.PaneMargin)
	s.CopyOnSelect = c.Term.CopyOnSelect == nil || *c.Term.CopyOnSelect
	s.Links = c.Term.Links == nil || *c.Term.Links
	s.CheckUpdates = c.Updates.Check == nil || *c.Updates.Check
	var di []issue
	var dn []issue
	s.Decisions, di, dn = resolveDecisions(c.Decisions)
	fi = append(fi, di...)
	s.Notes = append(s.Notes, locate("config.toml", data, dn)...)
	sort.SliceStable(s.Notes, func(i, j int) bool { return s.Notes[i].Line < s.Notes[j].Line })
	if s.Font.UIFamily == "" {
		s.Font.UIFamily = DefaultUIFamily
	}
	if s.Font.MonoFamily == "" {
		s.Font.MonoFamily = DefaultMonoFamily
	}
	probs = append(probs, locate("config.toml", data, fi)...)
	sort.SliceStable(probs, func(i, j int) bool {
		if probs[i].File != probs[j].File {
			return probs[i].File == "config.toml"
		}
		return probs[i].Line < probs[j].Line
	})
	return s, probs
}

// or is p, or def when p is nil.
func or(p, def *float64) *float64 {
	if p != nil {
		return p
	}
	return def
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func customThemes(dir string) []string {
	var out []string
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if n, ok := strings.CutSuffix(e.Name(), ".toml"); ok {
			out = append(out, n)
		}
	}
	return out
}

// Palette is the terminal palette of the config at Path, for the daemon.
// Problems are the GUI's to report.
func Palette() vt.Palette {
	s, _ := Load()
	return s.Theme.Palette()
}

// Palette converts the terminal colors, which must all be set.
func (t Theme) Palette() vt.Palette {
	p := vt.Palette{Fg: rgb(t.Terminal.Foreground), Bg: rgb(t.Terminal.Background), Cursor: rgb(t.Terminal.Cursor)}
	for i, c := range t.Terminal.ANSI {
		if i < len(p.ANSI) {
			p.ANSI[i] = rgb(c)
		}
	}
	return p
}

var hexRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}([0-9A-Fa-f]{2})?$`)

func rgb(c Color) uint32 {
	var v uint32
	fmt.Sscanf(string(c)[1:7], "%06x", &v)
	return v
}

// overlay sets every color o sets on base, skipping (and reporting) bad ones.
func overlay(base, o Theme, path string) (Theme, []issue) {
	var issues []issue
	ok := func(p string, c Color) bool {
		if c == "" {
			return false
		}
		if !hexRe.MatchString(string(c)) {
			issues = append(issues, issue{path + p, fmt.Sprintf("%q is not a #rrggbb color", c)})
			return false
		}
		return true
	}
	bv, ov := reflect.ValueOf(&base.Colors).Elem(), reflect.ValueOf(o.Colors)
	for _, f := range fields(bv.Type()) {
		if c := ov.Field(f.index).Interface().(Color); ok("colors."+f.name, c) {
			bv.Field(f.index).Set(reflect.ValueOf(c))
		}
	}
	for _, f := range []struct {
		dst *Color
		src Color
		n   string
	}{{&base.Terminal.Foreground, o.Terminal.Foreground, "foreground"}, {&base.Terminal.Background, o.Terminal.Background, "background"}, {&base.Terminal.Cursor, o.Terminal.Cursor, "cursor"}} {
		if ok("terminal."+f.n, f.src) {
			*f.dst = f.src
		}
	}
	if o.Terminal.ANSI != nil {
		if len(o.Terminal.ANSI) != 16 {
			issues = append(issues, issue{path + "terminal.ansi", fmt.Sprintf("has %d colors, want 16", len(o.Terminal.ANSI))})
		} else {
			ansi := slices.Clone(base.Terminal.ANSI)
			for i, c := range o.Terminal.ANSI {
				if ok("terminal.ansi", c) {
					ansi[i] = c
				}
			}
			base.Terminal.ANSI = ansi
		}
	}
	base.Name = ""
	return base, issues
}

// parse decodes TOML data into the struct v points to. Syntax errors keep
// all defaults; unknown keys and wrong types drop only their entry. With
// renamed set, [keys] entries under an old action name count as the new
// name, and renamed gets a note for each.
func parse(file string, data []byte, v any, renamed *[]issue) []Problem {
	if len(data) == 0 {
		return nil
	}
	var m map[string]any
	if _, err := toml.Decode(string(data), &m); err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			return []Problem{{File: file, Line: pe.Position.Line, Msg: pe.Message}}
		}
		return []Problem{{File: file, Msg: err.Error()}}
	}
	if d, ok := m["decisions"].(map[string]any); ok && renamed != nil {
		if a, ok := d["approvals"].(map[string]any); ok {
			for _, k := range RemovedApprovals {
				if _, set := a[k]; set {
					delete(a, k)
					*renamed = append(*renamed, issue{"decisions.approvals." + k, "no longer supported: automatic approval was removed; this line is ignored"})
				}
			}
		}
	}
	if keys, ok := m["keys"].(map[string]any); ok && renamed != nil {
		for _, old := range slices.Sorted(mapKeys(keys)) {
			now, ok := Renamed[old]
			if !ok {
				continue
			}
			if _, set := keys[now]; set {
				*renamed = append(*renamed, issue{"keys." + old, fmt.Sprintf("renamed to %s, which is also set; this line is ignored", now)})
			} else {
				keys[now] = keys[old]
				*renamed = append(*renamed, issue{"keys." + old, fmt.Sprintf("renamed to %s; the old name still works for now", now)})
			}
			delete(keys, old)
		}
	}
	var issues []issue
	decode(m, reflect.ValueOf(v).Elem(), "", &issues)
	return locate(file, data, issues)
}

var (
	bindingType = reflect.TypeFor[Binding]()
	colorType   = reflect.TypeFor[Color]()
	colorsType  = reflect.TypeFor[[]Color]()
	stringsType = reflect.TypeFor[[]string]()
)

type field struct {
	name, doc string
	index     int
	typ       reflect.Type
	tag       reflect.StructTag
}

// fields are t's TOML fields in declaration order.
func fields(t reflect.Type) []field {
	var out []field
	for i := range t.NumField() {
		f := t.Field(i)
		if n := f.Tag.Get("toml"); n != "" && n != "-" {
			out = append(out, field{n, f.Tag.Get("doc"), i, f.Type, f.Tag})
		}
	}
	return out
}

func decode(m map[string]any, v reflect.Value, path string, issues *[]issue) {
	fs := fields(v.Type())
	var names []string
	for _, f := range fs {
		names = append(names, f.name)
	}
	for _, k := range slices.Sorted(mapKeys(m)) {
		p := path + k
		i := slices.Index(names, k)
		if i < 0 {
			where := "[" + strings.TrimSuffix(path, ".") + "]"
			if path == "" {
				where = "the top level"
			}
			*issues = append(*issues, issue{p, fmt.Sprintf("unknown key %q in %s%s", k, where, suggest(k, names))})
			continue
		}
		if msg := set(v.Field(fs[i].index), m[k], p, issues); msg != "" {
			*issues = append(*issues, issue{p, msg})
		}
	}
}

func strs(val any) ([]string, bool) {
	a, ok := val.([]any)
	if !ok {
		return nil, false
	}
	out := []string{}
	for _, x := range a {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func set(f reflect.Value, val any, path string, issues *[]issue) string {
	switch f.Type() {
	case bindingType:
		if s, ok := val.(string); ok {
			f.Set(reflect.ValueOf(Binding{s}))
		} else if ss, ok := strs(val); ok {
			f.Set(reflect.ValueOf(Binding(ss)))
		} else {
			return "want a chord string or an array of them"
		}
		return ""
	case stringsType:
		ss, ok := strs(val)
		if !ok {
			return "want an array of strings"
		}
		f.Set(reflect.ValueOf(ss))
		return ""
	case colorsType:
		ss, ok := strs(val)
		if !ok {
			return "want an array of 16 colors"
		}
		cs := make([]Color, len(ss))
		for i, s := range ss {
			cs[i] = Color(s)
		}
		f.Set(reflect.ValueOf(cs))
		return ""
	}
	switch f.Kind() {
	case reflect.Struct:
		t, ok := val.(map[string]any)
		if !ok {
			return "want a table"
		}
		decode(t, f, path+".", issues)
	case reflect.String:
		s, ok := val.(string)
		if !ok {
			return "want a string"
		}
		f.SetString(s)
	case reflect.Pointer:
		p := reflect.New(f.Type().Elem())
		if msg := set(p.Elem(), val, path, issues); msg != "" {
			return msg
		}
		f.Set(p)
	case reflect.Bool:
		b, ok := val.(bool)
		if !ok {
			return "want true or false"
		}
		f.SetBool(b)
	case reflect.Float64:
		switch n := val.(type) {
		case float64:
			f.SetFloat(n)
		case int64:
			f.SetFloat(float64(n))
		default:
			return "want a number"
		}
	}
	return ""
}

// locate turns dotted paths into problems with the line that sets them.
func locate(file string, data []byte, issues []issue) []Problem {
	lines := keyLines(string(data))
	var out []Problem
	for _, is := range issues {
		p := Problem{File: file, Msg: is.path + ": " + is.msg}
		for k := is.path; k != ""; {
			if l, ok := lines[k]; ok {
				p.Line = l
				break
			}
			i := strings.LastIndex(k, ".")
			if i < 0 {
				break
			}
			k = k[:i]
		}
		out = append(out, p)
	}
	return out
}

var (
	tableRe = regexp.MustCompile(`^\s*\[\s*([^\]\[]+?)\s*\]`)
	keyRe   = regexp.MustCompile(`^\s*([A-Za-z0-9_.\-"' ]+?)\s*=`)
)

// keyLines maps each dotted key and table the file sets to its first line.
// Multi-line values and inline tables are not followed: their keys resolve
// to the line that starts them.
func keyLines(s string) map[string]int {
	out := map[string]int{}
	table := ""
	for i, line := range strings.Split(s, "\n") {
		if m := tableRe.FindStringSubmatch(line); m != nil {
			table = clean(m[1])
			if _, ok := out[table]; !ok {
				out[table] = i + 1
			}
			continue
		}
		if m := keyRe.FindStringSubmatch(line); m != nil {
			k := clean(m[1])
			if table != "" {
				k = table + "." + k
			}
			if _, ok := out[k]; !ok {
				out[k] = i + 1
			}
		}
	}
	return out
}

func clean(k string) string {
	parts := strings.Split(k, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, ".")
}

// suggest is ` (did you mean "x"?)` for the closest of names, or "".
func suggest(s string, names []string) string {
	best, bd := "", 1<<30
	for _, n := range names {
		if d := distance(strings.ToLower(s), strings.ToLower(n)); d < bd {
			best, bd = n, d
		}
	}
	if best == "" || bd > max(2, len(s)/3) {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

// distance is the Levenshtein edit distance.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
