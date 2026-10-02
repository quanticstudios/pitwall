package term

import (
	"image"
	"io"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const otherMods = key.ModCtrl | key.ModShift | key.ModSuper | key.ModCommand

// NavKey reports whether e is a window navigation key the pane never
// consumes: Alt (with any other modifiers) plus H, J, K, L or an arrow. The
// pane's key filters exclude these, so a window-level key.Filter for them
// receives the events whether it runs before or after View.Layout.
func NavKey(e key.Event) bool {
	if e.Modifiers&key.ModAlt == 0 {
		return false
	}
	switch e.Name {
	case "H", "J", "K", "L", key.NameLeftArrow, key.NameRightArrow, key.NameUpArrow, key.NameDownArrow:
		return true
	}
	return false
}

// keyFilters matches every key except NavKey ones. Gio filters can't
// subtract, so Alt combinations are listed one name at a time.
func keyFilters(tag event.Tag) []event.Filter {
	fs := []event.Filter{
		key.Filter{Focus: tag, Optional: otherMods},
		// Tab is a focus-move system key in Gio and only reaches a filter
		// that names it.
		key.Filter{Focus: tag, Name: key.NameTab, Optional: otherMods},
	}
	alt := func(n key.Name) {
		if !NavKey(key.Event{Name: n, Modifiers: key.ModAlt}) {
			fs = append(fs, key.Filter{Focus: tag, Name: n, Required: key.ModAlt, Optional: otherMods})
		}
	}
	for c := byte('!'); c <= '~'; c++ {
		if c < 'a' || c > 'z' { // Gio names letter keys in upper case
			alt(key.Name(c))
		}
	}
	for _, n := range []key.Name{
		key.NameReturn, key.NameEnter, key.NameEscape, key.NameHome, key.NameEnd,
		key.NameDeleteBackward, key.NameDeleteForward, key.NamePageUp, key.NamePageDown,
		key.NameTab, key.NameSpace, key.NameF1, key.NameF2, key.NameF3, key.NameF4,
		key.NameF5, key.NameF6, key.NameF7, key.NameF8, key.NameF9, key.NameF10,
		key.NameF11, key.NameF12,
	} {
		alt(n)
	}
	return fs
}

// textKey reports whether Gio also delivers e as a key.EditEvent, in which
// case the text event is the one sent so keyboard layouts and compose work.
func textKey(e key.Event) bool {
	if e.Modifiers&^key.ModShift != 0 {
		return false
	}
	return e.Name == key.NameSpace || len(e.Name) == 1 && e.Name[0] > ' ' && e.Name[0] <= '~'
}

func (v *View) events(gtx layout.Context, g *vt.Grid, m vt.Modes, focused bool, rows int) []byte {
	if v.filters == nil {
		v.filters = append(keyFilters(v),
			key.FocusFilter{Target: v},
			transfer.TargetFilter{Target: v, Type: "application/text"},
			pointer.Filter{
				Target:  v,
				Kinds:   pointer.Press | pointer.Release | pointer.Drag | pointer.Move | pointer.Scroll | pointer.Cancel,
				ScrollX: pointer.ScrollRange{Min: -1e6, Max: 1e6},
				ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6},
			},
		)
	}
	if focused && !gtx.Focused(v) {
		gtx.Execute(key.FocusCmd{Tag: v})
	}
	kittyAll := m.KittyKeyboard&8 != 0 // report all keys as escape codes
	var out []byte
	for {
		ev, ok := gtx.Event(v.filters...)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.Event:
			if e.Modifiers == key.ModCtrl|key.ModShift && (e.Name == "C" || e.Name == "V") {
				if e.State == key.Press && e.Name == "C" && v.sel.on {
					gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(selectionText(g, v.sel)))})
				}
				if e.State == key.Press && e.Name == "V" {
					gtx.Execute(clipboard.ReadCmd{Tag: v})
				}
				continue
			}
			// Shift+PageUp/PageDown page through the scrollback.
			if e.Modifiers == key.ModShift && (e.Name == key.NamePageUp || e.Name == key.NamePageDown) {
				if e.State == key.Press && e.Name == key.NamePageUp {
					v.scrollLines += rows
				} else if e.State == key.Press {
					v.scrollLines -= rows
				}
				continue
			}
			if textKey(e) && !kittyAll {
				continue
			}
			out = append(out, input.Key(e, m)...)
		case key.EditEvent:
			if !kittyAll {
				out = append(out, input.Text(e.Text)...)
			}
		case transfer.DataEvent:
			r := e.Open()
			b, _ := io.ReadAll(r)
			r.Close()
			out = append(out, input.Paste(string(b), m)...)
		case pointer.Event:
			out = append(out, v.pointer(e, g, m, focused)...)
		}
	}
	return out
}

func (v *View) pointer(e pointer.Event, g *vt.Grid, m vt.Modes, focused bool) []byte {
	if g.Cols == 0 || g.Rows == 0 {
		return nil
	}
	cell := image.Pt(
		min(max((int(e.Position.X)-v.pad)/v.cell.X, 0), g.Cols-1),
		min(max((int(e.Position.Y)-v.pad)/v.cell.Y, 0), g.Rows-1),
	)
	// Shift forces local selection even when the program owns the mouse.
	if m.Mouse != vt.MouseOff && e.Modifiers&key.ModShift == 0 {
		if focused {
			return input.Mouse(e, cell.X, cell.Y, m)
		}
		return nil
	}
	switch e.Kind {
	case pointer.Scroll:
		v.scroll(e)
	case pointer.Press:
		if e.Buttons != pointer.ButtonPrimary {
			return nil
		}
		double := e.Time-v.lastPress.Time < 400*time.Millisecond && cell == v.lastCell
		v.lastPress, v.lastCell = e, cell
		if double {
			x0, x1 := wordAt(g, cell.X, cell.Y)
			v.sel = selection{a: image.Pt(x0, cell.Y), b: image.Pt(x1, cell.Y), on: true}
			v.dragging = false
			v.lastPress.Time = -time.Hour // a third click starts over
			return nil
		}
		v.sel = selection{a: cell, b: cell}
		v.dragging = true
	case pointer.Drag:
		if v.dragging {
			v.sel.b = cell
			v.sel.on = v.sel.on || cell != v.sel.a
		}
	case pointer.Release, pointer.Cancel:
		v.dragging = false
	}
	return nil
}

// scroll gathers wheel distance into whole lines of scrollback, keeping the
// remainder so touchpad pixel deltas add up. Gio turns Shift+wheel into a
// horizontal scroll, which counts as vertical here.
func (v *View) scroll(e pointer.Event) {
	d := e.Scroll.Y
	if d == 0 && e.Modifiers&key.ModShift != 0 {
		d = e.Scroll.X
	}
	v.scrollPx -= d // wheel up is back in history
	n := int(v.scrollPx / float32(v.cell.Y))
	v.scrollPx -= float32(n * v.cell.Y)
	v.scrollLines += n
}

// selection runs from anchor a to head b in reading order, both ends
// included, in grid cells.
type selection struct {
	a, b image.Point
	on   bool
}

func (s selection) ordered() (image.Point, image.Point) {
	if s.b.Y < s.a.Y || s.b.Y == s.a.Y && s.b.X < s.a.X {
		return s.b, s.a
	}
	return s.a, s.b
}

// cols is the selected column range [s0,s1) of row y in a row n wide, or
// (-1,-1) when the row has none.
func (s selection) cols(y, n int) (int, int) {
	if !s.on {
		return -1, -1
	}
	p, q := s.ordered()
	if y < p.Y || y > q.Y {
		return -1, -1
	}
	s0, s1 := 0, n
	if y == p.Y {
		s0 = p.X
	}
	if y == q.Y {
		s1 = q.X + 1
	}
	return min(s0, n), min(s1, n)
}

func selectionText(g *vt.Grid, s selection) string {
	p, q := s.ordered()
	var lines []string
	for y := max(p.Y, 0); y <= min(q.Y, g.Rows-1); y++ {
		s0, s1 := s.cols(y, g.Cols)
		var b strings.Builder
		for x := max(s0, 0); x < s1; x++ {
			c := g.At(x, y)
			switch {
			case c.Width == 0 && c.Content == "":
			case c.Content == "":
				b.WriteByte(' ')
			default:
				b.WriteString(c.Content)
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return strings.Join(lines, "\n")
}

// wordAt is the inclusive column range of the word under (x,y). Separators
// follow Ghostty's defaults minus ':', so URLs and paths select whole.
func wordAt(g *vt.Grid, x, y int) (int, int) {
	word := func(x int) bool {
		c := g.At(x, y).Content
		return c != "" && !strings.ContainsAny(c, " \t'\"`|;,()[]{}<>$│")
	}
	if !word(x) {
		return x, x
	}
	x0, x1 := x, x
	for x0 > 0 && (word(x0-1) || g.At(x0-1, y).Width == 0) {
		x0--
	}
	for x1 < g.Cols-1 && (word(x1+1) || g.At(x1+1, y).Width == 0) {
		x1++
	}
	return x0, x1
}
