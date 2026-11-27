package mobile

import (
	"context"
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// tabMenu is the chat header's ⋯ menu: Restart for a tab whose command
// exited, and Close tab, which asks for a second tap.
type tabMenu struct {
	more, scrim, restart, close widget.Clickable
	open, armed                 bool
	gone                        bool // the tab closed from here: the chat leaves
}

// tabActions reports which of the menu's actions env's tab takes.
func tabActions(env chatEnv) (restart, close bool) {
	if !env.Open || !env.Online {
		return false, false
	}
	return env.CanRestart && env.Tab.Exited, env.CanClose
}

// tabMenuButton is the header's ⋯, nothing when the tab takes no action.
func (c *chatView) tabMenuButton(gtx C, th *theme.Theme, env chatEnv) D {
	if r, cl := tabActions(env); !r && !cl {
		return D{}
	}
	return roundButton(gtx, th, &c.menu.more, "ellipsis", 22, th.Fg)
}

// toggle opens or closes the menu on a tap of ⋯; the chat calls it before
// the header lays the button out, which takes its taps.
func (m *tabMenu) toggle(gtx C) {
	if m.more.Clicked(gtx) {
		m.open, m.armed = !m.open, false
	}
}

// tabMenuLayout handles the menu's taps and draws it, open, under the
// header at the right.
func (c *chatView) tabMenuLayout(gtx C, th *theme.Theme, env chatEnv) {
	m := &c.menu
	restart, closes := tabActions(env)
	if !m.open {
		return
	}
	if (!restart && !closes) || m.scrim.Clicked(gtx) {
		m.open, m.armed = false, false
		return
	}
	t := env.Tab
	if restart && m.restart.Clicked(gtx) {
		m.open = false
		pane := c.pane
		env.Do(func(ctx context.Context, api chatAPI) error { return api.Restart(ctx, t.Pane) }, func(err error) {
			if err != nil && c.pane == pane {
				c.cmp.note = sentence(err.Error())
			}
		})
		return
	}
	if closes && m.close.Clicked(gtx) {
		if !m.armed {
			m.armed = true
		} else {
			m.open, m.armed = false, false
			pane := c.pane
			env.Do(func(ctx context.Context, api chatAPI) error { return api.CloseTab(ctx, t.ID) }, func(err error) {
				if c.pane != pane {
					return
				}
				if err != nil {
					c.cmp.note = sentence(err.Error())
					return
				}
				c.menu.gone = true
			})
			return
		}
	}
	m.scrim.Layout(gtx, func(gtx C) D {
		paint.FillShape(gtx.Ops, color.NRGBA{A: 0x5a}, clip.Rect{Max: gtx.Constraints.Max}.Op())
		return D{Size: gtx.Constraints.Max}
	})
	var rows []layout.FlexChild
	if restart {
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return menuItem(gtx, th, &m.restart, 0, "restart", "Restart", "Run the tab's command again", false)
		}))
	}
	if closes {
		name, note := "Close tab", "End what runs in it"
		if m.armed {
			name, note = "Tap again to close", "This ends what runs in the tab"
		}
		rows = append(rows, layout.Rigid(func(gtx C) D {
			return menuItem(gtx, th, &m.close, 0, "close", name, note, m.armed)
		}))
	}
	rec := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Exact(image.Pt(gtx.Dp(260), 0))
	g.Constraints.Max.Y = gtx.Constraints.Max.Y
	d := layout.UniformInset(6).Layout(g, func(gtx C) D {
		gtx.Constraints.Min.Y = 0
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})
	call := rec.Stop()
	anchor := image.Rect(0, 0, gtx.Constraints.Max.X-gtx.Dp(8), gtx.Dp(64))
	at := kit.Place(anchor, d.Size, image.Rectangle{Max: gtx.Constraints.Max}, kit.BelowEnd, gtx.Dp(2), gtx.Dp(10))
	defer op.Offset(at).Push(gtx.Ops).Pop()
	kit.Surface(gtx, image.Rectangle{Max: d.Size}, gtx.Dp(11), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	defer clip.Rect{Max: d.Size}.Push(gtx.Ops).Pop()
	// why: a tap on the panel between rows must not reach the scrim under it.
	(&widget.Clickable{}).Layout(gtx, func(gtx C) D { return D{Size: d.Size} })
	call.Add(gtx.Ops)
}
