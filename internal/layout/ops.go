package layout

import "math"

func Leaf(pane string) *Node { return &Node{Pane: pane} }

// Split puts newPane beside target, halving target's space, and returns the
// new root.
func Split(root *Node, target, newPane string, dir Dir) *Node {
	if root == nil {
		return nil
	}
	if root.Pane != "" {
		if root.Pane != target {
			return root
		}
		return &Node{Dir: dir, Ratios: []float64{.5, .5}, Children: []*Node{root, Leaf(newPane)}}
	}
	for i, child := range root.Children {
		replacement := Split(child, target, newPane, dir)
		if replacement != child {
			next := *root
			next.Children = append([]*Node(nil), root.Children...)
			next.Children[i] = replacement
			next.Ratios = ratios(root)
			return &next
		}
	}
	return root
}

// Remove drops pane, collapses single-child splits, and returns nil when the
// tree is empty.
func Remove(root *Node, pane string) *Node {
	if root == nil {
		return nil
	}
	if root.Pane != "" {
		if root.Pane == pane {
			return nil
		}
		return root
	}
	children := make([]*Node, 0, len(root.Children))
	weights := make([]float64, 0, len(root.Children))
	original := ratios(root)
	changed := false
	for i, child := range root.Children {
		next := Remove(child, pane)
		changed = changed || next != child
		if next != nil {
			children = append(children, next)
			weights = append(weights, original[i])
		}
	}
	if !changed {
		return root
	}
	if len(children) == 0 {
		return nil
	}
	if len(children) == 1 {
		return children[0]
	}
	next := &Node{Dir: root.Dir, Children: children, Ratios: weights}
	next.Ratios = ratios(next)
	return next
}

// ratios accepts persisted weights as well as normalized ratios.
func ratios(n *Node) []float64 {
	result := make([]float64, len(n.Children))
	total := 0.0
	valid := len(n.Ratios) == len(result)
	if valid {
		for _, r := range n.Ratios {
			if r <= 0 || math.IsNaN(r) || math.IsInf(r, 0) {
				valid = false
				break
			}
			total += r
		}
		valid = valid && !math.IsInf(total, 0)
	}
	sum := 0.0
	for i := range result {
		if valid {
			result[i] = n.Ratios[i] / total
		} else {
			result[i] = 1 / float64(len(result))
		}
		if i == len(result)-1 {
			result[i] = 1 - sum
		}
		sum += result[i]
	}
	return result
}

// Rects lays the tree out in area with gap pixels between siblings.
func Rects(root *Node, area Rect, gap int) map[string]Rect {
	result := make(map[string]Rect)
	var walk func(*Node, Rect)
	walk = func(n *Node, r Rect) {
		if n == nil {
			return
		}
		if n.Pane != "" {
			result[n.Pane] = r
			return
		}
		count := len(n.Children)
		if count == 0 {
			return
		}
		length := r.W
		if n.Dir == Vertical {
			length = r.H
		}
		spacing := max(0, gap)
		if count > 1 {
			spacing = min(spacing, length/(count-1))
		}
		available := length - spacing*(count-1)
		weights := ratios(n)
		cursor, remaining := 0, available
		for i, child := range n.Children {
			size := min(remaining, int(float64(available)*weights[i]))
			if i == count-1 {
				size = remaining
			}
			bounds := r
			if n.Dir == Horizontal {
				bounds.X += cursor
				bounds.W = size
			} else {
				bounds.Y += cursor
				bounds.H = size
			}
			walk(child, bounds)
			cursor += size + spacing
			remaining -= size
		}
	}
	area.W, area.H = max(0, area.W), max(0, area.H)
	walk(root, area)
	return result
}

// Neighbor is the pane in direction d from pane from, by geometry.
func Neighbor(root *Node, area Rect, from string, d Direction) (string, bool) {
	if d < Left || d > Down {
		return "", false
	}
	rects := Rects(root, area, 0)
	active, ok := rects[from]
	if !ok {
		return "", false
	}
	// Rank primary gaps, then overlap and center distance, as aide does.
	// Transpose vertical navigation so both axes use the same comparison.
	project := func(r Rect) Rect {
		if d == Up || d == Down {
			return Rect{r.Y, r.X, r.H, r.W}
		}
		return r
	}
	active, bounds := project(active), project(area)
	panes := Panes(root)
	for _, wrap := range []bool{false, true} {
		best := ""
		bestGap, bestOverlap, bestCenter := 0, 0, 0
		for _, pane := range panes {
			if pane == from {
				continue
			}
			candidate := project(rects[pane])
			gap := candidate.X - (active.X + active.W)
			if d == Left || d == Up {
				gap = active.X - (candidate.X + candidate.W)
			}
			if (gap < 0) != wrap {
				continue
			}
			if wrap {
				gap += bounds.W
			}
			overlap := max(0, min(active.Y+active.H, candidate.Y+candidate.H)-max(active.Y, candidate.Y))
			center := abs(2*active.Y + active.H - 2*candidate.Y - candidate.H)
			if best == "" || gap < bestGap || gap == bestGap && (overlap > bestOverlap || overlap == bestOverlap && center < bestCenter) {
				best, bestGap, bestOverlap, bestCenter = pane, gap, overlap, center
			}
		}
		if best != "" {
			return best, true
		}
	}
	return "", false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Panes lists leaves in reading order.
func Panes(root *Node) []string {
	var result []string
	var walk func(*Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		if n.Pane != "" {
			result = append(result, n.Pane)
			return
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)
	return result
}
