package settings

import (
	"fmt"
	"image"
	"strings"
	"time"

	"gioui.org/font"
	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// limits is Usage's Limits section: a card per plan limit the agents
// reported, a way to set up Claude Code's, and the sidebar meter's switch.
func (p *Page) limits() section {
	th := p.th
	now := time.Now()
	var rows []row
	seen := map[model.Provider]bool{}
	for _, l := range p.Limits {
		seen[l.Provider] = true
		rows = append(rows, row{label: limitTitle(l), desc: "As of " + ago(now.Sub(l.Seen)), wide: true,
			extra: "limits plan rate 5-hour weekly reset quota",
			control: func(gtx gl.Context) gl.Dimensions {
				var kids []gl.FlexChild
				for i, w := range l.Windows {
					if i > 0 {
						kids = append(kids, gl.Rigid(gl.Spacer{Height: 16}.Layout))
					}
					kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions { return p.window(gtx, l, w, now) }))
				}
				return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
			}})
	}
	if !seen[model.ProviderClaude] {
		rows = append(rows, row{label: "Claude Code", desc: "Claude Code reports its 5-hour and weekly limits to its status line only. pitwall statusline saves them, then prints your own status line as before.",
			extra: "limits plan rate statusline status line", control: func(gtx gl.Context) gl.Dimensions {
				c := p.btn("limits-statusline")
				for c.Clicked(gtx) {
					p.result = InstallStatusline
				}
				return p.button(gtx, c, secondary, "Set up")
			}})
	}
	if !seen[model.ProviderCodex] {
		rows = append(rows, row{label: "Codex", desc: "Codex logs its limits in its session files. None of the last 8 days has them.",
			extra: "limits plan rate", control: func(gtx gl.Context) gl.Dimensions {
				return p.text(gtx, th.UIFont, p.th.Sp(theme.Body), th.Muted, "Not seen")
			}})
	}
	rows = append(rows, row{label: "Show in sidebar", desc: "The fullest window of each agent at the bottom of the sidebar, yellow from 80% and red from 95%.",
		extra: "limits sidebar meter limits_in_sidebar", control: p.toggle("usage", "limits_in_sidebar", p.s.LimitsInSidebar)})
	return section{title: "Limits", desc: "How much of each plan limit the agents used, as they last reported it. A window at 90% shows a notice once.", rows: rows}
}

// limitTitle is "Claude Code" or "Codex GPT-Spark".
func limitTitle(l flow.Limit) string {
	if l.Provider == model.ProviderClaude {
		return "Claude Code"
	}
	return sidebar.LimitName(l)
}

// window is one window: its name and use, a bar, then when it resets and
// when it fills at its pace so far.
func (p *Page) window(gtx gl.Context, l flow.Limit, w flow.Window, now time.Time) gl.Dimensions {
	th := p.th
	cur := w.Now(now)
	col := sidebar.LimitColor(th, cur.Used, th.Fg)
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return gl.Flex{Alignment: gl.Baseline}.Layout(gtx,
				gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
					d := p.text(gtx, th.UIFont, p.th.Sp(theme.Body), th.Fg, capital(sidebar.WindowName(w.Minutes))+" window")
					d.Size.X = gtx.Constraints.Max.X // pushes the percentage to the right
					return d
				}),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, weight(th.UIFont, font.Medium), p.th.Sp(theme.Body), col, fmt.Sprintf("%.0f%%", cur.Used))
				}),
			)
		}),
		gl.Rigid(gl.Spacer{Height: 6}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			bw, bh := gtx.Constraints.Max.X, gtx.Dp(6)
			rrect(gtx, theme.Mix(th.SurfaceSecondary, th.Fg, 0.1), image.Rect(0, 0, bw, bh), bh/2)
			if fw := int(float64(bw) * min(cur.Used, 100) / 100); fw > 0 {
				rrect(gtx, sidebar.LimitColor(th, cur.Used, sidebar.AgentColor(l.Provider, th.Fg)), image.Rect(0, 0, max(fw, bh), bh), bh/2)
			}
			return gl.Dimensions{Size: image.Pt(bw, bh)}
		}),
		gl.Rigid(gl.Spacer{Height: 6}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), th.Muted, windowNote(w, l.Seen, now))
		}),
	)
}

// windowNote is when w, seen then, resets and when it fills at its pace:
// "Resets in 2h 14m, at 16:40 · At this pace you hit 100% around 15:55".
func windowNote(w flow.Window, seen, now time.Time) string {
	switch {
	case w.Resets.IsZero():
		return "Its reset time is unknown."
	case !now.Before(w.Resets):
		return "Reset at " + sidebar.Clock(w.Resets, now) + "; no report since."
	}
	parts := []string{"Resets in " + sidebar.Span(w.Resets.Sub(now)) + ", at " + sidebar.Clock(w.Resets, now)}
	if w.Used >= 100 {
		parts = append(parts, "Used up")
	} else if hit := w.Hits(seen); !hit.IsZero() {
		parts = append(parts, "At this pace you hit 100% around "+sidebar.Clock(hit, now))
	}
	return strings.Join(parts, " · ")
}

// ago is how long ago d was: "just now", "3m ago", "2h 14m ago".
func ago(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	return sidebar.Span(d) + " ago"
}

func capital(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
