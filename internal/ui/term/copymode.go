package term

import (
	"image"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// copyMode moves a cursor through the scrollback with vi keys and selects
// from where v, V or Ctrl+V was pressed to the cursor. While it is on, no
// key reaches the program.
type copyMode struct {
	on     bool
	cur    vt.Pos
	marked bool // v, V or Ctrl+V started the selection
	reveal bool // a key moved the cursor: scroll it into view
	// The last reveal scroll, until a frame shows it: sent lines from a
	// view whose top row showed sentTop, at sentAt.
	sent    int
	sentTop uint64
	sentAt  time.Time
}

// CopyMode is the copy mode badge's text, "" while the mode is off.
func (v *View) CopyMode() string {
	switch {
	case !v.cm.on:
		return ""
	case !v.cm.marked:
		return "COPY"
	case v.sel.Mode == vt.SelectLines:
		return "VISUAL LINE"
	case v.sel.Mode == vt.SelectBlock:
		return "VISUAL BLOCK"
	}
	return "VISUAL"
}

// FindWanted reports and clears whether copy mode's / or ? asked for the
// find bar. The match the bar makes current moves the cursor there.
func (v *View) FindWanted() bool {
	w := v.wantFind
	v.wantFind = false
	return w
}

// toggleCopyMode enters copy mode with the cursor on the terminal's, or on
// the bottom row when that is out of view, or leaves it.
func (v *View) toggleCopyMode(g *vt.Grid, rows int) {
	if v.cm.on {
		v.exitCopyMode()
		return
	}
	v.cm = copyMode{on: true, cur: v.pos(image.Pt(g.Cursor.X, g.Cursor.Y))}
	if !g.Cursor.Visible || g.Cursor.Y >= g.Rows {
		v.cm.cur = vt.Pos{Line: v.top + uint64(max(0, min(rows, g.Rows)-1))}
	}
	v.selOn, v.dragging = false, false
}

func (v *View) exitCopyMode() {
	v.cm = copyMode{}
	v.selOn = false
}

// copyModeKey runs one key press in copy mode.
func (v *View) copyModeKey(e key.Event, g *vt.Grid, rows int) {
	plain := e.Modifiers&^key.ModShift == 0
	mark := func(m vt.SelectMode) {
		switch {
		case v.cm.marked && v.sel.Mode == m:
			v.cm.marked, v.selOn = false, false
		case v.cm.marked:
			v.sel.Mode = m
		default:
			v.cm.marked = true
			v.sel, v.selOn, v.selCols = vt.Selection{A: v.cm.cur, B: v.cm.cur, Mode: m}, true, g.Cols
		}
	}
	switch {
	case e.Name == key.NameEscape || plain && e.Name == "Q":
		v.exitCopyMode()
	case plain && e.Name == "Y" || e.Name == key.NameReturn || e.Name == key.NameEnter:
		if v.selOn {
			v.copy(g)
		}
		v.exitCopyMode()
	case e.Name == "V" && e.Modifiers == 0:
		mark(vt.SelectChars)
	case e.Name == "V" && e.Modifiers == key.ModShift:
		mark(vt.SelectLines)
	case e.Name == "V" && e.Modifiers == key.ModCtrl:
		mark(vt.SelectBlock)
	case plain && (e.Name == "/" || e.Name == "?"):
		v.wantFind = true
	default:
		first := uint64(0)
		if v.pushed >= uint64(v.scrMax) {
			first = v.pushed - uint64(v.scrMax)
		}
		at := view{first: first, last: v.pushed + uint64(max(g.Rows, 1)-1), top: v.top, rows: max(1, min(rows, g.Rows)), cols: max(g.Cols, 1), lines: vt.GridLines(g, v.top)}
		if c, ok := motion(e, v.cm.cur, at); ok {
			v.cm.cur, v.cm.reveal = c, true
			if v.cm.marked {
				v.sel.B = c
			}
		}
	}
}

// view is what motion needs to know of the pane: its first and last
// lines, the lines the view shows from top, rows high and cols wide.
type view struct {
	first, last, top uint64
	rows, cols       int
	lines            vt.LineFunc
}

// motion is where key e moves the cursor c: h j k l and the arrows, w and b
// by word, 0 and $ (Home, End) to the line's ends, g and G to the oldest
// and newest line, Ctrl+U and Ctrl+D by half a view, PageUp and PageDown
// by a view. ok is false for any other key.
func motion(e key.Event, c vt.Pos, v view) (vt.Pos, bool) {
	up := func(n int) {
		c.Line = max(v.first, c.Line-min(c.Line, uint64(n)))
	}
	down := func(n int) {
		c.Line = min(v.last, c.Line+uint64(n))
	}
	plain := e.Modifiers&^key.ModShift == 0
	switch {
	case e.Modifiers == key.ModCtrl && e.Name == "U":
		up(v.rows / 2)
	case e.Modifiers == key.ModCtrl && e.Name == "D":
		down(v.rows / 2)
	case !plain:
		return c, false
	case e.Name == "H" || e.Name == key.NameLeftArrow:
		c.Col = max(0, c.Col-1)
	case e.Name == "L" || e.Name == key.NameRightArrow:
		c.Col = min(v.cols-1, c.Col+1)
	case e.Name == "K" || e.Name == key.NameUpArrow:
		up(1)
	case e.Name == "J" || e.Name == key.NameDownArrow:
		down(1)
	case e.Name == key.NamePageUp:
		up(v.rows)
	case e.Name == key.NamePageDown:
		down(v.rows)
	case e.Name == "0" || e.Name == key.NameHome:
		c.Col = 0
	case e.Name == "$" || e.Name == key.NameEnd:
		c.Col = 0
		if cells, _, ok := v.lines(c.Line); ok {
			for x := len(cells) - 1; x >= 0; x-- {
				if s := cells[x].Content; s != "" && s != " " {
					c.Col = x
					break
				}
			}
		}
	case e.Name == "G" && e.Modifiers == 0:
		c = vt.Pos{Line: v.first}
	case e.Name == "G":
		c = vt.Pos{Line: v.last}
	case e.Name == "W" && e.Modifiers == 0:
		c = nextWord(c, v)
	case e.Name == "B" && e.Modifiers == 0:
		c = prevWord(c, v)
	default:
		return c, false
	}
	return c, true
}

// wordCell reports whether cell x of cells is part of a word, with the
// separators wordAt uses; the right half of a wide char counts as its left.
func wordCell(cells []vt.Cell, x int) bool {
	for x > 0 && cells[x].Width == 0 && cells[x].Content == "" {
		x--
	}
	return isWord(cells[x].Content)
}

// nextWord is the start of the next word after c, on a later line when
// none follows on c's, as far as the view shows lines.
func nextWord(c vt.Pos, v view) vt.Pos {
	for n, from := c.Line, c.Col; n <= v.last; n, from = n+1, -1 {
		cells, _, ok := v.lines(n)
		if !ok {
			return vt.Pos{Line: min(n, v.last)}
		}
		for x := from + 1; x < len(cells); x++ {
			if wordCell(cells, x) && (x == 0 || !wordCell(cells, x-1)) {
				return vt.Pos{Line: n, Col: x}
			}
		}
	}
	return c
}

// prevWord is the start of the word before c, on an earlier line when none
// comes before it on c's, as far as the view shows lines.
func prevWord(c vt.Pos, v view) vt.Pos {
	for n, from := c.Line, c.Col; n >= v.first; n-- {
		cells, _, ok := v.lines(n)
		if !ok {
			return vt.Pos{Line: n}
		}
		if from < 0 {
			from = len(cells)
		}
		for x := min(from, len(cells)) - 1; x >= 0; x-- {
			if wordCell(cells, x) && (x == 0 || !wordCell(cells, x-1)) {
				return vt.Pos{Line: n, Col: x}
			}
		}
		if n == 0 {
			break
		}
		from = -1
	}
	return c
}

// revealDelta is the scroll, > 0 back, that brings line into a view rows
// high whose top row shows top: 0 when it shows already, else just enough.
func revealDelta(line, top uint64, rows int) int {
	switch {
	case line < top:
		return int(top - line)
	case line-top >= uint64(rows):
		return -int(line - top - uint64(rows) + 1)
	}
	return 0
}

// keepCopyMode keeps the cursor in the pane's lines, and in view after a
// key moved it. Scrolls the frame has yet to show count as done, so keys
// faster than the daemon do not scroll twice.
func (v *View) keepCopyMode(gtx layout.Context, g *vt.Grid) {
	first := uint64(0)
	if v.pushed >= uint64(v.scrMax) {
		first = v.pushed - uint64(v.scrMax)
	}
	v.cm.cur.Line = min(max(v.cm.cur.Line, first), v.pushed+uint64(max(g.Rows, 1)-1))
	v.cm.cur.Col = min(v.cm.cur.Col, max(g.Cols, 1)-1)
	if v.cm.marked {
		v.sel.B = v.cm.cur
	}
	if !v.cm.reveal || g.Rows == 0 {
		return
	}
	v.cm.reveal = false
	top, pending := v.top, 0
	if v.cm.sent != 0 && v.top == v.cm.sentTop && gtx.Now.Sub(v.cm.sentAt) < 300*time.Millisecond {
		pending = v.cm.sent
		top = uint64(int64(top) - int64(pending))
	}
	if d := revealDelta(v.cm.cur.Line, top, g.Rows); d != 0 {
		v.scrollLines += d
		v.cm.sent, v.cm.sentTop, v.cm.sentAt = pending+d, v.top, gtx.Now
		gtx.Execute(op.InvalidateCmd{})
	}
}

// copyCursor draws copy mode's cursor, a steady block, when it is in view.
func (v *View) copyCursor(ops *op.Ops, g *vt.Grid, n, h int) {
	c := v.cm.cur
	if c.Line < v.top || c.Line-v.top >= uint64(h) || c.Col >= n {
		return
	}
	v.cursor(ops, g, vt.Cursor{X: c.Col, Y: int(c.Line - v.top), Visible: true, Shape: vt.CursorBlock}, true)
}
