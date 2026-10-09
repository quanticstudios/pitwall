package proto

import (
	"reflect"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/vt"
)

func textFrame(rows ...string) Frame {
	g := vt.Grid{Cols: 3, Rows: len(rows), Wrapped: make([]bool, len(rows))}
	for _, r := range rows {
		for x := range 3 {
			g.Cells = append(g.Cells, vt.Cell{Content: r[x : x+1], Width: 1})
		}
	}
	return Frame{Pane: "p", Grid: g}
}

// Diff sends the rows that changed, and Apply brings the old frame to the
// new one without touching the old frame's cells.
func TestDiffApply(t *testing.T) {
	old := textFrame("aaa", "bbb", "ccc", "ddd")
	f := textFrame("aaa", "BBB", "ccc", "ddd")
	f.Grid.Cursor = vt.Cursor{X: 2, Y: 1, Visible: true}
	f.Grid.Title, f.ScrollPushed, f.Modes.BracketedPaste = "t", 9, true
	r, ok := Diff(&old, f).(FrameRows)
	if !ok || !slices.Equal(r.Rows, []int{1}) || len(r.Cells) != 3 {
		t.Fatalf("one changed row: %#v", Diff(&old, f))
	}
	before := slices.Clone(old.Grid.Cells)
	got, ok := r.Apply(old)
	if !ok || !reflect.DeepEqual(got, f) {
		t.Fatalf("applied:\n%#v\nwant\n%#v", got, f)
	}
	if !slices.Equal(old.Grid.Cells, before) {
		t.Fatal("Apply changed the old frame's cells")
	}

	if m := Diff(&f, f); m != nil {
		t.Fatalf("no change sent %#v", m)
	}
	moved := f
	moved.Grid.Cursor.X = 0
	if r, ok := Diff(&f, moved).(FrameRows); !ok || len(r.Rows) != 0 {
		t.Fatalf("a cursor move: %#v", Diff(&f, moved))
	}
	for name, n := range map[string]Frame{
		"no old frame":   {},
		"scrolled":       textFrame("bbb", "ccc", "ddd", "eee"),
		"resized":        textFrame("aaa", "bbb", "ccc"),
		"scroll offset":  func() Frame { s := old; s.ScrollOffset = 1; return s }(),
		"the alt screen": func() Frame { s := old; s.Grid.AltScreen = true; return s }(),
	} {
		base := &old
		if name == "no old frame" {
			base, n = nil, old
		}
		if _, ok := Diff(base, n).(Frame); !ok {
			t.Errorf("%s: want a full frame, got %#v", name, Diff(base, n))
		}
	}
	if _, ok := (FrameRows{Rows: []int{4}, Cells: make([]vt.Cell, 3)}).Apply(old); ok {
		t.Error("applied a row past the grid")
	}
}
