package vt

import "strings"

// Scrollback returns the history and screen as plain text with soft wraps
// joined and trailing empty rows removed. The caller prevents writes and resizes.
func Scrollback(e Emulator) string { return CaptureScrollback(e)() }

// CaptureScrollback captures rows while the caller prevents writes and resizes.
// The returned function builds plain text without holding the emulator or pane lock.
// Other Emulator implementations may opt in through ScrollbackSnapshot.
func CaptureScrollback(e Emulator) func() string {
	if c, ok := e.(interface{ ScrollbackSnapshot() func() string }); ok {
		return c.ScrollbackSnapshot()
	}
	text := scrollback(e)
	return func() string { return text }
}

func scrollback(e Emulator) string {
	g := e.Snapshot()
	if g.Rows == 0 || g.Cols == 0 {
		return ""
	}
	pushed := e.ScrollbackPushed()
	text := e.Text(Selection{A: Pos{Line: pushed - uint64(e.ScrollbackLen())}, B: Pos{Line: pushed + uint64(g.Rows-1), Col: g.Cols - 1}})
	text = strings.TrimRight(text, "\n")
	if text != "" {
		text += "\n"
	}
	return text
}

func (t *emulator) ScrollbackSnapshot() func() string {
	t.mu.Lock()
	g := t.snapshot()
	var history []line
	if !g.AltScreen {
		history = make([]line, t.st.hist.len())
		// Packed rows are immutable; copying their headers also survives ring reuse and reflow.
		for i := range history {
			history[i] = *t.st.hist.at(i)
		}
	}
	t.mu.Unlock()
	return func() string {
		if g.Rows == 0 || g.Cols == 0 {
			return ""
		}
		screen := GridLines(&g, uint64(len(history)))
		var buf []Cell
		lines := func(n uint64) ([]Cell, bool, bool) {
			if n >= uint64(len(history)) {
				return screen(n)
			}
			l := &history[n]
			buf = append(buf[:0], make([]Cell, l.width())...)
			l.fill(buf)
			return buf, l.wrapped(), true
		}
		s := Selection{B: Pos{Line: uint64(len(history) + g.Rows - 1), Col: g.Cols - 1}}
		text := strings.TrimRight(s.Text(lines), "\n")
		if text != "" {
			text += "\n"
		}
		return text
	}
}
