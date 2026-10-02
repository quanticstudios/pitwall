// Package layout is the split tree of panes inside one workspace. Ops (split,
// close, neighbor navigation, rects) live beside these types.
package layout

type Dir int

const (
	Horizontal Dir = iota // children side by side, left to right
	Vertical              // children stacked, top to bottom
)

// Node is a leaf when Pane != "", otherwise a split with two or more children.
type Node struct {
	Pane     string
	Dir      Dir
	Ratios   []float64 // one per child, sums to 1
	Children []*Node
}

type Rect struct{ X, Y, W, H int }

type Direction int

const (
	Left Direction = iota
	Right
	Up
	Down
)
