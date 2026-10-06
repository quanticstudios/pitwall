// Package input encodes Gio key, text, paste and mouse events into the bytes
// a terminal program expects, honoring the pane's current vt.Modes.
package input

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// Key encodes a key.Press. It returns nil for events the terminal should not
// see (releases unless kitty flag 2 is on, bare modifiers).
// Printable keys without shortcuts use Text, except in kitty report-all mode.
func Key(e key.Event, m vt.Modes) []byte {
	switch e.Name {
	case key.NameCtrl, key.NameShift, key.NameAlt, key.NameSuper, key.NameCommand:
		return nil
	}
	if e.State != key.Press && e.State != key.Release {
		return nil
	}
	if e.State == key.Release && m.KittyKeyboard&2 == 0 {
		return nil
	}
	if m.KittyKeyboard != 0 {
		return kittyKey(e, m)
	}
	return legacyKey(e, m)
}

func modifiers(m key.Modifiers) int {
	result := 1
	if m&key.ModShift != 0 {
		result += 1
	}
	if m&key.ModAlt != 0 {
		result += 2
	}
	if m&key.ModCtrl != 0 {
		result += 4
	}
	if m&(key.ModSuper|key.ModCommand) != 0 {
		result += 8
	}
	return result
}

// functional uses the forms in kitty's functional-key table.
// https://sw.kovidgoyal.net/kitty/keyboard-protocol/#functional-key-definitions
func functional(name key.Name) (int, byte) {
	switch name {
	case key.NameReturn:
		return 13, 'u'
	case key.NameEnter:
		return 57414, 'u' // Gio distinguishes keypad Enter.
	case key.NameEscape:
		return 27, 'u'
	case key.NameTab:
		return 9, 'u'
	case key.NameDeleteBackward:
		return 127, 'u'
	case key.NameSpace:
		return 32, 'u'
	case key.NameUpArrow:
		return 1, 'A'
	case key.NameDownArrow:
		return 1, 'B'
	case key.NameRightArrow:
		return 1, 'C'
	case key.NameLeftArrow:
		return 1, 'D'
	case key.NameHome:
		return 1, 'H'
	case key.NameEnd:
		return 1, 'F'
	// Gio v0.10.3 has no NameInsert constant.
	case key.Name("Insert"):
		return 2, '~'
	case key.NameDeleteForward:
		return 3, '~'
	case key.NamePageUp:
		return 5, '~'
	case key.NamePageDown:
		return 6, '~'
	case key.NameF1:
		return 1, 'P'
	case key.NameF2:
		return 1, 'Q'
	case key.NameF3:
		return 13, '~'
	case key.NameF4:
		return 1, 'S'
	case key.NameF5:
		return 15, '~'
	case key.NameF6:
		return 17, '~'
	case key.NameF7:
		return 18, '~'
	case key.NameF8:
		return 19, '~'
	case key.NameF9:
		return 20, '~'
	case key.NameF10:
		return 21, '~'
	case key.NameF11:
		return 23, '~'
	case key.NameF12:
		return 24, '~'
	}
	return 0, 0
}

func printable(name key.Name, mods key.Modifiers) (rune, bool) {
	if name == key.NameSpace {
		return ' ', true
	}
	s := string(name)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) != 1 {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(s)
	if !unicode.IsPrint(r) {
		return 0, false
	}
	// Gio names letters in uppercase regardless of Shift.
	if mods&key.ModShift == 0 {
		r = unicode.ToLower(r)
	}
	return r, true
}

func legacyKey(e key.Event, m vt.Modes) []byte {
	if e.State != key.Press {
		return nil
	}
	var result string
	switch e.Name {
	case key.NameReturn, key.NameEnter:
		result = "\r"
	case key.NameDeleteBackward:
		result = "\x7f"
		// why: Ctrl+W is the delete-word key in shells (bash, zsh, fish) and in Claude Code and Codex.
		if e.Modifiers&key.ModCtrl != 0 {
			result = "\x17"
		}
	case key.NameTab:
		result = "\t"
		if e.Modifiers&key.ModShift != 0 {
			result = "\x1b[Z"
		}
	case key.NameEscape:
		result = "\x1b"
	default:
		n, final := functional(e.Name)
		if final != 0 && final != 'u' {
			mod := modifiers(e.Modifiers)
			if e.Name == key.NameF3 {
				n, final = 1, 'R'
			}
			if final == '~' {
				if mod == 1 {
					return []byte(fmt.Sprintf("\x1b[%d~", n))
				}
				return []byte(fmt.Sprintf("\x1b[%d;%d~", n, mod))
			}
			if mod != 1 {
				return []byte(fmt.Sprintf("\x1b[1;%d%c", mod, final))
			}
			prefix := "\x1b["
			if m.AppCursorKeys || final >= 'P' && final <= 'S' {
				prefix = "\x1bO"
			}
			return []byte(prefix + string(final))
		}
		r, ok := printable(e.Name, e.Modifiers)
		if !ok || e.Modifiers&(key.ModSuper|key.ModCommand) != 0 {
			return nil
		}
		if e.Modifiers&(key.ModCtrl|key.ModAlt) == 0 {
			return nil
		}
		if e.Modifiers&key.ModCtrl != 0 {
			upper := unicode.ToUpper(r)
			switch {
			case upper >= 'A' && upper <= 'Z':
				r = upper - 'A' + 1
			case r >= '[' && r <= '_':
				r &= 31
			case r == ' ' || r == '@' || r == '2':
				r = 0
			case r == '3':
				r = 27
			case r == '4':
				r = 28
			case r == '5':
				r = 29
			case r == '6' || r == '~':
				r = 30
			case r == '7' || r == '/':
				r = 31
			case r == '8' || r == '?':
				r = 127
			}
		}
		result = string(r)
	}
	if e.Modifiers&key.ModAlt != 0 {
		result = "\x1b" + result
	}
	return []byte(result)
}

func kittyKey(e key.Event, m vt.Modes) []byte {
	flags := m.KittyKeyboard
	n, final := functional(e.Name)
	text, isText := printable(e.Name, e.Modifiers)
	if final != 0 && e.Name != key.NameSpace {
		isText = false
	}
	// Unmodified Enter, Tab and Backspace keep shell-compatible bytes until
	// report-all, so a shell works after a program left the mode on. With any
	// modifier, Shift included, they are disambiguated: Claude Code and Codex
	// read Shift+Enter as a newline.
	if flags&8 == 0 && (e.Name == key.NameReturn || e.Name == key.NameTab || e.Name == key.NameDeleteBackward) && e.Modifiers == 0 {
		if e.State == key.Release {
			return nil
		}
		return legacyKey(e, m)
	}
	if isText && flags&8 == 0 && e.Modifiers&^key.ModShift == 0 {
		return nil
	}
	if flags&(1|8) == 0 && e.State == key.Press {
		return legacyKey(e, m)
	}
	if final == 0 {
		if !isText {
			return nil
		}
		n, final = int(unicode.ToLower(text)), 'u'
	}
	field := strconv.Itoa(n)
	// ponytail: Gio omits base-layout keys and unshifted punctuation; report
	// only letter alternates until the event API supplies those values.
	if flags&4 != 0 && isText && final == 'u' && e.Modifiers&key.ModShift != 0 && unicode.ToLower(text) != text {
		field += ":" + strconv.Itoa(int(text))
	}
	mod := strconv.Itoa(modifiers(e.Modifiers))
	if flags&2 != 0 {
		event := 1
		if e.State == key.Release {
			event = 3
		}
		mod += ":" + strconv.Itoa(event)
	}
	// ponytail: Key has no committed text; associate the printable name only.
	// IME text still goes through Text until the contract carries modes there.
	associated := ""
	if flags&(8|16) == 8|16 && isText && e.State == key.Press && e.Modifiers&^(key.ModShift) == 0 {
		associated = ";" + strconv.Itoa(int(text))
	}
	return []byte("\x1b[" + field + ";" + mod + associated + string(final))
}

// Text encodes committed text from a key.EditEvent.
func Text(s string) []byte { return []byte(s) }

// Focus encodes a focus-in or focus-out report for mode 1004, or nil when the
// mode is off.
func Focus(in, mode bool) []byte {
	switch {
	case !mode:
		return nil
	case in:
		return []byte("\x1b[I")
	}
	return []byte("\x1b[O")
}

func Paste(s string, m vt.Modes) []byte {
	s = strings.ReplaceAll(s, "\x1b", "")
	if m.BracketedPaste {
		s = "\x1b[200~" + s + "\x1b[201~"
	}
	return []byte(s)
}

// Mouse encodes a pointer event at zero-based cell (col,row), or nil when the
// program has not asked for that event. Scroll emits one report per active axis.
// For Press and Release, e.Buttons names the button that changed. Gio reports
// the buttons held after the event instead, so callers track the difference.
func Mouse(e pointer.Event, col, row int, m vt.Modes) []byte {
	if m.Mouse == vt.MouseOff || e.Source != pointer.Mouse || col < 0 || row < 0 {
		return nil
	}
	code := mouseButton(e.Buttons)
	release := false
	switch e.Kind {
	case pointer.Press:
		if code == 3 {
			return nil
		}
	case pointer.Release:
		if m.Mouse == vt.MouseX10 {
			return nil
		}
		release = true
		// A caller that tracks no state passes no buttons; assume primary.
		if code == 3 {
			code = 0
		}
	case pointer.Move, pointer.Drag:
		if m.Mouse != vt.MouseAny && !(m.Mouse == vt.MouseButton && e.Buttons != 0) {
			return nil
		}
		code += 32
	case pointer.Scroll:
		if m.Mouse == vt.MouseX10 {
			return nil
		}
		var result []byte
		if e.Scroll.Y != 0 {
			wheel := 64
			if e.Scroll.Y > 0 {
				wheel = 65
			}
			result = append(result, mouseReport(wheel, false, col, row, e.Modifiers, m)...)
		}
		if e.Scroll.X != 0 {
			wheel := 66
			if e.Scroll.X > 0 {
				wheel = 67
			}
			result = append(result, mouseReport(wheel, false, col, row, e.Modifiers, m)...)
		}
		return result
	default:
		return nil
	}
	return mouseReport(code, release, col, row, e.Modifiers, m)
}

func mouseButton(buttons pointer.Buttons) int {
	switch {
	case buttons&pointer.ButtonPrimary != 0:
		return 0
	case buttons&pointer.ButtonTertiary != 0:
		return 1
	case buttons&pointer.ButtonSecondary != 0:
		return 2
	case buttons&pointer.ButtonQuaternary != 0:
		return 128
	case buttons&pointer.ButtonQuinary != 0:
		return 129
	}
	return 3
}

func mouseReport(code int, release bool, col, row int, mods key.Modifiers, m vt.Modes) []byte {
	if release && !m.MouseSGR {
		code = 3
	}
	if m.Mouse != vt.MouseX10 {
		if mods&key.ModShift != 0 {
			code += 4
		}
		if mods&key.ModAlt != 0 {
			code += 8
		}
		if mods&key.ModCtrl != 0 {
			code += 16
		}
	}
	if m.MouseSGR {
		final := 'M'
		if release {
			final = 'm'
		}
		return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", code, col+1, row+1, final))
	}
	// Classic mouse coordinates must fit in one byte without wrapping.
	if col > 222 || row > 222 {
		return nil
	}
	return []byte{'\x1b', '[', 'M', byte(code + 32), byte(col + 33), byte(row + 33)}
}
