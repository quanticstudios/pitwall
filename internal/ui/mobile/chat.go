package mobile

import (
	"context"
	"image"
	"image/color"
	"io"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/x/markdown"
	"gioui.org/x/richtext"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// chatView is an open tab's chat.
type chatView struct {
	pane    string
	data    phone.Chat
	list    layout.List
	rows    map[int64]*entryRow
	loading bool // a page of older entries is on its way
	fresh   bool // new entries came while scrolled up
	md      *markdown.Renderer

	back, pick, newBtn, send widget.Clickable
	ed                       widget.Editor
	card                     cardState
}

// entryRow is an entry's widgets, kept across frames by seq.
type entryRow struct {
	click widget.Clickable
	open  bool // a tool call shows its output
	text  string
	spans []richtext.SpanStyle
	rt    richtext.InteractiveText
}

func (c *chatView) reset(e phone.Entries) {
	c.data.Reset(e)
	c.rows = nil
	c.fresh = false
	c.list.Position = layout.Position{BeforeEnd: false}
}

func (c *chatView) add(e phone.Entries) {
	if e.Model != "" {
		c.data.Model = e.Model
	}
	if c.data.Add(e.Entries) && c.list.Position.BeforeEnd {
		c.fresh = true
	}
}

func (c *chatView) row(seq int64) *entryRow {
	if c.rows == nil {
		c.rows = map[int64]*entryRow{}
	}
	r := c.rows[seq]
	if r == nil {
		r = &entryRow{}
		c.rows[seq] = r
	}
	return r
}

func (a *App) chatLayout(gtx C) {
	c, th := &a.chat, a.th
	t, open := a.tab()
	if c.back.Clicked(gtx) {
		a.closeChat()
		return
	}
	if c.pick.Clicked(gtx) {
		a.sheet = sheetView{open: "model"}
	}
	if c.newBtn.Clicked(gtx) {
		c.list.Position = layout.Position{BeforeEnd: false}
		c.fresh = false
	}
	if !c.list.Position.BeforeEnd {
		c.fresh = false
	}
	if c.send.Clicked(gtx) && open && t.Send {
		a.sendMessage(t)
	}
	a.older()

	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return a.chatBar(gtx, t, open) }),
		layout.Rigid(a.offlineLine),
		layout.Flexed(1, func(gtx C) D {
			fillMax(gtx, th.Bg)
			return layout.Stack{Alignment: layout.S}.Layout(gtx,
				layout.Expanded(func(gtx C) D { return a.log(gtx, t, open) }),
				layout.Stacked(func(gtx C) D {
					if !c.fresh {
						return D{}
					}
					return layout.Inset{Bottom: 10}.Layout(gtx, func(gtx C) D {
						return button(gtx, th, &c.newBtn, kit.Secondary, "New messages")
					})
				}),
			)
		}),
		layout.Rigid(func(gtx C) D { return a.composer(gtx, t, open) }),
	)
}

// chatBar is Back, the tab's name over its state, agent and model, and
// the model button for Claude Code.
func (a *App) chatBar(gtx C, t remote.Tab, open bool) D {
	c, th := &a.chat, a.th
	name := "Tab"
	if open {
		name = t.Tab
	}
	var pick layout.Widget
	if open && t.Picker {
		pick = func(gtx C) D {
			m := phone.ShortModel(c.data.Model)
			if m == "" {
				m = "Model"
			}
			return kit.Button(gtx, th, &c.pick, kit.Secondary, kit.Medium, m+" ▾")
		}
	}
	return a.bar(gtx, func(gtx C) D { return iconButton(gtx, th, &c.back, back) }, func(gtx C) D {
		return layout.Inset{Left: 2}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Body), th.Fg, 1, name)
				}),
				layout.Rigid(func(gtx C) D {
					if !open {
						return small(gtx, th, th.Muted, "This tab is not open now.")
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							if t.State == "" {
								return D{}
							}
							return layout.Inset{Right: 5}.Layout(gtx, func(gtx C) D {
								return sidebar.StateGlyph(gtx, th, model.AgentState(t.State), th.Sidebar)
							})
						}),
						layout.Flexed(1, func(gtx C) D {
							parts := []string{}
							if t.Label != "" {
								parts = append(parts, t.Label)
							}
							if n := sidebar.AgentName(model.Provider(t.Agent)); n != "" && t.Agent != "" {
								parts = append(parts, n)
							}
							if m := phone.ShortModel(c.data.Model); m != "" {
								parts = append(parts, m)
							}
							return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, 1, strings.Join(parts, " · "))
						}),
					)
				}),
			)
		})
	}, pick)
}

// older loads the page before the first entry once the top is near.
func (a *App) older() {
	c := &a.chat
	if c.loading || !c.data.More || len(c.data.Entries) == 0 || c.list.Position.First > 1 {
		return
	}
	c.loading = true
	pane, first := c.pane, c.data.First()
	var es []flow.Entry
	var more bool
	a.do(func(ctx context.Context, cl *phone.Client) (err error) {
		es, more, err = cl.Older(ctx, pane, first)
		return err
	}, func(err error) {
		if c.pane != pane {
			return
		}
		c.loading = false
		if err != nil {
			c.data.More = false
			return
		}
		// why: what was on screen stays put as the page goes in above it.
		n := c.data.Older(es, more)
		if c.list.Position.First > 0 {
			c.list.Position.First += n
		} else {
			c.list.Position.First = n
		}
	})
}

// log is the scrolling chat: a note at the top, the entries, the messages
// on their way, the Working line and the card of what the tab asks.
func (a *App) log(gtx C, t remote.Tab, open bool) D {
	c, th := &a.chat, a.th
	var items []layout.Widget
	note := ""
	switch {
	case c.loading:
		note = "Loading earlier messages"
	case c.data.More:
		note = "Scroll up for earlier messages"
	}
	items = append(items, func(gtx C) D {
		if note == "" {
			return D{Size: image.Pt(0, gtx.Dp(8))}
		}
		return layout.Center.Layout(gtx, func(gtx C) D {
			return layout.UniformInset(8).Layout(gtx, func(gtx C) D { return small(gtx, th, th.TextQuiet, note) })
		})
	})
	if c.data.None {
		items = append(items, noteItem(th, "No transcript to show. pitwall shows the state of a shell or of an agent without hooks, never its screen."))
	}
	if c.data.Gone {
		items = append(items, noteItem(th, "This tab closed."))
	}
	for i := range c.data.Entries {
		e := c.data.Entries[i]
		items = append(items, func(gtx C) D { return a.entry(gtx, e) })
	}
	for _, p := range c.data.Pending {
		items = append(items, func(gtx C) D { return a.pending(gtx, p) })
	}
	if w, ok := c.data.Working(t); ok && open {
		items = append(items, func(gtx C) D {
			return layout.Inset{Left: pad, Right: pad, Top: 4, Bottom: 6}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return dot(gtx, th.Readable(th.Yellow, th.Bg)) }),
					layout.Flexed(1, func(gtx C) D {
						return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, 1, w)
					}),
				)
			})
		})
	}
	if open && asks(t) {
		items = append(items, func(gtx C) D { return a.askCard(gtx, t) })
	}
	items = append(items, func(gtx C) D { return D{Size: image.Pt(0, gtx.Dp(8))} })
	return c.list.Layout(gtx, len(items), func(gtx C, i int) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return items[i](gtx)
	})
}

func noteItem(th *theme.Theme, s string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Left: pad, Right: pad, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D { return small(gtx, th, th.Muted, s) })
	}
}

func dot(gtx C, col color.NRGBA) D {
	s := gtx.Dp(7)
	return layout.Inset{Right: 8}.Layout(gtx, func(gtx C) D {
		paint.FillShape(gtx.Ops, col, clip.Ellipse(image.Rect(0, 0, s, s)).Op(gtx.Ops))
		return D{Size: image.Pt(s, s)}
	})
}

func (a *App) entry(gtx C, e flow.Entry) D {
	th := a.th
	r := a.chat.row(e.Seq)
	in := layout.Inset{Left: pad, Right: pad, Top: 5, Bottom: 5}
	switch e.Kind {
	case flow.EntryUser:
		return in.Layout(gtx, func(gtx C) D { return bubble(gtx, th, e.Text, 1) })
	case flow.EntryAssistant:
		return in.Layout(gtx, func(gtx C) D { return a.markdown(gtx, r, e.Text) })
	case flow.EntryError:
		bg, fg := th.Chip(th.Red, th.Bg)
		return in.Layout(gtx, func(gtx C) D {
			return box(gtx, bg, color.NRGBA{}, theme.RadiusPopover, layout.UniformInset(10), func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return body(gtx, th, fg, e.Text)
			})
		})
	case flow.EntryTool:
		return in.Layout(gtx, func(gtx C) D { return a.tool(gtx, r, e) })
	}
	return D{}
}

// bubble is a prompt of the user's, on the right.
func bubble(gtx C, th *theme.Theme, s string, opacity float32) D {
	maxW := gtx.Constraints.Max.X * 85 / 100
	return layout.E.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = maxW
		gtx.Constraints.Min.X = 0
		if opacity < 1 {
			defer paint.PushOpacity(gtx.Ops, opacity).Pop()
		}
		return box(gtx, theme.Mix(th.Bg, th.Primary, 0.22), color.NRGBA{}, theme.RadiusCard, layout.Inset{Left: 12, Right: 12, Top: 8, Bottom: 8}, func(gtx C) D {
			return body(gtx, th, th.Fg, s)
		})
	})
}

// markdown draws a reply: paragraphs, lists, code and links. Tapping a
// link copies it, as the app opens nothing itself.
func (a *App) markdown(gtx C, r *entryRow, src string) D {
	th := a.th
	if r.spans == nil || r.text != src {
		c := &a.chat
		if c.md == nil {
			c.md = markdown.NewRenderer()
			size := th.Sp(theme.Body)
			c.md.Config = markdown.Config{DefaultFont: th.UIFont, MonospaceFont: th.MonoFont, DefaultSize: size,
				DefaultColor: th.Fg, InteractiveColor: th.Readable(th.Blue, th.Bg)}
			c.md.Config.H1Size, c.md.Config.H2Size, c.md.Config.H3Size = size*1.25, size*1.15, size*1.08
			c.md.Config.H4Size, c.md.Config.H5Size, c.md.Config.H6Size = size, size, size
		}
		spans, err := c.md.Render([]byte(src))
		if err != nil || len(spans) == 0 {
			spans = []richtext.SpanStyle{{Font: th.UIFont, Size: th.Sp(theme.Body), Color: th.Fg, Content: src}}
		}
		r.spans, r.text = spans, src
	}
	for {
		span, ev, ok := r.rt.Update(gtx)
		if !ok {
			break
		}
		if url, _ := span.Get(markdown.MetadataURL).(string); ev.Type == richtext.Click && url != "" {
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(url))})
		}
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return richtext.Text(&r.rt, th.Shaper, r.spans...).Layout(gtx)
}

// tool is a call's line, its name and what it ran, which opens to its
// output.
func (a *App) tool(gtx C, r *entryRow, e flow.Entry) D {
	th := a.th
	if r.click.Clicked(gtx) {
		r.open = !r.open
	}
	ring := th.BorderSubtle
	if e.Failed {
		ring = theme.Mix(th.Bg, th.Red, 0.45)
	}
	return box(gtx, th.Surface, ring, theme.RadiusPopover, layout.Inset{}, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return r.click.Layout(gtx, func(gtx C) D {
					gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(touch))
					return layout.Inset{Left: 10, Right: 10}.Layout(gtx, func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								return label(gtx, th, weight(th.MonoFont, font.Bold), th.Sp(theme.Small), th.Fg, 1, e.Tool)
							}),
							layout.Flexed(1, func(gtx C) D {
								return layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx C) D {
									return label(gtx, th, th.MonoFont, th.Sp(theme.Small), th.Muted, 1, e.Arg)
								})
							}),
							layout.Rigid(func(gtx C) D {
								switch {
								case !e.Done:
									return dot(gtx, th.Readable(th.Yellow, th.Surface))
								case e.Failed:
									return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Readable(th.Red, th.Surface), 1, "failed")
								}
								return D{}
							}),
						)
					})
				})
			}),
			layout.Rigid(func(gtx C) D {
				if !r.open {
					return D{}
				}
				out, f := e.Out, th.MonoFont
				if out == "" {
					out, f = map[bool]string{true: "No output.", false: "Running."}[e.Done], th.UIFont
				}
				return layout.Inset{Left: 10, Right: 10, Bottom: 10}.Layout(gtx, func(gtx C) D {
					return box(gtx, th.Bg, color.NRGBA{}, theme.RadiusControl, layout.UniformInset(8), func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return label(gtx, th, f, th.Sp(theme.Caption), th.Muted, 60, out)
					})
				})
			}),
		)
	})
}

// pending is a message sent from here, with its state under it.
func (a *App) pending(gtx C, p *phone.Pending) D {
	th := a.th
	col := th.TextQuiet
	if p.Err != "" {
		col = th.Readable(th.Red, th.Bg)
	}
	return layout.Inset{Left: pad, Right: pad, Top: 5, Bottom: 5}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return bubble(gtx, th, p.Text, 0.7) }),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: 3, Right: 4}.Layout(gtx, func(gtx C) D {
					return label(gtx, th, th.UIFont, th.Sp(theme.Caption), col, 2, p.Label())
				})
			}),
		)
	})
}

// sendMessage sends the composer's text, which shows as Sending, then
// Queued or Sent, until the transcript has it.
func (a *App) sendMessage(t remote.Tab) {
	c := &a.chat
	text := c.ed.Text()
	if strings.TrimSpace(text) == "" {
		return
	}
	p := &phone.Pending{Text: text}
	c.data.Pending = append(c.data.Pending, p)
	c.ed.SetText("")
	c.list.Position = layout.Position{BeforeEnd: false}
	var id uint64
	pane := c.pane
	a.do(func(ctx context.Context, cl *phone.Client) (err error) {
		id, err = cl.Send(ctx, pane, text)
		return err
	}, func(err error) {
		if err != nil {
			p.Err = err.Error()
			return
		}
		tab, _ := a.tab()
		if c.pane == pane {
			c.data.Sent(p, id, tab, time.Now())
		}
	})
}

// composer is the message box and Send, or why the tab takes none.
func (a *App) composer(gtx C, t remote.Tab, open bool) D {
	c, th := &a.chat, a.th
	m := op.Record(gtx.Ops)
	var d D
	if !open || !t.Send {
		msg := "This tab is not open now."
		if open {
			msg = "This tab runs no agent, so it takes no messages from here."
		}
		d = layout.UniformInset(pad).Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return small(gtx, th, th.Muted, msg)
		})
	} else {
		hint := "Message " + sidebar.AgentName(model.Provider(t.Agent))
		switch {
		case t.State == "working" || t.State == "connecting" || t.State == "pending-approval":
			hint = "Queue a message"
		case t.State == "awaiting-input":
			hint = "Answer"
		}
		d = layout.Inset{Left: pad, Right: pad, Top: 8, Bottom: 8}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.End}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					gtx.Constraints.Max.Y = gtx.Dp(140)
					return field(gtx, th, &c.ed, hint, th.UIFont)
				}),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
						return button(gtx, th, &c.send, kit.Primary.When(strings.TrimSpace(c.ed.Text()) != ""), "Send")
					})
				}),
			)
		})
	}
	call := m.Stop()
	paint.FillShape(gtx.Ops, th.Sidebar, clip.Rect{Max: d.Size}.Op())
	divider(gtx, th.BorderSubtle)
	call.Add(gtx.Ops)
	return d
}

// asks reports whether t waits on the user, for the card above the box.
func asks(t remote.Tab) bool {
	return t.Answer || len(t.Requests) > 0 || slices.Contains([]string{"pending-approval", "plan-ready", "awaiting-input"}, t.State)
}
