// Package input encodes Gio key, text, paste and mouse events into the bytes
// a terminal program expects, honoring the pane's current vt.Modes.
package input

import (
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// Key encodes a key.Press. It returns nil for events the terminal should not
// see (releases unless kitty flag 2 is on, bare modifiers).
func Key(e key.Event, m vt.Modes) []byte { panic("unimplemented") }

// Text encodes committed text from a key.EditEvent.
func Text(s string) []byte { panic("unimplemented") }

func Paste(s string, m vt.Modes) []byte { panic("unimplemented") }

// Mouse encodes a pointer event at cell (col,row), or nil when the program
// has not asked for mouse reporting.
func Mouse(e pointer.Event, col, row int, m vt.Modes) []byte { panic("unimplemented") }
