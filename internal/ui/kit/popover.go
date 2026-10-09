package kit

import "image"

// Side is where a popover opens from its anchor.
type Side uint8

const (
	BelowEnd   Side = iota // under the anchor, right edges aligned: a "…" menu
	BelowStart             // under the anchor, left edges aligned
	Above                  // over the anchor, centred on it
	Beside                 // right of the anchor, tops aligned: a submenu, a hover card
)

// Place is the top-left of a popover of size opening on side of anchor,
// gap from it, kept margin inside bounds. One that does not fit below
// flips above, and beside flips to the left; when the flip does not fit
// either, it is clamped to the edge it overflows.
func Place(anchor image.Rectangle, size image.Point, bounds image.Rectangle, side Side, gap, margin int) image.Point {
	lo, hi := bounds.Min.Add(image.Pt(margin, margin)), bounds.Max.Sub(image.Pt(margin, margin))
	var p image.Point
	switch side {
	case BelowEnd, BelowStart:
		p.X = anchor.Min.X
		if side == BelowEnd {
			p.X = anchor.Max.X - size.X
		}
		p.Y = anchor.Max.Y + gap
		if p.Y+size.Y > hi.Y {
			if up := anchor.Min.Y - gap - size.Y; up >= lo.Y {
				p.Y = up
			}
		}
	case Above:
		p.X = anchor.Min.X + (anchor.Dx()-size.X)/2
		p.Y = anchor.Min.Y - gap - size.Y
		if p.Y < lo.Y {
			if down := anchor.Max.Y + gap; down+size.Y <= hi.Y {
				p.Y = down
			}
		}
	case Beside:
		p.X, p.Y = anchor.Max.X+gap, anchor.Min.Y
		if p.X+size.X > hi.X {
			if left := anchor.Min.X - gap - size.X; left >= lo.X {
				p.X = left
			}
		}
	}
	p.X = max(min(p.X, hi.X-size.X), lo.X)
	p.Y = max(min(p.Y, hi.Y-size.Y), lo.Y)
	return p
}
