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
		name        string
		text        string
		agent, host bool
		m           vt.Modes
		want        string
		image       bool
	}{
		{"agent text", "hi", true, false, bracketed, "\x1b[200~hi\x1b[201~", false},
		{"shell text", "hi", false, false, bracketed, "\x1b[200~hi\x1b[201~", false},
		// An image, or nothing, reads as no text.
		{"agent image", "", true, false, bracketed, "\x16", false},
		{"agent image kitty", "", true, false, kitty, "\x1b[118;5u", false},
		{"shell image", "", false, false, bracketed, "", false},
		// On a --host window the agent has no clipboard: the window pastes.
		{"host agent text", "hi", true, true, bracketed, "\x1b[200~hi\x1b[201~", false},
		{"host agent image", "", true, true, bracketed, "", true},
		{"host shell image", "", false, true, bracketed, "", false},
	} {
		got, image := clipInput(c.text, c.agent, c.host, c.m)
		if string(got) != c.want || image != c.image {
			t.Errorf("%s: got %q image %v, want %q image %v", c.name, got, image, c.want, c.image)
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
