package panel

import (
	"cmp"
	"fmt"
	"image"
	"maps"
	"slices"
	"strings"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// usage is the agent's token use, nil before its first call with usage.
func (in *Input) usage() *flow.Usage {
	if in.agent() == "" || in.Feed == nil || in.Feed.Usage.Tokens().Total() == 0 {
		return nil
	}
	return &in.Feed.Usage
}

// headerText is the compact line under the tabs: the latest model, then
// sidebar.UsageText.
func headerText(u flow.Usage, cost bool) string {
	s := sidebar.UsageText(u, cost)
	if u.Model != "" {
		s = u.Model + " · " + s
	}
	return s
}

// usageStats are the session's tokens by kind, for the Session grid.
func usageStats(u flow.Usage) []stat {
	t := u.Tokens()
	n := func(v int64) string {
		if u.Partial {
			return "≥" + sidebar.TokenCount(v)
		}
		return sidebar.TokenCount(v)
	}
	return []stat{
		{key: "Input", value: n(t.Input)},
		{key: "Output", value: n(t.Output)},
		{key: "Cache read", value: n(t.CacheRead)},
		{key: "Cache write", value: n(t.CacheWrite)},
	}
}

// usageNote is the paragraph under the Session grid: tokens by model when
// there are several, the context, the cost or why there is none, and
// what a partial count misses.
func usageNote(u flow.Usage, cost bool) string {
	var out []string
	if len(u.Models) > 1 {
		ids := slices.SortedFunc(maps.Keys(u.Models), func(a, b string) int {
			return cmp.Or(cmp.Compare(u.Models[b].Total(), u.Models[a].Total()), strings.Compare(a, b))
		})
		var by []string
		for _, id := range ids {
			by = append(by, id+" "+sidebar.TokenCount(u.Models[id].Total()))
		}
		out = append(out, "By model: "+strings.Join(by, ", ")+".")
	}
	if u.Context > 0 {
		s := "Context: " + sidebar.TokenCount(u.Context)
		if f, ok := u.Fill(); ok {
			w := u.Window
			if w == 0 {
				w = flow.ContextWindow(u.Model)
			}
			s += fmt.Sprintf(" of %s (%s)", sidebar.TokenCount(w), sidebar.FillText(f))
		}
		out = append(out, s+".")
	}
	if cost {
		if c, ok := u.Cost(); ok {
			out = append(out, fmt.Sprintf("%s at list prices of %s.", sidebar.Dollars(c), flow.PricesAsOf))
		} else {
			var none []string
			for _, id := range slices.Sorted(maps.Keys(u.Models)) {
				if _, ok := flow.PriceOf(id); !ok && u.Models[id] != (flow.Tokens{}) {
					none = append(none, id)
				}
			}
			out = append(out, "No list price for "+strings.Join(none, ", ")+", so no cost.")
		}
	}
	if u.Partial {
		out = append(out, "The session file is long: these count its last 8 MiB.")
	}
	return strings.Join(out, " ")
}

// usageLine is the header's compact line: a context meter when the fill
// is known, then headerText. It draws nothing without usage.
func (d *drawer) usageLine(gtx layout.Context) layout.Dimensions {
	u := d.in.usage()
	if u == nil {
		return layout.Dimensions{}
	}
	return layout.Inset{Left: 14, Right: 14, Bottom: 9}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		var parts []part
		if f, ok := u.Fill(); ok {
			parts = append(parts, part{w: d.meter(f)})
		}
		parts = append(parts, part{gap: gapIf(len(parts) > 0, gtx.Dp(8)), flex: true,
			w: d.text(d.th.UIFont, d.th.Sp(theme.Caption), d.c.muted, headerText(*u, d.in.ShowCost), 1)})
		return row(gtx, parts...)
	})
}

// meter is a 36dp bar filled to f, yellow from 80% and red from 95%, when
// agents start to compact or fail.
func (d *drawer) meter(f float64) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		w, h := gtx.Dp(36), gtx.Dp(4)
		lh := gtx.Sp(d.th.Sp(theme.Caption) * 1.4)
		y := (lh - h) / 2
		col := d.c.blue
		switch {
		case f >= 0.95:
			col = d.c.red
		case f >= 0.8:
			col = d.c.yellow
		}
		track := image.Rect(0, y, w, y+h)
		paint.FillShape(gtx.Ops, theme.Mix(d.c.bg, d.c.fg, 0.1), clip.UniformRRect(track, h/2).Op(gtx.Ops))
		fill := track
		fill.Max.X = max(h, int(float64(w)*f))
		paint.FillShape(gtx.Ops, col, clip.UniformRRect(fill, h/2).Op(gtx.Ops))
		return layout.Dimensions{Size: image.Pt(w, lh)}
	}
}

// session is Flow's Session section: the grid and its note.
func (d *drawer) session(u flow.Usage) []layout.Widget {
	return []layout.Widget{d.section("Session"), d.stats(usageStats(u)), space(8), d.note(usageNote(u, d.in.ShowCost))}
}
