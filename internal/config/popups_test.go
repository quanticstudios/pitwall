package config

import (
	"slices"
	"strings"
	"testing"

	"gioui.org/io/key"
)

func TestPopups(t *testing.T) {
	s, probs := LoadFile(write(t, t.TempDir(), "config.toml", `
[keys]
preset = "conventional"

[[popups]]
name = "lazygit"
key = "Ctrl+Alt+G"
command = ["lazygit"]
width = "90%"
height = "40"

[[popups]]
name = "top"
key = "Ctrl+Alt+T"
command = ["htop"]

[[popups]]
name = "keyless"
command = ["htop"]

[[popups]]
name = "nameless"

[[popups]]
name = "lazygit"
key = "Ctrl+Alt+L"
command = ["tig"]

[[popups]]
name = "clash"
key = "Ctrl+Shift+T"
command = ["x"]

[[popups]]
name = "twice"
key = "Ctrl+Alt+G"
command = ["x"]

[[popups]]
name = "wide"
key = "Ctrl+Alt+W"
width = "200%"
command = ["x"]
`))
	var names []string
	for _, p := range s.Popups {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"lazygit", "top"}) {
		t.Fatalf("popups %q", names)
	}
	want := []string{"needs a key", "needs a command", "second popup is named", "is new_tab's key", "another popup has", "more than 100%"}
	if len(probs) != len(want) {
		t.Fatalf("problems %v", probs)
	}
	for i, w := range want {
		if !strings.Contains(probs[i].Msg, w) {
			t.Errorf("problem %d: %+v, want %q", i, probs[i], w)
		}
	}
	p, ok := s.PopupFor(key.Event{Name: "G", Modifiers: key.ModCtrl | key.ModAlt})
	if !ok || p.Name != "lazygit" || p.Width != "90%" || p.Height != "40" || !slices.Equal(p.Command, []string{"lazygit"}) {
		t.Fatalf("PopupFor: %+v %v", p, ok)
	}
	if _, ok := s.PopupFor(key.Event{Name: "G", Modifiers: key.ModCtrl}); ok {
		t.Fatal("Ctrl+G opened a popup")
	}
	if !strings.Contains(string(Schema()), `"popups"`) {
		t.Fatal("popups missing from the schema")
	}
}
