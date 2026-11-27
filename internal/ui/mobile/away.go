package mobile

import (
	"context"
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// awayState is the Since you left card's taps and whether the reader
// dismissed it or scrolled through what they missed.
type awayState struct {
	jump, close widget.Clickable
	hid         string // the card dismissed here, by awayKey, until the tab list drops it
	read        string // the card whose first missed entry the reader scrolled up to
	mark        int    // the list row the log's marker sits above, -1 for none
	label       string // the marker's text, "Since you left · 47m"
	tall        bool   // what was missed is taller than the log's view: the card shows
}

// awayTall reports whether what the reader missed, from list row first
// on, is taller than the log's view, the list at its end at pos: that
// row starts above the view's top. first -1 is an entry not loaded yet.
func awayTall(first int, pos layout.Position) bool {
	return first < 0 || first < pos.First || first == pos.First && pos.Offset > 0
}

func awayKey(a *model.Away) string { return a.Chat + "@" + a.At.String() }

// phoneBeat is how often the open chat tells the computer it is on
// screen. The daemon stops counting a phone that went quiet 75 s after
// its last beat (phoneViewTTL), so a screen that went off, or an app the
// OS froze, stops counting as watching without saying so.
const phoneBeat = 30 * time.Second

// viewBeat tells the computer, every phoneBeat, that the app shows this
// chat, while it has the screen and the stream works; the App says when
// it leaves the foreground or the chat.
func (c *chatView) viewBeat(gtx C, ce chatEnv) {
	if !ce.Foreground || !ce.Open || !ce.Online || ce.Do == nil {
		c.beatAt = time.Time{} // beat again at once on the way back
		return
	}
	if now := ce.now(); c.beatAt.IsZero() || now.Sub(c.beatAt) >= phoneBeat {
		c.beatAt = now
		pane := c.pane
		ce.Do(func(ctx context.Context, api chatAPI) error { return api.View(ctx, pane, true) }, func(error) {})
	}
	gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(phoneBeat)})
}

// awayShown is the open tab's card when it draws.
func (c *chatView) awayShown(ce chatEnv) *model.Away {
	a := ce.Tab.Away
	if !ce.Open || a == nil || awayKey(a) == c.away.hid {
		return nil
	}
	return a
}

// awayHide dismisses card a here and on the computer.
func (c *chatView) awayHide(ce chatEnv, a *model.Away) {
	c.away.hid = awayKey(a)
	if ce.Do != nil {
		pane := ce.Tab.Pane
		ce.Do(func(ctx context.Context, api chatAPI) error { return api.AwayDismiss(ctx, pane) }, func(error) {})
	}
}

// awayEntry is the list row of the first entry the reader missed, -1
// when it is in history not loaded yet.
func (c *chatView) awayEntry(a *model.Away) int {
	for j, e := range c.data.Entries {
		if e.Seq >= a.Seq {
			if j == 0 && e.Seq > a.Seq {
				return -1
			}
			return j + 1
		}
	}
	return len(c.data.Entries) + 1
}

// awayMark notes, before the log draws, which row the marker sits above.
func (c *chatView) awayMark(ce chatEnv) {
	c.away.mark = -1
	if a := c.awayShown(ce); a != nil && !c.data.None && !c.data.Gone {
		c.away.mark, c.away.label = c.awayEntry(a), "Since you left · "+model.AwayFor(a.At.Sub(a.Since))
	}
}

// awayMarker is the log's marker: a rule with the time away in it.
func awayMarker(gtx C, th *theme.Theme, s string) D {
	rule := func(gtx C) D {
		return layout.Center.Layout(gtx, func(gtx C) D {
			sz := image.Pt(gtx.Constraints.Max.X, max(1, gtx.Dp(1)))
			paint.FillShape(gtx.Ops, theme.Mix(th.BorderSubtle, th.Muted, 0.35), clip.Rect{Max: sz}.Op())
			return D{Size: sz}
		})
	}
	return layout.Inset{Top: 6, Bottom: 14}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, rule),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: 10, Right: 10}.Layout(gtx, func(gtx C) D {
					return label(gtx, th, weight(th.UIFont, font.SemiBold), 13, th.Muted, 1, s)
				})
			}),
			layout.Flexed(1, rule),
		)
	})
}

// awayTrack hides the card once the reader scrolled up to the first
// missed entry and back down to the end.
func (c *chatView) awayTrack(ce chatEnv) {
	a := c.awayShown(ce)
	if a == nil {
		return
	}
	first := c.awayEntry(a)
	switch {
	case !c.list.Position.BeforeEnd && c.away.read == awayKey(a):
		c.awayHide(ce, a)
	case c.list.Position.BeforeEnd && c.list.Position.First <= max(0, first):
		c.away.read = awayKey(a)
	case !c.list.Position.BeforeEnd:
		c.away.tall = awayTall(first, c.list.Position)
	}
}

// awayCard is the open tab's Since you left card, above the composer
// when what was missed is taller than the log's view (the log's marker
// says the rest):
// how long the reader was away, the summary once it came, what the log
// shows, a tap to the first missed message, and the x that dismisses it.
func (c *chatView) awayCard(gtx C, th *theme.Theme, ce chatEnv) D {
	a := c.awayShown(ce)
	if a == nil || !c.away.tall {
		return D{}
	}
	if c.away.close.Clicked(gtx) {
		c.awayHide(ce, a)
		return D{}
	}
	if c.away.jump.Clicked(gtx) {
		c.list.Position = layout.Position{First: max(0, c.awayEntry(a)), BeforeEnd: true}
		c.away.read = awayKey(a)
	}
	head := "Since you left · " + model.AwayFor(a.At.Sub(a.Since))
	if now := model.AwayNow(model.AgentState(ce.Tab.State)); now != "" {
		head += " · " + now
	}
	w := []layout.Widget{func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return glyph(gtx, icClock, 16, th.Muted) }),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Flexed(1, func(gtx C) D { return label(gtx, th, weight(th.UIFont, font.SemiBold), 14, th.Muted, 1, head) }),
			layout.Rigid(func(gtx C) D {
				return iconButton(gtx, th, &c.away.close, func(gtx C, col color.NRGBA, size int) { sidebar.Icon(gtx, "x", size, col) })
			}),
		)
	}}
	text := func(s string, size unit.Sp, col color.NRGBA, lines int) {
		if s != "" {
			w = append(w, gap(4), func(gtx C) D { return label(gtx, th, th.UIFont, size, col, lines, s) })
		}
	}
	text(a.Status, 15.5, th.Fg, 3)
	if a.Asking {
		text("Summarizing…", 15.5, th.Muted, 1)
	} else {
		text(a.Needs, 15.5, th.Fg, 3)
	}
	text(strings.Join(model.AwayFacts(*a), " · "), 13.5, th.Muted, 2)
	if n := len(a.Edited); n > 0 {
		s := "Edited " + strings.Join(a.Edited, ", ")
		if more := a.EditedN - n; more > 0 {
			s += " and " + strconv.Itoa(more) + " more"
		}
		text(s, 13.5, th.Muted, 2)
	}
	if a.Failed != "" {
		text("Last failed: "+a.Failed, 13.5, th.Muted, 2)
	}
	w = append(w, func(gtx C) D {
		gtx.Constraints.Min.X = 0
		return layout.Inset{Left: -12}.Layout(gtx, func(gtx C) D { return linkButton(gtx, th, &c.away.jump, "Show what you missed") })
	})
	return layout.Inset{Left: 12, Right: 12, Bottom: 8, Top: 4}.Layout(gtx, func(gtx C) D {
		return box(gtx, th.Surface, th.BorderSubtle, 14, layout.Inset{Left: 14, Right: 4, Top: 4}, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			children := make([]layout.FlexChild, len(w))
			for i := range w {
				children[i] = layout.Rigid(w[i])
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	})
}
