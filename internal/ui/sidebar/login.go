package sidebar

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Login is what a machine's ssh waits on a person for.
type Login struct {
	Reason  string // one line, such as "Tailscale SSH wants an approval"
	Command string // what to run in a shell
	URL     string // the page that approves it, else ""
}

// OpenLogin opens a Login's page in the browser.
type OpenLogin struct{ URL string }

// RetryHosts connects to the machines that are down now.
type RetryHosts struct{}

// loginBlock draws l under its machine's heading, as wide as gtx, and
// returns its height.
func (s *Sidebar) loginBlock(gtx layout.Context, th *theme.Theme, l Login, link, retry *widget.Clickable) int {
	for link.Clicked(gtx) {
		s.events = append(s.events, OpenLogin{URL: l.URL})
	}
	for retry.Clicked(gtx) {
		s.events = append(s.events, RetryHosts{})
	}
	pad, gap := gtx.Dp(18), gtx.Dp(4)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, gtx.Constraints.Max.X-2*pad), gtx.Constraints.Max.Y)}
	y := 0
	place := func(w layout.Widget) {
		o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		y += w(g).Size.Y + gap
		o.Pop()
	}
	text := func(f font.Font, size unit.Sp, c color.NRGBA, txt string, lines int) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions { return para(gtx, th, f, size, c, txt, lines) }
	}
	place(text(th.UIFont, th.Sp(theme.Small), th.Fg, l.Reason+". Run in a shell:", 3))
	// why: a host's name can be long, and a cut command cannot be typed.
	place(text(th.MonoFont, th.Sp(theme.Small), th.Fg, l.Command, 4))
	if l.URL != "" {
		place(func(gtx layout.Context) layout.Dimensions {
			return clickable(gtx, link, text(th.UIFont, th.Sp(theme.Small), th.Readable(th.Blue, th.Sidebar), l.URL, 2))
		})
	}
	place(func(gtx layout.Context) layout.Dimensions {
		return kit.Button(gtx, th, retry, kit.Secondary, kit.Small, "Retry now")
	})
	return y + gap
}
