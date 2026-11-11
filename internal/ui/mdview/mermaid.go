package mdview

import (
	"image"
	"math"
	"regexp"
	"slices"
	"strings"
)

// Shape is a flowchart node's outline.
type Shape uint8

const (
	Box     Shape = iota // A[x] and the shapes drawn as it
	Round                // A(x)
	Stadium              // A([x])
	Diamond              // A{x}
	Circle               // A((x))
)

// Node is a flowchart node.
type Node struct {
	ID, Label string
	Shape     Shape
}

// Edge is an arrow between two nodes, by index.
type Edge struct {
	From, To int
	Label    string
	Head     bool // an arrowhead at To
	Dotted   bool
	Thick    bool
}

// Graph is a parsed mermaid flowchart.
type Graph struct {
	Dir   string // TD, BT, LR or RL
	Nodes []Node
	Edges []Edge
	ids   map[string]int
}

// MaxNodes caps how many nodes a flowchart may have before it shows as
// code, so a generated chart cannot stall the window: layering passes
// over every edge once per layer.
const MaxNodes = 200

// ParseMermaid parses a ```mermaid block. A flowchart (graph or
// flowchart) gives its Graph; anything else gives a note on why it shows
// as code.
func ParseMermaid(src string) (*Graph, string) {
	var lines []string
	for l := range strings.SplitSeq(src, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "%%") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) > 0 && lines[0] == "---" { // front matter
		for i := 1; i < len(lines); i++ {
			if lines[i] == "---" {
				lines = lines[i+1:]
				break
			}
		}
	}
	if len(lines) == 0 {
		return nil, "Empty mermaid block"
	}
	head := strings.Fields(strings.TrimSuffix(lines[0], ";"))
	if head[0] != "graph" && head[0] != "flowchart" {
		return nil, "Mermaid " + head[0] + " is not drawn, only flowcharts"
	}
	g := &Graph{Dir: "TD", ids: map[string]int{}}
	if len(head) > 1 {
		switch d := strings.ToUpper(head[1]); d {
		case "TB", "TD":
		case "BT", "LR", "RL":
			g.Dir = d
		}
	}
	for _, l := range lines[1:] {
		for st := range strings.SplitSeq(l, ";") {
			if err := g.statement(strings.TrimSpace(st)); err != "" {
				return nil, "Flowchart not drawn: " + err
			}
		}
	}
	if len(g.Nodes) == 0 {
		return nil, "Flowchart not drawn: no nodes"
	}
	if len(g.Nodes) > MaxNodes {
		return nil, "Flowchart not drawn: more than 200 nodes"
	}
	return g, ""
}

var skipWords = []string{"classDef", "class", "style", "linkStyle", "click", "subgraph", "end", "direction", "accTitle", "accDescr"}

var (
	nodeID = regexp.MustCompile(`^[\p{L}\p{N}_]+`)
	// arrow is a link without text in it, with an optional |label|:
	// -->, ---, -.->, ==>, <-->, --o, --x.
	arrow = regexp.MustCompile(`^(<|o|x)?(-{2,}|={2,}|-\.+-|-\.)(>|o|x)?\s*(?:\|([^|]*)\|)?`)
	// textArrow is a link with its text inside: -- yes -->, == no ==>,
	// -. maybe .->.
	textArrow = regexp.MustCompile(`^(--|==|-\.)\s+(.+?)\s+(-{2,}>|-{3,}|={2,}>|={3,}|\.-+>|\.-+)`)
)

// statement adds one line's nodes and edges; it says what it could not
// read.
func (g *Graph) statement(s string) string {
	if s == "" {
		return ""
	}
	if w, _, _ := strings.Cut(s, " "); slices.Contains(skipWords, w) || strings.HasPrefix(s, "style ") {
		return ""
	}
	prev, rest, err := g.nodes(s)
	if err != "" {
		return err
	}
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return ""
		}
		var e Edge
		if m := textArrow.FindStringSubmatch(rest); m != nil {
			e = Edge{Label: m[2], Head: strings.HasSuffix(m[3], ">"), Dotted: m[1] == "-.", Thick: m[1] == "=="}
			rest = rest[len(m[0]):]
		} else if m := arrow.FindStringSubmatch(rest); m != nil {
			e = Edge{Label: strings.TrimSpace(m[4]), Head: m[3] != "", Dotted: strings.Contains(m[2], "."), Thick: m[2][0] == '='}
			rest = rest[len(m[0]):]
		} else {
			return "cannot read " + quote(rest)
		}
		next, r, err := g.nodes(strings.TrimSpace(rest))
		if err != "" {
			return err
		}
		for _, a := range prev {
			for _, b := range next {
				e.From, e.To = a, b
				g.Edges = append(g.Edges, e)
			}
		}
		prev, rest = next, r
	}
}

// nodes reads "A[x] & B" and returns the nodes' indexes and the rest.
func (g *Graph) nodes(s string) ([]int, string, string) {
	var out []int
	for {
		i, rest, err := g.node(s)
		if err != "" {
			return nil, "", err
		}
		out = append(out, i)
		t := strings.TrimSpace(rest)
		if !strings.HasPrefix(t, "&") {
			return out, rest, ""
		}
		s = strings.TrimSpace(t[1:])
	}
}

// shapes pairs each opening bracket with its closing one and shape,
// longest first.
var shapes = []struct {
	open, close string
	shape       Shape
}{
	{"((", "))", Circle}, {"([", "])", Stadium}, {"[[", "]]", Box}, {"[(", ")]", Round},
	{"{{", "}}", Box}, {"[/", "/]", Box}, {"[\\", "\\]", Box}, {"[/", "\\]", Box}, {"[\\", "/]", Box},
	{"[", "]", Box}, {"(", ")", Round}, {"{", "}", Diamond}, {">", "]", Box},
}

// node reads one node, defining it the first time it has a shape.
func (g *Graph) node(s string) (int, string, string) {
	id := nodeID.FindString(s)
	if id == "" {
		return 0, "", "cannot read " + quote(s)
	}
	rest := s[len(id):]
	label, shape, has := id, Box, false
	for _, sh := range shapes {
		if !strings.HasPrefix(rest, sh.open) {
			continue
		}
		body := rest[len(sh.open):]
		var end int
		if strings.HasPrefix(body, `"`) {
			q := strings.IndexByte(body[1:], '"')
			if q < 0 || !strings.HasPrefix(body[q+2:], sh.close) {
				continue
			}
			label, end = body[1:q+1], q+2
		} else {
			end = strings.Index(body, sh.close)
			if end < 0 {
				continue
			}
			label = body[:end]
		}
		rest, shape, has = body[end+len(sh.close):], sh.shape, true
		break
	}
	if r, ok := strings.CutPrefix(rest, ":::"); ok { // a class name
		rest = r[len(className.FindString(r)):]
	}
	label = brTag2.ReplaceAllString(strings.TrimSpace(label), "\n")
	if i, ok := g.ids[id]; ok {
		if has {
			g.Nodes[i].Label, g.Nodes[i].Shape = label, shape
		}
		return i, rest, ""
	}
	g.ids[id] = len(g.Nodes)
	g.Nodes = append(g.Nodes, Node{ID: id, Label: label, Shape: shape})
	return len(g.Nodes) - 1, rest, ""
}

var (
	brTag2    = regexp.MustCompile(`(?i)<br\s*/?>`)
	className = regexp.MustCompile(`^[\w-]+`)
)

func quote(s string) string {
	if len(s) > 30 {
		s = s[:30] + "…"
	}
	return `"` + s + `"`
}

// Placed is a laid out flowchart: each node's box, and each edge's line
// through the points between its layers, both in pixels from the top
// left.
type Placed struct {
	Size  image.Point
	Nodes []image.Rectangle
	Lines [][]image.Point // Lines[i] is Edges[i]'s, From to To
}

// Layout places g's nodes, sized as sizes says, in layers along g.Dir:
// the longest path from a node without edges into it picks its layer,
// a few barycenter sweeps order each layer, and an edge that skips
// layers bends through a point in each one it crosses. gap is the space
// between nodes in a layer, rank the space between layers.
func (g *Graph) Layout(sizes []image.Point, gap, rank int) Placed {
	n := len(g.Nodes)
	across := g.Dir == "LR" || g.Dir == "RL"
	sz := func(i int) image.Point { // along the layer, between layers
		if i >= n {
			return image.Point{}
		}
		if across {
			return image.Pt(sizes[i].Y, sizes[i].X)
		}
		return sizes[i]
	}

	// Break cycles: an edge back to a node on the walk's stack counts
	// as reversed for layering.
	out := make([][]int, n)
	for i, e := range g.Edges {
		out[e.From] = append(out[e.From], i)
	}
	back := make([]bool, len(g.Edges))
	state := make([]uint8, n) // 0 new, 1 on stack, 2 done
	var walk func(int)
	walk = func(v int) {
		state[v] = 1
		for _, ei := range out[v] {
			w := g.Edges[ei].To
			switch state[w] {
			case 0:
				walk(w)
			case 1:
				back[ei] = true
			}
		}
		state[v] = 2
	}
	for v := range n {
		if state[v] == 0 {
			walk(v)
		}
	}
	type dag struct{ from, to int }
	fwd := make([]dag, len(g.Edges))
	for i, e := range g.Edges {
		fwd[i] = dag{e.From, e.To}
		if back[i] {
			fwd[i] = dag{e.To, e.From}
		}
	}

	// Longest path layering, relaxed until nothing moves.
	layer := make([]int, n)
	for changed := true; changed; {
		changed = false
		for _, e := range fwd {
			if e.from != e.to && layer[e.to] < layer[e.from]+1 {
				layer[e.to] = layer[e.from] + 1
				changed = true
			}
		}
	}

	// Dummy points for edges that skip layers.
	chains := make([][]int, len(g.Edges)) // each edge's points, dag order
	total := n
	for i, e := range fwd {
		chains[i] = []int{e.from}
		if e.from == e.to {
			chains[i] = append(chains[i], e.to)
			continue
		}
		for l := layer[e.from] + 1; l < layer[e.to]; l++ {
			layer = append(layer, l)
			chains[i] = append(chains[i], total)
			total++
		}
		chains[i] = append(chains[i], e.to)
	}
	depth := 0
	for _, l := range layer {
		depth = max(depth, l+1)
	}
	layers := make([][]int, depth)
	for v := range total {
		layers[layer[v]] = append(layers[layer[v]], v)
	}
	ups := make([][]int, total)
	downs := make([][]int, total)
	for _, c := range chains {
		for k := 1; k < len(c); k++ {
			if c[k-1] != c[k] {
				downs[c[k-1]] = append(downs[c[k-1]], c[k])
				ups[c[k]] = append(ups[c[k]], c[k-1])
			}
		}
	}
	pos := make([]float64, total)
	reindex := func(l []int) {
		for i, v := range l {
			pos[v] = float64(i)
		}
	}
	for _, l := range layers {
		reindex(l)
	}
	sweep := func(l []int, nb [][]int) {
		bary := make(map[int]float64, len(l))
		for _, v := range l {
			if len(nb[v]) == 0 {
				bary[v] = pos[v]
				continue
			}
			s := 0.0
			for _, u := range nb[v] {
				s += pos[u]
			}
			bary[v] = s / float64(len(nb[v]))
		}
		slices.SortStableFunc(l, func(a, b int) int {
			switch {
			case bary[a] < bary[b]:
				return -1
			case bary[a] > bary[b]:
				return 1
			}
			return 0
		})
		reindex(l)
	}
	for range 4 {
		for i := 1; i < depth; i++ {
			sweep(layers[i], ups)
		}
		for i := depth - 2; i >= 0; i-- {
			sweep(layers[i], downs)
		}
	}

	// Coordinates: layers stacked along the flow.
	dummy := gap / 2
	width := func(v int) int {
		if v >= n {
			return dummy
		}
		return sz(v).X
	}
	along := make([]int, depth) // each layer's top
	thick := make([]int, depth)
	wide := 0
	for i, l := range layers {
		w := 0
		for k, v := range l {
			if k > 0 {
				w += gap
			}
			w += width(v)
			thick[i] = max(thick[i], sz(v).Y)
		}
		wide = max(wide, w)
		if i > 0 {
			along[i] = along[i-1] + thick[i-1] + rank
		}
	}
	tall := 0
	if depth > 0 {
		tall = along[depth-1] + thick[depth-1]
	}
	// Each layer starts centered, then a few passes pull every point
	// toward the middle of its neighbors in the layers either side,
	// keeping the layer's order and gaps, so a child sits under its
	// parent rather than under the middle of the chart.
	xs := make([]float64, total) // centers across
	for _, l := range layers {
		w := 0
		for k, v := range l {
			if k > 0 {
				w += gap
			}
			w += width(v)
		}
		x := float64(wide-w) / 2
		for _, v := range l {
			xs[v] = x + float64(width(v))/2
			x += float64(width(v) + gap)
		}
	}
	place := func(l []int, nb [][]int) {
		want := make([]float64, len(l))
		for k, v := range l {
			want[k] = xs[v]
			if len(nb[v]) > 0 {
				s := 0.0
				for _, u := range nb[v] {
					s += xs[u]
				}
				want[k] = s / float64(len(nb[v]))
			}
		}
		// Left to right and right to left, each node as near its wish as
		// its neighbors in the layer let it be; the mean of the two keeps
		// the gaps and leans neither way.
		ltr, rtl := make([]float64, len(l)), make([]float64, len(l))
		for k, v := range l {
			ltr[k] = want[k]
			if k > 0 {
				ltr[k] = max(ltr[k], ltr[k-1]+float64(width(l[k-1])+width(v))/2+float64(gap))
			}
		}
		for k := len(l) - 1; k >= 0; k-- {
			rtl[k] = want[k]
			if k < len(l)-1 {
				rtl[k] = min(rtl[k], rtl[k+1]-float64(width(l[k+1])+width(l[k]))/2-float64(gap))
			}
		}
		for k, v := range l {
			xs[v] = (ltr[k] + rtl[k]) / 2
		}
	}
	for range 4 {
		for i := 1; i < depth; i++ {
			place(layers[i], ups)
		}
		for i := depth - 2; i >= 0; i-- {
			place(layers[i], downs)
		}
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for v := range total {
		lo = min(lo, xs[v]-float64(width(v))/2)
		hi = max(hi, xs[v]+float64(width(v))/2)
	}
	if total > 0 {
		wide = int(math.Ceil(hi - lo))
	}
	center := make([]image.Point, total) // across, along
	for i, l := range layers {
		for _, v := range l {
			center[v] = image.Pt(int(math.Round(xs[v]-lo)), along[i]+thick[i]/2)
		}
	}
	flip := func(p image.Point) image.Point {
		if across {
			p = image.Pt(p.Y, p.X)
		}
		switch g.Dir {
		case "BT":
			p.Y = tall - p.Y
		case "RL":
			p.X = tall - p.X
		}
		return p
	}
	pl := Placed{Size: image.Pt(wide, tall), Nodes: make([]image.Rectangle, n), Lines: make([][]image.Point, len(g.Edges))}
	if across {
		pl.Size = image.Pt(tall, wide)
	}
	for v := range n {
		c := flip(center[v])
		h := sizes[v].Div(2)
		pl.Nodes[v] = image.Rectangle{Min: c.Sub(h), Max: c.Sub(h).Add(sizes[v])}
	}
	for i, c := range chains {
		pts := make([]image.Point, len(c))
		for k, v := range c {
			pts[k] = flip(center[v])
		}
		if back[i] {
			slices.Reverse(pts)
		}
		// End on the boxes' edges, not their centers.
		if len(pts) >= 2 && g.Edges[i].From != g.Edges[i].To {
			pts[0] = edgePoint(pl.Nodes[g.Edges[i].From], pts[1], g.Nodes[g.Edges[i].From].Shape)
			pts[len(pts)-1] = edgePoint(pl.Nodes[g.Edges[i].To], pts[len(pts)-2], g.Nodes[g.Edges[i].To].Shape)
		}
		pl.Lines[i] = pts
	}
	return pl
}

// edgePoint is where the line from r's center toward p leaves r's
// outline.
func edgePoint(r image.Rectangle, p image.Point, s Shape) image.Point {
	c := r.Min.Add(r.Max).Div(2)
	dx, dy := float64(p.X-c.X), float64(p.Y-c.Y)
	if dx == 0 && dy == 0 {
		return c
	}
	hw, hh := float64(r.Dx())/2, float64(r.Dy())/2
	var t float64
	switch s {
	case Diamond:
		t = 1 / (math.Abs(dx)/hw + math.Abs(dy)/hh)
	case Circle:
		t = 1 / math.Sqrt(dx*dx/(hw*hw)+dy*dy/(hh*hh))
	default:
		t = min(hw/max(math.Abs(dx), 1e-9), hh/max(math.Abs(dy), 1e-9))
	}
	return image.Pt(c.X+int(dx*t), c.Y+int(dy*t))
}
