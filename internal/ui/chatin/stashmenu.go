package chatin

import (
	"image"
	"time"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// StashTip is the stash chip's tooltip with n drafts stashed.
func StashTip(n int) string {
	s := "1 stashed draft"
	if n != 1 {
		s = itoa(n) + " stashed drafts"
	}
	return s + ". Ctrl+S on an empty box brings one back"
}

// snapMenu opens or closes the popover at once, as a key does: nothing
// keyboard-driven animates.
func (c *Composer) snapMenu(at time.Time, open bool) {
	c.menu = open
	v := float32(0)
	if open {
		v = 1
	}
	c.popT.Set(at, v, 0)
}

// stashMenu is the stash popover's body: a heading, then a row per
// stashed draft, newest first, each its preview line, its image count,
// how long ago, and a delete x; the highlighted row is the one Enter
// restores.
func (c *Composer) stashMenu(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	kids := []layout.FlexChild{layout.Rigid(heading(th, "Stashed drafts"))}
	for i, s := range c.stash {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return c.stashRow(gtx, th, i, s)
		}))
	}
	kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: theme.SpaceS, Right: theme.SpaceS, Top: theme.SpaceXS, Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, "Enter restores · Delete removes · Esc closes")
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func (c *Composer) stashRow(gtx layout.Context, th *theme.Theme, i int, s Stash) layout.Dimensions {
	b, rm := c.click("stash:"+itoa(i)), c.click("stashrm:"+itoa(i))
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("Restore stashed draft: " + StashPreview(s)).Add(gtx.Ops)
		h, w := gtx.Dp(32), gtx.Constraints.Max.X
		rect := image.Rectangle{Max: image.Pt(w, h)}
		switch {
		case b.Pressed():
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		case i == c.stashSel:
			paint.FillShape(gtx.Ops, th.SelectedBg, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		case b.Hovered():
			paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		// The right side first, to know what the preview may take.
		var ws []layout.Widget
		if n := len(s.Attachments); n > 0 {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return row(gtx, gtx.Dp(theme.Space2XS), h,
					func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, "image", gtx.Dp(12), th.Muted) },
					func(gtx layout.Context) layout.Dimensions {
						return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, itoa(n))
					})
			})
		}
		ws = append(ws,
			func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, Ago(s.At, gtx.Now))
			},
			func(gtx layout.Context) layout.Dimensions {
				return iconButton(gtx, th, rm, "x", "Delete stashed draft")
			})
		m := op.Record(gtx.Ops)
		rg := gtx
		rg.Constraints = layout.Constraints{Max: image.Pt(w, h)}
		rd := row(rg, gtx.Dp(theme.SpaceS), h, ws...)
		rcall := m.Stop()
		o := op.Offset(image.Pt(w-rd.Size.X-gtx.Dp(theme.Space2XS), 0)).Push(gtx.Ops)
		rcall.Add(gtx.Ops)
		o.Pop()
		tg := gtx
		tg.Constraints = layout.Constraints{Max: image.Pt(max(0, w-rd.Size.X-gtx.Dp(theme.SpaceM)-gtx.Dp(theme.SpaceS)), h)}
		o = op.Offset(image.Pt(gtx.Dp(theme.SpaceS), 0)).Push(gtx.Ops)
		row(tg, 0, h, func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, StashPreview(s))
		})
		o.Pop()
		return layout.Dimensions{Size: rect.Max}
	})
}
