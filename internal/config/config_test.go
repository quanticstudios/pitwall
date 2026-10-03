package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gioui.org/io/key"
	"github.com/BurntSushi/toml"

	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestParseChord(t *testing.T) {
	for in, want := range map[string]Chord{
		"Alt+J":               {key.ModAlt, "J"},
		"ctrl+shift+t":        {key.ModCtrl | key.ModShift, "T"},
		"Control+PgDn":        {key.ModCtrl, key.NamePageDown},
		"Ctrl+Alt+left":       {key.ModCtrl | key.ModAlt, key.NameLeftArrow},
		"Ctrl++":              {key.ModCtrl, "+"},
		"Super+Enter":         {key.ModSuper, key.NameReturn},
		"Shift+Tab":           {key.ModShift, key.NameTab},
		"F5":                  {0, key.NameF5},
		"1":                   {0, "1"},
		"Ctrl+Shift+PageDown": {key.ModCtrl | key.ModShift, key.NamePageDown},
	} {
		got, err := ParseChord(in)
		if err != nil || got != want {
			t.Errorf("ParseChord(%q) = %v, %v; want %v", in, got, err, want)
		}
		if back, err := ParseChord(got.String()); err != nil || back != got {
			t.Errorf("%q does not round-trip through %q", in, got.String())
		}
	}
	for _, in := range []string{"", "Alt+", "Hyper+J", "Ctrl+Ctrl+J", "Ctrl+Shift", "Alt+Foo", "Shift+ä"} {
		if c, err := ParseChord(in); err == nil {
			t.Errorf("ParseChord(%q) = %v, want an error", in, c)
		}
	}
	re := regexp.MustCompile(ChordPattern())
	for _, s := range []string{"Alt+J", "ctrl+shift+t", "Ctrl++", "PgDn", "super+F12"} {
		if !re.MatchString(s) {
			t.Errorf("pattern rejects %q", s)
		}
	}
	for _, s := range []string{"Alt+", "Hyper+J", "Ctrl+Shift", "Alt+Foo"} {
		if re.MatchString(s) {
			t.Errorf("pattern accepts %q", s)
		}
	}
}

func chords(b *Bindings, action string) string {
	var out []string
	for _, c := range b.Global[action] {
		out = append(out, c.String())
	}
	return strings.Join(out, " ")
}

// TestPresets pins both presets and checks neither binds one chord twice.
func TestPresets(t *testing.T) {
	aide, conv := Preset("aide"), Preset("conventional")
	for _, tc := range []struct {
		b            *Bindings
		action, want string
	}{
		{aide, "next_session", "Alt+J Alt+Down"},
		{aide, "split_down", "Alt+Shift+N"},
		{aide, "jump_session_3", "Alt+3"},
		{aide, "tab_prefix", "Ctrl+T"},
		{aide, "goto_tab_1", ""},
		{conv, "new_tab", "Ctrl+Shift+T"},
		{conv, "close_pane", "Ctrl+Shift+W"},
		{conv, "next_tab", "Ctrl+Tab Ctrl+PageDown"},
		{conv, "prev_tab", "Ctrl+Shift+Tab Ctrl+PageUp"},
		{conv, "goto_tab_9", "Alt+9"},
		{conv, "new_session", "Ctrl+Shift+N"},
		{conv, "split_right", "Ctrl+Shift+O"},
		{conv, "split_down", "Ctrl+Shift+E"},
		{conv, "next_pane", "Ctrl+Alt+Right Ctrl+Alt+Down"},
		{conv, "prev_session", "Ctrl+Shift+PageUp"},
		{conv, "switcher", "Ctrl+Shift+Space"},
		{conv, "copy", "Ctrl+Shift+C"},
		{conv, "scroll_page_up", "Shift+PageUp"},
		{conv, "tab_prefix", ""},
		{conv, "jump_session_1", ""},
	} {
		if got := chords(tc.b, tc.action); got != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.b.Preset, tc.action, got, tc.want)
		}
	}
	if aide.Hold != key.ModAlt || conv.Hold != 0 {
		t.Errorf("hold: aide %v, conventional %v", aide.Hold, conv.Hold)
	}
	for _, name := range Presets {
		if _, issues := resolveKeys(Keys{Preset: name}); len(issues) > 0 {
			t.Errorf("%s: %v", name, issues)
		}
	}
	// Plain Ctrl+letters and readline's Alt+B/F/D/. stay with the shell.
	for _, s := range []string{"Ctrl+T", "Ctrl+W", "Ctrl+C", "Ctrl+R", "Alt+B", "Alt+F", "Alt+D", "Alt+."} {
		c, _ := ParseChord(s)
		if a := conv.Action(key.Event{Name: c.Name, Modifiers: c.Mods}); a != "" {
			t.Errorf("conventional takes %s for %s", s, a)
		}
	}
}

func write(t *testing.T, dir, name, s string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func msgs(ps []Problem) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.String())
	}
	return strings.Join(out, "\n")
}

func TestLoadDefaults(t *testing.T) {
	s, probs := LoadFile(filepath.Join(t.TempDir(), "config.toml"))
	if len(probs) > 0 {
		t.Fatal(msgs(probs))
	}
	if s.Keys.Preset != "conventional" || s.ThemeName != "aide-dark" || s.Font.MonoSize != 13 {
		t.Fatalf("defaults: %+v", s)
	}
	if s.Theme.Palette() != vt.DefaultPalette {
		t.Fatal("aide-dark's terminal colors differ from vt.DefaultPalette")
	}
	// The commented default file loads the same.
	p := write(t, t.TempDir(), "config.toml", Default())
	s2, probs := LoadFile(p)
	if len(probs) > 0 || !reflect.DeepEqual(s2.Keys.Global, s.Keys.Global) || s2.Theme.Colors != s.Theme.Colors {
		t.Fatalf("default file: %v", msgs(probs))
	}
}

func TestLoadOverridesAndProblems(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "config.toml", `[keys]
preset = "aide"
switcher_modifier = ""
next_tab = "Ctrl+Tab"
new_tab = ["ctrl+shift+t", "Ctrl+Shift+T"]
split_right = []
next_sesion = "Alt+X"
close_tab = "Alt+J"
prev_tab = "Hyper+Q"

[keys.tab]
new = "A"

[theme]
name = "tokyo-night"

[theme.colors]
bg = "#123456"
fg = "white"

[theme.terminal]
ansi = ["#000000"]

[font]
mono_size = 99
`)
	s, probs := LoadFile(p)
	want := []string{
		`config.toml:7: keys.next_sesion: unknown key "next_sesion" in [keys] (did you mean "next_session"?)`,
		`config.toml:8: keys.close_tab: Alt+J is also bound to keys.next_session`,
		`config.toml:9: keys.prev_tab: "Hyper+Q": unknown modifier "Hyper" (use Ctrl, Alt, Shift, Super)`,
		`config.toml:19: theme.colors.fg: "white" is not a #rrggbb color`,
		`config.toml:22: theme.terminal.ansi: has 1 colors, want 16`,
		`config.toml:25: font.mono_size: 99 is outside 6-48`,
	}
	if got := msgs(probs); got != strings.Join(want, "\n") {
		t.Fatalf("problems:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	b := s.Keys
	if b.Hold != 0 || chords(b, "next_tab") != "Ctrl+Tab" || chords(b, "new_tab") != "Ctrl+Shift+T" ||
		chords(b, "split_right") != "" || chords(b, "close_tab") != "" || chords(b, "next_session") != "Alt+J Alt+Down" {
		t.Fatalf("bindings: %v", b.Global)
	}
	if b.TabAction(key.Event{Name: "A"}) != "new" || b.TabAction(key.Event{Name: "N"}) != "" {
		t.Fatal("tab override not applied")
	}
	tn, _ := Builtin("tokyo-night")
	if s.Theme.Colors.Bg != "#123456" || s.Theme.Colors.Fg != tn.Colors.Fg || len(s.Theme.Terminal.ANSI) != 16 || s.Font.MonoSize != 13 {
		t.Fatalf("theme fallback: %+v %+v", s.Theme.Colors, s.Font)
	}
}

func TestSyntaxErrorAndCustomTheme(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "config.toml", "[keys]\npreset = \"aide\n")
	s, probs := LoadFile(p)
	if len(probs) != 1 || probs[0].Line != 2 || s.Keys.Preset != "conventional" {
		t.Fatalf("syntax error: %v", msgs(probs))
	}

	write(t, dir, "config.toml", "[theme]\nname = \"mine\"\n[theme.colors]\nred = \"#ff0000\"\n")
	write(t, dir, "themes/mine.toml", "name = \"aide-light\"\n[colors]\nbg = \"#fefefe\"\nbogus = \"#000000\"\n")
	s, probs = LoadFile(p)
	if got := msgs(probs); got != `themes/mine.toml:4: colors.bogus: unknown key "bogus" in [colors]` {
		t.Fatalf("theme file problems: %q", got)
	}
	light, _ := Builtin("aide-light")
	if s.ThemeName != "mine" || s.Theme.Colors.Bg != "#fefefe" || s.Theme.Colors.Red != "#ff0000" || s.Theme.Colors.Fg != light.Colors.Fg {
		t.Fatalf("custom theme: %+v", s.Theme.Colors)
	}

	write(t, dir, "config.toml", "[theme]\nname = \"tokyo-nite\"\n")
	s, probs = LoadFile(p)
	if len(probs) != 1 || !strings.Contains(probs[0].String(), `config.toml:2: theme.name: no theme "tokyo-nite"`) ||
		!strings.Contains(probs[0].Msg, `did you mean "tokyo-night"?`) || s.ThemeName != "aide-dark" {
		t.Fatalf("unknown theme: %v", msgs(probs))
	}
}

// validate checks v against the subset of JSON Schema that Schema emits.
func validate(root, s map[string]any, v any, path string) []string {
	if ref, ok := s["$ref"].(string); ok {
		s = root["$defs"].(map[string]any)[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
	}
	var errs []string
	fail := func(f string, a ...any) { errs = append(errs, path+": "+fmt.Sprintf(f, a...)) }
	if alts, ok := s["oneOf"].([]any); ok {
		n := 0
		for _, a := range alts {
			if len(validate(root, a.(map[string]any), v, path)) == 0 {
				n++
			}
		}
		if n != 1 {
			fail("matches %d of oneOf", n)
		}
	}
	if alts, ok := s["anyOf"].([]any); ok {
		if !slices.ContainsFunc(alts, func(a any) bool { return len(validate(root, a.(map[string]any), v, path)) == 0 }) {
			fail("matches none of anyOf")
		}
	}
	if e, ok := s["enum"].([]any); ok && !slices.Contains(e, v) {
		fail("%v not in %v", v, e)
	}
	switch s["type"] {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			fail("not an object")
			break
		}
		props := s["properties"].(map[string]any)
		for k, x := range m {
			ps, ok := props[k].(map[string]any)
			if !ok {
				fail("unknown property %q", k)
				continue
			}
			errs = append(errs, validate(root, ps, x, path+"."+k)...)
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			fail("not an array")
			break
		}
		if n, ok := s["minItems"].(float64); ok && float64(len(a)) < n {
			fail("fewer than %v items", n)
		}
		if n, ok := s["maxItems"].(float64); ok && float64(len(a)) > n {
			fail("more than %v items", n)
		}
		for i, x := range a {
			errs = append(errs, validate(root, s["items"].(map[string]any), x, fmt.Sprint(path, "[", i, "]"))...)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			fail("not a string")
		} else if p, ok := s["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(str) {
			fail("%q does not match %s", str, p)
		}
	case "number":
		n, ok := v.(float64)
		if i, isInt := v.(int64); isInt {
			n, ok = float64(i), true
		}
		if !ok {
			fail("not a number")
		} else if m, ok := s["minimum"].(float64); ok && n < m {
			fail("below %v", m)
		} else if m, ok := s["maximum"].(float64); ok && n > m {
			fail("above %v", m)
		}
	}
	return errs
}

func TestSchema(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		t.Fatal(err)
	}
	if root["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatal("not draft 2020-12")
	}
	keys := root["properties"].(map[string]any)["keys"].(map[string]any)["properties"].(map[string]any)
	for _, a := range []string{"next_session", "goto_tab_9", "jump_session_1", "tab_prefix", "pin_switcher", "scroll_page_down"} {
		if _, ok := keys[a]; !ok {
			t.Errorf("schema lacks action %s", a)
		}
	}
	check := func(name, doc string, valid bool) {
		t.Helper()
		var m map[string]any
		if _, err := toml.Decode(doc, &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		errs := validate(root, root, m, "")
		if valid != (len(errs) == 0) {
			t.Errorf("%s: valid=%v, errors %v", name, valid, errs)
		}
	}
	// The default file with every commented option switched on.
	var on []string
	for _, l := range strings.Split(Default(), "\n") {
		if s, ok := strings.CutPrefix(l, "# "); ok && strings.Contains(s, " = ") {
			l = s
		}
		on = append(on, l)
	}
	full := strings.Join(on, "\n")
	check("default", Default(), true)
	check("default, all set", full, true)
	if _, probs := LoadFile(write(t, t.TempDir(), "config.toml", full)); len(probs) > 0 {
		t.Errorf("default with all set: %s", msgs(probs))
	}
	check("typo'd action", "[keys]\nnext_sesion = \"Alt+J\"\n", false)
	check("bad chord", "[keys]\nnext_session = \"Alt+Foo\"\n", false)
	check("bad chord in array", "[keys]\nnext_session = [\"Alt+J\", \"Hyper+J\"]\n", false)
	check("bad color", "[theme.colors]\nbg = \"#12345\"\n", false)
	check("bad preset", "[keys]\npreset = \"emacs\"\n", false)
	check("short ansi", "[theme.terminal]\nansi = [\"#000000\"]\n", false)
	check("custom theme name", "[theme]\nname = \"mine\"\n", true)
	check("unbind", "[keys]\ntab_prefix = []\nswitcher_modifier = \"\"\n", true)

	var ts map[string]any
	if err := json.Unmarshal(ThemeSchema(), &ts); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	toml.Decode("name = \"aide-light\"\n[colors]\nbg = \"#ffffff\"\n[terminal]\ncursor = \"#000000\"\n", &m)
	if errs := validate(ts, ts, m, ""); len(errs) > 0 {
		t.Errorf("theme file: %v", errs)
	}
}

func TestDistance(t *testing.T) {
	if d := distance("next_sesion", "next_session"); d != 1 {
		t.Fatalf("distance = %d", d)
	}
	if s := suggest("zzzz", []string{"next_session"}); s != "" {
		t.Fatalf("far suggestion %q", s)
	}
}
