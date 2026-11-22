package chatin

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// ledgerSidePad matches panel's ledgerSidePad (28 dp) so the docked card and
// status line align with the Ledger conversation's edges.
const ledgerSidePad unit.Dp = theme.SpaceXL + theme.SpaceXS

// attachEvents runs the round + attach button and its popover menu.
func (c *Composer) attachEvents(gtx layout.Context, env Env) {
	if c.attachBtn.Clicked(gtx) {
		c.toggle(gtx.Now, &c.attachBtn)
	}
	if !c.menu || c.which != &c.attachBtn {
		return
	}
	if c.click("attach:image").Clicked(gtx) {
		c.setMenu(gtx.Now, false)
		if env.ReadImage != nil {
			c.Reading(env, env.ReadImage)
		}
		gtx.Execute(key.FocusCmd{Tag: &c.ed})
	}
	if c.click("attach:file").Clicked(gtx) {
		c.setMenu(gtx.Now, false)
		c.insertTrigger("@")
		gtx.Execute(key.FocusCmd{Tag: &c.ed})
	}
	if c.click("attach:slash").Clicked(gtx) {
		c.setMenu(gtx.Now, false)
		if strings.TrimSpace(c.Text()) == "" {
			c.ed.Insert("/")
			c.draft.Sync(c.Text())
		}
		gtx.Execute(key.FocusCmd{Tag: &c.ed})
	}
}

// insertTrigger inserts tok (such as "@") at the caret, preceded by a space
// when the character before it is not whitespace, so the popover opens.
func (c *Composer) insertTrigger(tok string) {
	txt := c.Text()
	_, end := c.ed.Selection()
	r := []rune(txt)
	if end > 0 && end <= len(r) && r[end-1] != ' ' && r[end-1] != '\n' && r[end-1] != '\t' {
		c.ed.Insert(" " + tok)
	} else {
		c.ed.Insert(tok)
	}
	c.draft.Sync(c.Text())
}

// attachMenu is the attach popover's rows.
func (c *Composer) attachMenu(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	var kids []layout.FlexChild
	if env.Images && env.ReadImage != nil {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return pointerRow(gtx, func(gtx layout.Context) layout.Dimensions {
				return iconMenuRow(gtx, th, c.click("attach:image"), "image", "Paste image", th.Fg, false)
			})
		}))
	}
	if env.Mentions != nil {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return pointerRow(gtx, func(gtx layout.Context) layout.Dimensions {
				return iconMenuRow(gtx, th, c.click("attach:file"), "file", "Mention file (@)", th.Fg, false)
			})
		}))
	}
	if env.Commands && strings.TrimSpace(c.Text()) == "" {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return pointerRow(gtx, func(gtx layout.Context) layout.Dimensions {
				return iconMenuRow(gtx, th, c.click("attach:slash"), "terminal", "Slash command (/)", th.Fg, false)
			})
		}))
	}
	if len(kids) == 0 {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceS, Right: theme.SpaceS, Top: theme.SpaceXS, Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, "Paste an image or type @ for files")
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func pointerRow(gtx layout.Context, w layout.Widget) layout.Dimensions {
	d := w(gtx)
	defer clip.Rect{Max: d.Size}.Push(gtx.Ops).Pop()
	pointer.CursorPointer.Add(gtx.Ops)
	return d
}

// ledgerCard is Ledger's floating composer card (.dock .card): 12 dp radius,
// subtle border and shadow, focus ring when focused, taller text area on top,
// round attach + pill pickers + track context meter + round send/stop below.
func (c *Composer) ledgerCard(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	m := op.Record(gtx.Ops)
	c.barY = 0
	d := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { d := c.shelf(gtx, th); c.barY += d.Size.Y; return d }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d := c.ledgerField(gtx, th, env)
			c.barY += d.Size.Y
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return c.ledgerBar(gtx, th, env) }),
	)
	c.cardSize = d.Size
	call := m.Stop()

	r := gtx.Dp(theme.RadiusCard)
	rect := image.Rectangle{Max: d.Size}
	kit.Shadow(gtx, rect, r, kit.Floating)
	ring := th.BorderStrong
	if gtx.Focused(&c.ed) {
		glow := gtx.Dp(3)
		glowCol := color.NRGBA{R: th.Primary.R, G: th.Primary.G, B: th.Primary.B, A: 0x24}
		paint.FillShape(gtx.Ops, glowCol, clip.UniformRRect(rect.Inset(-glow), r+glow).Op(gtx.Ops))
		ring = theme.Mix(th.Surface, th.Primary, 0.5)
	}
	paint.FillShape(gtx.Ops, ring, clip.UniformRRect(rect, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(rect.Inset(gtx.Dp(1)), max(0, r-gtx.Dp(1))).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return d
}

// ledgerField is the Ledger card's text area: slightly roomier top padding
// and minimum height, matching the mock's .dock .ta.
func (c *Composer) ledgerField(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	return layout.Inset{Top: theme.SpaceM + theme.Space2XS, Bottom: theme.SpaceXS + theme.Space2XS, Left: theme.SpaceL, Right: theme.SpaceL}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		line := gtx.Sp(c.ed.LineHeight)
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, max(line, gtx.Dp(32)))
		gtx.Constraints.Max.Y = 10 * line
		size := th.Sp(theme.Large)
		if c.ed.Len() == 0 {
			lg := gtx
			lg.Constraints.Min = image.Point{}
			if sug := c.suggestion(env); sug != "" {
				c.suggestHint(lg, th, size, sug)
			} else {
				text(lg, th, th.UIFont, size, th.Muted, Placeholder(env.Snap))
			}
		}
		d := c.ed.Layout(gtx, th.Shaper, th.UIFont, size, colorOp(gtx, th.Fg), colorOp(gtx, theme.Mix(th.Surface, th.Primary, 0.35)))
		c.tokens(gtx, th)
		c.partTokens(gtx, th)
		c.mentionChips(gtx, th)
		c.mentionAnchor(image.Pt(gtx.Dp(theme.SpaceL), c.barY+gtx.Dp(theme.SpaceM+theme.Space2XS)))
		c.slashAnchor(image.Pt(gtx.Dp(theme.SpaceL), c.barY+gtx.Dp(theme.SpaceM+theme.Space2XS)))
		return d
	})
}

// ledgerBar is Ledger's bottom control row inside the floating card: round
// attach button, pill mode/model/effort/branch/stash controls, track context
// meter, and round 32 dp Send/Stop button.
func (c *Composer) ledgerBar(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	now := gtx.Now
	if ModeOf(env.Snap) == ModeBypass {
		c.bypass = true
	}
	mode := c.mode.show(ModeOf(env.Snap), now)
	modeCol, modeIcon, modeTip := modeLook(th, mode)
	mdl := c.model.show(deref(env.Snap.Model), now)
	eff := EffortName(c.effort.show(deref(env.Snap.Effort), now))
	effCol := pickFg(th)
	if eff == EffortName("") {
		effCol = th.Muted
	}
	label := "Model"
	if mdl != "" {
		label = ModelName(mdl)
	}
	used, total, metered := Meter(env.Snap.Usage)
	fixed := fixedSettings(env.Snap)
	if c.anchors == nil {
		c.anchors = map[*widget.Clickable]image.Rectangle{}
	}
	rowH := gtx.Dp(36)
	x := gtx.Dp(theme.SpaceS)
	anchor := func(b *widget.Clickable, size image.Point, gap int) {
		y := c.barY + gtx.Dp(theme.Space2XS) + (rowH-size.Y)/2
		c.anchors[b] = image.Rectangle{Min: image.Pt(x, y), Max: image.Pt(x+size.X, y+size.Y)}
		x += size.X + gap
	}
	return layout.Inset{Top: theme.Space2XS, Left: theme.SpaceS, Right: theme.SpaceS, Bottom: theme.SpaceS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, rowH)
		left := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d := c.roundIconButton(gtx, th, &c.attachBtn, "plus", "Attach")
				c.tip(gtx, th, &c.attachBtn, d.Size, "Attach", false)
				anchor(&c.attachBtn, d.Size, gtx.Dp(theme.SpaceXS))
				return d
			}),
			layout.Rigid(layout.Spacer{Width: theme.SpaceXS}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if fixed || modeUnknown(env.Snap) {
					return layout.Dimensions{}
				}
				d := c.ledgerPick(gtx, th, &c.modeBtn, modeIcon, ModeName(mode), modeCol, true)
				c.tip(gtx, th, &c.modeBtn, d.Size, modeTip, false)
				anchor(&c.modeBtn, d.Size, gtx.Dp(theme.SpaceXS))
				return d
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if fixed || modeUnknown(env.Snap) {
					return layout.Dimensions{}
				}
				return layout.Spacer{Width: theme.SpaceXS}.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if fixed {
					if mdl == "" {
						return layout.Dimensions{}
					}
					s := ModelName(mdl)
					if e := deref(env.Snap.Effort); e != "" {
						s += " · " + EffortName(e)
					}
					return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, s)
					})
				}
				d := c.ledgerPick(gtx, th, &c.modelBtn, "", label, pickFg(th), true)
				c.tip(gtx, th, &c.modelBtn, d.Size, "Model", false)
				anchor(&c.modelBtn, d.Size, gtx.Dp(theme.SpaceXS))
				return d
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if EffortsOf(env.Snap.Provider) == nil {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					d := c.ledgerPick(gtx, th, &c.effortBtn, "", eff, effCol, true)
					c.tip(gtx, th, &c.effortBtn, d.Size, "Effort", false)
					anchor(&c.effortBtn, d.Size, 0)
					return d
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return c.ledgerBranch(gtx, th, env) }),
		}
		var stashSize image.Point
		if len(c.stash) > 0 {
			left = append(left, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					d := c.ledgerPick(gtx, th, &c.stashBtn, "archive", itoa(len(c.stash)), pickFg(th), false)
					c.tip(gtx, th, &c.stashBtn, d.Size, StashTip(len(c.stash)), false)
					stashSize = d.Size
					return d
				})
			}))
		}
		chips := func(gtx layout.Context) layout.Dimensions {
			defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
			gtx.Constraints.Min.X = 0
			d := layout.Flex{Alignment: layout.Middle}.Layout(gtx, left...)
			if stashSize != (image.Point{}) {
				x0, y := gtx.Dp(theme.SpaceS)+d.Size.X-stashSize.X, c.barY+gtx.Dp(theme.Space2XS)+(rowH-stashSize.Y)/2
				c.anchors[&c.stashBtn] = image.Rectangle{Min: image.Pt(x0, y), Max: image.Pt(x0+stashSize.X, y+stashSize.Y)}
			}
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, d.Size.Y)}
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, chips),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if metered {
					return c.ledgerMeter(gtx, th, used, total)
				}
				if win, ok := meterWindow(env.Snap.Usage); ok {
					return c.ledgerMeterUnknown(gtx, th, win)
				}
				return layout.Dimensions{}
			}),
			layout.Rigid(layout.Spacer{Width: theme.SpaceXS}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if b := c.button(env); b != ButtonQueue && b != ButtonSteer {
					return layout.Dimensions{}
				}
				return layout.Inset{Right: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					d := c.ledgerGhostStop(gtx, th)
					c.tip(gtx, th, &c.stopBtn, d.Size, "Stop (Esc)", true)
					return d
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				b := c.button(env)
				d := c.ledgerSend(gtx, th, b)
				send, other := sendKeys(env)
				tip := map[Button]string{ButtonSendOff: "Send (" + send + ")", ButtonSend: "Send (" + send + ")", ButtonStop: "Stop (Esc)",
					ButtonQueue: "Queue: sends when the agent finishes (" + send + "). Send now: " + other,
					ButtonSteer: "Steer: sends into the running turn (" + send + "). Queue: " + other,
					ButtonSave:  "Save the queued message (" + send + "). Esc cancels"}[b]
				c.tip(gtx, th, &c.sendBtn, d.Size, tip, true)
				return d
			}),
		)
	})
}

func (c *Composer) ledgerBranch(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	if env.Branch == "" {
		return layout.Dimensions{}
	}
	ic := "git-branch"
	if Copied(c.branch.copied, gtx.Now) {
		ic = "check"
		gtx.Execute(op.InvalidateCmd{At: c.branch.copied.Add(copiedFor)})
	}
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(200))
	return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		d := c.ledgerPick(gtx, th, &c.branch.b, ic, env.Branch, pickFg(th), false)
		c.tip(gtx, th, &c.branch.b, d.Size, "Copy branch name", false)
		return d
	})
}

// roundIconButton is Ledger's 28 dp circular icon button (.dock .round).
func (c *Composer) roundIconButton(gtx layout.Context, th *theme.Theme, b *widget.Clickable, icon, label string) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp(label).Add(gtx.Ops)
		s, is := gtx.Dp(28), gtx.Dp(14)
		fill := theme.Transparent
		switch {
		case b.Pressed():
			fill = th.Pressed
		case b.Hovered() || b == c.which && c.menu:
			fill = th.Hover
		}
		if fill.A > 0 {
			paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: image.Pt(s, s)}, s/2).Op(gtx.Ops))
		}
		o := op.Offset(image.Pt((s-is)/2, (s-is)/2)).Push(gtx.Ops)
		sidebar.Icon(gtx, icon, is, pickFg(th))
		o.Pop()
		defer clip.Rect{Max: image.Pt(s, s)}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: image.Pt(s, s)}
	})
}

// ledgerPick is Ledger's rounded pill picker (.dock .pick).
func (c *Composer) ledgerPick(gtx layout.Context, th *theme.Theme, b *widget.Clickable, icon, label string, col color.NRGBA, chevron bool) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp(label).Add(gtx.Ops)
		h, pad := gtx.Dp(28), gtx.Dp(10)
		m := op.Record(gtx.Ops)
		var ws []layout.Widget
		if icon != "" {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, icon, gtx.Dp(12), col) })
		}
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), col, label)
		})
		if chevron {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return sidebar.Icon(gtx, "chevron-down", gtx.Dp(12), th.Muted)
			})
		}
		g := gtx
		g.Constraints.Max.X -= 2 * pad
		d := row(g, gtx.Dp(5), h, ws...)
		call := m.Stop()
		size := image.Pt(d.Size.X+2*pad, h)
		rect := image.Rectangle{Max: size}
		rad := h / 2
		fill := th.SurfaceSecondary
		switch {
		case b.Pressed():
			fill = th.Pressed
		case b.Hovered() || b == c.which && c.menu:
			fill = th.Hover
		}
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.UniformRRect(rect, rad).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect.Inset(gtx.Dp(1)), max(0, rad-gtx.Dp(1))).Op(gtx.Ops))
		o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		defer clip.Rect(rect).Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: size}
	})
}

// ledgerMeter is Ledger's horizontal-track context meter (.dock .ctx), with
// the same hover breakdown card as Column's ring meter.
func (c *Composer) ledgerMeter(gtx layout.Context, th *theme.Theme, used, total int64) layout.Dimensions {
	col := th.Muted
	switch Level(used, total) {
	case MeterWarning:
		col = th.Readable(th.Yellow, th.Surface)
	case MeterDanger:
		col = th.Readable(th.Red, th.Surface)
	}
	pct := Percent(used, total)
	frac := float32(min(used, total)) / float32(total)
	gtx.Constraints.Min = image.Point{}
	d := c.meterBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("Context " + itoa(pct) + "% used").Add(gtx.Ops)
		h, pad := gtx.Dp(28), gtx.Dp(theme.SpaceS)
		m := op.Record(gtx.Ops)
		rd := row(gtx, gtx.Dp(6), h,
			func(gtx layout.Context) layout.Dimensions {
				tw, thk := gtx.Dp(46), gtx.Dp(4)
				rad := thk / 2
				paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rect(0, 0, tw, thk), rad).Op(gtx.Ops))
				if fw := int(float32(tw) * frac); fw > 0 {
					fw = max(thk, fw)
					paint.FillShape(gtx.Ops, col, clip.UniformRRect(image.Rect(0, 0, fw, thk), rad).Op(gtx.Ops))
				}
				return layout.Dimensions{Size: image.Pt(tw, thk)}
			},
			func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, th.MonoFont, th.Sp(theme.Caption), col, Tokens(used)+" / "+Tokens(total))
			},
		)
		call := m.Stop()
		size := image.Pt(rd.Size.X+2*pad, h)
		ghostFill(gtx, th, &c.meterBtn, size)
		o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: size}
	})
	c.hoverCard(gtx, th, &c.meterBtn, d.Size, true, func(gtx layout.Context) layout.Dimensions {
		left := AutoCompactAt(total) - used
		room := Tokens(left) + " left before auto-compact"
		if left <= 0 {
			room = "At the auto-compact point"
		}
		line := func(f font.Font, size unit.Sp, col color.NRGBA, s string) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: theme.Space2XS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return text(gtx, th, f, size, col, s)
				})
			})
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			line(medium(th.UIFont), th.Sp(theme.Small), th.Muted, "Context"),
			line(th.UIFont, th.Sp(theme.Body), th.Fg, Tokens(used)+" of "+Tokens(total)+" tokens, "+itoa(pct)+"%"),
			line(th.UIFont, th.Sp(theme.Small), th.Muted, room),
			line(th.UIFont, th.Sp(theme.Small), th.Muted, Tokens(total)+" window"),
		)
	})
	return d
}

// ledgerMeterUnknown is Ledger's context meter when the window is known
// but current context usage is unknown (as right after compaction): an
// empty track and a muted dash in place of the used count.
func (c *Composer) ledgerMeterUnknown(gtx layout.Context, th *theme.Theme, total int64) layout.Dimensions {
	col := th.Muted
	gtx.Constraints.Min = image.Point{}
	d := c.meterBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("Context unknown").Add(gtx.Ops)
		h, pad := gtx.Dp(28), gtx.Dp(theme.SpaceS)
		m := op.Record(gtx.Ops)
		rd := row(gtx, gtx.Dp(6), h,
			func(gtx layout.Context) layout.Dimensions {
				tw, thk := gtx.Dp(46), gtx.Dp(4)
				paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(image.Rect(0, 0, tw, thk), thk/2).Op(gtx.Ops))
				return layout.Dimensions{Size: image.Pt(tw, thk)}
			},
			func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, th.MonoFont, th.Sp(theme.Caption), col, "— / "+Tokens(total))
			},
		)
		call := m.Stop()
		size := image.Pt(rd.Size.X+2*pad, h)
		ghostFill(gtx, th, &c.meterBtn, size)
		o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: size}
	})
	c.hoverCard(gtx, th, &c.meterBtn, d.Size, true, func(gtx layout.Context) layout.Dimensions {
		line := func(f font.Font, size unit.Sp, col color.NRGBA, s string) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: theme.Space2XS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return text(gtx, th, f, size, col, s)
				})
			})
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			line(medium(th.UIFont), th.Sp(theme.Small), th.Muted, "Context"),
			line(th.UIFont, th.Sp(theme.Body), th.Fg, "Updated on next reply"),
			line(th.UIFont, th.Sp(theme.Small), th.Muted, Tokens(total)+" window"),
		)
	})
	return d
}

// ledgerSend is Ledger's 32 dp circular Send/Stop button (.dock .send).
func (c *Composer) ledgerSend(gtx layout.Context, th *theme.Theme, b Button) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return c.sendBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(32)
		rect := image.Rectangle{Max: image.Pt(s, s)}
		fill, fg := th.SurfaceElevated, th.Muted
		switch b {
		case ButtonStop:
			fill, fg = th.Fg, th.Bg
			semantic.LabelOp("Stop").Add(gtx.Ops)
		case ButtonQueue:
			fill, fg = th.Primary, th.OnPrimary
			semantic.LabelOp("Queue").Add(gtx.Ops)
		case ButtonSteer:
			fill, fg = th.Primary, th.OnPrimary
			semantic.LabelOp("Steer").Add(gtx.Ops)
		case ButtonSave:
			fill, fg = th.Primary, th.OnPrimary
			semantic.LabelOp("Save").Add(gtx.Ops)
		case ButtonSend:
			fill, fg = th.Primary, th.OnPrimary
			semantic.LabelOp("Send").Add(gtx.Ops)
		default:
			semantic.LabelOp("Send").Add(gtx.Ops)
		}
		live := b != ButtonSendOff
		switch {
		case c.sendBtn.Pressed() && live:
			fill = theme.Mix(fill, th.Bg, 0.2)
		case c.sendBtn.Hovered() && live:
			fill = theme.Mix(fill, th.Fg, 0.12)
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, s/2).Op(gtx.Ops))
		is := gtx.Dp(14)
		o := op.Offset(image.Pt((s-is)/2, (s-is)/2)).Push(gtx.Ops)
		switch b {
		case ButtonStop:
			stopSquare(gtx, is, fg)
		case ButtonQueue:
			sidebar.Icon(gtx, "list-plus", is, fg)
		case ButtonSave:
			sidebar.Icon(gtx, "check", is, fg)
		default:
			sidebar.Icon(gtx, "arrow-up", is, fg)
		}
		o.Pop()
		if live {
			defer clip.Rect(rect).Push(gtx.Ops).Pop()
			pointer.CursorPointer.Add(gtx.Ops)
		}
		return layout.Dimensions{Size: rect.Max}
	})
}

func (c *Composer) ledgerGhostStop(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return c.stopBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("Stop").Add(gtx.Ops)
		s := gtx.Dp(28)
		fill, fg := theme.Transparent, th.Muted
		switch {
		case c.stopBtn.Pressed():
			fill, fg = th.Pressed, th.Fg
		case c.stopBtn.Hovered():
			fill, fg = th.Hover, th.Fg
		}
		if fill.A > 0 {
			paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: image.Pt(s, s)}, s/2).Op(gtx.Ops))
		}
		is := gtx.Dp(14)
		o := op.Offset(image.Pt((s-is)/2, (s-is)/2)).Push(gtx.Ops)
		stopSquare(gtx, is, fg)
		o.Pop()
		defer clip.Rect{Max: image.Pt(s, s)}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: image.Pt(s, s)}
	})
}

// ledgerStatus is the left side of the status line under the Ledger card.
func ledgerStatus(s *session.Snapshot, note string, noteOK bool) (icon, msg, tone string) {
	if note != "" {
		if noteOK {
			return "", note, "muted"
		}
		return "circle-alert", note, "red"
	}
	if s == nil {
		return "", "", ""
	}
	switch {
	case len(s.PendingApprovals) > 0:
		return "clock", "Waiting for approval", "yellow"
	case len(s.PendingUserInputs) > 0:
		return "clock", "Waiting for answer", "yellow"
	case s.PlanApproval != nil:
		return "clock", "Waiting for plan review", "yellow"
	case s.UsageLimit != nil:
		return "circle-alert", "Usage limit reached", "yellow"
	case s.Busy():
		return "clock", "Working…", "muted"
	}
	return "", "", ""
}

// ledgerStatusLine draws the quiet line under the floating card (.statusl):
// waiting/working state on the left, key hints (Esc stop, / commands, @ files)
// on the right.
func (c *Composer) ledgerStatusLine(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	w := gtx.Constraints.Max.X
	pad := gtx.Dp(6)
	h := gtx.Dp(20)
	innerW := max(0, w-2*pad)

	type hint struct{ k, label string }
	var hints []hint
	if env.Snap != nil && (env.Snap.Busy() || Pending(env.Snap)) {
		hints = append(hints, hint{"Esc", "stop"})
	}
	if env.Commands {
		hints = append(hints, hint{"/", "commands"})
	}
	if env.Mentions != nil {
		hints = append(hints, hint{"@", "files"})
	}
	// Even when Commands/Mentions aren't wired in a bare test snapshot, keep
	// the default hints visible when empty so the status line stays balanced.
	if len(hints) == 0 {
		hints = []hint{{"/", "commands"}, {"@", "files"}}
	}

	hintCall, hintDims := func() (op.CallOp, layout.Dimensions) {
		var ws []layout.Widget
		for _, ht := range hints {
			ht := ht
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return row(gtx, gtx.Dp(5), h,
					func(gtx layout.Context) layout.Dimensions { return c.hintKeycap(gtx, th, ht.k) },
					func(gtx layout.Context) layout.Dimensions {
						return text(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, ht.label)
					},
				)
			})
		}
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(innerW, h)}
		d := row(g, gtx.Dp(theme.SpaceM), h, ws...)
		return m.Stop(), d
	}()

	ic, msg, tone := ledgerStatus(env.Snap, c.note, c.noteOK)
	col := th.Muted
	switch tone {
	case "yellow":
		col = th.Readable(th.Yellow, th.Bg)
	case "red":
		col = th.Readable(th.Red, th.Bg)
	}
	leftRoom := innerW
	showHints := hintDims.Size.X > 0 && innerW-hintDims.Size.X >= gtx.Dp(120)
	if showHints {
		leftRoom = innerW - hintDims.Size.X - gtx.Dp(theme.SpaceM)
	}
	if msg != "" && leftRoom > 0 {
		lg := gtx
		lg.Constraints = layout.Constraints{Max: image.Pt(leftRoom, h)}
		o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		var ws []layout.Widget
		if ic != "" {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, ic, gtx.Dp(12), col) })
		}
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, th.UIFont, th.Sp(theme.Caption), col, msg)
		})
		row(lg, gtx.Dp(6), h, ws...)
		o.Pop()
	}
	if showHints {
		o := op.Offset(image.Pt(w-pad-hintDims.Size.X, 0)).Push(gtx.Ops)
		hintCall.Add(gtx.Ops)
		o.Pop()
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// hintKeycap draws a small mono keycap for the status line's key hints.
func (c *Composer) hintKeycap(gtx layout.Context, th *theme.Theme, k string) layout.Dimensions {
	m := op.Record(gtx.Ops)
	td := text(gtx, th, th.MonoFont, th.Sp(theme.Caption), theme.Mix(th.Fg, th.Muted, 0.2), k)
	call := m.Stop()
	px, py := gtx.Dp(5), gtx.Dp(2)
	sz := image.Pt(td.Size.X+2*px, td.Size.Y+2*py)
	r := image.Rectangle{Max: sz}
	rad := gtx.Dp(5)
	paint.FillShape(gtx.Ops, th.BorderStrong, clip.UniformRRect(r, rad).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(r.Inset(gtx.Dp(1)), max(0, rad-gtx.Dp(1))).Op(gtx.Ops))
	o := op.Offset(image.Pt(px, py)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: sz}
}
