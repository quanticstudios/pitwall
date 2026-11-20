package panel

import (
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// cardCache is a command output's card, parsed once per text.
type cardCache struct {
	text string
	card any // a ctxCard, useCard or statusCard; nil for none
}

// outCard is out's card, nil when no parser reads it.
func (d *cdraw) outCard(id, out string) any {
	if c, ok := d.t.cards[id]; ok && c.text == out {
		return c.card
	}
	var card any
	if c, ok := parseContext(out); ok {
		card = c
	} else if u, ok := parseUsage(out); ok {
		card = u
	} else if s, ok := parseStatus(out); ok {
		card = s
	}
	if d.t.cards == nil {
		d.t.cards = map[string]cardCache{}
	}
	d.t.cards[id] = cardCache{out, card}
	return card
}

// cmdCard draws a parsed output's card, no wider than cardMax.
func (d *cdraw) cmdCard(gtx layout.Context, id string, card any) layout.Dimensions {
	var parts []layout.Widget
	switch c := card.(type) {
	case ctxCard:
		parts = d.contextParts(id, c)
	case useCard:
		parts = d.usageParts(id, c)
	case statusCard:
		parts = d.statusParts(id, c)
	}
	return d.cardFrame(gtx, parts...)
}

// cardMax is a command card's widest: past it the bars only stretch.
const cardMax = 600

// cardFrame is a card on the surface, its parts a step apart, clipped to
// its corners.
func (d *cdraw) cardFrame(gtx layout.Context, parts ...layout.Widget) layout.Dimensions {
	th := d.th
	full := gtx.Constraints.Max.X
	w := min(full, gtx.Dp(cardMax))
	px, py := gtx.Dp(cardPadX), gtx.Dp(cardPadY)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*px), gtx.Constraints.Max.Y)}
	c, bd := measure(g, func(gtx layout.Context) layout.Dimensions {
		return vstack(gtx, gtx.Dp(theme.SpaceS), parts...)
	})
	r := image.Rect(0, 0, w, bd.Size.Y+2*py)
	rad := gtx.Dp(theme.RadiusCard)
	rrect(gtx, r, rad, th.Surface, th.BorderSubtle)
	cl := clip.UniformRRect(r, rad).Push(gtx.Ops)
	at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { c.Add(gtx.Ops); return bd })
	cl.Pop()
	return layout.Dimensions{Size: image.Pt(full, r.Max.Y)}
}

// cardHead is a card's title line: an icon, the title, a muted aside,
// and a figure on the right.
func (d *cdraw) cardHead(ic, title, aside, right string) layout.Widget {
	th := d.th
	return func(gtx layout.Context) layout.Dimensions {
		left := func(gtx layout.Context) layout.Dimensions {
			isz := gtx.Dp(14)
			tc, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Body), th.Fg, title)
			})
			x := isz + gtx.Dp(theme.SpaceS)
			h := td.Size.Y
			icon(gtx, image.Pt(0, (h-isz)/2), ic, 14, th.Muted)
			at(gtx, image.Pt(x, 0), func(gtx layout.Context) layout.Dimensions { tc.Add(gtx.Ops); return td })
			x += td.Size.X
			if aside != "" {
				x += gtx.Dp(theme.SpaceS)
				g := gtx
				g.Constraints.Max.X = max(0, gtx.Constraints.Max.X-x)
				ac, ad := measure(g, func(gtx layout.Context) layout.Dimensions {
					return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Muted, aside)
				})
				at(gtx, image.Pt(x, (h-ad.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { ac.Add(gtx.Ops); return ad })
				x += ad.Size.X
			}
			return layout.Dimensions{Size: image.Pt(x, h)}
		}
		return d.split(gtx, left, d.muted(right))
	}
}

// muted is one line of small muted text.
func (d *cdraw) muted(txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		if txt == "" {
			return layout.Dimensions{}
		}
		return d.lbl(gtx, d.th.UIFont, d.th.Sp(theme.Small), d.th.Muted, txt)
	}
}

// split draws left and right on one center line, right flush right;
// left gets what right leaves.
func (d *cdraw) split(gtx layout.Context, left, right layout.Widget) layout.Dimensions {
	w := gtx.Constraints.Max.X
	gtx.Constraints.Min = image.Point{}
	rc, rd := measure(gtx, right)
	g := gtx
	g.Constraints.Max.X = max(0, w-rd.Size.X-gtx.Dp(theme.SpaceM))
	lc, ld := measure(g, left)
	h := max(ld.Size.Y, rd.Size.Y)
	at(gtx, image.Pt(0, (h-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
	at(gtx, image.Pt(w-rd.Size.X, (h-rd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { rc.Add(gtx.Ops); return rd })
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// track is a bar's empty part on a card.
func (d *cdraw) track() color.NRGBA { return theme.Mix(d.th.Surface, d.th.Fg, 0.08) }

// bar is a 4dp bar filled to used percent, warning yellow from 80 and
// danger red from 95.
func (d *cdraw) bar(used float64) layout.Widget {
	th := d.th
	return func(gtx layout.Context) layout.Dimensions {
		w, h := gtx.Constraints.Max.X, gtx.Dp(theme.SpaceXS)
		col := th.Blue
		switch {
		case used >= 95:
			col = th.Red
		case used >= 80:
			col = th.Yellow
		}
		r := image.Rect(0, 0, w, h)
		paint.FillShape(gtx.Ops, d.track(), clip.UniformRRect(r, h/2).Op(gtx.Ops))
		if used > 0 {
			r.Max.X = max(h, int(float64(w)*min(used, 100)/100))
			paint.FillShape(gtx.Ops, col, clip.UniformRRect(r, h/2).Op(gtx.Ops))
		}
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
}

// fold is a card's Details row and, open, its body under it.
func (d *cdraw) fold(id string, body layout.Widget) layout.Widget {
	th := d.th
	return func(gtx layout.Context) layout.Dimensions {
		key := "card:" + id
		open := d.t.toggled(gtx, key, false)
		ck := d.t.click(key)
		w, h := gtx.Constraints.Max.X, gtx.Dp(hit)
		head := clickable(gtx, ck, func(gtx layout.Context) layout.Dimensions {
			c := th.Muted
			if ck.Hovered() {
				c = th.Fg
			}
			chev := "chevron-right"
			if open {
				chev = "chevron-down"
			}
			icon(gtx, image.Pt(0, (h-gtx.Dp(12))/2), chev, 12, c)
			lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), c, "Details")
			})
			at(gtx, image.Pt(gtx.Dp(12+theme.SpaceXS), (h-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
			return layout.Dimensions{Size: image.Pt(w, h)}
		})
		if !open {
			return head
		}
		bd := at(gtx, image.Pt(0, head.Size.Y), body)
		return layout.Dimensions{Size: image.Pt(w, head.Size.Y+bd.Size.Y)}
	}
}

// longValue is the most runes a details value shows beside its name.
const longValue = 48

// details are sections of rows: a title and its note, then each row's
// name, what else Claude says of it, and its value on the right.
func (d *cdraw) details(id string, secs []detailSection) layout.Widget {
	th := d.th
	return func(gtx layout.Context) layout.Dimensions {
		var ws []layout.Widget
		for i, s := range secs {
			var rows []layout.Widget
			if s.title != "" {
				rows = append(rows, func(gtx layout.Context) layout.Dimensions {
					return d.split(gtx, func(gtx layout.Context) layout.Dimensions {
						return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Fg, s.title)
					}, d.muted(s.note))
				})
			}
			for j, r := range s.rows {
				name := r.name
				if r.meta != "" {
					name += " · " + r.meta
				}
				sid := id + ":" + strconv.Itoa(i) + ":" + strconv.Itoa(j)
				if len([]rune(r.value)) > longValue {
					// A sentence of a value goes under its name, wrapped, so the name keeps its room.
					rows = append(rows, func(gtx layout.Context) layout.Dimensions {
						return vstack(gtx, 0, d.muted(name), func(gtx layout.Context) layout.Dimensions {
							return d.selectable(gtx, d.t.sel(sid), d.small(th.Muted), r.value)
						})
					})
					continue
				}
				rows = append(rows, func(gtx layout.Context) layout.Dimensions {
					return d.split(gtx, func(gtx layout.Context) layout.Dimensions {
						return d.selectable(gtx, d.t.sel(sid), d.small(th.Muted), name)
					}, d.muted(r.value))
				})
			}
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return vstack(gtx, gtx.Dp(theme.Space2XS), rows...)
			})
		}
		return vstack(gtx, gtx.Dp(theme.SpaceS), ws...)
	}
}

// --- /context ---

// catColor is a context category's color in the bar and its legend.
func (d *cdraw) catColor(name string) color.NRGBA {
	th := d.th
	n := strings.ToLower(name)
	switch {
	case n == "free space":
		return d.track()
	case strings.Contains(n, "buffer"):
		return theme.Mix(d.track(), th.Muted, 0.35)
	case strings.Contains(n, "system prompt"):
		return th.Muted
	case strings.Contains(n, "system tools"):
		return theme.Mix(th.Muted, th.Blue, 0.55)
	case strings.Contains(n, "mcp"):
		return th.Green
	case strings.Contains(n, "memory"):
		return theme.Mix(th.Yellow, th.Red, 0.45)
	case strings.Contains(n, "skill"):
		return th.Yellow
	case strings.Contains(n, "agent"):
		return th.Blue
	case strings.Contains(n, "message"):
		return th.Purple
	}
	return th.Primary
}

// deferred reports whether a category is tools Claude loads on demand,
// which take no room until then.
func deferred(c ctxCat) bool { return strings.Contains(c.name, "(deferred)") }

// tokShort is n tokens as Claude prints them: 812, 28.2k, 1m.
func tokShort(n int64) string { return strings.ToLower(strings.Replace(count(n), ".0", "", 1)) }

// ctxSummary is /context's figure: 42.6k / 1m · 4%.
func ctxSummary(c ctxCard) string {
	return fmt.Sprintf("%s / %s · %s%%", tokShort(c.used), tokShort(c.window), strconv.FormatFloat(c.pct, 'f', -1, 64))
}

func (d *cdraw) contextParts(id string, c ctxCard) []layout.Widget {
	var shown []ctxCat
	var later []detailRow
	for _, k := range c.cats {
		if deferred(k) {
			later = append(later, detailRow{name: k.name, value: tokShort(k.tokens) + " tokens"})
			continue
		}
		shown = append(shown, k)
	}
	parts := []layout.Widget{d.cardHead("layers", "Context", c.model, ctxSummary(c)), d.stacked(c.window, shown), d.legend(id, shown)}
	secs := c.details
	if len(later) > 0 {
		secs = append([]detailSection{{title: "Deferred", note: "loaded when used", rows: later}}, secs...)
	}
	if len(secs) > 0 {
		parts = append(parts, d.fold(id, d.details(id, secs)))
	}
	return parts
}

// stacked is the context window as one 8dp bar: a segment per category
// from the left, the free space empty, the autocompact buffer at the end.
func (d *cdraw) stacked(window int64, cats []ctxCat) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		w, h := gtx.Constraints.Max.X, gtx.Dp(theme.SpaceS)
		r := image.Rect(0, 0, w, h)
		paint.FillShape(gtx.Ops, d.track(), clip.UniformRRect(r, h/2).Op(gtx.Ops))
		cl := clip.UniformRRect(r, h/2).Push(gtx.Ops)
		part := func(n int64) int { return int(float64(w)*float64(n)/float64(window) + 0.5) }
		x, gap := 0, gtx.Dp(1)
		for _, k := range cats {
			switch {
			case k.tokens <= 0 || k.name == "Free space":
			case strings.Contains(strings.ToLower(k.name), "buffer"):
				paint.FillShape(gtx.Ops, d.catColor(k.name), clip.Rect{Min: image.Pt(max(x, w-part(k.tokens)), 0), Max: image.Pt(w, h)}.Op())
			default:
				x1 := min(w, x+max(gtx.Dp(2), part(k.tokens))) // a sliver still shows
				paint.FillShape(gtx.Ops, d.catColor(k.name), clip.Rect{Min: image.Pt(x, 0), Max: image.Pt(max(x, x1-gap), h)}.Op())
				x = x1
			}
		}
		cl.Pop()
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
}

// legend lists the categories two to a row when there is room: a swatch,
// the name, its tokens and share.
func (d *cdraw) legend(id string, cats []ctxCat) layout.Widget {
	th := d.th
	return func(gtx layout.Context) layout.Dimensions {
		w := gtx.Constraints.Max.X
		cols := 1
		if w >= gtx.Dp(400) {
			cols = 2
		}
		gap := gtx.Dp(theme.SpaceXL)
		cw := (w - gap*(cols-1)) / cols
		y, rowH := 0, 0
		for i, k := range cats {
			g := gtx
			g.Constraints = layout.Constraints{Max: image.Pt(cw, gtx.Constraints.Max.Y)}
			x := (i % cols) * (cw + gap)
			dm := at(g, image.Pt(x, y), func(gtx layout.Context) layout.Dimensions {
				return d.split(gtx, func(gtx layout.Context) layout.Dimensions {
					sw := gtx.Dp(theme.SpaceS)
					lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
						return d.lbl(gtx, th.UIFont, th.Sp(theme.Small), th.Fg, k.name)
					})
					sy := (ld.Size.Y - sw) / 2
					ring := theme.Transparent
					if k.name == "Free space" {
						ring = th.BorderStrong // the track's own color needs an edge to show
					}
					rrect(gtx, image.Rect(0, sy, sw, sy+sw), gtx.Dp(2), d.catColor(k.name), ring)
					at(gtx, image.Pt(sw+gtx.Dp(theme.SpaceS), 0), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
					return layout.Dimensions{Size: image.Pt(sw+gtx.Dp(theme.SpaceS)+ld.Size.X, ld.Size.Y)}
				}, d.muted(tokShort(k.tokens)+" · "+strconv.FormatFloat(k.pct, 'f', -1, 64)+"%"))
			})
			rowH = max(rowH, dm.Size.Y)
			if i%cols == cols-1 || i == len(cats)-1 {
				y += rowH + gtx.Dp(theme.Space2XS)
				rowH = 0
			}
		}
		return layout.Dimensions{Size: image.Pt(w, max(0, y-gtx.Dp(theme.Space2XS)))}
	}
}

// --- /usage and /cost ---

// windowLeft is a window's right-hand figure: 48% left · resets Nov 24, 9pm.
func windowLeft(w useWindow) string {
	s := strconv.FormatFloat(max(0, 100-w.used), 'f', -1, 64) + "% left"
	if w.resets != "" {
		s += " · resets " + w.resets
	}
	return s
}

// modelTokens is a model's tokens in and out: what it read, cached or
// not, and what it wrote.
func modelTokens(m useModel) string {
	return tokShort(m.in+m.cacheRead+m.cacheWrt) + " in · " + tokShort(m.out) + " out"
}

func (d *cdraw) usageParts(id string, c useCard) []layout.Widget {
	th := d.th
	title := "Usage"
	if len(c.windows) == 0 {
		title = "Cost"
	}
	parts := []layout.Widget{d.cardHead("gauge", title, c.plan, c.cost)}
	for _, m := range c.models {
		right := modelTokens(m)
		if m.cost != "" && len(c.models) > 1 {
			right += " · " + m.cost
		}
		parts = append(parts, func(gtx layout.Context) layout.Dimensions {
			return d.split(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), th.Fg, chatin.ModelName(m.name))
			}, d.muted(right))
		})
	}
	for _, w := range c.windows {
		parts = append(parts, func(gtx layout.Context) layout.Dimensions {
			return vstack(gtx, gtx.Dp(theme.SpaceXS+theme.Space2XS),
				func(gtx layout.Context) layout.Dimensions {
					return d.split(gtx, func(gtx layout.Context) layout.Dimensions {
						return d.lbl(gtx, th.UIFont, th.Sp(theme.Body), th.Fg, w.name)
					}, d.muted(windowLeft(w)))
				},
				d.bar(w.used))
		})
	}
	var secs []detailSection
	if len(c.models) > 0 {
		s := detailSection{title: "Tokens by model"}
		for _, m := range c.models {
			s.rows = append(s.rows, detailRow{name: chatin.ModelName(m.name), value: fmt.Sprintf("%s input · %s output · %s cache read · %s cache write",
				tokShort(m.in), tokShort(m.out), tokShort(m.cacheRead), tokShort(m.cacheWrt))})
		}
		secs = append(secs, s)
	}
	if len(c.rows) > 0 {
		secs = append(secs, detailSection{title: "Session", rows: c.rows})
	}
	secs = append(secs, c.contrib...)
	if len(secs) > 0 {
		parts = append(parts, d.fold(id, d.details(id, secs)))
	}
	return parts
}

// --- /status ---

func (d *cdraw) statusParts(id string, c statusCard) []layout.Widget {
	th := d.th
	return []layout.Widget{d.cardHead("settings", "Status", "", ""), func(gtx layout.Context) layout.Dimensions {
		kw := 0
		for _, r := range c.rows {
			_, dm := measure(gtx, d.muted(r.name))
			kw = max(kw, dm.Size.X)
		}
		kw += gtx.Dp(theme.SpaceL)
		var rows []layout.Widget
		for i, r := range c.rows {
			if r.name == "Model" {
				r.value = chatin.ModelName(r.value) // as the composer names it
			}
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				g := gtx
				g.Constraints.Max.X = max(0, gtx.Constraints.Max.X-kw)
				vc, vd := measure(g, func(gtx layout.Context) layout.Dimensions {
					return d.selectable(gtx, d.t.sel(id+":"+strconv.Itoa(i)), d.body(th.Fg), r.value)
				})
				line := gtx.Sp(th.Sp(theme.Body)) * 145 / 100
				kc, kd := measure(gtx, d.muted(r.name))
				at(gtx, image.Pt(0, (line-kd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { kc.Add(gtx.Ops); return kd })
				at(gtx, image.Pt(kw, 0), func(gtx layout.Context) layout.Dimensions { vc.Add(gtx.Ops); return vd })
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, max(vd.Size.Y, line))}
			})
		}
		return vstack(gtx, gtx.Dp(theme.Space2XS), rows...)
	}}
}
