package panel

import (
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// recapCard is Claude Code's recap of the session, which it writes for
// someone coming back to it, or on /recap: a quiet card, a Recap label
// over the recap as prose.
func (d *cdraw) recapCard(gtx layout.Context, it *session.Item, txt string) layout.Dimensions {
	th := d.th
	w := gtx.Constraints.Max.X
	px, py := gtx.Dp(cardPadX), gtx.Dp(cardPadY)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px), gtx.Constraints.Max.Y)}
	c, bd := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceXS),
			func(gtx layout.Context) layout.Dimensions {
				return hrow(gtx, gtx.Dp(theme.SpaceXS+theme.Space2XS),
					func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, "clock", gtx.Dp(12), th.Muted) },
					func(gtx layout.Context) layout.Dimensions {
						return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, "Recap")
					})
			},
			func(gtx layout.Context) layout.Dimensions {
				// shortcut: a recap's `code` spans show as plain words; draw them as code if recaps grow markdown.
				return d.selectable(gtx, d.t.sel(it.ID), d.body(th.Muted), strings.ReplaceAll(txt, "`", ""))
			})
	})
	h := bd.Size.Y + 2*py
	rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusCard), th.Surface, th.BorderSubtle)
	at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return bd })
	return layout.Dimensions{Size: image.Pt(w, h)}
}
