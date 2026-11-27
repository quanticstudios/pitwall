package panel

import (
	"image"
	"image/color"
	"slices"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// away is the Since you left card's state in the transcript.
type away struct {
	jump string // the first missed item to bring into view
	read string // the card whose first missed item the reader scrolled to
	hid  string // the card the reader dismissed or read through, by awayKey
	out  bool   // hid is new: AwayDismissed has not said so yet
	row  int    // the row the log's marker sits on, -1 for none
	tall bool   // what was missed is taller than the log's view: the card shows
}

// awayTall reports whether what the reader missed, from the list
// element first on, is taller than the log's view, the list at its end
// at pos: that element starts above the view's top. first -1 is an item
// not loaded yet, older than anything in view.
func awayTall(first int, pos layout.Position) bool {
	return first < 0 || first < pos.First || first == pos.First && pos.Offset > 0
}

// awayRowOf is the row the marker for content item i sits on: the row
// that draws i, the work log that holds it, else the first row after it;
// -1 when none does.
func (t *Transcript) awayRowOf(i int) int {
	for k, r := range t.rows {
		if r.isTail() || r.res != nil {
			continue
		}
		lo := rowItem(r)
		if lo == i || r.seg != segItem && r.seg != segProse && r.b.lo <= i && i < r.b.hi || lo > i {
			return k
		}
	}
	return -1
}

// awayMark notes the marker's row for this frame's rows.
func (t *Transcript) awayMark(s *session.Snapshot) {
	t.away.row = -1
	if a := t.awayShown(s); a != nil {
		if i := slices.IndexFunc(s.Content, func(it session.Item) bool { return it.ID == a.Item }); i >= 0 {
			t.away.row = t.awayRowOf(i)
		}
	}
}

// awayMarker draws the log's marker over the column at y, a rule with
// "Since you left · 47m" in it, and returns its height.
func (t *Transcript) awayMarker(gtx layout.Context, d *cdraw, x0, col, y int) int {
	a := t.Away
	th := d.th
	c, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, "Since you left · "+model.AwayFor(a.At.Sub(a.Since)))
	})
	h := ld.Size.Y + gtx.Dp(theme.SpaceM)
	mid := y + h/2
	lx := x0 + (col-ld.Size.X)/2
	gap := gtx.Dp(theme.SpaceS)
	rule := theme.Mix(th.BorderSubtle, th.Muted, 0.35)
	paint.FillShape(gtx.Ops, rule, clip.Rect{Min: image.Pt(x0, mid), Max: image.Pt(max(x0, lx-gap), mid+max(1, gtx.Dp(1)))}.Op())
	paint.FillShape(gtx.Ops, rule, clip.Rect{Min: image.Pt(min(x0+col, lx+ld.Size.X+gap), mid), Max: image.Pt(x0+col, mid+max(1, gtx.Dp(1)))}.Op())
	at(gtx, image.Pt(lx, mid-ld.Size.Y/2), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return ld })
	return h
}

// awayMeasure decides, with the list at its end, whether the card shows
// above the composer: only when the marker's row is out of view.
func (t *Transcript) awayMeasure(gtx layout.Context, s *session.Snapshot) {
	if t.awayShown(s) == nil || t.list.Position.BeforeEnd {
		return
	}
	first := -1
	if t.away.row >= 0 {
		first = t.away.row + 1 // the list's first element is the space above the rows
	}
	if tall := awayTall(first, t.list.Position); tall != t.away.tall {
		t.away.tall = tall
		gtx.Execute(op.InvalidateCmd{}) // the composer's band laid out before this
	}
}

// awayKey tells one card from the next return's.
func awayKey(a *model.Away) string { return a.Chat + "@" + a.At.String() }

// awayShown is t.Away when its card draws: not dismissed, and Claude
// Code wrote no recap of its own since, which says the same.
func (t *Transcript) awayShown(s *session.Snapshot) *model.Away {
	a := t.Away
	if a == nil || awayKey(a) == t.away.hid {
		return nil
	}
	for i := range s.Content {
		it := &s.Content[i]
		if it.Kind == session.KindSystem && it.Phase == session.PhaseRecap && itemTime(it).After(a.Since) {
			return nil
		}
	}
	return a
}

// AwayDismissed reports, once, that the reader dismissed the card or
// scrolled through what they missed: the window tells the daemon.
func (t *Transcript) AwayDismissed() bool {
	out := t.away.out
	t.away.out = false
	return out
}

func (t *Transcript) hideAway() {
	if t.Away != nil {
		t.away.hid, t.away.out = awayKey(t.Away), true
	}
}

// awayJump brings the first missed item into view once Jump asked for
// it, reading older history until the chat has it.
func (t *Transcript) awayJump(s *session.Snapshot) {
	if t.away.jump == "" {
		return
	}
	i := slices.IndexFunc(s.Content, func(it session.Item) bool { return it.ID == t.away.jump })
	if i < 0 {
		if t.Older == nil || !t.Older() {
			t.away.jump = ""
		}
		return
	}
	if k := t.findRow(s, i); k >= 0 {
		t.list.Position.First, t.list.Position.Offset, t.list.Position.BeforeEnd = k+1, 0, true
		t.away.jump = ""
	}
}

// awayTrack hides the card once the reader scrolled up to the first
// missed item and back down to the end.
func (t *Transcript) awayTrack(s *session.Snapshot) {
	a := t.awayShown(s)
	if a == nil {
		return
	}
	if !t.list.Position.BeforeEnd {
		if t.away.read == awayKey(a) {
			t.hideAway()
		}
		return
	}
	i := slices.IndexFunc(s.Content, func(it session.Item) bool { return it.ID == a.Item })
	if k := t.list.Position.First - 1; i >= 0 && k >= 0 && k < len(t.rows) {
		if r := rowItem(t.rows[k]); r >= 0 && r <= i {
			t.away.read = awayKey(a)
		}
	}
}

// AwayCard draws t.Away for above the composer, as wide as the
// transcript's column, when what was missed is taller than the log's
// view (the log's marker says the rest): how long the reader was away, what the log shows
// since, the agent's state now, and the summary once it came. Jump
// brings the first missed message into view; the x dismisses it. Zero
// size when there is no card.
func (t *Transcript) AwayCard(gtx layout.Context, th *theme.Theme, s *session.Snapshot) layout.Dimensions {
	a := t.awayShown(s)
	if a == nil || !t.away.tall {
		return layout.Dimensions{}
	}
	if t.open == nil {
		t.init()
	}
	jump, closeBtn := t.click("away-jump"), t.click("away-close")
	for jump.Clicked(gtx) {
		t.away.jump = a.Item
	}
	for closeBtn.Clicked(gtx) {
		t.hideAway()
		return layout.Dimensions{}
	}
	d := &cdraw{t: t, th: th, s: s}
	size := gtx.Constraints.Max
	col := max(0, size.X-2*gtx.Dp(theme.SpaceXL))
	if t.Width > 0 {
		col = min(col, gtx.Dp(t.Width))
	}
	d.col = col
	x0 := (size.X - col) / 2
	px, py := gtx.Dp(cardPadX), gtx.Dp(cardPadY)-gtx.Dp(theme.SpaceXS)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, col-2*px), size.Y)}
	small, body := th.Sp(theme.Small), th.Sp(theme.Body)
	head := "Since you left · " + model.AwayFor(a.At.Sub(a.Since))
	if t.AwayNow != "" {
		head += " · " + t.AwayNow
	}
	var edited, failed string
	if n := len(a.Edited); n > 0 {
		edited = "Edited " + strings.Join(a.Edited, ", ")
		if more := a.EditedN - n; more > 0 {
			edited += " and " + strconv.Itoa(more) + " more"
		}
	}
	if a.Failed != "" {
		failed = "Last failed: " + a.Failed
	}
	needs, needsFg := a.Needs, th.Fg
	if a.Asking {
		needs, needsFg = "Summarizing…", th.Muted
	}
	// line is txt in up to two lines, nil for none.
	line := func(txt string, fg color.NRGBA) layout.Widget {
		if txt == "" {
			return nil
		}
		return func(gtx layout.Context) layout.Dimensions { return label(gtx, th, th.UIFont, body, fg, txt, 2) }
	}
	c, bd := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.Space2XS),
			func(gtx layout.Context) layout.Dimensions {
				return row(gtx,
					part{w: func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: gtx.Metric.PxToDp((gtx.Dp(hit) - gtx.Sp(small)) / 2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return hrow(gtx, gtx.Dp(theme.SpaceXS+theme.Space2XS),
								func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, "clock", gtx.Dp(12), th.Muted) },
								func(gtx layout.Context) layout.Dimensions {
									return d.lbl(gtx, medium(th.UIFont), small, th.Muted, head)
								})
						})
					}, flex: true},
					part{w: func(gtx layout.Context) layout.Dimensions {
						return d.ghost(gtx, jump, "arrow-up", "First missed", false)
					}, gap: gtx.Dp(theme.SpaceXS)},
					part{w: func(gtx layout.Context) layout.Dimensions { return d.ghost(gtx, closeBtn, "x", "", false) }})
			},
			line(a.Status, th.Fg),
			line(needs, needsFg),
			line(strings.Join(model.AwayFacts(*a), " · "), th.Muted),
			line(edited, th.Muted),
			line(failed, th.Muted),
		)
	})
	h := bd.Size.Y + 2*py
	return at(gtx, image.Pt(x0, gtx.Dp(theme.SpaceXS)), func(gtx layout.Context) layout.Dimensions {
		rrect(gtx, image.Rect(0, 0, col, h), gtx.Dp(theme.RadiusCard), th.Surface, th.BorderSubtle)
		at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return bd })
		return layout.Dimensions{Size: image.Pt(col, h+gtx.Dp(theme.SpaceXS))}
	})
}
