package config

import (
	"testing"

	"gioui.org/io/key"
)

// TestKeyQuery: +, - and spaces separate, every alias and glyph reads as
// its key, modifier order does not matter, and a query that is not all
// keys with a modifier is words.
func TestKeyQuery(t *testing.T) {
	cs := key.ModCtrl | key.ModShift
	for _, tc := range []struct {
		q    string
		want Chord
		ok   bool
	}{
		{"ctrl+shift+d", Chord{cs, "D"}, true},
		{"ctrl shift d", Chord{cs, "D"}, true},
		{"Shift-Ctrl-D", Chord{cs, "D"}, true},
		{"  control + ⇧ +d ", Chord{cs, "D"}, true},
		{"^⇧D", Chord{cs, "D"}, true},
		{"⌃d", Chord{key.ModCtrl, "D"}, true},
		{"cmd+shift+r", Chord{superMod | key.ModShift, "R"}, true},
		{"command r", Chord{superMod, "R"}, true},
		{"⌘⇧R", Chord{superMod | key.ModShift, "R"}, true},
		{"super r", Chord{superMod, "R"}, true},
		{"win r", Chord{superMod, "R"}, true},
		{"meta r", Chord{superMod, "R"}, true},
		{"alt 1", Chord{key.ModAlt, "1"}, true},
		{"option left", Chord{key.ModAlt, key.NameLeftArrow}, true},
		{"opt ←", Chord{key.ModAlt, key.NameLeftArrow}, true},
		{"⌥→", Chord{key.ModAlt, key.NameRightArrow}, true},
		{"ctrl alt up", Chord{key.ModCtrl | key.ModAlt, key.NameUpArrow}, true},
		{"ctrl ↓", Chord{key.ModCtrl, key.NameDownArrow}, true},
		{"shift return", Chord{key.ModShift, key.NameReturn}, true},
		{"shift enter", Chord{key.ModShift, key.NameReturn}, true},
		{"alt esc", Chord{key.ModAlt, key.NameEscape}, true},
		{"alt escape", Chord{key.ModAlt, key.NameEscape}, true},
		{"ctrl del", Chord{key.ModCtrl, key.NameDeleteForward}, true},
		{"ctrl delete", Chord{key.ModCtrl, key.NameDeleteForward}, true},
		{"shift pgup", Chord{key.ModShift, key.NamePageUp}, true},
		{"shift pageup", Chord{key.ModShift, key.NamePageUp}, true},
		{"ctrl pgdown", Chord{key.ModCtrl, key.NamePageDown}, true},
		{"ctrl tab", Chord{key.ModCtrl, key.NameTab}, true},
		{"ctrl shift }", Chord{cs, "]"}, true}, // the symbol Shift types, as a preset writes it
		{"ctrl shift", Chord{Mods: cs}, true},
		{"ctrl+", Chord{Mods: key.ModCtrl}, true},
		{"d", Chord{}, false},
		{"tab", Chord{}, false},
		{"ctrl d e", Chord{}, false},
		{"ctrl click", Chord{}, false},
		{"split", Chord{}, false},
		{"", Chord{}, false},
	} {
		got, ok := KeyQuery(tc.q)
		if got != tc.want || ok != tc.ok {
			t.Errorf("KeyQuery(%q) = %v, %v; want %v, %v", tc.q, got, ok, tc.want, tc.ok)
		}
	}
	q, _ := KeyQuery("ctrl shift")
	if !q.Finds(Chord{cs | key.ModAlt, "X"}) || q.Finds(Chord{key.ModCtrl, "X"}) {
		t.Error("modifiers alone find every chord holding them, and only those")
	}
	q, _ = KeyQuery("ctrl d")
	if q.Finds(Chord{cs, "D"}) || !q.Finds(Chord{key.ModCtrl, "D"}) {
		t.Error("a chord finds itself only")
	}
}
