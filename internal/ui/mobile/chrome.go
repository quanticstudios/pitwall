package mobile

import (
	"context"
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// pillClear is the room the floating title pill takes over the log's
// top: its 8dp margin, 48dp height and 8dp under it.
const pillClear unit.Dp = 64

// pillState is the title pill's state word and its color: Needs you in
// amber while the tab waits on the user, else the sidebar's label.
func pillState(th *theme.Theme, t remote.Tab, open bool) (string, color.NRGBA) {
	switch {
	case !open:
		return "Closed", th.Muted
	case t.Exited:
		return t.Label, th.Muted // why: StateColor's green would read "Exited 1" as a success
	case asks(t):
		return "Needs you", th.Yellow
	case t.State == "":
		return "", th.Muted
	}
	s := model.AgentState(t.State)
	text := t.Label
	if text == "" {
		text = model.PillLabel(model.Activity{State: s})
	}
	return text, sidebar.StateColor(th, s)
}

// titleBar is the floating chrome over the log's top: Back in a round
// surface and the title pill, which opens the tab's menu. A fade under it
// keeps the log from showing between the two.
func (c *chatView) titleBar(gtx C, th *theme.Theme, env chatEnv) D {
	clear := th.Bg
	clear.A = 0
	h := gtx.Dp(pillClear) + gtx.Dp(8)
	cl := clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, h)}.Push(gtx.Ops)
	paint.LinearGradientOp{Stop1: layout.FPt(image.Pt(0, h-gtx.Dp(24))), Stop2: layout.FPt(image.Pt(0, h)), Color1: th.Bg, Color2: clear}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	cl.Pop()
	return layout.Inset{Left: 10, Right: 10, Top: 8}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return floatButton(gtx, th, &c.backBtn, icBack) }),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Flexed(1, func(gtx C) D { return c.titlePill(gtx, th, env) }),
		)
	})
}

// floatButton is a 48dp round floating surface around a lucide icon.
func floatButton(gtx C, th *theme.Theme, b *widget.Clickable, icon string) D {
	return b.Layout(gtx, func(gtx C) D {
		s := gtx.Dp(48)
		fill := th.SurfaceElevated
		if b.Pressed() {
			fill = theme.Mix(fill, th.Fg, 0.1)
		}
		kit.Surface(gtx, image.Rect(0, 0, s, s), s/2, kit.Floating, th.BorderStrong, fill)
		is := gtx.Dp(22)
		o := op.Offset(image.Pt((s-is)/2, (s-is)/2)).Push(gtx.Ops)
		glyph(gtx, icon, 22, th.Fg)
		o.Pop()
		return D{Size: image.Pt(s, s)}
	})
}

// titlePill is the tab's name and state on a 48dp floating pill; a tap
// opens the tab's menu (Screen, Restart, Close tab) when it has one.
func (c *chatView) titlePill(gtx C, th *theme.Theme, env chatEnv) D {
	t := env.Tab
	name := t.Tab
	if !env.Open {
		name = "Tab"
	}
	word, col := pillState(th, t, env.Open)
	r, cl := tabActions(env)
	menu := r || cl || screenAction(env)
	draw := func(gtx C) D {
		h := gtx.Dp(48)
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, h)
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 16, Right: 14}.Layout(gtx, func(gtx C) D {
			return midFlex(gtx,
				layout.Rigid(func(gtx C) D {
					if word == "" {
						return D{}
					}
					return layout.Inset{Right: 10}.Layout(gtx, func(gtx C) D { return circle(gtx, col, 8) })
				}),
				layout.Flexed(1, func(gtx C) D {
					return label(gtx, th, weight(th.UIFont, font.SemiBold), 15.5, th.Fg, 1, name)
				}),
				layout.Rigid(func(gtx C) D {
					if env.Host == "" {
						return D{}
					}
					return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return hostTag(gtx, th, env) })
				}),
				layout.Rigid(func(gtx C) D {
					if word == "" {
						return D{}
					}
					return layout.Inset{Left: 10}.Layout(gtx, func(gtx C) D {
						return label(gtx, th, weight(th.UIFont, font.Medium), 13.5, th.Readable(col, th.SurfaceElevated), 1, word)
					})
				}),
				layout.Rigid(func(gtx C) D {
					if !menu {
						return D{}
					}
					return layout.Inset{Left: 6}.Layout(gtx, func(gtx C) D { return glyph(gtx, icDown, 15, th.Muted) })
				}),
			)
		})
		call := m.Stop()
		fill := th.SurfaceElevated
		if c.menu.more.Pressed() || c.menu.open {
			fill = theme.Mix(fill, th.Fg, 0.1)
		}
		kit.Surface(gtx, image.Rectangle{Max: d.Size}, d.Size.Y/2, kit.Floating, th.BorderStrong, fill)
		call.Add(gtx.Ops)
		return d
	}
	if !menu {
		return draw(gtx)
	}
	return c.menu.more.Layout(gtx, draw)
}

// screenAction reports whether the tab's menu offers the Screen view: a
// terminal tab on a machine that serves it.
func screenAction(env chatEnv) bool { return env.Open && !env.Tab.Chat && env.CanScreen }

// statusLine is the composer's top row while the agent works, its spinner
// row as the terminal shows it, or why the queue waits; "" for none.
func (c *chatView) statusLine(env chatEnv) (text string, working bool) {
	if !env.Open {
		return "", false
	}
	if w, busy := c.data.Working(env.Tab); busy {
		return w, true
	}
	if env.Tab.QueueHold != "" && len(env.Tab.Queue) > 0 {
		return "Waiting: " + env.Tab.QueueHold, false
	}
	return "", false
}

// statusRow draws statusLine's text with a spinner, or a clock while the
// queue waits, over a line.
func statusRow(gtx C, th *theme.Theme, text string, working bool) D {
	return withRule(gtx, th, func(gtx C) D {
		return layout.Inset{Left: 16, Right: 12}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.Y = gtx.Dp(36)
			return midFlex(gtx,
				layout.Rigid(func(gtx C) D {
					if working {
						return spinner(gtx, 14, th.Readable(th.Blue, th.Surface))
					}
					return glyph(gtx, icClock, 14, th.Muted)
				}),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Flexed(1, func(gtx C) D {
					gtx.Constraints.Min.X = 0
					return label(gtx, th, th.UIFont, 13.5, th.Muted, 1, text)
				}),
			)
		})
	})
}

// withRule draws w with a 1px line under it, the box's divider.
func withRule(gtx C, th *theme.Theme, w layout.Widget) D {
	d := w(gtx)
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, d.Size.Y-1), Max: d.Size}.Op())
	return d
}

// queueRows are the tab's queued messages in the box, oldest first, each
// with Move up and Remove on 48dp buttons.
func (c *chatView) queueRows(gtx C, th *theme.Theme, env chatEnv) D {
	k, q := &c.cmp, env.Tab.Queue
	for i, r := range q {
		if i > 0 && k.item("qup "+r.ID).Clicked(gtx) {
			c.queueDo(env, r.ID, "move", i-1)
		}
		if k.item("qx " + r.ID).Clicked(gtx) {
			c.queueDo(env, r.ID, "remove", 0)
		}
	}
	rows := make([]layout.FlexChild, len(q))
	for i, r := range q {
		rows[i] = layout.Rigid(func(gtx C) D {
			return withRule(gtx, th, func(gtx C) D {
				return layout.Inset{Left: 16, Right: 2}.Layout(gtx, func(gtx C) D {
					gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(48))
					return midFlex(gtx,
						layout.Rigid(func(gtx C) D {
							if r.Error != "" {
								return layout.Inset{Right: 8}.Layout(gtx, func(gtx C) D { return glyph(gtx, icAlert, 15, th.Readable(th.Red, th.Surface)) })
							}
							return D{}
						}),
						layout.Flexed(1, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X // why: the buttons line up across rows
							return label(gtx, th, th.UIFont, 14.5, th.Fg, 1, strings.Join(strings.Fields(r.Text), " "))
						}),
						layout.Rigid(func(gtx C) D {
							if i == 0 {
								return D{Size: image.Pt(gtx.Dp(48), 0)} // why: Remove stays in one column
							}
							return roundButton(gtx, th, k.item("qup "+r.ID), "chevron-up", 18, th.Muted)
						}),
						layout.Rigid(func(gtx C) D { return roundButton(gtx, th, k.item("qx "+r.ID), "x", 18, th.Muted) }),
					)
				})
			})
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// queueDo runs a queue action on the computer; the next tab list shows it.
func (c *chatView) queueDo(env chatEnv, id, action string, index int) {
	if env.Do == nil {
		return
	}
	pane, tp := c.pane, env.Tab.Pane
	env.Do(func(ctx context.Context, api chatAPI) error { return api.Queue(ctx, tp, id, action, index) }, func(err error) {
		if c.pane == pane && err != nil {
			c.cmp.note = sentence(err.Error())
		}
	})
}

// composerHint is the one quiet line under the box: a failed pick, the
// offline note, or how the box takes keys.
func composerHint(note string, online, slash bool) string {
	switch {
	case !online:
		return "Sends when the connection is back"
	case note != "":
		return note
	case slash:
		return "Enter adds a line · / for commands"
	}
	return "Enter adds a line"
}
