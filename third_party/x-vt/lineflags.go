package vt

// LineFlags is per-row metadata. A row's flags move with it when lines are
// inserted, deleted or scrolled, and go with it into the scrollback. The
// emulator sets LineWrapped; the program embedding it sets the rest.
type LineFlags uint8

const (
	// LineWrapped: the row's text ran past the right margin onto the next
	// row (a soft wrap), rather than ending in a line break. Erasing the
	// row through its last column or a resize to another width clears it.
	LineWrapped LineFlags = 1 << iota

	// Shell integration marks (OSC 133), on the row each one arrived on.
	LinePrompt // A: a prompt starts
	LineInput  // B: the command line starts
	LineOutput // C: the command's output starts
	LineEnd    // D: the command ended
)

// LineFlags returns row y's flags on the active screen.
func (e *Emulator) LineFlags(y int) LineFlags { return e.scr.LineFlags(y) }

// SetLineFlags sets row y's flags on the active screen.
func (e *Emulator) SetLineFlags(y int, f LineFlags) { e.scr.SetLineFlags(y, f) }
