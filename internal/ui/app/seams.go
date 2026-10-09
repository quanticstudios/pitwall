package app

import (
	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/term"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func newTheme() *theme.Theme { return theme.Dark() }

func drawSidebar(gtx gl.Context, s *sidebar.Sidebar, th *theme.Theme, st *model.State, session, active string) []sidebar.Event {
	_, evs := s.Layout(gtx, th, st, session, active)
	return evs
}

func drawTerm(gtx gl.Context, v *term.View, th *theme.Theme, g *vt.Grid, m vt.Modes, focused bool) (input []byte, cols, rows int) {
	_, input, cols, rows = v.Layout(gtx, th, g, m, focused)
	return input, cols, rows
}

func panesOf(root *layout.Node) []string {
	if root == nil {
		return nil
	}
	return layout.Panes(root)
}

func rectsOf(root *layout.Node, area layout.Rect, gap int) map[string]layout.Rect {
	return layout.Rects(root, area, gap)
}

func splitTree(root *layout.Node, target, pane string, dir layout.Dir) *layout.Node {
	if root == nil {
		return layout.Leaf(pane)
	}
	return layout.Split(root, target, pane, dir)
}

func removeTree(root *layout.Node, pane string) *layout.Node { return layout.Remove(root, pane) }

// watchFeed and listFiles are what the side panel reads through; tests
// replace them.
var (
	watchFeed = flow.Watch
	listFiles = gitstat.Files
)
