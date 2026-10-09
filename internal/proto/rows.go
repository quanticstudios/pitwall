package proto

import "slices"

// Diff is what brings a client that holds old, nil for none, to f: f
// itself when old differs in size, screen, scroll offset or most rows (as
// after output scrolled the screen), a FrameRows of the rows that changed
// otherwise, and nil when nothing did.
func Diff(old *Frame, f Frame) any {
	g := f.Grid
	if old == nil || old.Grid.Cols != g.Cols || old.Grid.Rows != g.Rows || old.Grid.AltScreen != g.AltScreen ||
		old.ScrollOffset != f.ScrollOffset || len(old.Grid.Cells) != len(g.Cells) || len(g.Cells) != g.Cols*g.Rows {
		return f
	}
	r := FrameRows{Pane: f.Pane, Wrapped: g.Wrapped, Cursor: g.Cursor, Title: g.Title, Modes: f.Modes, ScrollMax: f.ScrollMax, ScrollPushed: f.ScrollPushed}
	for y := range g.Rows {
		row := g.Cells[y*g.Cols : (y+1)*g.Cols]
		if !slices.Equal(row, old.Grid.Cells[y*g.Cols:(y+1)*g.Cols]) {
			r.Rows = append(r.Rows, y)
			r.Cells = append(r.Cells, row...)
		}
	}
	if 2*len(r.Rows) > g.Rows {
		return f // as small, and the client copies nothing
	}
	if len(r.Rows) == 0 && slices.Equal(g.Wrapped, old.Grid.Wrapped) && g.Cursor == old.Grid.Cursor && g.Title == old.Grid.Title &&
		f.Modes == old.Modes && f.ScrollMax == old.ScrollMax && f.ScrollPushed == old.ScrollPushed {
		return nil
	}
	return r
}

// Apply returns f with r's rows and fields, and false, with f unchanged,
// when r does not fit f's Grid. f's Cells are copied, not changed.
func (r FrameRows) Apply(f Frame) (Frame, bool) {
	g := &f.Grid
	if len(r.Cells) != len(r.Rows)*g.Cols || len(g.Cells) != g.Cols*g.Rows {
		return f, false
	}
	for _, y := range r.Rows {
		if y < 0 || y >= g.Rows {
			return f, false
		}
	}
	g.Cells = slices.Clone(g.Cells)
	for i, y := range r.Rows {
		copy(g.Cells[y*g.Cols:(y+1)*g.Cols], r.Cells[i*g.Cols:(i+1)*g.Cols])
	}
	g.Wrapped, g.Cursor, g.Title = r.Wrapped, r.Cursor, r.Title
	f.Modes, f.ScrollMax, f.ScrollPushed = r.Modes, r.ScrollMax, r.ScrollPushed
	return f, true
}
