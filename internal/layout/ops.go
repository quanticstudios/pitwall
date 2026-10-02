package layout

func Leaf(pane string) *Node { panic("unimplemented") }

// Split puts newPane beside target, halving target's space, and returns the
// new root.
func Split(root *Node, target, newPane string, dir Dir) *Node { panic("unimplemented") }

// Remove drops pane, collapses single-child splits, and returns nil when the
// tree is empty.
func Remove(root *Node, pane string) *Node { panic("unimplemented") }

// Rects lays the tree out in area with gap pixels between siblings.
func Rects(root *Node, area Rect, gap int) map[string]Rect { panic("unimplemented") }

// Neighbor is the pane in direction d from pane from, by geometry.
func Neighbor(root *Node, area Rect, from string, d Direction) (string, bool) {
	panic("unimplemented")
}

// Panes lists leaves in reading order.
func Panes(root *Node) []string { panic("unimplemented") }
