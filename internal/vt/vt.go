// Package vt turns PTY output into a cell grid. The interface here is the
// contract; the emulator behind New is chosen inside this package.
package vt

import "io"

// Color: 0 is the terminal default. Palette colors are PaletteFlag|index
// (0-255). Truecolor is RGBFlag|0xRRGGBB.
type Color uint32

const (
	PaletteFlag Color = 1 << 24
	RGBFlag     Color = 2 << 24
)

type Attr uint16

const (
	Bold Attr = 1 << iota
	Faint
	Italic
	Underline
	Blink
	Reverse
	Invisible
	Strike
)

type Cell struct {
	Content string // one grapheme; "" for the trailing half of a wide char
	Width   uint8  // 1 or 2; 0 for the trailing half
	FG, BG  Color
	Attrs   Attr
	Link    string // OSC 8 hyperlink target; "" for none
}

type CursorShape uint8

const (
	CursorBlock CursorShape = iota
	CursorUnderline
	CursorBar
)

type Cursor struct {
	X, Y    int
	Visible bool
	Shape   CursorShape
}

// Grid is an immutable copy of the screen, safe to hand to another goroutine.
type Grid struct {
	Cols, Rows int
	Cells      []Cell // row-major, len == Cols*Rows
	Cursor     Cursor
	Title      string
	AltScreen  bool
}

func (g *Grid) At(x, y int) Cell { return g.Cells[y*g.Cols+x] }

type MouseMode uint8

const (
	MouseOff MouseMode = iota
	MouseX10
	MouseNormal // 1000
	MouseButton // 1002
	MouseAny    // 1003
)

// Modes are the terminal modes the client needs to encode input correctly.
type Modes struct {
	AppCursorKeys  bool // DECCKM
	AppKeypad      bool // DECKPAM
	BracketedPaste bool // 2004
	FocusEvents    bool // 1004
	Mouse          MouseMode
	MouseSGR       bool  // 1006
	KittyKeyboard  uint8 // current kitty keyboard protocol flags
}

type Emulator interface {
	// Write feeds PTY output. It never blocks on the reader side.
	Write(p []byte) (int, error)
	Resize(cols, rows int)
	Snapshot() Grid
	// SnapshotAt is the view off lines above the live screen: history lines
	// fill the top rows. 0 equals Snapshot. Clamped to ScrollbackLen; the alt
	// screen ignores it.
	SnapshotAt(off int) Grid
	// ScrollbackLen is how many lines of main-screen history there are, up to
	// 10,000; 0 while the alt screen is up.
	ScrollbackLen() int
	// ScrollbackPushed counts lines that ever entered history. It keeps
	// growing once the oldest lines drop out, so the change between two calls
	// is how far the screen scrolled.
	ScrollbackPushed() uint64
	Modes() Modes
}

// NewFunc builds an emulator. Replies to terminal queries (DA, DSR, kitty
// keyboard queries) go to reply, which the caller wires to the PTY input.
type NewFunc func(cols, rows int, reply io.Writer) Emulator
