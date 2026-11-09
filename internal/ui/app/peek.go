package app

import (
	"image"
	"strings"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// PeekTab is a tab id the window peeks at once it shows a tab, for
// pwdemo-app's -peek; "" for none.
var PeekTab string

// peekView is the peek overlay: the pane it last drew, when that pane's
// peek opened, and its pointer tags.
type peekView struct {
	shown          string
	at             time.Time
	backdrop, card int
}

// openPeek peeks at tab ws, at pane or else the pane it has focused. The
// sidebar's keys, hints mode, a pane menu and the find bar let go of the
// keyboard, which the peeked pane takes.
func (u *ui) openPeek(st *model.State, ws, pane string) {
	u.nav.peekTab(st, ws, pane)
	if u.nav.peek == "" {
		return
	}
	u.leaveSidebar()
	u.closeHints()
	u.paneMenu.close()
	if id := u.find.pane; id != "" {
		u.closeFind(findPane(st, id) != nil)
	}
}

// keysHeld reports whether something other than a pane holds the
// keyboard: a dialog, a rename field, tab or pane mode, the palette, a
// form, hints mode, a pane menu, the command browser or the host picker.
func (u *ui) keysHeld(st *model.State) bool {
	return u.modal.kind != modalNone || u.sidebar.Editing() || u.sidebar.Focused() || u.nav.tabMode || u.nav.paneMode || u.pal.open ||
		u.offline || u.formHoldsKeys(st) || u.hints.on || u.paneMenu.pane != "" || u.browser.open || u.picker.open
}

// drawPeek draws nav.peek's pane in a card over the dimmed window, as the
// palette's card: a header naming its tab, agent, state and machine, the
// pane as a split draws it, with the keyboard, and the peek's keys along
// the bottom. A press outside the card closes it.
func (u *ui) drawPeek(gtx gl.Context, st *model.State) {
	pv, th := &u.peek, u.th
	id := u.nav.peek
	if id != pv.shown {
		pv.shown, pv.at = id, gtx.Now
	}
	pn := findPane(st, id)
	if pn == nil {
		return
	}
	ws := findWorkspace(st, pn.WorkspaceID)
	if ws == nil {
		return
	}
	t := anim.At(gtx, pv.at, anim.Overlay)
	if u.backdrop(gtx, t, &pv.backdrop) {
		u.nav.peek = ""
		return
	}
	size := gtx.Constraints.Max
	w, h := size.X*4/5, size.Y*4/5
	at := image.Pt((size.X-w)/2, (size.Y-h)/2)
	defer u.overlayCard(gtx, t, w, h, &pv.card)()

	// The header: the marks, the tab's name, then its agent, state and
	// machine, muted.
	pad, head, footH := gtx.Dp(16), gtx.Dp(48), gtx.Dp(44)
	x, act := u.paneMarks(gtx, st, pn, image.Pt(pad, 0), head, th.Surface)
	tgtx := gtx
	tgtx.Constraints.Max.X = max(0, w-pad-x)
	call, sz := textCall(tgtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, tabTitle(*ws))
	o := op.Offset(image.Pt(x, (head-sz.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	var detail []string
	if model.IsAgent(pn.Provider) {
		detail = append(detail, sidebar.AgentName(pn.Provider))
	}
	if act != nil {
		detail = append(detail, model.PillLabel(st.AwaitPR(*act)))
	}
	if len(u.hosts) > 1 {
		detail = append(detail, "on "+machineName(u.hostOf(id)))
	}
	if x += sz.X + gtx.Dp(10); len(detail) > 0 && x < w-pad {
		tgtx.Constraints.Max.X = w - pad - x
		call, dsz := textCall(tgtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, strings.Join(detail, " · "))
		o := op.Offset(image.Pt(x, (head-dsz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
	}

	// The pane, in window pixels for its rect, as layoutPane keeps them.
	origin := u.contentOrigin
	u.contentOrigin = at
	r := layout.Rect{X: pad, Y: head, W: max(0, w-2*pad), H: max(0, h-head-footH)}
	u.layoutPane(gtx, st, u.paneView(st, id), id, r, !u.keysHeld(st) && u.find.pane == "", false, false, nil)
	u.contentOrigin = origin
	// ponytail: the pane menu draws under the card, so a peek has none;
	// draw it over the card if people right-click in a peek.
	if u.paneMenu.pane == id {
		u.paneMenu.close()
	}

	paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.08), clip.Rect{Min: image.Pt(1, h-footH), Max: image.Pt(w-1, h-footH+1)}.Op())
	o = op.Offset(image.Pt(pad, h-footH)).Push(gtx.Ops)
	hints := [][2]string{{"Esc", "close"}}
	if k := firstChord(u.nav.bind().Global["keep_peek"]); k != "" {
		hints = append(hints, [2]string{k, "go to tab"})
	}
	u.drawHints(gtx, footH, hints)
	o.Pop()
}

// machineName is host as the peek's header names it, "this machine" for "".
func machineName(host string) string {
	if host == "" {
		return "this machine"
	}
	return host
}
