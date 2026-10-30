package app

import (
	"fmt"
	"image"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func (u *ui) searchRow(gtx gl.Context, st *model.State, c *widget.Clickable, m proto.SessionMatch, first bool, size image.Point) {
	g := gtx
	g.Constraints = gl.Exact(size)
	c.Layout(g, func(gl.Context) gl.Dimensions { return gl.Dimensions{Size: size} })
	th := u.th
	label := ""
	if first {
		if w := findWorkspace(st, m.Workspace); w != nil {
			label = tabTitle(*w)
			if s := st.Session(w.SessionID); s != nil {
				label = s.Name + " / " + label
			}
			if w.Detached {
				label += " · detached"
			}
		}
	}
	source := "scrollback captured"
	if m.Transcript {
		source = fmt.Sprintf("transcript · record %d", m.Match.Line)
	}
	at := "time unknown"
	if !m.Time.IsZero() {
		at = m.Time.Local().Format("Jan 2 15:04:05")
	}
	if label != "" {
		label += " · "
	}
	gtx.Constraints.Max.X = size.X - gtx.Dp(24)
	drawText(gtx, th, image.Pt(gtx.Dp(12), gtx.Dp(6)), semibold(th.UIFont), th.Sp(theme.Small), th.Muted, label+source+" · "+at)
	defer op.Offset(image.Pt(gtx.Dp(12), gtx.Dp(26))).Push(gtx.Ops).Pop()
	lines := 2
	if u.all.preview != nil {
		lines = 6
	}
	widget.Label{MaxLines: lines, Truncator: "…"}.Layout(gtx, th.Shaper, th.MonoFont, th.Sp(theme.Body), m.Snippet, colorCall(gtx, th.Fg))
}
