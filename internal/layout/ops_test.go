package layout

import (
	"math"
	"reflect"
	"testing"
)

func splitNode(dir Dir, weights []float64, children ...*Node) *Node {
	return &Node{Dir: dir, Ratios: weights, Children: children}
}

func checkRatios(t *testing.T, n *Node) {
	t.Helper()
	if n == nil || n.Pane != "" {
		return
	}
	sum := 0.0
	for _, r := range n.Ratios {
		if r <= 0 {
			t.Fatalf("nonpositive ratio %v", n.Ratios)
		}
		sum += r
	}
	if len(n.Ratios) != len(n.Children) || math.Abs(sum-1) > 1e-14 {
		t.Fatalf("invalid ratios %v", n.Ratios)
	}
	for _, c := range n.Children {
		checkRatios(t, c)
	}
}

func TestSplit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		root   *Node
		target string
		dir    Dir
		want   *Node
	}{
		{"empty", nil, "a", Horizontal, nil},
		{"leaf", Leaf("a"), "a", Horizontal, splitNode(Horizontal, []float64{.5, .5}, Leaf("a"), Leaf("new"))},
		{"missing", Leaf("a"), "x", Vertical, Leaf("a")},
		{"same direction nests like aide", splitNode(Horizontal, []float64{.25, .75}, Leaf("a"), Leaf("b")), "b", Horizontal, splitNode(Horizontal, []float64{.25, .75}, Leaf("a"), splitNode(Horizontal, []float64{.5, .5}, Leaf("b"), Leaf("new")))},
		{"deep", splitNode(Horizontal, []float64{.5, .5}, Leaf("a"), splitNode(Vertical, []float64{.5, .5}, Leaf("b"), Leaf("c"))), "c", Horizontal, splitNode(Horizontal, []float64{.5, .5}, Leaf("a"), splitNode(Vertical, []float64{.5, .5}, Leaf("b"), splitNode(Horizontal, []float64{.5, .5}, Leaf("c"), Leaf("new"))))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := Rects(tc.root, Rect{W: 100, H: 100}, 0)
			got := Split(tc.root, tc.target, "new", tc.dir)
			if !sameTree(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			checkRatios(t, got)
			if !reflect.DeepEqual(Rects(tc.root, Rect{W: 100, H: 100}, 0), before) {
				t.Fatal("Split mutated input")
			}
		})
	}
}

func TestRemove(t *testing.T) {
	deep := splitNode(Horizontal, []float64{.4, .6}, Leaf("a"), splitNode(Vertical, []float64{.5, .5}, Leaf("b"), Leaf("c")))
	for _, tc := range []struct {
		name string
		root *Node
		pane string
		want *Node
	}{
		{"empty", nil, "a", nil}, {"last", Leaf("a"), "a", nil}, {"missing", deep, "x", deep},
		{"collapse root", splitNode(Horizontal, []float64{.5, .5}, Leaf("a"), Leaf("b")), "a", Leaf("b")},
		{"collapse deep", deep, "b", splitNode(Horizontal, []float64{.4, .6}, Leaf("a"), Leaf("c"))},
		{"redistribute proportionally", splitNode(Horizontal, []float64{.2, .3, .5}, Leaf("a"), Leaf("b"), Leaf("c")), "a", splitNode(Horizontal, []float64{.375, .625}, Leaf("b"), Leaf("c"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := Rects(tc.root, Rect{W: 100, H: 100}, 0)
			got := Remove(tc.root, tc.pane)
			if !sameTree(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			checkRatios(t, got)
			if !reflect.DeepEqual(Rects(tc.root, Rect{W: 100, H: 100}, 0), before) {
				t.Fatal("Remove mutated input")
			}
		})
	}
}

func TestRects(t *testing.T) {
	for _, tc := range []struct {
		name string
		root *Node
		area Rect
		gap  int
		want map[string]Rect
	}{
		{"empty", nil, Rect{W: 10, H: 10}, 1, map[string]Rect{}},
		{"leaf", Leaf("a"), Rect{2, 3, 11, 13}, 2, map[string]Rect{"a": {2, 3, 11, 13}}},
		{"remainder", splitNode(Horizontal, []float64{.2, .3, .5}, Leaf("a"), Leaf("b"), Leaf("c")), Rect{2, 3, 17, 8}, 2, map[string]Rect{"a": {2, 3, 2, 8}, "b": {6, 3, 3, 8}, "c": {11, 3, 8, 8}}},
		{"deep gaps", splitNode(Horizontal, []float64{.5, .5}, Leaf("a"), splitNode(Vertical, []float64{.5, .5}, Leaf("b"), Leaf("c"))), Rect{2, 3, 11, 11}, 2, map[string]Rect{"a": {2, 3, 4, 11}, "b": {8, 3, 5, 4}, "c": {8, 9, 5, 5}}},
		{"tiny", splitNode(Horizontal, []float64{.5, .5}, Leaf("a"), Leaf("b")), Rect{W: 1, H: 2}, 5, map[string]Rect{"a": {0, 0, 0, 2}, "b": {1, 0, 0, 2}}},
		{"missing ratios", splitNode(Vertical, nil, Leaf("a"), Leaf("b")), Rect{W: 3, H: 5}, 0, map[string]Rect{"a": {0, 0, 3, 2}, "b": {0, 2, 3, 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Rects(tc.root, tc.area, tc.gap); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNeighbor(t *testing.T) {
	root := splitNode(Horizontal, []float64{.5, .5}, splitNode(Vertical, []float64{.25, .75}, Leaf("a"), Leaf("b")), splitNode(Vertical, []float64{.5, .5}, Leaf("c"), Leaf("d")))
	for _, tc := range []struct {
		from string
		dir  Direction
		want string
		ok   bool
	}{
		{"a", Right, "c", true}, {"b", Right, "d", true}, {"c", Left, "a", true}, {"d", Left, "b", true},
		{"a", Down, "b", true}, {"b", Up, "a", true}, {"c", Down, "d", true}, {"d", Up, "c", true},
		{"a", Left, "c", true}, {"d", Right, "b", true}, {"missing", Right, "", false}, {"a", Direction(99), "", false},
	} {
		t.Run(tc.from+string(rune('0'+tc.dir)), func(t *testing.T) {
			got, ok := Neighbor(root, Rect{5, 7, 100, 100}, tc.from, tc.dir)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %q %v, want %q %v", got, ok, tc.want, tc.ok)
			}
		})
	}
	if _, ok := Neighbor(Leaf("a"), Rect{W: 1, H: 1}, "a", Right); ok {
		t.Fatal("single pane has neighbor")
	}
}

func TestPanes(t *testing.T) {
	for _, tc := range []struct {
		root *Node
		want []string
	}{
		{nil, nil}, {Leaf("a"), []string{"a"}},
		{splitNode(Horizontal, []float64{.5, .5}, splitNode(Vertical, []float64{.5, .5}, Leaf("a"), Leaf("b")), Leaf("c")), []string{"a", "b", "c"}},
	} {
		if got := Panes(tc.root); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("got %v, want %v", got, tc.want)
		}
	}
}

func sameTree(a, b *Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Pane != b.Pane || a.Dir != b.Dir || len(a.Ratios) != len(b.Ratios) || len(a.Children) != len(b.Children) {
		return false
	}
	for i, r := range a.Ratios {
		if math.Abs(r-b.Ratios[i]) > 1e-14 {
			return false
		}
	}
	for i, child := range a.Children {
		if !sameTree(child, b.Children[i]) {
			return false
		}
	}
	return true
}
