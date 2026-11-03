package config

import (
	"gioui.org/io/key"
	"strings"
	"testing"
)

func TestEditScrollbackBinding(t *testing.T) {
	for _, name := range Presets {
		if got := Preset(name).Global["edit_scrollback"]; len(got) != 0 {
			t.Errorf("%s binds edit_scrollback: %v", name, got)
		}
	}
	b, issues := resolveKeys(Keys{EditScrollback: Binding{"Ctrl+Shift+F12"}})
	if len(issues) != 0 || b.Action(key.Event{Name: key.NameF12, Modifiers: key.ModCtrl | key.ModShift}) != "edit_scrollback" {
		t.Fatalf("binding: %v, %v", b.Global, issues)
	}
	if !strings.Contains(Default(), "edit_scrollback") || !strings.Contains(string(Schema()), "edit_scrollback") {
		t.Fatal("action missing from config documentation")
	}
}
