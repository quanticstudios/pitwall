package config

import (
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"

	"gioui.org/io/key"
)

// Chord is one key with the exact modifiers held.
type Chord struct {
	Mods key.Modifiers
	Name key.Name
}

// Match reports whether e is this chord, ignoring press or release.
func (c Chord) Match(e key.Event) bool { return e.Name == c.Name && e.Modifiers == c.Mods }

var modNames = []struct {
	mod   key.Modifiers
	names []string
}{
	{key.ModCtrl, []string{"Ctrl", "Control"}},
	{key.ModAlt, []string{"Alt"}},
	{key.ModShift, []string{"Shift"}},
	{key.ModSuper, []string{"Super"}},
	{key.ModCommand, []string{"Cmd", "Command"}},
}

// superMod is what Super and Cmd in a config mean: the logo key, or on
// macOS the Command key, which Gio reports as ModCommand.
var superMod = superFor(runtime.GOOS)

func superFor(goos string) key.Modifiers {
	if goos == "darwin" {
		return key.ModCommand
	}
	return key.ModSuper
}

// keyNames maps the spellings a config may use to Gio key names. The first
// spelling of each is the one String prints. Single printable characters
// are accepted as themselves.
var keyNames = []struct {
	name   key.Name
	spells []string
}{
	{key.NameSpace, []string{"Space"}},
	{key.NameTab, []string{"Tab"}},
	{key.NameReturn, []string{"Enter", "Return"}},
	{key.NameEscape, []string{"Esc", "Escape"}},
	{key.NameDeleteBackward, []string{"Backspace"}},
	{key.NameDeleteForward, []string{"Delete", "Del"}},
	{key.Name("Insert"), []string{"Insert", "Ins"}}, // io/key has no NameInsert
	{key.NameHome, []string{"Home"}},
	{key.NameEnd, []string{"End"}},
	{key.NamePageUp, []string{"PageUp", "PgUp"}},
	{key.NamePageDown, []string{"PageDown", "PgDn"}},
	{key.NameUpArrow, []string{"Up"}},
	{key.NameDownArrow, []string{"Down"}},
	{key.NameLeftArrow, []string{"Left"}},
	{key.NameRightArrow, []string{"Right"}},
	{key.NameF1, []string{"F1"}}, {key.NameF2, []string{"F2"}}, {key.NameF3, []string{"F3"}},
	{key.NameF4, []string{"F4"}}, {key.NameF5, []string{"F5"}}, {key.NameF6, []string{"F6"}},
	{key.NameF7, []string{"F7"}}, {key.NameF8, []string{"F8"}}, {key.NameF9, []string{"F9"}},
	{key.NameF10, []string{"F10"}}, {key.NameF11, []string{"F11"}}, {key.NameF12, []string{"F12"}},
}

// ParseChord reads "Ctrl+Shift+T", case-insensitively. "Ctrl++" is Ctrl
// with the plus key.
func ParseChord(s string) (Chord, error) {
	var c Chord
	rest := s
	if strings.HasSuffix(rest, "++") || rest == "+" {
		c.Name = "+"
		rest = strings.TrimSuffix(rest, "+")
	} else if i := strings.LastIndex(rest, "+"); i >= 0 {
		c.Name, rest = key.Name(rest[i+1:]), rest[:i+1]
	} else {
		c.Name, rest = key.Name(rest), ""
	}
	if rest != "" {
		for _, m := range strings.Split(strings.TrimSuffix(rest, "+"), "+") {
			mod, ok := parseMod(m)
			if !ok {
				return Chord{}, fmt.Errorf("%q: unknown modifier %q (use Ctrl, Alt, Shift, Super)", s, m)
			}
			if c.Mods&mod != 0 {
				return Chord{}, fmt.Errorf("%q: %s twice", s, m)
			}
			c.Mods |= mod
		}
	}
	n, ok := parseKey(string(c.Name))
	if !ok {
		if _, mod := parseMod(string(c.Name)); mod {
			return Chord{}, fmt.Errorf("%q: needs a key after the modifiers", s)
		}
		return Chord{}, fmt.Errorf("%q: unknown key %q", s, c.Name)
	}
	c.Name = n
	return c, nil
}

func parseMod(s string) (key.Modifiers, bool) {
	for _, m := range modNames {
		for _, n := range m.names {
			if !strings.EqualFold(s, n) {
				continue
			}
			if m.mod == key.ModSuper || m.mod == key.ModCommand {
				return superMod, true
			}
			return m.mod, true
		}
	}
	return 0, false
}

func parseKey(s string) (key.Name, bool) {
	if len(s) == 1 && s[0] > ' ' && s[0] <= '~' {
		return key.Name(strings.ToUpper(s)), true // Gio names letters in upper case
	}
	for _, k := range keyNames {
		for _, sp := range k.spells {
			if strings.EqualFold(s, sp) {
				return k.name, true
			}
		}
	}
	return "", false
}

// String is the chord as a config writes it: "Ctrl+Alt+Shift+Super+Key".
func (c Chord) String() string {
	var b strings.Builder
	for _, m := range modNames {
		if c.Mods&m.mod != 0 {
			b.WriteString(m.names[0] + "+")
		}
	}
	for _, k := range keyNames {
		if k.name == c.Name {
			return b.String() + k.spells[0]
		}
	}
	return b.String() + string(c.Name)
}

// Binding is the value of one action: nil when the config leaves it to the
// preset, empty (non-nil) when it unbinds the action.
type Binding []string

// paneActions are handled by the terminal view, not the window.
var paneActions = []string{"copy", "copy_mode", "paste", "scroll_page_up", "scroll_page_down", "prev_prompt", "next_prompt"}

// presets hold every action's chords. Actions a preset leaves out are
// unbound in it.
var presets = map[string]struct {
	hold        string
	global, tab map[string][]string
	pane        map[string][]string
}{
	"aide": {
		hold: "Alt",
		global: map[string][]string{
			"next_tab": {"Alt+J", "Alt+Down"}, "prev_tab": {"Alt+K", "Alt+Up"},
			"next_pane": {"Alt+L", "Alt+Right"}, "prev_pane": {"Alt+H", "Alt+Left"},
			"split_right": {"Alt+N"}, "split_down": {"Alt+Shift+N"}, "close_pane": {"Alt+Shift+W"},
			"new_tab": {"Alt+Shift+T"}, "pin_switcher": {"Alt+Space"},
			"copy": {"Ctrl+Shift+C", "Ctrl+Insert"}, "paste": {"Ctrl+Shift+V", "Shift+Insert"}, "copy_mode": {"Ctrl+Shift+X"},
			"scroll_page_up": {"Shift+PageUp"}, "scroll_page_down": {"Shift+PageDown"}, "find": {"Ctrl+Shift+F"},
			"prev_prompt": {"Ctrl+Shift+Up"}, "next_prompt": {"Ctrl+Shift+Down"},
			"allow_prompt": {"Ctrl+Shift+Y"}, "deny_prompt": {"Ctrl+Shift+D"},
			"tab_prefix": {"Ctrl+T"}, "toggle_sidebar": {"Ctrl+B"}, "toggle_panel": {"Ctrl+Shift+L"}, "open_settings": {"Ctrl+,"},
			"command_palette":  {"Ctrl+Shift+P"},
			"jump_attention":   {"Alt+U"},
			"new_task":         {"Ctrl+Shift+A"},
			"pane_prefix":      {"Ctrl+P"},
			"session_switcher": {"Alt+S"}, "session_next": {"Alt+]"}, "session_prev": {"Alt+["},
		},
	},
	"conventional": {
		hold: "",
		global: map[string][]string{
			"next_tab": {"Ctrl+Tab", "Ctrl+PageDown"}, "prev_tab": {"Ctrl+Shift+Tab", "Ctrl+PageUp"},
			"next_group": {"Ctrl+Shift+PageDown"}, "prev_group": {"Ctrl+Shift+PageUp"},
			"next_pane": {"Ctrl+Alt+Right", "Ctrl+Alt+Down"}, "prev_pane": {"Ctrl+Alt+Left", "Ctrl+Alt+Up"},
			"split_right": {"Ctrl+Shift+O"}, "split_down": {"Ctrl+Shift+E"}, "close_pane": {"Ctrl+Shift+W"},
			"new_tab":  {"Ctrl+Shift+T"},
			"switcher": {"Ctrl+Shift+Space"},
			"copy":     {"Ctrl+Shift+C", "Ctrl+Insert"}, "paste": {"Ctrl+Shift+V", "Shift+Insert"}, "copy_mode": {"Ctrl+Shift+X"},
			"scroll_page_up": {"Shift+PageUp"}, "scroll_page_down": {"Shift+PageDown"}, "find": {"Ctrl+Shift+F"},
			"prev_prompt": {"Ctrl+Shift+Up"}, "next_prompt": {"Ctrl+Shift+Down"},
			"allow_prompt": {"Ctrl+Shift+Y"}, "deny_prompt": {"Ctrl+Shift+D"},
			"toggle_sidebar": {"Ctrl+Shift+B"}, "toggle_panel": {"Ctrl+Shift+L"}, "open_settings": {"Ctrl+,"},
			"command_palette":  {"Ctrl+Shift+P"},
			"jump_attention":   {"Ctrl+Shift+U"},
			"new_task":         {"Ctrl+Shift+A"},
			"session_switcher": {"Ctrl+Shift+S"}, "session_new": {"Ctrl+Shift+N"},
			"session_next": {"Ctrl+Shift+]"}, "session_prev": {"Ctrl+Shift+["},
		},
	},
	// mac is conventional with its Ctrl+Shift keys on Cmd (Super), and
	// Cmd's usual macOS meanings: Cmd+D splits as in iTerm2, Cmd+Shift+[
	// and ] change tabs as in Safari, Cmd+1-9 go to a tab.
	"mac": {
		hold: "",
		global: map[string][]string{
			"next_tab": {"Ctrl+Tab", "Super+Shift+]"}, "prev_tab": {"Ctrl+Shift+Tab", "Super+Shift+["},
			"next_group": {"Super+Shift+PageDown"}, "prev_group": {"Super+Shift+PageUp"},
			"next_pane": {"Super+Alt+Right", "Super+Alt+Down"}, "prev_pane": {"Super+Alt+Left", "Super+Alt+Up"},
			"split_right": {"Super+D"}, "split_down": {"Super+Shift+D"}, "close_pane": {"Super+W"},
			"new_tab":  {"Super+T"},
			"switcher": {"Ctrl+Shift+Space"},
			"copy":     {"Super+C"}, "paste": {"Super+V"}, "copy_mode": {"Super+Shift+X"},
			"scroll_page_up": {"Shift+PageUp"}, "scroll_page_down": {"Shift+PageDown"}, "find": {"Super+F"},
			"prev_prompt": {"Super+Up"}, "next_prompt": {"Super+Down"},
			"toggle_sidebar": {"Super+B"}, "toggle_panel": {"Super+L"}, "open_settings": {"Super+,"},
			"allow_prompt": {"Super+Shift+Y"}, "deny_prompt": {"Super+Shift+N"},
			"command_palette":  {"Super+Shift+P"},
			"jump_attention":   {"Super+U"},
			"new_task":         {"Super+Shift+A"},
			"session_switcher": {"Super+S"}, "session_new": {"Super+N"},
			"session_next": {"Super+]"}, "session_prev": {"Super+["},
		},
	},
}

func init() {
	for name, p := range presets {
		mod := "Alt+"
		if name == "mac" {
			mod = "Super+" // Option+digit types a symbol on macOS
		}
		for i := 1; i <= 9; i++ {
			d := fmt.Sprint(i)
			p.global["goto_tab_"+d] = []string{mod + d}
		}
		tab := map[string][]string{"new": {"N"}, "new_in_group": {"G"}, "close": {"X"}, "rename": {"R"}, "prev": {"H", "Left"}, "next": {"L", "Right"}, "attention": {"U"}, "sessions": {"S"}}
		for i := 1; i <= 9; i++ {
			tab[fmt.Sprint("goto_", i)] = []string{fmt.Sprint(i)}
		}
		p.tab = tab
		p.pane = map[string][]string{
			"new": {"N"}, "split_down": {"D"}, "split_right": {"R"}, "close": {"X"},
			"focus_left": {"H", "Left"}, "focus_down": {"J", "Down"}, "focus_up": {"K", "Up"}, "focus_right": {"L", "Right"},
			"fullscreen": {"F"}, "next": {"P", "Tab"},
		}
		presets[name] = p
	}
}

// Renamed maps action names from before tabs replaced sessions to their
// new names. A config may still use them; `pitwall config check` notes it.
var Renamed = map[string]string{
	"next_session": "next_tab", "prev_session": "prev_tab", "new_session": "new_tab",
	"jump_session_1": "goto_tab_1", "jump_session_2": "goto_tab_2", "jump_session_3": "goto_tab_3",
	"jump_session_4": "goto_tab_4", "jump_session_5": "goto_tab_5", "jump_session_6": "goto_tab_6",
	"jump_session_7": "goto_tab_7", "jump_session_8": "goto_tab_8", "jump_session_9": "goto_tab_9",
}

// SwitcherHidden turns the tab switcher off for now: no hold modifier, its
// actions unbound and left out of Actions. The switcher code stays.
var SwitcherHidden = true

var switcherActions = map[string]bool{"switcher": true, "pin_switcher": true}

// DefaultPreset is the preset a config without one gets: mac on macOS,
// conventional elsewhere.
var DefaultPreset = defaultPreset(runtime.GOOS)

func defaultPreset(goos string) string {
	if goos == "darwin" {
		return "mac"
	}
	return "conventional"
}

// Presets are the preset names, the default first.
var Presets = append([]string{DefaultPreset}, slices.DeleteFunc([]string{"conventional", "aide", "mac"}, func(p string) bool { return p == DefaultPreset })...)

// Bindings are the effective keys: a preset with the config's overrides.
type Bindings struct {
	Preset string
	// Hold is the modifier that shows the switcher while held, 0 for none.
	Hold   key.Modifiers
	Global map[string][]Chord // by action name
	Tab    map[string][]Chord // tab-mode keys, by [keys.tab] name
	Pane   map[string][]Chord // pane-mode keys, by [keys.pane] name

	global, tab map[Chord]string
	pane        map[Chord]string
}

// Action is the action e's chord runs outside tab mode, or "".
func (b *Bindings) Action(e key.Event) string {
	if a := b.global[Chord{e.Modifiers, e.Name}]; a != "" || e.Modifiers&key.ModShift == 0 {
		return a
	}
	return b.global[Chord{e.Modifiers, unshift[e.Name]}]
}

// unshift maps the symbol a shifted key types on a US layout to the key's
// own name. Gio names a key by what it types, so Ctrl+Shift+] arrives as
// Ctrl+Shift+}; a chord that names the key still matches.
// ponytail: US layout only; other layouts can bind the typed symbol.
var unshift = map[key.Name]key.Name{
	"{": "[", "}": "]", "_": "-", "+": "=", ":": ";", "\"": "'", "<": ",", ">": ".", "?": "/", "|": "\\", "~": "`",
	"!": "1", "@": "2", "#": "3", "$": "4", "%": "5", "^": "6", "&": "7", "*": "8", "(": "9", ")": "0",
}

// Is reports whether e is one of action's chords.
func (b *Bindings) Is(action string, e key.Event) bool { return b.Action(e) == action }

// TabAction is the [keys.tab] action e runs in tab mode, or "".
func (b *Bindings) TabAction(e key.Event) string { return b.tab[Chord{e.Modifiers, e.Name}] }

// PaneModeAction is the [keys.pane] action e runs in pane mode, or "".
func (b *Bindings) PaneModeAction(e key.Event) string { return b.pane[Chord{e.Modifiers, e.Name}] }

// HoldKey is the key name of the switcher's hold modifier, "" for none.
func (b *Bindings) HoldKey() key.Name {
	switch b.Hold {
	case key.ModAlt:
		return key.NameAlt
	case key.ModSuper:
		return key.NameSuper
	case key.ModCtrl:
		return key.NameCtrl
	}
	return ""
}

// PaneAction reports whether action is one the terminal view handles.
func PaneAction(action string) bool { return slices.Contains(paneActions, action) }

// Unshift is c with a shifted symbol key named by the key, as a preset
// writes it: Ctrl+Shift+} becomes Ctrl+Shift+].
func Unshift(c Chord) Chord {
	if k, ok := unshift[c.Name]; ok && c.Mods&key.ModShift != 0 {
		c.Name = k
	}
	return c
}

// WindowChords are the chords the window takes before any pane sees them:
// every bound action except the ones the terminal view handles, and for a
// Shift chord on a symbol key, the symbol it types too (see unshift).
func (b *Bindings) WindowChords() []Chord {
	var out []Chord
	for c, a := range b.global {
		if PaneAction(a) {
			continue
		}
		out = append(out, c)
		for typed, k := range unshift {
			if c.Mods&key.ModShift != 0 && k == c.Name {
				out = append(out, Chord{c.Mods, typed})
			}
		}
	}
	return out
}

var (
	presetMu    sync.Mutex
	presetCache = map[string]*Bindings{}
)

// Preset returns a preset's bindings with no overrides. Callers must not
// modify them.
func Preset(name string) *Bindings {
	presetMu.Lock()
	defer presetMu.Unlock()
	if b := presetCache[name]; b != nil {
		return b
	}
	b, _ := resolveKeys(Keys{Preset: name})
	presetCache[name] = b
	return b
}

func parseHold(s string) (key.Modifiers, bool) {
	if s == "" {
		return 0, true
	}
	m, ok := parseMod(s)
	return m, ok && m != key.ModShift
}

// resolveKeys applies k's overrides to its preset. A bad entry keeps the
// preset's value and adds a problem keyed by its dotted path.
func resolveKeys(k Keys) (*Bindings, []issue) {
	var issues []issue
	name := k.Preset
	if name == "" {
		name = DefaultPreset
	}
	p, ok := presets[name]
	if !ok {
		issues = append(issues, issue{"keys.preset", fmt.Sprintf("unknown preset %q%s", name, suggest(name, Presets))})
		name = DefaultPreset
		p = presets[name]
	}
	b := &Bindings{Preset: name, Global: map[string][]Chord{}, Tab: map[string][]Chord{}, Pane: map[string][]Chord{}}
	b.Hold, _ = parseHold(p.hold)
	if k.SwitcherModifier != nil {
		if m, ok := parseHold(*k.SwitcherModifier); ok {
			b.Hold = m
		} else {
			issues = append(issues, issue{"keys.switcher_modifier", fmt.Sprintf("%q is not Alt, Super, Ctrl or \"\"", *k.SwitcherModifier)})
		}
	}
	issues = append(issues, apply(b.Global, p.global, reflect.ValueOf(k), "keys")...)
	issues = append(issues, apply(b.Tab, p.tab, reflect.ValueOf(k.Tab), "keys.tab")...)
	b.global = index(b.Global, p.global, reflect.ValueOf(k), "keys", &issues)
	b.tab = index(b.Tab, p.tab, reflect.ValueOf(k.Tab), "keys.tab", &issues)
	issues = append(issues, apply(b.Pane, p.pane, reflect.ValueOf(k.Pane), "keys.pane")...)
	b.pane = index(b.Pane, p.pane, reflect.ValueOf(k.Pane), "keys.pane", &issues)
	if SwitcherHidden {
		b.Hold = 0
		for c, a := range b.global {
			if switcherActions[a] {
				delete(b.global, c)
			}
		}
		for a := range switcherActions {
			delete(b.Global, a)
		}
	}
	return b, issues
}

// apply fills dst with the preset's chords, then the overrides set in v's
// Binding fields.
func apply(dst map[string][]Chord, preset map[string][]string, v reflect.Value, path string) []issue {
	var issues []issue
	for _, f := range fields(v.Type()) {
		if f.typ != bindingType {
			continue
		}
		dst[f.name] = mustChords(preset[f.name])
		set := v.Field(f.index).Interface().(Binding)
		if set == nil {
			continue
		}
		var cs []Chord
		bad := false
		for _, s := range set {
			c, err := ParseChord(s)
			if err != nil {
				issues = append(issues, issue{path + "." + f.name, err.Error()})
				bad = true
				continue
			}
			if !slices.Contains(cs, c) {
				cs = append(cs, c)
			}
		}
		if !bad {
			dst[f.name] = cs
		}
	}
	return issues
}

// index builds the chord lookup. When two actions share a chord, the one
// the config set goes back to its preset value, until none clash.
func index(m map[string][]Chord, preset map[string][]string, v reflect.Value, path string, issues *[]issue) map[Chord]string {
	user := map[string]bool{}
	var order []string
	for _, f := range fields(v.Type()) {
		if f.typ == bindingType {
			order = append(order, f.name)
			user[f.name] = v.Field(f.index).Interface().(Binding) != nil
		}
	}
	for {
		idx := map[Chord]string{}
		clash := ""
		for _, a := range order {
			for _, c := range m[a] {
				if o, ok := idx[c]; ok {
					victim := a
					if !user[a] {
						victim = o
					}
					other := o
					if victim == o {
						other = a
					}
					*issues = append(*issues, issue{path + "." + victim, fmt.Sprintf("%s is also bound to %s.%s", c, path, other)})
					clash = victim
					break
				}
				idx[c] = a
			}
			if clash != "" {
				break
			}
		}
		if clash == "" {
			return idx
		}
		m[clash], user[clash] = mustChords(preset[clash]), false
	}
}

func mustChords(ss []string) []Chord {
	out := []Chord{}
	for _, s := range ss {
		c, err := ParseChord(s)
		if err != nil {
			panic("config: preset chord " + err.Error())
		}
		out = append(out, c)
	}
	return out
}

// Action is a bindable action: its config name, description and group.
// Tab marks a [keys.tab] action, Pane a [keys.pane] one.
type Action struct {
	Name, Doc, Group string
	Tab              bool
	Pane             bool
}

// Title is the action's name for people: its doc up to the first ". " or
// ": ", so "Tab mode: the next key..." is "Tab mode".
func (a Action) Title() string {
	t, _, _ := strings.Cut(a.Doc, ". ")
	t, _, _ = strings.Cut(t, ": ")
	return strings.TrimSuffix(t, ".")
}

// Actions lists every action in config order, [keys], [keys.tab], then
// [keys.pane]. [keys] actions take their group tag; the others are in
// "Tab mode" and "Pane mode".
func Actions() []Action {
	var out []Action
	for _, t := range []reflect.Type{reflect.TypeFor[Keys](), reflect.TypeFor[TabKeys](), reflect.TypeFor[PaneKeys]()} {
		tab, pane := t == reflect.TypeFor[TabKeys](), t == reflect.TypeFor[PaneKeys]()
		for _, f := range fields(t) {
			if f.typ != bindingType || SwitcherHidden && switcherActions[f.name] {
				continue
			}
			g := f.tag.Get("group")
			if tab {
				g = "Tab mode"
			} else if pane {
				g = "Pane mode"
			}
			out = append(out, Action{f.name, f.doc, g, tab, pane})
		}
	}
	return out
}
