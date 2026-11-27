package mobile

import (
	"context"
	"image"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// screenView is a terminal tab's screen, read about once a second while it
// shows, and the key bar that drives it, for a menu pitwall cannot answer.
type screenView struct {
	open    bool
	text    string
	read    bool      // a read came back
	reading bool      // a read is on its way
	next    time.Time // when the next read may go
	readErr string    // why the last read failed
	keyErr  string    // why the last key did not go in

	back         widget.Clickable
	keys         [len(screenKeys)]widget.Clickable
	vlist, hlist layout.List
}

// screenKey is one key of the bar: its send-key name, and its label or
// icon.
type screenKey struct{ name, label, icon string }

// screenKeys are the bar's keys, in its rows (screenRows); each is one of
// remote.Keys.
var screenKeys = [...]screenKey{
	{"esc", "Esc", ""}, {"tab", "Tab", ""}, {"shift+tab", "Shift+Tab", ""}, {"ctrl+c", "Ctrl+C", ""}, {"enter", "Enter", ""},
	{"left", "", icBack}, {"up", "", "chevron-up"}, {"down", "", icDown}, {"right", "", icRight},
	{"1", "1", ""}, {"2", "2", ""}, {"3", "3", ""}, {"4", "4", ""}, {"5", "5", ""}, {"6", "6", ""}, {"7", "7", ""}, {"8", "8", ""}, {"9", "9", ""},
}

var screenRows = [][2]int{{0, 5}, {5, 9}, {9, 18}}

const (
	screenEvery = time.Second            // between reads while the view shows
	keySettle   = 150 * time.Millisecond // from a key to the read that shows what it did
)

// show opens the view afresh, its bottom in sight, where menus draw.
func (s *screenView) show() {
	*s = screenView{open: true}
	s.vlist.Axis, s.vlist.ScrollToEnd = layout.Vertical, true
	s.hlist.Axis = layout.Horizontal
}

// due reports whether a read goes at now: the view shows, none is on its
// way, and next has come.
func (s *screenView) due(now time.Time) bool { return s.open && !s.reading && !now.Before(s.next) }

// readScreen reads the screen when due and asks for the frame of the next
// read. No frames come while the app is in the background, so no reads do.
func (c *chatView) readScreen(gtx C, env chatEnv) {
	s := &c.scr
	if !env.Online || env.Do == nil {
		return
	}
	if s.due(gtx.Now) {
		s.reading, s.next = true, gtx.Now.Add(screenEvery)
		pane := c.pane
		var text string
		env.Do(func(ctx context.Context, api chatAPI) (err error) {
			text, err = api.Screen(ctx, pane)
			return err
		}, func(err error) {
			if c.pane != pane {
				return
			}
			s.reading = false
			if err != nil {
				s.readErr = err.Error()
				return
			}
			s.text, s.read, s.readErr = text, true, ""
		})
	}
	if !s.reading {
		gtx.Execute(op.InvalidateCmd{At: s.next})
	}
}

// pressKey sends key to the tab, then reads the screen soon after to show
// what it did.
func (c *chatView) pressKey(env chatEnv, key string) {
	if env.Do == nil {
		return
	}
	pane, s := c.pane, &c.scr
	env.Do(func(ctx context.Context, api chatAPI) error { return api.Key(ctx, pane, key) }, func(err error) {
		if c.pane != pane {
			return
		}
		s.keyErr = ""
		if err != nil {
			s.keyErr = err.Error()
			return
		}
		s.next = time.Now().Add(keySettle)
	})
}

// stopTurn interrupts the agent's turn, as the window's Stop does.
func (c *chatView) stopTurn(env chatEnv) {
	if env.Do == nil {
		return
	}
	pane, k := c.pane, &c.cmp
	k.stopping, k.notSent = true, ""
	env.Do(func(ctx context.Context, api chatAPI) error { return api.Stop(ctx, pane) }, func(err error) {
		if c.pane != pane {
			return
		}
		k.stopping = false
		if err != nil {
			k.notSent = "Stop: " + err.Error()
		}
	})
}

// screenSize is the text size that fits text's widest line in width dp,
// between 9 and 13: a wider screen scrolls sideways.
func screenSize(text string, width float32) unit.Sp {
	cols := 0
	for line := range strings.SplitSeq(text, "\n") {
		cols = max(cols, utf8.RuneCountInString(line))
	}
	if cols == 0 {
		return 13
	}
	// ponytail: a monospace advance is about 0.6 em in the fonts the app ships.
	return unit.Sp(min(13, max(9, width/(float32(cols)*0.6))))
}

// screenLayout draws the Screen view over the chat: Back, the screen, and
// the key bar.
func (c *chatView) screenLayout(gtx C, th *theme.Theme, env chatEnv) {
	s := &c.scr
	if s.back.Clicked(gtx) {
		s.open = false
		return
	}
	for i := range screenKeys {
		if s.keys[i].Clicked(gtx) && env.Online {
			c.pressKey(env, screenKeys[i].name)
		}
	}
	c.readScreen(gtx, env)
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return s.header(gtx, th, env) }),
		layout.Rigid(func(gtx C) D {
			if env.Online {
				return D{}
			}
			return offlineLine(gtx, th, env.Offline, &c.retry, env.Retry != nil)
		}),
		layout.Flexed(1, func(gtx C) D { return s.body(gtx, th) }),
		layout.Rigid(func(gtx C) D {
			msg := s.keyErr
			if msg == "" {
				msg = s.readErr
			}
			if msg == "" {
				return D{}
			}
			red := th.Readable(th.Red, th.Bg)
			return layout.Inset{Left: 16, Right: 16, Bottom: 8}.Layout(gtx, func(gtx C) D {
				return midFlex(gtx,
					layout.Rigid(func(gtx C) D { return glyph(gtx, icAlert, 16, red) }),
					layout.Rigid(layout.Spacer{Width: 8}.Layout),
					layout.Flexed(1, func(gtx C) D { return label(gtx, th, th.UIFont, 14, red, 2, msg) }),
				)
			})
		}),
		layout.Rigid(func(gtx C) D { return s.keyBar(gtx, th, env.Online) }),
	)
}

func (s *screenView) header(gtx C, th *theme.Theme, env chatEnv) D {
	name := env.Tab.Tab
	if !env.Open {
		name = "Tab"
	}
	d := layout.Inset{Left: 4, Right: 12}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(64))
		return midFlex(gtx,
			layout.Rigid(func(gtx C) D { return roundButton(gtx, th, &s.back, icBack, 24, th.Fg) }),
			layout.Flexed(1, func(gtx C) D {
				return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return label(gtx, th, weight(th.UIFont, font.SemiBold), 18, th.Fg, 1, name) }),
						layout.Rigid(layout.Spacer{Height: 3}.Layout),
						layout.Rigid(func(gtx C) D {
							return label(gtx, th, th.UIFont, 14, th.Muted, 1, "Screen, as the terminal shows it now")
						}),
					)
				})
			}),
		)
	})
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(0, d.Size.Y-1), Max: d.Size}.Op())
	return d
}

// body is the screen's text in a dark box, scrolling both ways.
func (s *screenView) body(gtx C, th *theme.Theme) D {
	return layout.Inset{Left: 10, Right: 10, Top: 10, Bottom: 10}.Layout(gtx, func(gtx C) D {
		size := gtx.Constraints.Max
		rr := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(12))
		paint.FillShape(gtx.Ops, th.Surface, rr.Op(gtx.Ops))
		defer rr.Push(gtx.Ops).Pop()
		layout.UniformInset(12).Layout(gtx, func(gtx C) D {
			if !s.read {
				return label(gtx, th, th.UIFont, 15, th.Muted, 0, "Reading the screen")
			}
			sp := screenSize(s.text, float32(gtx.Constraints.Max.X)/gtx.Metric.PxPerDp)
			return s.vlist.Layout(gtx, 1, func(gtx C, _ int) D {
				return s.hlist.Layout(gtx, 1, func(gtx C, _ int) D {
					return widget.Label{LineHeight: sp * 1.3, LineHeightScale: 1}.Layout(gtx, th.Shaper, th.MonoFont, sp, s.text, colorOp(gtx, toolOut))
				})
			})
		})
		return D{Size: size}
	})
}

// keyBar is the keys in their rows, 44dp tall; offline they are dimmed.
func (s *screenView) keyBar(gtx C, th *theme.Theme, on bool) D {
	if !on {
		defer paint.PushOpacity(gtx.Ops, 0.45).Pop()
	}
	return layout.Inset{Left: 10, Right: 10, Bottom: 10}.Layout(gtx, func(gtx C) D {
		rows := make([]layout.FlexChild, 0, len(screenRows))
		for _, r := range screenRows {
			rows = append(rows, layout.Rigid(func(gtx C) D { return s.keyRow(gtx, th, r[0], r[1]) }))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})
}

func (s *screenView) keyRow(gtx C, th *theme.Theme, from, to int) D {
	n, gap, h := to-from, gtx.Dp(6), gtx.Dp(44)
	w := (gtx.Constraints.Max.X - gap*(n-1)) / n
	for i := from; i < to; i++ {
		o := op.Offset(image.Pt((i-from)*(w+gap), 0)).Push(gtx.Ops)
		cg := gtx
		cg.Constraints = layout.Exact(image.Pt(w, h))
		k, b := screenKeys[i], &s.keys[i]
		b.Layout(cg, func(gtx C) D {
			fill := th.SurfaceSecondary
			if b.Pressed() {
				fill = th.Pressed
			}
			paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, gtx.Dp(10)).Op(gtx.Ops))
			return layout.Center.Layout(gtx, func(gtx C) D {
				if k.icon != "" {
					return glyph(gtx, k.icon, 20, th.Fg)
				}
				return label(gtx, th, weight(th.UIFont, font.SemiBold), 15, th.Fg, 1, k.label)
			})
		})
		o.Pop()
	}
	return D{Size: image.Pt(gtx.Constraints.Max.X, h+gap)}
}
