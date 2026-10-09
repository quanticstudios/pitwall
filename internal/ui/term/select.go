package term

import (
	"image"
	"math"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// pos is the text position of cell in the frame.
func (v *View) pos(cell image.Point) vt.Pos {
	return vt.Pos{Line: v.top + uint64(cell.Y), Col: cell.X}
}

// selectDone sets a finished selection, such as a double-clicked word.
func (v *View) selectDone(s vt.Selection, g *vt.Grid) {
	v.sel, v.selOn, v.selCols = s, true, g.Cols
	v.dragging, v.selDone = false, true
}

// drag moves the selection's head to the cell under v.dragAt. Past the top
// or bottom of the view it takes the edge row and sets v.auto, the rows
// beyond it, for autoscroll.
func (v *View) drag(g *vt.Grid) {
	if g.Rows == 0 || g.Cols == 0 {
		return
	}
	y := int(math.Floor(float64(v.dragAt.Y-float32(v.pad)) / float64(v.cell.Y)))
	v.auto = 0
	if y < 0 {
		v.auto = y
	} else if y >= g.Rows {
		v.auto = y - g.Rows + 1
	}
	x := (int(v.dragAt.X) - v.pad) / v.cell.X
	head := v.pos(image.Pt(min(max(x, 0), g.Cols-1), min(max(y, 0), g.Rows-1)))
	v.sel.B = head
	v.selOn = v.selOn || head != v.sel.A
}

// autoRate is how many lines a second a drag d rows past the view scrolls:
// faster the further it goes.
func autoRate(d int) float64 {
	d = max(d, -d)
	return float64(10*d + d*d)
}

// dropStale drops a selection whose text left history, or whose lines a
// rewrap renumbered. It runs before the frame's events.
func (v *View) dropStale(g *vt.Grid) {
	if !v.selOn && !v.dragging {
		return
	}
	p, _ := v.sel.Ordered()
	trimmed := v.pushed >= uint64(v.scrMax) && p.Line < v.pushed-uint64(v.scrMax)
	if g.Cols != v.selCols || trimmed {
		v.selOn, v.dragging, v.auto = false, false, 0
		v.cm.marked = false
	}
}

// keepSelection runs once a frame after the events. It keeps copy mode's
// cursor in place, a drag's head under the pointer as the view scrolls,
// and scrolls a drag that is past the top or bottom of the view.
func (v *View) keepSelection(gtx layout.Context, g *vt.Grid) {
	if v.cm.on {
		v.keepCopyMode(gtx, g)
	}
	if !v.dragging || v.auto == 0 {
		v.autoAt, v.autoAcc = time.Time{}, 0
		return
	}
	v.drag(g)
	if !v.autoAt.IsZero() {
		// Speed follows the clock on a slow display too; only a stall is cut short.
		dt := min(gtx.Now.Sub(v.autoAt), time.Second)
		v.autoAcc += autoRate(v.auto) * dt.Seconds()
	}
	v.autoAt = gtx.Now
	n := int(v.autoAcc)
	v.autoAcc -= float64(n)
	if v.auto < 0 {
		v.scrollLines += n // back into history
	} else {
		v.scrollLines -= n
	}
	gtx.Execute(op.InvalidateCmd{})
}

// copy asks the caller to put the selection on the clipboard.
func (v *View) copy(g *vt.Grid) {
	lines := vt.GridLines(g, v.top)
	v.copied = &Copy{Sel: v.sel, Text: v.sel.Text(lines), Whole: v.shows(v.sel, g)}
}

// shows reports whether the frame shows all of s, so its text needs no
// daemon. A logical line is known to start in the frame only below its
// top row, since the frame does not say whether the row above wraps.
func (v *View) shows(s vt.Selection, g *vt.Grid) bool {
	lines := vt.GridLines(g, v.top)
	p, q := s.Expand(lines).Ordered()
	if p.Line < v.top || q.Line-v.top >= uint64(g.Rows) || q.Line < v.top {
		return false
	}
	if s.Mode == vt.SelectLines {
		_, wrapped, _ := lines(q.Line)
		return p.Line > v.top && !wrapped
	}
	return true
}
