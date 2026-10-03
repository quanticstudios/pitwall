package config

import (
	"os"
	"testing"
)

// The switcher is hidden in builds; its tests run with it shown.
func TestMain(m *testing.M) {
	SwitcherHidden = false
	os.Exit(m.Run())
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
