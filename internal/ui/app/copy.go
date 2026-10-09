package app

import (
	"image"
	"io"
	"strings"

	"gioui.org/io/clipboard"
	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// Texter is optionally implemented by a Backend: the reply to the pane's
// last proto.Text, which the call takes.
type Texter interface {
	TakeText(pane string) (proto.TextResult, bool)
}

// canText reports whether the daemon's proto.Level knows proto.Text.
func (u *ui) canText() bool { return u.link().Level >= proto.Since(proto.Text{}) }

// paneCopy puts what pane id's view asked to copy on the clipboard. A
// selection that runs past the frame comes from the daemon, which has the
// whole history; a daemon that predates proto.Text gives the frame's part.
// It also opens the find bar for copy mode's / and ?.
func (u *ui) paneCopy(gtx gl.Context, p *paneUI, id string, r image.Rectangle) {
	if c, ok := p.view.Copied(); ok {
		if !c.Whole && u.canText() && u.send(proto.Text{Pane: id, Sel: c.Sel}) {
			p.copyWant = &c.Sel
		} else {
			u.copyText(gtx, c.Text, r)
		}
	}
	if t, ok := u.b.(Texter); ok && p.copyWant != nil {
		if res, ok := t.TakeText(id); ok && res.Sel == *p.copyWant {
			p.copyWant = nil
			u.copyText(gtx, res.Text, r)
		}
	}
	if p.view.FindWanted() && id == u.nav.focused() {
		u.openFind()
	}
}

// copyText puts s on the clipboard and says so under the pane at r. Gio's
// X11 backend sets PRIMARY along with CLIPBOARD; on Wayland it has no
// primary selection to set.
func (u *ui) copyText(gtx gl.Context, s string, r image.Rectangle) {
	if s == "" {
		return
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
	u.showNotice(gtx, copiedText(s), r)
}

// copyBadge shows copy mode's name and keys at the bottom right of the
// pane's terminal, grid.
func (u *ui) copyBadge(gtx gl.Context, p *paneUI, grid image.Rectangle) {
	name := p.view.CopyMode()
	if name == "" {
		return
	}
	call, sz := pill(gtx, u.th, name, [][2]string{{"v V ^V", "select"}, {"y", "copy"}, {"/", "find"}, {"Esc", "exit"}})
	pad := gtx.Dp(6)
	box := image.Rectangle{Max: sz.Add(image.Pt(2*pad, 2*pad))}
	in := gtx.Dp(10)
	if box.Dx()+2*in > grid.Dx() {
		call, sz = pill(gtx, u.th, name, nil)
		box = image.Rectangle{Max: sz.Add(image.Pt(2*pad, 2*pad))}
	}
	u.drawPill(gtx, call, box, image.Pt(grid.Max.X-in-box.Dx(), grid.Max.Y-in-box.Dy()))
}
