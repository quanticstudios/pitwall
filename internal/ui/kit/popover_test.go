package kit

import (
	"image"
	"testing"
)

// TestPlace opens a 100x200 popover near each edge of a 1000x800 window.
func TestPlace(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 800)
	size := image.Pt(100, 200)
	btn := func(x, y int) image.Rectangle { return image.Rect(x, y, x+24, y+24) }
	for _, c := range []struct {
		name   string
		anchor image.Rectangle
		side   Side
		want   image.Point
	}{
		{"below, room", btn(500, 100), BelowEnd, image.Pt(424, 128)},
		{"below start", btn(500, 100), BelowStart, image.Pt(500, 128)},
		{"bottom edge flips above", btn(500, 700), BelowEnd, image.Pt(424, 496)},
		{"no room either way clamps", image.Rect(500, 150, 524, 690), BelowEnd, image.Pt(424, 592)},
		{"left edge clamps", btn(10, 100), BelowEnd, image.Pt(8, 128)},
		{"right edge clamps", btn(980, 100), BelowStart, image.Pt(892, 128)},
		{"top edge flips below", btn(500, 50), Above, image.Pt(462, 78)},
		{"beside", btn(500, 100), Beside, image.Pt(528, 100)},
		{"beside the right edge flips left", btn(950, 100), Beside, image.Pt(846, 100)},
		{"beside the bottom edge clamps", btn(500, 750), Beside, image.Pt(528, 592)},
	} {
		got := Place(c.anchor, size, bounds, c.side, 4, 8)
		if got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if r := (image.Rectangle{Min: got, Max: got.Add(size)}); !r.In(bounds) {
			t.Errorf("%s: %v leaves the window", c.name, r)
		}
	}
}
