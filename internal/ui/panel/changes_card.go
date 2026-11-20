package panel

import (
	"image"
	"strconv"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// cardFiles is how many files the change card lists before Show all.
const cardFiles = 6

// peekLines is how many lines of a file's diff its row's popover shows.
const peekLines = 14

// changeCard is a finished turn's edits at a glance: "Edited 2 files
// +5 −5" with View changes, then a row per file with its counts. A row's
// click opens its diff in the side panel; resting on it shows its first
// lines over the chat.
func (d *cdraw) changeCard(gtx layout.Context, b block) layout.Dimensions {
	th := d.th
	fs := d.t.changesOf(d, b)
	if len(fs) == 0 {
		return layout.Dimensions{}
	}
	turn := d.s.Content[b.lo].ID
	w := gtx.Constraints.Max.X
	px, py := gtx.Dp(cardPadX), gtx.Dp(cardPadY)
	view := d.t.click("chg:view:" + turn)
	for view.Clicked(gtx) {
		d.t.showChanges(turn, "")
	}

	// The head: the icon, the title and totals, View changes on the right.
	add, del := changeTotals(fs)
	bc, bd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return kit.Button(gtx, th, view, kit.Secondary, kit.Medium, "View changes")
	})
	isz := gtx.Dp(hit)
	headH := max(isz, bd.Size.Y) + 2*py
	rh := gtx.Dp(rowH)
	all := d.t.toggled(gtx, "chg:all:"+turn, false)
	shown := fs
	more := 0
	if len(fs) > cardFiles+1 && !all { // one more file than the cap shows instead of "1 more"
		shown, more = fs[:cardFiles], len(fs)-cardFiles
	}
	h := headH + len(shown)*rh + gtx.Dp(theme.SpaceS)
	if more > 0 || all && len(fs) > cardFiles+1 {
		h += rh
	}
	rad := gtx.Dp(theme.RadiusCard)
	r := image.Rect(0, 0, w, h)
	rrect(gtx, r, rad, th.Surface, th.BorderSubtle)
	cl := clip.UniformRRect(r.Inset(1), rad-1).Push(gtx.Ops)

	box := image.Rect(px, (headH-isz)/2, px+isz, (headH+isz)/2)
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(box, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	icon(gtx, box.Min.Add(image.Pt((isz-gtx.Dp(14))/2, (isz-gtx.Dp(14))/2)), "pencil", 14, th.Muted)
	at(gtx, image.Pt(w-px-bd.Size.X, (headH-bd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { bc.Add(gtx.Ops); return bd })
	tx := box.Max.X + gtx.Dp(theme.SpaceM)
	tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = max(0, w-tx-bd.Size.X-px-gtx.Dp(theme.SpaceM))
		return hrow(gtx, gtx.Dp(theme.SpaceS),
			func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, editedTitle(fs))
			},
			func(gtx layout.Context) layout.Dimensions { return d.counts(gtx, add, del, false) })
	})
	at(gtx, image.Pt(tx, (headH-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, headH-1), Max: image.Pt(w, headH)}.Op())

	// A row per file.
	y := headH + gtx.Dp(theme.SpaceXS)
	var peek func()
	for _, f := range shown {
		f := f
		ck := d.t.click("chg:file:" + turn + ":" + f.path)
		for ck.Clicked(gtx) {
			d.t.showChanges(turn, f.path)
		}
		ry := y
		at(gtx, image.Pt(0, ry), func(gtx layout.Context) layout.Dimensions {
			return clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
				rowFill(gtx, th, ck, image.Pt(w, rh))
				cc, cd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.counts(gtx, f.add, f.del, f.created) })
				at(gtx, image.Pt(w-px-cd.Size.X, (rh-cd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { cc.Add(gtx.Ops); return cd })
				room := max(0, w-2*px-cd.Size.X-gtx.Dp(theme.SpaceM))
				at(gtx, image.Pt(px, 0), func(gtx layout.Context) layout.Dimensions { return d.pathLine(gtx, f.path, room, rh, false) })
				return layout.Dimensions{Size: image.Pt(w, rh)}
			})
		})
		if d.peeking(gtx, "chg:file:"+turn+":"+f.path, ck.Hovered()) {
			peek = func() { d.peek(gtx, turn, f, ry, w) }
		}
		y += rh
	}
	if more > 0 || all && len(fs) > cardFiles+1 {
		label := "Show " + strconv.Itoa(more) + " more"
		ic := "chevron-down"
		if all {
			label, ic = "Show fewer", "chevron-right"
		}
		at(gtx, image.Pt(px-gtx.Dp(theme.SpaceS), y+(rh-gtx.Dp(hit))/2), func(gtx layout.Context) layout.Dimensions {
			return d.ghost(gtx, d.t.click("chg:all:"+turn), ic, label, false)
		})
	}
	cl.Pop()
	if peek != nil {
		peek()
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// peeking is whether row id's popover shows: the pointer rested on it for
// tipDelay.
func (d *cdraw) peeking(gtx layout.Context, id string, hovered bool) bool {
	s := &d.t.side
	if !hovered {
		if s.peekOf == id {
			s.peekOf = ""
		}
		return false
	}
	if s.peekOf != id {
		s.peekOf, s.peekSince = id, gtx.Now
	}
	if wait := tipDelay - gtx.Now.Sub(s.peekSince); wait > 0 {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(wait)})
		return false
	}
	return true
}

// peek is a file's first diff lines in a popover above its row at y, over
// whatever the chat draws there.
func (d *cdraw) peek(gtx layout.Context, turn string, f fileChange, y, w int) {
	th := d.th
	pw := min(w, gtx.Dp(640))
	pad := gtx.Dp(theme.SpaceS)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(pw-2*pad, 1<<20)}
	lines := len(changeLines(f))
	call, cd := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceXS),
			func(gtx layout.Context) layout.Dimensions { return d.changeDiff(gtx, "peek:"+turn, f, peekLines) },
			func(gtx layout.Context) layout.Dimensions {
				if lines <= peekLines {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, plural(lines-peekLines, "more line")+" · click for all")
				})
			})
	})
	ph := cd.Size.Y + 2*pad
	p := image.Pt(gtx.Dp(cardPadX), y-ph-gtx.Dp(theme.SpaceXS))
	m := op.Record(gtx.Ops)
	o := op.Offset(p).Push(gtx.Ops)
	rad := gtx.Dp(theme.RadiusPopover)
	kit.Surface(gtx, image.Rect(0, 0, pw, ph), rad, kit.Floating, th.BorderStrong, th.SurfaceElevated)
	inner := clip.UniformRRect(image.Rect(0, 0, pw, ph), rad).Push(gtx.Ops)
	at(gtx, image.Pt(pad, pad), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return cd })
	inner.Pop()
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
