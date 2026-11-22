package input

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// ParseKey parses a key specification such as "enter", "esc", "tab", "up",
// "down", "ctrl+c", "shift+tab", "a", "A", or "1" into a Gio press key.Event
// and its printable text fallback (for modes where Key returns nil because the
// GUI would deliver the character through key.EditEvent).
func ParseKey(spec string) (key.Event, string, error) {
	if spec == "" {
		return key.Event{}, "", fmt.Errorf("empty key name")
	}
	// A single literal printable rune ("+", "-", "a", "A", "1", " ", ...).
	if utf8.ValidString(spec) && utf8.RuneCountInString(spec) == 1 {
		r, _ := utf8.DecodeRuneInString(spec)
		if !unicode.IsPrint(r) {
			return key.Event{}, "", fmt.Errorf("unknown key %q", spec)
		}
		return printableKeyEvent(r, 0), string(r), nil
	}

	mods, base, err := splitModifiers(spec)
	if err != nil {
		return key.Event{}, "", err
	}
	low := strings.ToLower(base)
	switch low {
	case "enter", "return", "ret", "cr":
		return key.Event{Name: key.NameReturn, Modifiers: mods, State: key.Press}, "", nil
	case "kpenter", "kp_enter":
		return key.Event{Name: key.NameEnter, Modifiers: mods, State: key.Press}, "", nil
	case "esc", "escape":
		return key.Event{Name: key.NameEscape, Modifiers: mods, State: key.Press}, "", nil
	case "tab":
		return key.Event{Name: key.NameTab, Modifiers: mods, State: key.Press}, "", nil
	case "btab", "backtab":
		return key.Event{Name: key.NameTab, Modifiers: mods | key.ModShift, State: key.Press}, "", nil
	case "space", "spc":
		text := ""
		if mods&^key.ModShift == 0 {
			text = " "
		}
		return key.Event{Name: key.NameSpace, Modifiers: mods, State: key.Press}, text, nil
	case "backspace", "bspace", "bs":
		return key.Event{Name: key.NameDeleteBackward, Modifiers: mods, State: key.Press}, "", nil
	case "delete", "del":
		return key.Event{Name: key.NameDeleteForward, Modifiers: mods, State: key.Press}, "", nil
	case "insert", "ins":
		return key.Event{Name: key.Name("Insert"), Modifiers: mods, State: key.Press}, "", nil
	case "up":
		return key.Event{Name: key.NameUpArrow, Modifiers: mods, State: key.Press}, "", nil
	case "down":
		return key.Event{Name: key.NameDownArrow, Modifiers: mods, State: key.Press}, "", nil
	case "left":
		return key.Event{Name: key.NameLeftArrow, Modifiers: mods, State: key.Press}, "", nil
	case "right":
		return key.Event{Name: key.NameRightArrow, Modifiers: mods, State: key.Press}, "", nil
	case "home":
		return key.Event{Name: key.NameHome, Modifiers: mods, State: key.Press}, "", nil
	case "end":
		return key.Event{Name: key.NameEnd, Modifiers: mods, State: key.Press}, "", nil
	case "pageup", "pgup", "ppage":
		return key.Event{Name: key.NamePageUp, Modifiers: mods, State: key.Press}, "", nil
	case "pagedown", "pgdn", "pgdown", "npage":
		return key.Event{Name: key.NamePageDown, Modifiers: mods, State: key.Press}, "", nil
	case "f1":
		return key.Event{Name: key.NameF1, Modifiers: mods, State: key.Press}, "", nil
	case "f2":
		return key.Event{Name: key.NameF2, Modifiers: mods, State: key.Press}, "", nil
	case "f3":
		return key.Event{Name: key.NameF3, Modifiers: mods, State: key.Press}, "", nil
	case "f4":
		return key.Event{Name: key.NameF4, Modifiers: mods, State: key.Press}, "", nil
	case "f5":
		return key.Event{Name: key.NameF5, Modifiers: mods, State: key.Press}, "", nil
	case "f6":
		return key.Event{Name: key.NameF6, Modifiers: mods, State: key.Press}, "", nil
	case "f7":
		return key.Event{Name: key.NameF7, Modifiers: mods, State: key.Press}, "", nil
	case "f8":
		return key.Event{Name: key.NameF8, Modifiers: mods, State: key.Press}, "", nil
	case "f9":
		return key.Event{Name: key.NameF9, Modifiers: mods, State: key.Press}, "", nil
	case "f10":
		return key.Event{Name: key.NameF10, Modifiers: mods, State: key.Press}, "", nil
	case "f11":
		return key.Event{Name: key.NameF11, Modifiers: mods, State: key.Press}, "", nil
	case "f12":
		return key.Event{Name: key.NameF12, Modifiers: mods, State: key.Press}, "", nil
	case "plus":
		if mods == 0 || mods == key.ModShift {
			return printableKeyEvent('+', mods), "+", nil
		}
		return key.Event{Name: "+", Modifiers: mods, State: key.Press}, "", nil
	case "minus":
		if mods == 0 || mods == key.ModShift {
			return printableKeyEvent('-', mods), "-", nil
		}
		return key.Event{Name: "-", Modifiers: mods, State: key.Press}, "", nil
	}

	if utf8.ValidString(base) && utf8.RuneCountInString(base) == 1 {
		r, _ := utf8.DecodeRuneInString(base)
		if !unicode.IsPrint(r) {
			return key.Event{}, "", fmt.Errorf("unknown key %q", spec)
		}
		ev := printableKeyEvent(r, mods)
		text := ""
		if ev.Modifiers&^key.ModShift == 0 {
			if ev.Modifiers&key.ModShift != 0 {
				r = unicode.ToUpper(r)
			}
			text = string(r)
		}
		return ev, text, nil
	}
	return key.Event{}, "", fmt.Errorf("unknown key %q", spec)
}

func printableKeyEvent(r rune, mods key.Modifiers) key.Event {
	if r == ' ' {
		return key.Event{Name: key.NameSpace, Modifiers: mods, State: key.Press}
	}
	if unicode.IsUpper(r) && unicode.ToLower(r) != r {
		mods |= key.ModShift
	}
	name := key.Name(string(unicode.ToUpper(r)))
	return key.Event{Name: name, Modifiers: mods, State: key.Press}
}

// splitModifiers splits leading modifiers from spec, supporting both "ctrl+c"
// and tmux-style "C-c" / "M-x" / "S-tab". A trailing "+" or "-" (as in
// "ctrl++" or "alt+-") is preserved as the base key.
func splitModifiers(spec string) (key.Modifiers, string, error) {
	var mods key.Modifiers
	rest := spec
	for {
		mod, next, ok := cutModifier(rest)
		if !ok {
			break
		}
		mods |= mod
		rest = next
	}
	if rest == "" {
		return 0, "", fmt.Errorf("unknown key %q", spec)
	}
	return mods, rest, nil
}

func cutModifier(s string) (key.Modifiers, string, bool) {
	sep := strings.IndexAny(s, "+-")
	if sep <= 0 || sep == len(s)-1 {
		return 0, s, false
	}
	tok, rest := strings.ToLower(s[:sep]), s[sep+1:]
	switch tok {
	case "ctrl", "control", "c":
		return key.ModCtrl, rest, true
	case "alt", "meta", "opt", "option", "m":
		return key.ModAlt, rest, true
	case "shift", "s":
		return key.ModShift, rest, true
	case "super", "cmd", "command":
		return key.ModSuper, rest, true
	}
	return 0, s, false
}

// EncodeKey encodes a named key (ParseKey) into the bytes the GUI sends for a
// pane in terminal modes m.
func EncodeKey(spec string, m vt.Modes) ([]byte, error) {
	ev, text, err := ParseKey(spec)
	if err != nil {
		return nil, err
	}
	out := Key(ev, m)
	if out == nil && text != "" {
		out = Text(text)
	}
	if out == nil {
		return nil, fmt.Errorf("key %q has no terminal encoding in current modes", spec)
	}
	if m.KittyKeyboard&2 != 0 {
		rel := ev
		rel.State = key.Release
		if rb := Key(rel, m); rb != nil {
			out = append(append([]byte(nil), out...), rb...)
		}
	}
	return out, nil
}
