package mobile

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// The chat screen draws in real dp, 412 across on a common phone, so its
// sizes are the design's: the caller undoes the sidebar's zoom.

// chatAPI is what the chat screen calls on the computer; *phone.Client is
// one.
type chatAPI interface {
	Send(ctx context.Context, pane, text string) (uint64, error)
	Command(ctx context.Context, pane, what, value string) error
	Answer(ctx context.Context, pane string, at int64, allow bool) error
	Approve(ctx context.Context, chat, request, decision, reason string) error
	Input(ctx context.Context, chat, request string, answers map[string][]string) error
	Older(ctx context.Context, pane string, before int64) ([]flow.Entry, bool, error)
}

// chatEnv is what the chat screen reads from the app each frame.
type chatEnv struct {
	Tab     remote.Tab // the open tab as the latest list shows it
	Open    bool       // the tab is in that list
	Online  bool
	Offline string // the offline line's text
	Retry   func() // reconnects now; nil hides Retry
	Models  []string
	Efforts []string
	// Host is the machine the tab runs on, "" for the paired computer
	// itself; Hosts are the machines, for the tag's color and up state.
	Host  string
	Hosts []sidebar.HostInfo
	// Do runs call off the window's goroutine and done on it after.
	Do func(call func(ctx context.Context, api chatAPI) error, done func(error))
	// Now is the clock, for tests and the demo; zero means time.Now.
	Now time.Time
	// OpenFile opens a markdown file the tab names in the reader, by its
	// path and #fragment; nil when the computer serves no files, and then
	// a path in a reply is plain text.
	OpenFile func(path, frag string)
}

func (e *chatEnv) now() time.Time {
	if e.Now.IsZero() {
		return time.Now()
	}
	return e.Now
}

// chatView is an open tab's chat.
type chatView struct {
	pane    string
	data    phone.Chat
	list    layout.List
	rows    map[int64]*entryRow
	loading bool   // a page of older entries is on its way
	fresh   bool   // new entries came while scrolled up
	files   bool   // replies link markdown paths: OpenFile is set
	tapped  string // a link tapped in a reply this frame

	backBtn, newBtn, retry widget.Clickable
	card                   cardState
	cmp                    composer
}

// entryRow is an entry's widgets, kept across frames by seq.
type entryRow struct {
	click  widget.Clickable
	open   bool             // a tool call shows its output
	file   widget.Clickable // a file tool's Open
	text   string           // the reply doc shows, parsed once
	files  bool             // its paths are links
	parsed bool
	doc    mdview.Doc
}

func newChat(pane string) chatView {
	c := chatView{pane: pane}
	c.list.Axis = layout.Vertical
	c.list.ScrollToEnd = true
	return c
}

// reset starts the chat over with a "reset" event.
func (c *chatView) reset(e phone.Entries) {
	c.data.Reset(e)
	c.rows = nil
	c.fresh = false
	c.list.Position = layout.Position{BeforeEnd: false}
}

// add puts in an "entries" event. New entries that come while the reader
// is scrolled up wait behind the New messages pill.
func (c *chatView) add(e phone.Entries) {
	if e.Model != "" {
		c.data.Model = e.Model
	}
	if c.data.Add(e.Entries) && c.list.Position.BeforeEnd {
		c.fresh = true
	}
}

// update moves the sent messages along with t, from a new tab list.
func (c *chatView) update(t remote.Tab, now time.Time) { c.data.Update(t, now) }

// handleBack is Back on the chat: it closes a menu, and false means
// there was none, so Back goes to the sidebar. A focused field is not a
// reason to stay: Android's keyboard takes the first Back itself to hide.
func (c *chatView) handleBack(gtx C) bool {
	if c.cmp.menu == "" {
		return false
	}
	c.cmp.menu = ""
	return true
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

// layout draws the chat and reports whether its Back button was pressed.
func (c *chatView) layout(gtx C, th *theme.Theme, env chatEnv) (closed bool) {
	if c.backBtn.Clicked(gtx) {
		return true
	}
	if c.retry.Clicked(gtx) && env.Retry != nil {
		env.Retry()
	}
	if c.newBtn.Clicked(gtx) {
		c.list.Position = layout.Position{BeforeEnd: false}
		c.fresh = false
	}
	if !c.list.Position.BeforeEnd {
		c.fresh = false
	}
	c.older(env)
	c.files = env.OpenFile != nil
	t := env.Tab
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	var top int // the composer's top, which the menus open above
	total := gtx.Constraints.Max.Y
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return c.header(gtx, th, env) }),
		layout.Rigid(func(gtx C) D {
			if env.Online {
				return D{}
			}
			return offlineLine(gtx, th, env.Offline, &c.retry, env.Retry != nil)
		}),
		layout.Flexed(1, func(gtx C) D {
			if !env.Online {
				defer paint.PushOpacity(gtx.Ops, 0.62).Pop()
			}
			return layout.Stack{Alignment: layout.S}.Layout(gtx,
				layout.Expanded(func(gtx C) D { return c.log(gtx, th, env) }),
				layout.Stacked(func(gtx C) D {
					if !c.fresh {
						return D{}
					}
					return c.newPill(gtx, th)
				}),
			)
		}),
		layout.Rigid(func(gtx C) D {
			if !env.Open || !asks(t) {
				return D{}
			}
			return c.askCard(gtx, th, env)
		}),
		layout.Rigid(func(gtx C) D {
			d := c.composer(gtx, th, env)
			top = total - d.Size.Y
			return d
		}),
	)
	c.menus(gtx, th, env, top)
	if l := c.tapped; l != "" {
		c.tapped = ""
		c.follow(gtx, env, l)
	}
	return false
}

// tap takes a link tapped in a reply, followed once the frame's layout is
// done.
func (c *chatView) tap(link string) { c.tapped = link }

// follow opens a markdown file in the reader when the computer serves
// files, and copies any other link: the app opens nothing else itself.
func (c *chatView) follow(gtx C, ce chatEnv, link string) {
	if strings.HasPrefix(link, "#") {
		return
	}
	if p, frag, ok := mdview.MarkdownLink(link); ok && ce.OpenFile != nil {
		ce.OpenFile(p, frag)
		return
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(link))})
}

// header is Back, the tab's name, and its state pill, agent and model.
func (c *chatView) header(gtx C, th *theme.Theme, env chatEnv) D {
	t := env.Tab
	name := t.Tab
	if !env.Open {
		name = "Tab"
	}
	h := gtx.Dp(64)
	d := layout.Inset{Left: 4, Right: 12}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, h)
		return midFlex(gtx,
			layout.Rigid(func(gtx C) D { return roundButton(gtx, th, &c.backBtn, icBack, 24, th.Fg) }),
			layout.Flexed(1, func(gtx C) D {
				return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return label(gtx, th, weight(th.UIFont, font.SemiBold), 18, th.Fg, 1, name)
						}),
						layout.Rigid(layout.Spacer{Height: 3}.Layout),
						layout.Rigid(func(gtx C) D {
							if !env.Open {
								return label(gtx, th, th.UIFont, 14, th.Muted, 1, "This tab is not open now.")
							}
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									if t.State == "" {
										return D{}
									}
									return layout.Inset{Right: 7}.Layout(gtx, func(gtx C) D { return statePill(gtx, th, t, th.Bg) })
								}),
								layout.Rigid(func(gtx C) D {
									if env.Host == "" {
										return D{}
									}
									return layout.Inset{Right: 7}.Layout(gtx, func(gtx C) D { return hostTag(gtx, th, env) })
								}),
								layout.Flexed(1, func(gtx C) D {
									return label(gtx, th, th.UIFont, 14, th.Muted, 1, c.agentLine(t))
								}),
							)
						}),
					)
				})
			}),
		)
	})
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, d.Size.Y-1), Max: d.Size}.Op())
	return d
}

// agentLine is "Claude Code · Opus 5.5": the agent, and its model by name.
func (c *chatView) agentLine(t remote.Tab) string {
	parts := []string{}
	if n := agentName(t.Agent); n != "" {
		parts = append(parts, n)
	}
	if m := c.cmp.modelShown(c.data.Model); m != "" {
		parts = append(parts, m)
	}
	return strings.Join(parts, " · ")
}

// agentName is the agent's name as the chat says it: Claude Code, not the
// sidebar's Claude.
func agentName(agent string) string {
	p := model.Provider(agent)
	if p == model.ProviderClaude {
		return "Claude Code"
	}
	if agent == "" {
		return ""
	}
	return sidebar.AgentName(p)
}

// statePill is the tab's state in the sidebar's colors, with a dot while
// it works.
func statePill(gtx C, th *theme.Theme, t remote.Tab, base color.NRGBA) D {
	s := model.AgentState(t.State)
	col := sidebar.StateColor(th, s)
	bg, fg := th.Chip(col, base)
	text := t.Label
	if text == "" {
		text = model.PillLabel(model.Activity{State: s})
	}
	dot := s == model.StateWorking || s == model.StateConnecting
	h := gtx.Dp(20)
	m := op.Record(gtx.Ops)
	d := layout.Inset{Left: 8, Right: 8}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.Y = h
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				if !dot {
					return D{}
				}
				return layout.Inset{Right: 5}.Layout(gtx, func(gtx C) D { return circle(gtx, fg, 6) })
			}),
			layout.Rigid(func(gtx C) D { return label(gtx, th, weight(th.UIFont, font.SemiBold), 12.5, fg, 1, text) }),
		)
	})
	call := m.Stop()
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(image.Rectangle{Max: d.Size}, d.Size.Y/2).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return d
}

// offlineText is the offline line's words, with the seconds until the
// stream tries again at retry.
func offlineText(retry, now time.Time) string {
	if left := retry.Sub(now); left > 0 {
		return fmt.Sprintf("Offline, reconnecting in %d s", int(math.Ceil(left.Seconds())))
	}
	return "Offline, reconnecting"
}

// offlineLine is the red line under the header while the stream is down,
// with Retry.
func offlineLine(gtx C, th *theme.Theme, text string, retry *widget.Clickable, canRetry bool) D {
	if text == "" {
		text = "Offline, reconnecting"
	}
	red := th.Readable(th.Red, th.Sidebar)
	bg := theme.Mix(th.Sidebar, th.Red, 0.09)
	m := op.Record(gtx.Ops)
	d := layout.Inset{Left: 16, Right: 6}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(52))
		return midFlex(gtx,
			layout.Rigid(func(gtx C) D { return glyph(gtx, icWifiOff, 18, red) }),
			layout.Rigid(layout.Spacer{Width: 10}.Layout),
			layout.Flexed(1, func(gtx C) D { return label(gtx, th, th.UIFont, 15, th.Fg, 2, text) }),
			layout.Rigid(func(gtx C) D {
				if !canRetry {
					return D{}
				}
				return linkButton(gtx, th, retry, "Retry")
			}),
		)
	})
	call := m.Stop()
	paint.FillShape(gtx.Ops, bg, clip.Rect{Max: d.Size}.Op())
	call.Add(gtx.Ops)
	paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Red, 0.22), clip.Rect{Min: image.Pt(0, d.Size.Y-1), Max: d.Size}.Op())
	return d
}

// textButton is a blue text button 48dp tall.
func linkButton(gtx C, th *theme.Theme, c *widget.Clickable, s string) D {
	return c.Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.Y = gtx.Dp(48)
			return layout.Center.Layout(gtx, func(gtx C) D {
				return label(gtx, th, weight(th.UIFont, font.SemiBold), 15, th.Readable(th.Blue, th.Sidebar), 1, s)
			})
		})
		call := m.Stop()
		if c.Pressed() {
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(image.Rectangle{Max: d.Size}, gtx.Dp(10)).Op(gtx.Ops))
		}
		call.Add(gtx.Ops)
		return d
	})
}

// older loads the page before the first entry once the top is near.
func (c *chatView) older(env chatEnv) {
	if c.loading || !c.data.More || len(c.data.Entries) == 0 || c.list.Position.First > 1 || env.Do == nil {
		return
	}
	c.loading = true
	pane, first := c.pane, c.data.First()
	var es []flow.Entry
	var more bool
	env.Do(func(ctx context.Context, api chatAPI) (err error) {
		es, more, err = api.Older(ctx, pane, first)
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
		c.pageIn(es, more)
	})
}

// pageIn puts a page of older entries above the first, keeping what was
// on screen where it was.
func (c *chatView) pageIn(es []flow.Entry, more bool) {
	n := c.data.Older(es, more)
	c.list.Position.First += n
}

// log is the scrolling chat: the loading line, the entries, the messages
// on their way and the Working line.
func (c *chatView) log(gtx C, th *theme.Theme, env chatEnv) D {
	n := 1 + len(c.data.Entries)
	if c.data.None {
		n++
	}
	if c.data.Gone {
		n++
	}
	for _, p := range c.data.Pending {
		if p.Sent && !p.Queued && p.Err == "" {
			n++
		}
	}
	working, busy := c.data.Working(env.Tab)
	if busy && env.Open {
		n++
	} else {
		working = ""
	}
	return c.list.Layout(gtx, n, func(gtx C, i int) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		in := layout.Inset{Left: 16, Right: 16, Bottom: 10}
		if i == 0 {
			in.Bottom = 0
		}
		return in.Layout(gtx, func(gtx C) D { return c.logItem(gtx, th, i, working) })
	})
}

func (c *chatView) logItem(gtx C, th *theme.Theme, i int, working string) D {
	if i == 0 {
		if c.loading {
			return loadingLine(gtx, th)
		}
		return D{Size: image.Pt(0, gtx.Dp(10))}
	}
	i--
	if c.data.None {
		if i == 0 {
			return label(gtx, th, th.UIFont, 15, th.Muted, 0, "No transcript to show. pitwall shows the state of a shell or of an agent without hooks, never its screen.")
		}
		i--
	}
	if c.data.Gone {
		if i == 0 {
			return label(gtx, th, th.UIFont, 15, th.Muted, 0, "This tab closed.")
		}
		i--
	}
	if i < len(c.data.Entries) {
		return c.entry(gtx, th, c.data.Entries[i])
	}
	i -= len(c.data.Entries)
	for _, p := range c.data.Pending {
		if !p.Sent || p.Queued || p.Err != "" {
			continue
		}
		if i == 0 {
			return prompt(gtx, th, p.Text, 0.6)
		}
		i--
	}
	return workingLine(gtx, th, working)
}

func loadingLine(gtx C, th *theme.Theme) D {
	gtx.Constraints.Min.Y = gtx.Dp(40)
	return layout.Center.Layout(gtx, func(gtx C) D {
		return midFlex(gtx,
			layout.Rigid(func(gtx C) D { return circle(gtx, theme.Mix(th.Bg, th.Primary, 0.7), 6) }),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(func(gtx C) D { return label(gtx, th, th.UIFont, 15, th.Muted, 1, "Loading earlier messages") }),
		)
	})
}

func workingLine(gtx C, th *theme.Theme, w string) D {
	gtx.Constraints.Min.Y = gtx.Dp(24)
	return midFlex(gtx,
		layout.Rigid(func(gtx C) D { return circle(gtx, th.Blue, 9) }),
		layout.Rigid(layout.Spacer{Width: 9}.Layout),
		layout.Flexed(1, func(gtx C) D { return label(gtx, th, th.UIFont, 15, th.Muted, 1, w) }),
	)
}

// newPill is the New messages button over the bottom of the log.
func (c *chatView) newPill(gtx C, th *theme.Theme) D {
	return layout.Inset{Bottom: 14}.Layout(gtx, func(gtx C) D {
		return c.newBtn.Layout(gtx, func(gtx C) D {
			m := op.Record(gtx.Ops)
			d := layout.Inset{Left: 14, Right: 14}.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min.Y = gtx.Dp(36)
				return midFlex(gtx,
					layout.Rigid(func(gtx C) D { return glyph(gtx, icDown, 16, th.Fg) }),
					layout.Rigid(layout.Spacer{Width: 6}.Layout),
					layout.Rigid(func(gtx C) D {
						return label(gtx, th, weight(th.UIFont, font.SemiBold), 14.5, th.Fg, 1, "New messages")
					}),
				)
			})
			call := m.Stop()
			fill := th.SurfaceElevated
			if c.newBtn.Pressed() {
				fill = theme.Mix(fill, th.Fg, 0.08)
			}
			kit.Surface(gtx, image.Rectangle{Max: d.Size}, d.Size.Y/2, kit.Floating, th.BorderStrong, fill)
			call.Add(gtx.Ops)
			return d
		})
	})
}

func (c *chatView) entry(gtx C, th *theme.Theme, e flow.Entry) D {
	switch e.Kind {
	case flow.EntryUser:
		return prompt(gtx, th, e.Text, 1)
	case flow.EntryAssistant:
		return c.reply(gtx, th, c.row(e.Seq), e.Text)
	case flow.EntryError:
		bg, fg := th.Chip(th.Red, th.Bg)
		return box(gtx, bg, theme.Mix(th.Bg, th.Red, 0.3), 12, layout.Inset{Left: 14, Right: 14, Top: 10, Bottom: 10}, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return label(gtx, th, th.UIFont, 15, fg, 0, e.Text)
		})
	case flow.EntryTool:
		return c.tool(gtx, th, c.row(e.Seq), e)
	}
	return D{}
}

// prompt is a message of the user's, on the right.
func prompt(gtx C, th *theme.Theme, s string, opacity float32) D {
	maxW := gtx.Constraints.Max.X * 84 / 100
	return layout.E.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = maxW
		gtx.Constraints.Min.X = 0
		if opacity < 1 {
			defer paint.PushOpacity(gtx.Ops, opacity).Pop()
		}
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 14, Right: 14, Top: 10, Bottom: 10}.Layout(gtx, func(gtx C) D {
			return widget.Label{WrapPolicy: text.WrapWords, LineHeight: 22.4, LineHeightScale: 1}.Layout(gtx, th.Shaper, th.UIFont, 16, s, colorOp(gtx, th.Fg))
		})
		call := m.Stop()
		r, small := gtx.Dp(16), gtx.Dp(4)
		paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.RRect{Rect: image.Rectangle{Max: d.Size}, NW: r, NE: r, SW: r, SE: small}.Op(gtx.Ops))
		call.Add(gtx.Ops)
		return d
	})
}

// reply draws an agent's markdown as the desktop's reader draws a file:
// parsed once for its text, laid out at the width each frame. Tapping a
// link opens a markdown file, or copies anything else.
func (c *chatView) reply(gtx C, th *theme.Theme, r *entryRow, src string) D {
	if !r.parsed || r.text != src || r.files != c.files {
		blocks := mdview.Parse([]byte(src))
		if c.files {
			linkPaths(blocks)
		}
		r.doc.Set(blocks)
		r.text, r.files, r.parsed = src, c.files, true
		r.doc.Size, r.doc.Follow = replySize, c.tap
	}
	return r.doc.Layout(gtx, th)
}

// replySize is a reply's text size, in the chat's real sp.
const replySize unit.Sp = 16.5

// tool is a call's 48dp line, its name and what it ran, which opens to its
// output.
func (c *chatView) tool(gtx C, th *theme.Theme, r *entryRow, e flow.Entry) D {
	if r.click.Clicked(gtx) {
		r.open = !r.open
	}
	_, _, md := mdview.MarkdownLink(e.Path)
	md = md && c.files
	if r.file.Clicked(gtx) && md {
		c.tap(e.Path)
	}
	return box(gtx, th.Surface, th.BorderSubtle, 12, layout.Inset{}, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return r.click.Layout(gtx, func(gtx C) D {
					gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(48))
					return layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx C) D {
						chev := icRight
						if r.open {
							chev = icDown
						}
						return midFlex(gtx,
							layout.Rigid(func(gtx C) D { return glyph(gtx, chev, 16, th.Muted) }),
							layout.Rigid(layout.Spacer{Width: 8}.Layout),
							layout.Rigid(func(gtx C) D {
								return label(gtx, th, weight(th.UIFont, font.SemiBold), 15, th.Muted, 1, e.Tool)
							}),
							layout.Rigid(layout.Spacer{Width: 8}.Layout),
							layout.Flexed(1, func(gtx C) D {
								gtx.Constraints.Min.X = 0
								return label(gtx, th, th.MonoFont, 14, th.Fg, 1, e.Arg)
							}),
							layout.Rigid(func(gtx C) D {
								if md {
									return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D { return linkButton(gtx, th, &r.file, "Open") })
								}
								return D{}
							}),
							layout.Rigid(func(gtx C) D {
								switch {
								case !e.Done:
									return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return circle(gtx, th.Blue, 9) })
								case e.Failed:
									return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
										return label(gtx, th, th.UIFont, 14, th.Readable(th.Red, th.Surface), 1, "failed")
									})
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
					return box(gtx, th.Bg, th.BorderSubtle, 9, layout.Inset{Left: 12, Right: 12, Top: 10, Bottom: 10}, func(gtx C) D {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return widget.Label{MaxLines: 40, Truncator: "…", LineHeight: 19.5, LineHeightScale: 1}.Layout(gtx, th.Shaper, f, 13, mdview.Detab(out), colorOp(gtx, toolOut))
					})
				})
			}),
		)
	})
}

var toolOut = color.NRGBA{R: 0xcf, G: 0xd2, B: 0xd8, A: 0xff}

// asks reports whether t waits on the user, for the card above the box.
func asks(t remote.Tab) bool {
	return t.Answer || len(t.Requests) > 0 || slices.Contains([]string{"pending-approval", "plan-ready", "awaiting-input"}, t.State)
}

// The sidebar.Icon glyphs the chat draws.
const (
	icBack    = "chevron-left"
	icDown    = "chevron-down"
	icRight   = "chevron-right"
	icAlert   = "circle-alert"
	icClock   = "clock"
	icLoader  = "loader"
	icCheck   = "check"
	icUp      = "arrow-up"
	icWifiOff = "wifi-off"
)

// glyph draws sidebar.Icon's name in a size dp box.
func glyph(gtx C, name string, size unit.Dp, col color.NRGBA) D {
	return sidebar.Icon(gtx, name, gtx.Dp(size), col)
}

// spinner is the loader icon, turning once a second.
func spinner(gtx C, size unit.Dp, col color.NRGBA) D {
	s := gtx.Dp(size)
	a := float32(gtx.Now.UnixMilli()%1000) / 1000 * 2 * math.Pi
	defer op.Affine(f32.AffineId().Rotate(f32.Pt(float32(s)/2, float32(s)/2), a)).Push(gtx.Ops).Pop()
	gtx.Execute(op.InvalidateCmd{})
	return sidebar.Icon(gtx, icLoader, s, col)
}

// circle is a filled dot of size dp.
func circle(gtx C, col color.NRGBA, size unit.Dp) D {
	s := gtx.Dp(size)
	paint.FillShape(gtx.Ops, col, clip.Ellipse(image.Rect(0, 0, s, s)).Op(gtx.Ops))
	return D{Size: image.Pt(s, s)}
}

// roundButton is a 48dp round button around a lucide icon.
func roundButton(gtx C, th *theme.Theme, c *widget.Clickable, d string, size unit.Dp, col color.NRGBA) D {
	return c.Layout(gtx, func(gtx C) D {
		s := gtx.Dp(48)
		if c.Pressed() {
			paint.FillShape(gtx.Ops, th.Pressed, clip.Ellipse(image.Rect(0, 0, s, s)).Op(gtx.Ops))
		}
		is := gtx.Dp(size)
		o := op.Offset(image.Pt((s-is)/2, (s-is)/2)).Push(gtx.Ops)
		glyph(gtx, d, size, col)
		o.Pop()
		return D{Size: image.Pt(s, s)}
	})
}

// midFlex is a row of children centred on its height, at least gtx's
// Min.Y: a Flex alone would stretch each child to that height and draw
// its text at the top.
func midFlex(gtx C, children ...layout.FlexChild) D {
	h := gtx.Constraints.Min.Y
	gtx.Constraints.Min.Y = 0
	m := op.Record(gtx.Ops)
	d := layout.Flex{Alignment: layout.Middle}.Layout(gtx, children...)
	call := m.Stop()
	off := op.Offset(image.Pt(0, max(0, (h-d.Size.Y)/2))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
	return D{Size: image.Pt(d.Size.X, max(h, d.Size.Y))}
}

// hostTag is the desktop's host tag for a tab on another machine, scaled
// from its 17dp to the state pill's 20dp so the two sit level.
func hostTag(gtx C, th *theme.Theme, env chatEnv) D {
	gtx.Metric.PxPerDp *= 20.0 / 17
	gtx.Metric.PxPerSp *= 20.0 / 17
	gtx.Constraints.Min = image.Point{}
	return sidebar.HostChip(gtx, th, env.Hosts, env.Host, th.Bg)
}
