package term

import (
	"testing"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestClipInput(t *testing.T) {
	bracketed := vt.Modes{BracketedPaste: true}
	kitty := vt.Modes{KittyKeyboard: 1}
	for _, c := range []struct {
		name  string
		text  string
		agent bool
		m     vt.Modes
		want  string
	}{
		{"agent text", "hi", true, bracketed, "\x1b[200~hi\x1b[201~"},
		{"shell text", "hi", false, bracketed, "\x1b[200~hi\x1b[201~"},
		// An image, or nothing, reads as no text.
		{"agent image", "", true, bracketed, "\x16"},
		{"agent image kitty", "", true, kitty, "\x1b[118;5u"},
		{"shell image", "", false, bracketed, ""},
	} {
		if got := string(clipInput(c.text, c.agent, c.m)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAgentPasteKey(t *testing.T) {
	ctrlV := key.Event{Name: "V", Modifiers: key.ModCtrl, State: key.Press}
	if !agentPasteKey(ctrlV, true) {
		t.Error("Ctrl+V in an agent pane should paste")
	}
	if agentPasteKey(ctrlV, false) {
		t.Error("Ctrl+V in a shell should reach the program")
	}
	if agentPasteKey(key.Event{Name: "V", Modifiers: key.ModCtrl | key.ModShift}, true) {
		t.Error("Ctrl+Shift+V is the paste binding's, not this key's")
	}
}
