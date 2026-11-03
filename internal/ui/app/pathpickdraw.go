package app

import (
	"fmt"
	"image"
	"path/filepath"
	"slices"

	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// pathRow draws one file of the path picker as paletteRow draws an action:
// a file icon, or a check once marked, then the path, its folder muted and
// its name in the foreground, or with a query, the matched characters
// semibold and the rest muted.
func (u *ui) pathRow(gtx gl.Context, c *widget.Clickable, path, query string, sel bool, size image.Point) {
	th := u.th
	g := gtx
	g.Constraints = gl.Exact(size)
	c.Layout(g, func(gtx gl.Context) gl.Dimensions {
		if c.Hovered() && !sel {
			paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.035), clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(8)).Op(gtx.Ops))
		}
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: size}
	})
	x := gtx.Dp(12)
	icon, ic := "file", th.Muted
	if slices.Contains(u.paths.marked, path) {
		icon, ic = "check", th.Primary
	}
	x += drawIconAt(gtx, image.Pt(x, (size.Y-gtx.Dp(14))/2), func(gtx gl.Context) { sidebar.Icon(gtx, icon, gtx.Dp(14), ic) }) + gtx.Dp(10)
	spans := hitSpans(path, query)
	if query == "" {
		dir, base := filepath.Split(path)
		spans = []hitSpan{{dir, false}, {base, true}}
	}
	start, room := x, size.X-gtx.Dp(12)-x
	for _, s := range spans {
		f, col := medium(th.UIFont), th.Muted
		switch {
		case s.hit && query != "":
			f, col = semibold(th.UIFont), th.Fg
		case s.hit:
			col = th.Fg
		}
		tg := gtx
		tg.Constraints.Max.X = room - (x - start)
		if tg.Constraints.Max.X <= 0 || s.s == "" {
			continue
		}
		tc, tsz := textCall(tg, th, f, th.Sp(theme.Body), col, s.s)
		o := op.Offset(image.Pt(x, (size.Y-tsz.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		x += tsz.X
	}
}

// clickPath is a click on file f: it types f's path, or marks or unmarks
// it while others are marked.
func (u *ui) clickPath(f string) {
	if len(u.paths.marked) > 0 {
		u.paths.toggle(f)
		return
	}
	u.insertPaths(u.paths.pane, []string{f})
	u.pal.close()
}

// pathEmpty is what the picker says with no file to show.
func (u *ui) pathEmpty() string {
	pp := &u.paths
	switch {
	case pp.err != "":
		return pp.err
	case pp.files == nil:
		return "Listing files…"
	case len(pp.files) == 0:
		return "No files here."
	}
	return "Nothing matches \"" + u.pal.query + "\"."
}

// pathHints are the path picker's footer keys with marked files marked.
func pathHints(marked int) [][2]string {
	verb := "insert"
	if marked > 0 {
		verb = fmt.Sprintf("insert %d", marked)
	}
	return [][2]string{{"↑↓", "move"}, {"Tab", "mark"}, {"Enter", verb}, {"Esc", "close"}}
}
