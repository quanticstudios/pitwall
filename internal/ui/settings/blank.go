package settings

import (
	"fmt"
	"image/color"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// blank is a category's loading line, or its empty state: the sidebar
// icon named icon over line, and action under it when there is one.
func (p *Page) blank(gtx gl.Context, wait bool, icon, line string, action gl.Widget) gl.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return gl.Inset{Top: theme.SpaceXL, Bottom: theme.SpaceXL}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		if wait {
			return kit.Loading(gtx, p.th, line)
		}
		draw := func(gtx gl.Context, size int, col color.NRGBA) gl.Dimensions {
			return sidebar.Icon(gtx, icon, size, col)
		}
		return kit.Empty(gtx, p.th, draw, line, action)
	})
}

// usageBlank is what Usage shows instead of its figures: a loading line
// before the first scan lands, an empty state when it found nothing, and
// ok false when there are figures.
func usageBlank(r *flow.Report) (wait bool, line string, ok bool) {
	switch {
	case r == nil:
		return true, "Reading your agents' session files…", true
	case r.Tokens.Total() == 0:
		return false, fmt.Sprintf("No Claude Code, Codex or pi sessions in the last %d days.", len(r.Days)), true
	}
	return false, "", false
}
