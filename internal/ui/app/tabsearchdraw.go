package app

import (
	"image"
	"slices"
	"strings"
	"time"

	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// tabRow draws one row of tab search as paletteRow draws an action: the
// tab's mark and state glyph, its title with the query's hits semibold, its
// group, session and host muted, and on the right what is unusual about it:
// settled, snoozed, detached, or closed and when. A closed tab's title is
// muted, and one whose folder is gone says where it would open.
func (u *ui) tabRow(gtx gl.Context, st *model.State, c *widget.Clickable, h tabHit, query string, sel bool, size image.Point) {
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
	px := gtx.Dp(12)
	if h.all {
		at := px
		at += drawIconAt(gtx, image.Pt(at, (size.Y-gtx.Dp(14))/2), func(gtx gl.Context) { sidebar.Icon(gtx, "search", gtx.Dp(14), th.Muted) }) + gtx.Dp(8)
		tc, tsz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, "Search all sessions for \""+query+"\"")
		o := op.Offset(image.Pt(at, (size.Y-tsz.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		return
	}
	var ws model.Workspace
	var tags []string
	var where []string
	fg := th.Fg
	if h.ws != nil {
		ws = *h.ws
		now := time.Now()
		switch {
		case ws.Detached:
			tags = append(tags, "Detached")
		case !ws.SnoozedUntil.IsZero() && ws.SnoozedUntil.After(now):
			tags = append(tags, "Snoozed")
		case !ws.Settled.IsZero():
			tags = append(tags, "Settled")
		}
		// Another session's tab names its session and host first, so a long
		// group name is what gets cut.
		if s := st.Session(ws.SessionID); s != nil && ws.SessionID != u.nav.session {
			where = append(where, s.Name, strings.TrimSuffix(hostPrefix(u.b, ws.SessionID), " · "))
		}
		if ws.ProjectID != "" {
			where = append(where, groupName(st, ws))
		}
	} else {
		cl := h.closed
		ws, fg = cl.Tab, th.Muted
		tags = append(tags, "Closed "+sidebar.RelTime(time.Now(), cl.ClosedAt))
		switch {
		case cl.Gone && cl.Root() != "":
			where = append(where, "Folder gone, opens in "+model.ShortPath(cl.Root()))
		case cl.Gone:
			where = append(where, "Folder and repository gone")
		default:
			where = append(where, cl.Session, strings.TrimSuffix(hostPrefix(u.b, cl.ID), " · "), cl.Group.Name)
		}
	}

	// The mark and the glyph, in the 14dp and 12dp slots the sidebar uses.
	mark := gtx.Dp(14)
	x := px
	x += drawIconAt(gtx, image.Pt(x, (size.Y-mark)/2), func(gtx gl.Context) {
		if h.closed != nil {
			if p := h.closed.Pane.Provider; model.IsAgent(p) {
				sidebar.AgentMark(gtx, p, mark, th.Fg)
			} else {
				sidebar.Icon(gtx, "terminal", mark, th.Muted)
			}
			return
		}
		sidebar.TabMark(gtx, st, ws, mark, th.Muted, th.Fg)
	}) + gtx.Dp(6)
	gs := gtx.Dp(12)
	drawIconAt(gtx, image.Pt(x, (size.Y-gs)/2), func(gtx gl.Context) {
		switch {
		case h.closed != nil && h.closed.Gone:
			sidebar.Icon(gtx, "circle-alert", gs, th.Red)
		case h.closed != nil:
			sidebar.Icon(gtx, "archive", gs, th.Muted)
		default:
			var acts []model.Activity
			for _, a := range st.Activities {
				if a.WorkspaceID == ws.ID {
					acts = append(acts, a)
				}
			}
			if a := model.Aggregate(acts); a != nil {
				sidebar.StateGlyph(gtx, th, a.State, th.Surface)
			} else if ws.Branch != "" {
				sidebar.Icon(gtx, "git-branch", gs, th.Muted)
			}
		}
	})
	x += gs + gtx.Dp(10)

	// The tags on the right, as the palette's keys are.
	kx := size.X - px
	for i := len(tags) - 1; i >= 0; i-- {
		tc, ts := chip(gtx, th, th.SurfaceElevated, th.Border, th.Muted, tags[i])
		kx -= ts.X
		o := op.Offset(image.Pt(kx, (size.Y-ts.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		kx -= gtx.Dp(6)
	}
	sub := strings.Join(slices.DeleteFunc(where, func(s string) bool { return s == "" }), " · ")
	gg := gtx
	gg.Constraints.Max.X = max(0, (kx-x)/2)
	gc, gsz := textCall(gg, th, th.UIFont, th.Sp(theme.Small), th.Muted, sub)
	room := max(0, kx-x-gtx.Dp(12)-gsz.X-gtx.Dp(10))
	start := x
	for _, s := range hitSpans(tabTitle(ws), query) {
		f, col := medium(th.UIFont), fg
		switch {
		case s.hit:
			f, col = semibold(th.UIFont), th.Fg
		case query != "":
			col = th.Muted
		}
		tg := gtx
		tg.Constraints.Max.X = room - (x - start)
		if tg.Constraints.Max.X <= 0 {
			break
		}
		tc, tsz := textCall(tg, th, f, th.Sp(theme.Body), col, s.s)
		o := op.Offset(image.Pt(x, (size.Y-tsz.Y)/2)).Push(gtx.Ops)
		tc.Add(gtx.Ops)
		o.Pop()
		x += tsz.X
	}
	o := op.Offset(image.Pt(x+gtx.Dp(10), (size.Y-gsz.Y)/2)).Push(gtx.Ops)
	gc.Add(gtx.Ops)
	o.Pop()
}

// drawIconAt draws f with its top left at p and returns the 14dp slot's
// width.
func drawIconAt(gtx gl.Context, p image.Point, f func(gl.Context)) int {
	defer op.Offset(p).Push(gtx.Ops).Pop()
	f(gtx)
	return gtx.Dp(14)
}

// tabHints are tab search's footer keys for the highlighted row sel.
func tabHints(hits []tabHit, sel int) [][2]string {
	verb := "show"
	var forget bool
	if sel >= 0 && sel < len(hits) {
		switch h := hits[sel]; {
		case h.all:
			verb = "search"
		case h.closed != nil && h.closed.Gone:
			verb, forget = "open in repository", true
		case h.closed != nil:
			verb, forget = "reopen", true
		}
	}
	out := [][2]string{{"↑↓", "move"}, {"Enter", verb}}
	if forget {
		out = append(out, [2]string{"Del", "forget"})
	}
	return append(out, [2]string{"Esc", "close"})
}
