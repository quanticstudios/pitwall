package app

import (
	"fmt"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
)

// hintDelay is how long the goto_tab modifiers stay down on their own
// before the sidebar shows its digits, so a quick Alt+H or Alt+J does not
// flash them.
const hintDelay = 200 * time.Millisecond

// modKeys are the modifiers a goto_tab chord can hold, with their keys.
var modKeys = []struct {
	mod  key.Modifiers
	name key.Name
}{{key.ModCtrl, key.NameCtrl}, {key.ModAlt, key.NameAlt}, {key.ModShift, key.NameShift}, {key.ModSuper, key.NameSuper}}

// gotoKeys is the modifier set every goto_tab_N chord shares and which N
// are bound with it. It is 0 when they are all unbound, one has no
// modifier, or two differ.
func gotoKeys(b *config.Bindings) (key.Modifiers, [9]bool) {
	var mods key.Modifiers
	var bound [9]bool
	for i := range bound {
		for _, c := range b.Global[fmt.Sprint("goto_tab_", i+1)] {
			if c.Mods == 0 || mods != 0 && c.Mods != mods {
				return 0, [9]bool{}
			}
			mods, bound[i] = c.Mods, true
		}
	}
	return mods, bound
}

// gotoHint follows the modifiers through the window's key events, so the
// sidebar can show its digits while the goto_tab ones are held alone.
type gotoHint struct {
	held  key.Modifiers // the modifiers down, as of the last key event
	since time.Time     // when held last changed
	chord bool          // a key went down before the digits showed: none this hold
}

// key follows one key event at now; mods are the goto_tab modifiers.
func (h *gotoHint) key(e key.Event, mods key.Modifiers, now time.Time) {
	held := e.Modifiers
	m := key.Modifiers(0)
	for _, k := range modKeys {
		if k.name == e.Name {
			m = k.mod
		}
	}
	switch {
	case m == 0:
		// A press in the delay is a chord like Alt+J, not a look at the digits.
		if on, _ := h.shown(mods, now); e.State == key.Press && !on {
			h.chord = true
		}
	case e.State == key.Press:
		held |= m
	default:
		held &^= m
	}
	if held != h.held {
		h.held, h.since = held, now
	}
	if held != mods {
		h.chord = false
	}
}

// shown reports whether the digits show at now and, when they are still
// waiting out hintDelay, when they will.
func (h *gotoHint) shown(mods key.Modifiers, now time.Time) (on bool, at time.Time) {
	if mods == 0 || h.held != mods || h.chord {
		return false, time.Time{}
	}
	at = h.since.Add(hintDelay)
	return !now.Before(at), at
}
