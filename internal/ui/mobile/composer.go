package mobile

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// composer is the chat's message box: a field that grows to six lines,
// and a toolbar with the model chip, the effort chip and the send button.
type composer struct {
	ed                              widget.Editor
	attachment                      *attachment
	attach, pasteImage, removeImage widget.Clickable
	loadingImage                    bool
	field, send                     widget.Clickable
	modelChip, effChip              widget.Clickable
	scrim                           widget.Clickable
	items                           map[string]*widget.Clickable
	sending                         bool         // a send is on its way
	notSent                         string       // why the last message did not go in
	menu                            string       // "model" or "effort" while its menu is open
	picks                           []*pick      // picks waiting for the agent to sit idle, one per setting, oldest first
	picking                         bool         // the first pick is on its way
	model, modelAt                  string       // the last model picked, and the turn's model then, until settle drops it
	effort, effortAt                string       // the last effort picked, and the server's effort then, until settle drops it
	pickedUntil                     [2]time.Time // when settle drops the model and the effort pick
	note                            string       // a pick that failed
	chipX                           [2]int       // the chips' left edges, which the menus open above
	chipW                           [2]int
	inited                          bool
}

// pick is a /model or /effort to type once the agent waits at its prompt.
type pick struct{ what, value string }

// pending is the waiting pick for what, "model" or "effort", nil for none.
func (k *composer) pending(what string) *pick {
	if i := slices.IndexFunc(k.picks, func(p *pick) bool { return p.what == what }); i >= 0 {
		return k.picks[i]
	}
	return nil
}

// choose makes p the waiting pick for its setting, after the other's.
func (k *composer) choose(p *pick) {
	k.picks = append(slices.DeleteFunc(k.picks, func(q *pick) bool { return q.what == p.what }), p)
}

// sendState is what the send button shows.
type sendState uint8

const (
	sendIdle    sendState = iota // nothing typed: quiet, does nothing
	sendReady                    // a draft and an idle agent: filled blue
	sendQueue                    // a draft and a working agent: a clock, queues
	sendSending                  // on its way: a spinner, inert
	sendOff                      // offline: outlined, inert
)

// sendStateOf is the button's state for draft text.
func sendStateOf(text string, sending, online, busy bool) sendState {
	switch {
	case !online:
		return sendOff
	case sending:
		return sendSending
	case strings.TrimSpace(text) == "":
		return sendIdle
	case busy:
		return sendQueue
	}
	return sendReady
}

// busy reports whether a message to t waits in pitwall's queue.
func busy(t remote.Tab) bool { return phone.Busy(t) || t.State == "pending-approval" }

// idle reports whether t's agent sits at its prompt, so a pick can go in.
func idle(t remote.Tab) bool { return !phone.Busy(t) && t.State != "awaiting-input" }

// placeholder is the empty field's text.
func placeholder(t remote.Tab) string {
	if busy(t) {
		return "Queue a message. Enter adds a line."
	}
	name := agentName(t.Agent)
	if name == "" {
		name = "the agent"
	}
	return "Message " + name + ". Enter adds a line."
}

// queueLine is the line above the box: what waits in pitwall's queue, the
// oldest first, or why the last message was not sent, or a message that
// was not sent, which waits for the box to empty.
func queueLine(t remote.Tab, ps []*phone.Pending, notSent string) (bold, rest string, bad bool) {
	if notSent != "" {
		return "Not sent:", notSent, true
	}
	if i := slices.IndexFunc(ps, func(p *phone.Pending) bool { return p.Err != "" }); i >= 0 {
		return "Not sent:", strings.Join(strings.Fields(ps[i].Message()), " "), true
	}
	var queued []*phone.Pending
	for _, p := range ps {
		if p.Sent && p.Queued && p.Err == "" {
			queued = append(queued, p)
		}
	}
	n := max(t.Queued, len(queued))
	if n == 0 {
		return "", "", false
	}
	if len(queued) > 0 {
		rest = strings.Join(strings.Fields(queued[0].Message()), " ")
	}
	return fmt.Sprintf("%d queued", n), rest, false
}

// takeNotSent moves a message the agent left before it went in back into
// the box and returns why it was not sent. While the box holds a draft
// the message waits, so neither is lost; the user discards it by clearing
// the box once it is back.
func takeNotSent(c *phone.Chat, draft string) (why, text string) {
	if strings.TrimSpace(draft) != "" {
		return "", ""
	}
	i := slices.IndexFunc(c.Pending, func(p *phone.Pending) bool { return p.Err != "" })
	if i < 0 {
		return "", ""
	}
	p := c.Pending[i]
	c.Pending = slices.Delete(c.Pending, i, i+1)
	return p.Err, p.Text
}

// pickHold is the longest a pick shows on its chip while the session has
// not reported it. Past that the session's own setting shows, so a pick
// that did not apply does not stay on the chip.
const pickHold = 5 * time.Second

// settle drops a pick once the session reports a change, its own or one
// made on the computer, or once pickHold passes, so the chips then show
// what the session runs. It returns when the next pick drops by time.
func (k *composer) settle(turn, effort string, now time.Time) (next time.Time) {
	if k.model != "" && (turn != k.modelAt || !now.Before(k.pickedUntil[0])) {
		k.model = ""
	}
	if k.effort != "" && (effort != k.effortAt || !now.Before(k.pickedUntil[1])) {
		k.effort = ""
	}
	for i, held := range []bool{k.model != "", k.effort != ""} {
		if held && (next.IsZero() || k.pickedUntil[i].Before(next)) {
			next = k.pickedUntil[i]
		}
	}
	return next
}

// modelShown is the model chip's name: a pick settle keeps, else the
// turn's model, else nothing.
func (k *composer) modelShown(turn string) string {
	if k.model != "" {
		return modelName(k.model)
	}
	if turn == "" {
		return ""
	}
	return modelName(turn)
}

func (k *composer) item(key string) *widget.Clickable {
	if k.items == nil {
		k.items = map[string]*widget.Clickable{}
	}
	b := k.items[key]
	if b == nil {
		b = new(widget.Clickable)
		k.items[key] = b
	}
	return b
}

// composer draws the box and handles its sends and picks.
func (c *chatView) composer(gtx C, th *theme.Theme, env chatEnv) D {
	k, t := &c.cmp, env.Tab
	if !k.inited {
		k.inited = true
		k.ed.LineHeight, k.ed.LineHeightScale = 23, 1
	}
	if !env.Open || !t.Send {
		msg := "This tab is not open now."
		if env.Open {
			msg = "This tab runs no agent, so it takes no messages from here."
		}
		return c.cmpFrame(gtx, th, func(gtx C) D {
			return layout.Inset{Left: 6, Right: 6, Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
				return label(gtx, th, th.UIFont, 15, th.Muted, 0, msg)
			})
		})
	}
	if why, text := takeNotSent(&c.data, k.ed.Text()); why != "" {
		k.notSent = why
		setDraft(&k.ed, text)
	}
	c.firePick(env)
	if next := k.settle(c.currentModel(t), t.Effort, env.now()); !next.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: next})
	}
	k.ed.ReadOnly = k.sending
	draft := k.ed.Text()
	if k.attachment != nil {
		draft += "image"
	}
	st := sendStateOf(draft, k.sending || k.loadingImage, env.Online, busy(t))
	c.imageClicks(gtx, env)
	if k.send.Clicked(gtx) && (st == sendReady || st == sendQueue) {
		c.sendDraft(env)
		st = sendSending
	}
	if k.field.Clicked(gtx) {
		gtx.Execute(key.FocusCmd{Tag: &k.ed})
		gtx.Execute(key.SoftKeyboardCmd{Show: true})
	}
	if k.modelChip.Clicked(gtx) && env.Online {
		k.menu = toggle(k.menu, "model")
	}
	if k.effChip.Clicked(gtx) && env.Online {
		k.menu = toggle(k.menu, "effort")
	}
	chips := t.Picker
	focused := gtx.Focused(&k.ed)
	hint := k.note
	if !env.Online {
		hint = "Sends when the connection is back. Your draft stays here."
	}
	return c.cmpFrame(gtx, th, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				bold, rest, bad := queueLine(t, c.data.Pending, k.notSent)
				if bold == "" {
					return D{}
				}
				return strip(gtx, th, bold, rest, bad)
			}),
			layout.Rigid(func(gtx C) D { return c.attachments(gtx, th, env) }),
			layout.Rigid(func(gtx C) D {
				return msgBox(gtx, th, focused, !env.Online, func(gtx C) D {
					fieldW := func(gtx C) D { return k.drawField(gtx, th, placeholder(t)) }
					sendW := func(gtx C) D { return sendButton(gtx, th, &k.send, st) }
					if !chips {
						return layout.Flex{Alignment: layout.End}.Layout(gtx,
							layout.Flexed(1, func(gtx C) D {
								return layout.Inset{Bottom: 10}.Layout(gtx, fieldW)
							}),
							layout.Rigid(func(gtx C) D {
								return layout.Inset{Right: 4, Bottom: 2}.Layout(gtx, sendW)
							}),
						)
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(fieldW),
						layout.Rigid(func(gtx C) D {
							return layout.Inset{Left: 6, Right: 4, Bottom: 2}.Layout(gtx, func(gtx C) D {
								gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(50))
								return midFlex(gtx,
									layout.Rigid(func(gtx C) D { return c.chip(gtx, th, 0, t) }),
									layout.Rigid(layout.Spacer{Width: 8}.Layout),
									layout.Rigid(func(gtx C) D { return c.chip(gtx, th, 1, t) }),
									layout.Flexed(1, func(gtx C) D { return D{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
									layout.Rigid(sendW),
								)
							})
						}),
					)
				})
			}),
			layout.Rigid(func(gtx C) D {
				if hint == "" {
					return D{}
				}
				return layout.Inset{Left: 8, Right: 8, Top: 6}.Layout(gtx, func(gtx C) D {
					return label(gtx, th, th.UIFont, 13.5, th.Muted, 2, hint)
				})
			}),
		)
	})
}

func toggle(cur, menu string) string {
	if cur == menu {
		return ""
	}
	return menu
}

// cmpFrame is the composer's band: the background, a line on top, and its
// padding.
func (c *chatView) cmpFrame(gtx C, th *theme.Theme, w layout.Widget) D {
	m := op.Record(gtx.Ops)
	d := layout.Inset{Left: 10, Right: 10, Top: 8, Bottom: 10}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return w(gtx)
	})
	call := m.Stop()
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: d.Size}.Op())
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Max: image.Pt(d.Size.X, 1)}.Op())
	call.Add(gtx.Ops)
	return d
}

// strip is the 34dp queued line: a clock, the count and the oldest text;
// red with an alert when a message was not sent.
func strip(gtx C, th *theme.Theme, bold, rest string, bad bool) D {
	col, ic := th.Readable(th.Blue, th.Bg), icClock
	if bad {
		col, ic = th.Readable(th.Red, th.Bg), icAlert
	}
	return layout.Inset{Left: 6, Right: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.Y = gtx.Dp(28)
		return midFlex(gtx,
			layout.Rigid(func(gtx C) D { return glyph(gtx, ic, 16, col) }),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(func(gtx C) D { return label(gtx, th, weight(th.UIFont, font.SemiBold), 14, col, 1, bold) }),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Flexed(1, func(gtx C) D {
				gtx.Constraints.Min.X = 0
				return label(gtx, th, th.UIFont, 14, th.Muted, 1, rest)
			}),
		)
	})
}

// msgBox is the rounded box around the field and toolbar: a focus ring
// with a halo while the keyboard is up, and a dimmer fill when disabled.
func msgBox(gtx C, th *theme.Theme, focused, off bool, w layout.Widget) D {
	m := op.Record(gtx.Ops)
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	d := w(gtx)
	call := m.Stop()
	rect, r := image.Rectangle{Max: d.Size}, gtx.Dp(20)
	fill, ring := th.Surface, th.BorderSubtle
	if off {
		fill = theme.Mix(th.Bg, th.Surface, 0.55)
	}
	if focused && !off {
		ring = theme.Mix(th.Surface, th.Primary, 0.7)
		halo := gtx.Dp(3)
		paint.FillShape(gtx.Ops, alpha(th.Primary, 0.22), clip.UniformRRect(rect.Inset(-halo), r+halo).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, ring, clip.UniformRRect(rect, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return d
}

func alpha(c color.NRGBA, a float32) color.NRGBA {
	c.A = uint8(float32(c.A) * a)
	return c
}

// drawField is the editor: one line at rest, growing to six, then
// scrolling inside with its top edge faded. A tap anywhere in it focuses
// the editor, which brings up the soft keyboard.
func (k *composer) drawField(gtx C, th *theme.Theme, hint string) D {
	return k.field.Layout(gtx, func(gtx C) D {
		return layout.Inset{Left: 16, Right: 16, Top: 12, Bottom: 2}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			gtx.Constraints.Min.Y = gtx.Dp(32)
			gtx.Constraints.Max.Y = gtx.Dp(6 * 23)
			col := th.Fg
			if k.sending {
				col = th.Muted
			}
			d := layout.Stack{}.Layout(gtx,
				layout.Stacked(func(gtx C) D {
					if k.ed.Len() > 0 {
						return D{}
					}
					return label(gtx, th, th.UIFont, 16.5, th.TextQuiet, 1, hint)
				}),
				layout.Stacked(func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return k.ed.Layout(gtx, th.Shaper, th.UIFont, 16.5, colorOp(gtx, col), colorOp(gtx, theme.Mix(th.Surface, th.Primary, 0.35)))
				}),
			)
			if d.Size.Y >= gtx.Constraints.Max.Y {
				fade(gtx, th.Surface, image.Pt(d.Size.X, gtx.Dp(22)))
			}
			return d
		})
	})
}

// fade draws col fading out downward over size, at the top.
func fade(gtx C, col color.NRGBA, size image.Point) {
	defer clip.Rect{Min: image.Pt(0, -gtx.Dp(12)), Max: size}.Push(gtx.Ops).Pop()
	clear := col
	clear.A = 0
	paint.LinearGradientOp{Stop1: layout.FPt(image.Pt(0, -gtx.Dp(12))), Stop2: layout.FPt(image.Pt(0, size.Y)), Color1: col, Color2: clear}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

// sendButton is the 44dp round button in a 48dp hit area: arrow-up, a
// clock to queue, or a spinner while sending.
func sendButton(gtx C, th *theme.Theme, c *widget.Clickable, st sendState) D {
	live := st == sendReady || st == sendQueue
	draw := func(gtx C) D {
		hit, s := gtx.Dp(48), gtx.Dp(44)
		o := (hit - s) / 2
		rect := image.Rect(o, o, o+s, o+s)
		var fill, fg, ring color.NRGBA
		switch st {
		case sendIdle:
			fill, fg = th.SurfaceSecondary, th.TextQuiet
		case sendReady:
			fill, fg = th.Primary, th.OnPrimary
			if c.Pressed() {
				fill = theme.Mix(th.Primary, color.NRGBA{A: 0xff}, 0.22)
				halo := gtx.Dp(4)
				paint.FillShape(gtx.Ops, alpha(th.Primary, 0.25), clip.Ellipse(rect.Inset(-halo)).Op(gtx.Ops))
			}
		case sendQueue:
			fill, fg, ring = theme.Mix(th.Surface, th.Primary, 0.24), th.Readable(th.Blue, th.Surface), theme.Mix(th.Surface, th.Primary, 0.7)
			if c.Pressed() {
				fill = theme.Mix(th.Surface, th.Primary, 0.4)
			}
		case sendSending:
			fill, fg = theme.Mix(th.Surface, th.Primary, 0.55), th.OnPrimary
		case sendOff:
			fg, ring = theme.Mix(th.Bg, th.TextQuiet, 0.55), th.BorderStrong
		}
		if ring.A > 0 {
			paint.FillShape(gtx.Ops, ring, clip.Ellipse(rect).Op(gtx.Ops))
			w := gtx.Dp(1.5)
			if st == sendOff {
				w = gtx.Dp(1)
			}
			if fill.A == 0 {
				fill = th.Surface
			}
			paint.FillShape(gtx.Ops, fill, clip.Ellipse(rect.Inset(w)).Op(gtx.Ops))
		} else {
			paint.FillShape(gtx.Ops, fill, clip.Ellipse(rect).Op(gtx.Ops))
		}
		is := gtx.Dp(22)
		off := op.Offset(image.Pt((hit-is)/2, (hit-is)/2)).Push(gtx.Ops)
		switch st {
		case sendQueue:
			glyph(gtx, icClock, 22, fg)
		case sendSending:
			spinner(gtx, 22, fg)
		default:
			glyph(gtx, icUp, 22, fg)
		}
		off.Pop()
		return D{Size: image.Pt(hit, hit)}
	}
	if !live {
		return draw(gtx)
	}
	return c.Layout(gtx, draw)
}

// chip is the model chip (0) or the effort chip (1): 36dp in a 48dp hit
// area, its name capped at 132dp so the send button never moves.
func (c *chatView) chip(gtx C, th *theme.Theme, which int, t remote.Tab) D {
	k := &c.cmp
	btn, what := &k.modelChip, "model"
	if which == 1 {
		btn, what = &k.effChip, "effort"
	}
	pk := k.pending(what)
	pend := pk != nil
	open := k.menu == what
	name := ""
	if which == 0 {
		name = k.modelShown(c.currentModel(t))
		if name == "" {
			name = "Model"
		}
		if pend {
			name = modelName(pk.value)
		}
	} else {
		name = k.effortShown(t.Effort)
		if pend {
			name = pk.value
		}
		if name == "" {
			name = "effort"
		}
	}
	gtx.Constraints.Min = image.Point{}
	d := btn.Layout(gtx, func(gtx C) D {
		h := gtx.Dp(48)
		m := op.Record(gtx.Ops)
		cd := layout.Inset{Left: 9, Right: 10}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.Y = gtx.Dp(36)
			fg := th.Fg
			if pend {
				fg = th.Readable(th.Blue, th.SurfaceSecondary)
			}
			return midFlex(gtx,
				layout.Rigid(func(gtx C) D {
					switch {
					case pend:
						return spinner(gtx, 15, fg)
					case which == 0:
						return sidebar.AgentMark(gtx, model.ProviderClaude, gtx.Dp(17), th.Fg)
					}
					return effortGlyph(gtx, th, effortLevel(name), th.Readable(th.Blue, th.SurfaceSecondary))
				}),
				layout.Rigid(layout.Spacer{Width: 7}.Layout),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(132))
					return label(gtx, th, weight(th.UIFont, font.Medium), 14.5, fg, 1, name)
				}),
				layout.Rigid(layout.Spacer{Width: 7}.Layout),
				layout.Rigid(func(gtx C) D {
					col := th.Muted
					if pend {
						col = fg
					}
					return glyph(gtx, icDown, 15, col)
				}),
			)
		})
		call := m.Stop()
		y := (h - cd.Size.Y) / 2
		rect := image.Rectangle{Min: image.Pt(0, y), Max: image.Pt(cd.Size.X, y+cd.Size.Y)}
		fill, ring := th.SurfaceSecondary, th.BorderSubtle
		switch {
		case pend:
			ring = theme.Mix(th.SurfaceSecondary, th.Blue, 0.55)
		case open:
			fill, ring = theme.Mix(th.SurfaceSecondary, th.Primary, 0.12), theme.Mix(th.SurfaceSecondary, th.Primary, 0.6)
		case btn.Pressed():
			fill, ring = th.SurfaceElevated, th.BorderStrong
		}
		r := rect.Dy() / 2
		paint.FillShape(gtx.Ops, ring, clip.UniformRRect(rect, r).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
		o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return D{Size: image.Pt(cd.Size.X, h)}
	})
	// why: the chips sit 16dp in from the screen's edge (the band's 10 and
	// the toolbar's 6), which the menus need to open above them.
	k.chipW[which] = d.Size.X
	k.chipX[which] = gtx.Dp(16)
	if which == 1 {
		k.chipX[1] += k.chipW[0] + gtx.Dp(8)
	}
	return d
}

// effortGlyph is five rising bars, level of them lit in col.
func effortGlyph(gtx C, th *theme.Theme, level int, col color.NRGBA) D {
	w, h := gtx.Dp(18), gtx.Dp(14)
	px := float32(w) / 18
	for i := range 5 {
		x := float32(i) * 3.7
		bh := float32(i)*2.6 + 3.6
		y := 12 - float32(i)*2.6 - 1.6
		c := th.SurfaceElevated
		if i < level {
			c = col
		}
		r := image.Rect(int(x*px), int(y*px), int((x+2.6)*px), int((y+bh)*px))
		paint.FillShape(gtx.Ops, c, clip.UniformRRect(r, int(1.1*px)).Op(gtx.Ops))
	}
	return D{Size: image.Pt(w, h)}
}

// sendDraft sends the box's text: Sending, then Queued or Sent, until the
// transcript has it. On a failure the text stays in the box.
func (c *chatView) sendDraft(env chatEnv) {
	k := &c.cmp
	text := k.ed.Text()
	attachment := k.attachment
	k.sending, k.notSent = true, ""
	pane := c.pane
	p := c.data.Send(text)
	p.Image = attachment != nil
	tabUpdates := c.tabUpdates
	var id uint64
	env.Do(func(ctx context.Context, api chatAPI) (err error) {
		if attachment != nil {
			a, ok := api.(imageAPI)
			if !ok {
				return fmt.Errorf("images are unavailable")
			}
			if attachment.remote == "" {
				attachment.remote, err = a.Upload(ctx, pane, attachment.data)
				if err != nil {
					return err
				}
			}
			id, err = a.SendImage(ctx, pane, text, attachment.remote)
		} else {
			id, err = api.Send(ctx, pane, text)
		}
		return err
	}, func(err error) {
		if c.pane != pane {
			return
		}
		k.sending = false
		if err != nil {
			c.data.Drop(p)
			k.notSent = err.Error()
			return
		}
		if k.attachment == attachment {
			k.attachment = nil
		}
		if k.ed.Text() == text {
			k.ed.SetText("")
		}
		if c.data.Has(p) { // else the transcript showed it before this answer
			tab := env.Tab
			if c.tabUpdates != tabUpdates {
				tab = c.tab
			}
			c.data.Sent(p, id, tab, env.now())
			if c.tabUpdates != tabUpdates {
				c.data.Update(tab, env.now()) // the queue can drain before the HTTP answer arrives
			}
		}
		c.list.Position = layout.Position{BeforeEnd: false}
	})
}

// firePick types the oldest waiting pick once the agent sits idle. One
// goes in at a time; the next goes on a later frame.
func (c *chatView) firePick(env chatEnv) {
	k := &c.cmp
	if len(k.picks) == 0 || k.picking || !env.Online || !idle(env.Tab) || env.Do == nil {
		return
	}
	p := k.picks[0]
	k.picking = true
	pane := c.pane
	env.Do(func(ctx context.Context, api chatAPI) error { return api.Command(ctx, pane, p.what, p.value) }, func(err error) {
		if c.pane != pane {
			return
		}
		k.picking = false
		k.picks = slices.DeleteFunc(k.picks, func(q *pick) bool { return q == p })
		if err != nil {
			k.note = sentence(err.Error())
			return
		}
		k.note = ""
		if p.what == "model" {
			k.model, k.modelAt, k.pickedUntil[0] = p.value, c.currentModel(env.Tab), env.now().Add(pickHold)
		} else {
			k.effort, k.effortAt, k.pickedUntil[1] = p.value, env.Tab.Effort, env.now().Add(pickHold)
		}
	})
}

// menus draws the open model or effort menu over a scrim, above its chip,
// with the composer's top at top.
func (c *chatView) menus(gtx C, th *theme.Theme, env chatEnv, top int) {
	k := &c.cmp
	if k.menu == "" {
		return
	}
	if k.scrim.Clicked(gtx) {
		k.menu = ""
		return
	}
	var values []string
	var width unit.Dp
	which := 0
	current := ""
	if k.menu == "model" {
		values, width, current = env.Models, 268, k.modelShown(c.currentModel(env.Tab))
		if p := k.pending("model"); p != nil {
			current = modelName(p.value)
		}
	} else {
		values, width, which, current = env.Efforts, 236, 1, k.effortShown(env.Tab.Effort)
		if p := k.pending("effort"); p != nil {
			current = p.value
		}
	}
	for _, v := range values {
		if k.item(k.menu + ":" + v).Clicked(gtx) {
			name := v
			if k.menu == "model" {
				name = modelName(v)
			}
			if name != current || v == "default" {
				k.choose(&pick{what: k.menu, value: v})
				k.note = ""
			}
			k.menu = ""
			gtx.Execute(op.InvalidateCmd{})
			return
		}
	}
	k.scrim.Layout(gtx, func(gtx C) D {
		paint.FillShape(gtx.Ops, color.NRGBA{A: 0x5a}, clip.Rect{Max: gtx.Constraints.Max}.Op())
		return D{Size: gtx.Constraints.Max}
	})
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Exact(image.Pt(gtx.Dp(width), 0))
	g.Constraints.Max.Y = top
	d := layout.UniformInset(6).Layout(g, func(gtx C) D {
		gtx.Constraints.Min.Y = 0
		children := []layout.FlexChild{layout.Rigid(func(gtx C) D {
			title := "Model"
			if which == 1 {
				title = "Effort"
			}
			return layout.Inset{Left: 12, Right: 12, Top: 4, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return label(gtx, th, weight(th.UIFont, font.SemiBold), 13, th.Muted, 1, title)
			})
		})}
		for _, v := range values {
			v := v
			on := false
			name, note := v, ""
			if which == 0 {
				name, note = modelName(v), modelNote(v, c.currentModel(env.Tab))
				on = v != "default" && name == current
			} else {
				on = v == current
			}
			children = append(children, layout.Rigid(func(gtx C) D {
				return menuItem(gtx, th, k.item(k.menu+":"+v), which, v, name, note, on)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
	call := m.Stop()
	x := k.chipX[which]
	anchor := image.Rect(x, top, x+k.chipW[which], top+gtx.Dp(48))
	p := kit.Place(anchor, d.Size, image.Rectangle{Max: gtx.Constraints.Max}, kit.BelowStart, gtx.Dp(6), gtx.Dp(10))
	defer op.Offset(p).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: d.Size}
	kit.Surface(gtx, rect, gtx.Dp(11), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	call.Add(gtx.Ops)
}

// menuItem is a 52dp row of a menu: the effort glyph or nothing, the name
// with a note under it, and a check on the current one.
func menuItem(gtx C, th *theme.Theme, b *widget.Clickable, which int, value, name, note string, on bool) D {
	return b.Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(52))
			return midFlex(gtx,
				layout.Rigid(func(gtx C) D {
					if which == 0 {
						return D{}
					}
					return layout.Inset{Right: 12}.Layout(gtx, func(gtx C) D {
						return effortGlyph(gtx, th, effortLevel(value), th.Readable(th.Blue, th.SurfaceElevated))
					})
				}),
				layout.Flexed(1, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return label(gtx, th, th.UIFont, 16.5, th.Fg, 1, name) }),
						layout.Rigid(func(gtx C) D {
							if note == "" {
								return D{}
							}
							return label(gtx, th, th.UIFont, 13.5, th.Muted, 1, note)
						}),
					)
				}),
				layout.Rigid(func(gtx C) D {
					if !on {
						return D{}
					}
					return glyph(gtx, icCheck, 18, th.Readable(th.Primary, th.SurfaceElevated))
				}),
			)
		})
		call := m.Stop()
		fill := color.NRGBA{}
		switch {
		case b.Pressed():
			fill = theme.Mix(th.SurfaceElevated, th.Fg, 0.08)
		case on:
			fill = theme.Mix(th.SurfaceElevated, th.Primary, 0.15)
		}
		if fill.A > 0 {
			paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: d.Size}, gtx.Dp(8)).Op(gtx.Ops))
		}
		call.Add(gtx.Ops)
		return d
	})
}

// setDraft puts s in ed with the caret at its end, where typing goes on.
func setDraft(ed *widget.Editor, s string) {
	ed.SetText(s)
	n := ed.Len()
	ed.SetCaret(n, n)
}
