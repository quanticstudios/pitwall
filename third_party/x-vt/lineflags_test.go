package vt

import (
	"slices"
	"testing"
)

// TestLineFlags checks that a row's flags follow it through insert line,
// delete line, scrolling into the scrollback, ED 2 and a resize.
func TestLineFlags(t *testing.T) {
	e := NewEmulator(10, 4)
	rows := func() []LineFlags {
		var out []LineFlags
		for y := range e.Height() {
			out = append(out, e.LineFlags(y))
		}
		return out
	}
	e.SetLineFlags(1, LinePrompt)
	e.WriteString("\x1b[1;1H\x1b[L") // IL at row 0 pushes row 1 to row 2
	if got := rows(); !slices.Equal(got, []LineFlags{0, 0, LinePrompt, 0}) {
		t.Fatalf("after IL: %v", got)
	}
	e.WriteString("\x1b[2;1H\x1b[M") // DL at row 1 pulls it back up
	if got := rows(); !slices.Equal(got, []LineFlags{0, LinePrompt, 0, 0}) {
		t.Fatalf("after DL: %v", got)
	}
	e.WriteString("\x1b[4;1H\nx\ny") // two lines scroll off the top
	if got := rows(); !slices.Equal(got, []LineFlags{0, 0, 0, 0}) {
		t.Fatalf("after scrolling: %v", got)
	}
	if got := e.Scrollback().Flags(); !slices.Equal(got, []LineFlags{0, LinePrompt}) {
		t.Fatalf("scrollback flags %v", got)
	}

	e.SetLineFlags(2, LineOutput)
	e.WriteString("\x1b[2J") // non-empty rows ("x", "y") go to the scrollback with their flags
	if got := e.Scrollback().Flags(); !slices.Equal(got, []LineFlags{0, LinePrompt, LineOutput, 0}) {
		t.Fatalf("after ED 2: %v", got)
	}
	if got := rows(); !slices.Equal(got, []LineFlags{0, 0, 0, 0}) {
		t.Fatalf("screen after ED 2: %v", got)
	}

	e.SetLineFlags(3, LineEnd)
	e.Resize(10, 6)
	if got := rows(); !slices.Equal(got, []LineFlags{0, 0, 0, LineEnd, 0, 0}) {
		t.Fatalf("after growing: %v", got)
	}
	e.Resize(10, 3)
	if got := rows(); !slices.Equal(got, []LineFlags{0, 0, 0}) {
		t.Fatalf("after shrinking: %v", got)
	}
}
