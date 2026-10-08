package config

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/testenv"
)

// The switcher is hidden in builds; its tests run with it shown.
func TestMain(m *testing.M) {
	SwitcherHidden = false
	testenv.Main(m)
}

func TestSwitcherHidden(t *testing.T) {
	SwitcherHidden = true
	defer func() { SwitcherHidden = false }()
	for _, name := range Presets {
		b, _ := resolveKeys(Keys{Preset: name})
		if b.Hold != 0 || len(b.Global["switcher"])+len(b.Global["pin_switcher"]) > 0 {
			t.Errorf("%s: hold %v, switcher %v, pin %v", name, b.Hold, b.Global["switcher"], b.Global["pin_switcher"])
		}
	}
	for _, a := range Actions() {
		if switcherActions[a.Name] {
			t.Errorf("Actions lists %s", a.Name)
		}
	}
}

// TestActionGroups: the command palette names and groups every action
// from its tags.
func TestActionGroups(t *testing.T) {
	titles := map[string]string{}
	for _, a := range Actions() {
		if a.Group == "" || a.Title() == "" {
			t.Errorf("%s: group %q, title %q", a.Name, a.Group, a.Title())
		}
		if !a.Tab && !a.Pane {
			titles[a.Name] = a.Group + "/" + a.Title()
		}
	}
	for name, want := range map[string]string{
		"tab_prefix":       "Tabs/Tab mode",
		"toggle_sidebar":   "Window/Show or hide the sidebar",
		"session_switcher": "Sessions/Show the session switcher",
		"command_palette":  "Window/Show the command palette",
	} {
		if titles[name] != want {
			t.Errorf("%s: %q, want %q", name, titles[name], want)
		}
	}
}
