package sidebar

import (
	"fmt"
	"image"
	"image/color"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// WindowName names a limit window by its length: "5-hour", "weekly".
func WindowName(minutes int64) string {
	switch {
	case minutes == 7*24*60:
		return "weekly"
	case minutes == 24*60:
		return "daily"
	case minutes > 0 && minutes%(24*60) == 0:
		return fmt.Sprintf("%d-day", minutes/(24*60))
	case minutes > 0 && minutes%60 == 0:
		return fmt.Sprintf("%d-hour", minutes/60)
	}
	return fmt.Sprintf("%d-minute", minutes)
}

// windowShort is WindowName in two or three characters: "5h", "7d".
func windowShort(minutes int64) string {
	if minutes > 0 && minutes%(24*60) == 0 {
		return fmt.Sprintf("%dd", minutes/(24*60))
	}
	if minutes > 0 && minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}

// LimitName is the agent a limit is of, with Codex's name for a model's
// own limit: "Claude", "Codex GPT-Spark".
func LimitName(l flow.Limit) string {
	if l.Name != "" {
		return AgentName(l.Provider) + " " + l.Name
	}
	return AgentName(l.Provider)
}

// Clock is t as now's reader wants it: "16:40" today, "Mon 16:40" this
// week, else "Oct 21 16:40".
func Clock(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	y, m, d := t.Date()
	ny, nm, nd := now.Date()
	switch days := time.Date(y, m, d, 0, 0, 0, 0, time.Local).Sub(time.Date(ny, nm, nd, 0, 0, 0, 0, time.Local)).Hours() / 24; {
	case days > -0.5 && days < 0.5:
		return t.Format("15:04")
	case days > -6.5 && days < 6.5:
		return t.Format("Mon 15:04")
	}
	return t.Format("Jan 2 15:04")
}

// Span is a duration in its two largest units: "2h 14m", "3d 4h", "45m",
// "<1m".
func Span(d time.Duration) string {
	m := int64(d / time.Minute)
	switch {
	case m < 1:
		return "<1m"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m < 24*60:
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
	return fmt.Sprintf("%dd %dh", m/(24*60), m%(24*60)/60)
}

// LimitColor is a window's color at used percent: yellow from 80, red
// from 95, else ok.
func LimitColor(th *theme.Theme, used float64, ok color.NRGBA) color.NRGBA {
	switch {
	case used >= 95:
		return th.Red
	case used >= 80:
		return th.Yellow
	}
	return ok
}

// limitMeter draws each limit's fullest window in a row of equal cells,
// h tall: the agent's mark, the window, a bar and its percentage.
func (s *Sidebar) limitMeter(gtx layout.Context, th *theme.Theme, h int) {
	n := len(s.Limits)
	gap := gtx.Dp(12)
	cw := (gtx.Constraints.Max.X - (n-1)*gap) / n
	for i, l := range s.Limits {
		w, _ := l.Tightest(s.now)
		col := LimitColor(th, w.Used, th.Fg)
		off := op.Offset(image.Pt(i*(cw+gap), 0)).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(image.Pt(cw, h))
		bar := func(gtx layout.Context) layout.Dimensions {
			bw, bh := gtx.Constraints.Max.X, gtx.Dp(4)
			y := (h - bh) / 2
			paint.FillShape(gtx.Ops, theme.Mix(th.Sidebar, th.Fg, 0.12), clip.UniformRRect(image.Rect(0, y, bw, y+bh), bh/2).Op(gtx.Ops))
			if fw := int(float64(bw) * min(w.Used, 100) / 100); fw > 0 {
				paint.FillShape(gtx.Ops, LimitColor(th, w.Used, AgentColor(l.Provider, th.Fg)), clip.UniformRRect(image.Rect(0, y, max(fw, bh), y+bh), bh/2).Op(gtx.Ops))
			}
			return layout.Dimensions{Size: image.Pt(bw, h)}
		}
		hrow(g, h, gtx.Dp(6),
			item{w: func(gtx layout.Context) layout.Dimensions { return AgentMark(gtx, l.Provider, gtx.Dp(12), th.Fg) }},
			item{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, windowShort(w.Minutes))
			}},
			item{shrink: true, w: bar},
			item{w: func(gtx layout.Context) layout.Dimensions {
				return label(gtx, th, medium(th.UIFont), th.Sp(theme.Caption), col, fmt.Sprintf("%.0f%%", w.Used))
			}},
		)
		off.Pop()
	}
}
