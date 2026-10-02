package app

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/font/gofont"
	gl "gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/term"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// placeholders swaps the theme, sidebar, term and layout packages for flat
// stand-ins while those packages are stubs. Integration sets it to false and
// deletes the ph* helpers below.
const placeholders = true

// Each seam below is the one call into a parallel package.

func newTheme() *theme.Theme {
	if !placeholders {
		return theme.Dark()
	}
	hex := func(v uint32) color.NRGBA { return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff} }
	return &theme.Theme{
		Shaper: text.NewShaper(text.NoSystemFonts(), text.WithCollection(gofont.Collection())),
		Bg:     hex(0x07080a), Sidebar: hex(0x07080a), Surface: hex(0x0d0d0d),
		SurfaceSecondary: hex(0x101111), SurfaceElevated: hex(0x121212),
		Border: hex(0x242728), Fg: hex(0xf4f4f6), Muted: hex(0x9c9c9d), Primary: hex(0x0066cc),
		Red: hex(0xff6161), Yellow: hex(0xffc533), Green: hex(0x59d499), Blue: hex(0x57c1ff), Purple: hex(0xbd93ff),
		TermFg: hex(0xcdcdcd), TermBg: hex(0x07080a), TermCur: hex(0xf4f4f6),
		UIFont: font.Font{Typeface: "Go"}, MonoFont: font.Font{Typeface: "Go Mono"},
		TextSize: 13, SmallSize: 11, MonoSize: 13,
	}
}

func drawSidebar(gtx gl.Context, s *sidebar.Sidebar, th *theme.Theme, st *model.State, active string) []sidebar.Event {
	if !placeholders {
		_, evs := s.Layout(gtx, th, st, active)
		return evs
	}
	paint.FillShape(gtx.Ops, th.Sidebar, clip.Rect{Max: gtx.Constraints.Max}.Op())
	y := gtx.Dp(12)
	project := ""
	for _, w := range ordered(st) {
		if w.ProjectID != project {
			project = w.ProjectID
			y += gtx.Dp(8)
			y += drawText(gtx, th, image.Pt(gtx.Dp(12), y), th.UIFont, th.SmallSize, th.Muted, strings.ToUpper(projectName(st, project)))
		}
		c := th.Muted
		if w.ID == active {
			c = th.Fg
			paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.Rect{Min: image.Pt(gtx.Dp(6), y), Max: image.Pt(gtx.Constraints.Max.X-gtx.Dp(6), y+gtx.Dp(24))}.Op())
		}
		y += drawText(gtx, th, image.Pt(gtx.Dp(16), y+gtx.Dp(4)), th.UIFont, th.TextSize, c, w.Name) + gtx.Dp(8)
	}
	return nil
}

func drawTerm(gtx gl.Context, v *term.View, th *theme.Theme, g *vt.Grid, m vt.Modes, focused bool) (input []byte, cols, rows int) {
	if !placeholders {
		_, input, cols, rows = v.Layout(gtx, th, g, m, focused)
		return input, cols, rows
	}
	sz := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, th.TermBg, clip.Rect{Max: sz}.Op())
	cw, ch := gtx.Sp(th.MonoSize)*6/10, gtx.Sp(th.MonoSize)*14/10
	for y := 0; y < g.Rows && (y+1)*ch <= sz.Y; y++ {
		var b strings.Builder
		for x := 0; x < g.Cols; x++ {
			b.WriteString(g.At(x, y).Content)
		}
		if line := strings.TrimRight(b.String(), " "); line != "" {
			drawText(gtx, th, image.Pt(gtx.Dp(4), y*ch), th.MonoFont, th.MonoSize, th.TermFg, line)
		}
	}
	return nil, max(1, sz.X/cw), max(1, sz.Y/ch)
}

func panesOf(root *layout.Node) []string {
	if root == nil {
		return nil
	}
	if !placeholders {
		return layout.Panes(root)
	}
	if root.Pane != "" {
		return []string{root.Pane}
	}
	var out []string
	for _, c := range root.Children {
		out = append(out, panesOf(c)...)
	}
	return out
}

func rectsOf(root *layout.Node, area layout.Rect, gap int) map[string]layout.Rect {
	if !placeholders {
		return layout.Rects(root, area, gap)
	}
	out := map[string]layout.Rect{}
	walkSplits(root, area, gap, nil, func(n *layout.Node, r layout.Rect, _ []int) {
		if n.Pane != "" {
			out[n.Pane] = r
		}
	})
	return out
}

func splitTree(root *layout.Node, target, pane string, dir layout.Dir) *layout.Node {
	if !placeholders {
		return layout.Split(root, target, pane, dir)
	}
	if root == nil {
		return &layout.Node{Pane: pane}
	}
	root = cloneNode(root)
	var walk func(n *layout.Node) *layout.Node
	walk = func(n *layout.Node) *layout.Node {
		if n.Pane == target {
			return &layout.Node{Dir: dir, Ratios: []float64{0.5, 0.5}, Children: []*layout.Node{n, {Pane: pane}}}
		}
		for i, c := range n.Children {
			n.Children[i] = walk(c)
		}
		return n
	}
	return walk(root)
}

func removeTree(root *layout.Node, pane string) *layout.Node {
	if !placeholders {
		return layout.Remove(root, pane)
	}
	if root == nil || root.Pane == pane {
		return nil
	}
	if root.Pane != "" {
		return root
	}
	n := &layout.Node{Dir: root.Dir}
	sum := 0.0
	for i, c := range root.Children {
		if c = removeTree(c, pane); c != nil {
			n.Children = append(n.Children, c)
			n.Ratios = append(n.Ratios, root.Ratios[i])
			sum += root.Ratios[i]
		}
	}
	switch len(n.Children) {
	case 0:
		return nil
	case 1:
		return n.Children[0]
	}
	for i := range n.Ratios {
		n.Ratios[i] /= sum
	}
	return n
}
