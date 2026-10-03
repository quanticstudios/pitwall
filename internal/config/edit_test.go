package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditTOML(t *testing.T) {
	const src = `# my config
[keys]
preset = "aide" # I like Alt
# Next tab in sidebar order.
# next_tab = ["Ctrl+Tab", "Ctrl+PageDown"]  # aide: ["Alt+J", "Alt+Down"]
mono_like = [
  "a", # keep
  "b",
]

# Fonts.
[font]
ui_size = 13
`
	set := func(v string) *string { return &v }
	for _, tc := range []struct {
		name, table, key string
		value            *string
		want             string
	}{
		{"existing", "keys", "preset", set(`"conventional"`),
			strings.Replace(src, `preset = "aide" # I like Alt`, `preset = "conventional" # I like Alt`, 1)},
		{"commented default", "keys", "next_tab", set(`"Ctrl+J"`),
			strings.Replace(src, `# next_tab = ["Ctrl+Tab", "Ctrl+PageDown"]  # aide`, `next_tab = "Ctrl+J"  # aide`, 1)},
		{"multi-line value", "keys", "mono_like", set(`["c"]`),
			strings.Replace(src, "[\n  \"a\", # keep\n  \"b\",\n]", `["c"]`, 1)},
		{"append to table", "font", "mono_size", set("14"), src + "mono_size = 14\n"},
		{"append mid-file", "keys", "copy", set(`"Ctrl+C"`),
			strings.Replace(src, "]\n\n# Fonts.", "]\ncopy = \"Ctrl+C\"\n\n# Fonts.", 1)},
		{"missing table", "layout", "pane_gap", set("8"), src + "\n[layout]\npane_gap = 8\n"},
		{"remove", "keys", "mono_like", nil, strings.Replace(src, "mono_like = [\n  \"a\", # keep\n  \"b\",\n]\n", "", 1)},
		{"remove absent", "keys", "nope", nil, src},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := editTOML(src, tc.table, tc.key, tc.value); got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
	// Prose that starts with "key =" is not a default line.
	prose := "[keys]\n# toggle_sidebar = \"Ctrl+Shift+B\" or [] to give it back\n"
	if got := editTOML(prose, "keys", "toggle_sidebar", set(`"X"`)); got != "[keys]\ntoggle_sidebar = \"X\"\n"+prose[len("[keys]\n"):] {
		t.Errorf("prose uncommented:\n%s", got)
	}
	if got := editTOML("", "font", "ui_size", set("14")); got != "[font]\nui_size = 14\n" {
		t.Errorf("empty file: %q", got)
	}
}

// TestSetKeyDefaultFile edits the file `config init` writes: every touched
// key loads, and the lines around it are unchanged.
func TestSetKeyDefaultFile(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(real, []byte(Default()), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink("real.toml", link); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct{ table, key, value string }{
		{"keys", "preset", Quote("aide")},
		{"keys", "next_tab", BindingValue([]Chord{{Name: "J", Mods: 1}})},
		{"keys.tab", "new", Quote("M")},
		{"font", "ui_size", Number(15)},
		{"layout", "pane_gap", Number(0)},
	} {
		if err := SetKey(link, e.table, e.key, e.value); err != nil {
			t.Fatal(err)
		}
	}
	s, probs := LoadFile(link)
	if len(probs) > 0 {
		t.Fatalf("problems: %v", probs)
	}
	if s.Keys.Preset != "aide" || s.Font.UISize != 15 || s.PaneGap != 0 ||
		s.Keys.Global["next_tab"][0].String() != "Ctrl+J" || s.Keys.Tab["new"][0].String() != "M" {
		t.Fatalf("not applied: %+v %+v", s.Keys.Global["next_tab"], s.Font)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config.toml is no longer a symlink")
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	got, _ := os.ReadFile(real)
	want := Default()
	for _, r := range [][2]string{
		{`preset = "conventional"`, `preset = "aide"`},
		{`# next_tab = ["Ctrl+Tab", "Ctrl+PageDown"]`, `next_tab = "Ctrl+J"`},
		{`# new = "N"`, `new = "M"`},
		{`# ui_size = 13`, `ui_size = 15`},
		{`# pane_gap = 4`, `pane_gap = 0`},
	} {
		if !strings.Contains(want, r[0]) {
			t.Fatalf("default file has no %q", r[0])
		}
		want = strings.Replace(want, r[0], r[1], 1)
	}
	if string(got) != want {
		t.Errorf("unrelated lines changed:\n%s", got)
	}

	if err := RemoveKey(link, "font", "ui_size"); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(real)
	if strings.Contains(string(got), "ui_size = 15") || !strings.Contains(string(got), "# UI text size\n\n# Terminal font") {
		t.Errorf("remove left:\n%s", got)
	}
}

// TestSetKeyBrokenFile: a file with a syntax error still gets the one key,
// and an edit never breaks a file that parsed.
func TestSetKeyBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	broken := "[font]\nui_size = = 3\n"
	os.WriteFile(path, []byte(broken), 0o644)
	if err := SetKey(path, "keys", "preset", Quote("aide")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != broken+"\n[keys]\npreset = \"aide\"\n" {
		t.Errorf("got %q", got)
	}
	os.WriteFile(path, []byte("[font]\n"), 0o644)
	if err := SetKey(path, "font", "ui_size", "= 3"); err == nil {
		t.Error("an edit broke a valid file")
	}
	if err := SetKey(filepath.Join(t.TempDir(), "new", "config.toml"), "font", "ui_size", "14"); err != nil {
		t.Errorf("missing file: %v", err)
	}
}

func TestAllThemes(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mine.toml"), []byte("name = \"tokyo-night\"\n[colors]\nbg = \"#123456\"\n"), 0o644)
	all := AllThemes(dir)
	last := all[len(all)-1]
	if len(all) != len(Themes)+1 || last.Name != "mine" || !last.Custom ||
		last.Theme.Colors.Bg != "#123456" || last.Theme.Colors.Fg != builtins["tokyo-night"].Colors.Fg {
		t.Fatalf("got %+v", last)
	}
}
