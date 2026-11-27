package app

import (
	"image"
	"image/color"
	"slices"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// awayPaneW is the widest a terminal pane's Since you left card gets.
const awayPaneW unit.Dp = 560

// awayPane is a terminal pane's Since you left card's buttons, and the
// card the user dismissed here until the daemon's state drops it.
type awayPane struct {
	open, shut widget.Clickable
	card       int // takes presses on the card, so they stop short of the terminal
	hid        string
}

func awayKey(a model.Away) string { return a.Chat + "@" + a.At.String() }

// awayOf is pane id's card while it shows over the pane: the daemon has
// one, it was not dismissed here, and the pane shows its terminal, not
// its chat.
func (u *ui) awayOf(st *model.State, id string) (model.Away, *awayPane, bool) {
	a, ok := st.Away[id]
	if c := terminalChat(st, id); !ok && c != nil {
		a, ok = st.Away[c.ID] // a chat tab showing its agent's TUI in pane id
	}
	if !ok {
		return model.Away{}, nil, false
	}
	if u.awayPanes == nil {
		u.awayPanes = map[string]*awayPane{}
	}
	ap := u.awayPanes[id]
	if ap == nil {
		ap = &awayPane{}
		u.awayPanes[id] = ap
	}
	if ap.hid == awayKey(a) {
		return a, ap, false
	}
	if p := findPane(st, id); p != nil && u.paneChatShown(st, findWorkspace(st, p.WorkspaceID), id) {
		return a, ap, false
	}
	return a, ap, true
}

// terminalChat is the chat whose tab shows its agent's TUI in pane id
// (proto.ChatTerminal), nil for none.
func terminalChat(st *model.State, id string) *model.Chat {
	if i := slices.IndexFunc(st.Chats, func(c model.Chat) bool { return id != "" && c.Terminal == id }); i >= 0 {
		return &st.Chats[i]
	}
	return nil
}

// awayDismiss closes the card here and tells the daemon, which clears it
// in every window, the phone and pitwall away.
func (u *ui) awayDismiss(id string, a model.Away, ap *awayPane) {
	ap.hid = awayKey(a)
	u.send(proto.ChatAwayDismiss{Chat: a.Chat})
}

// awayEsc takes Escape typed into pane id while its card shows: it
// closes the card instead of reaching the agent, where it would stop a
// turn. Other keys go to the pane, and the daemon drops the card on them.
func (u *ui) awayEsc(st *model.State, id string, input []byte) []byte {
	if string(input) != "\x1b" {
		return input
	}
	if a, ap, ok := u.awayOf(st, id); ok {
		u.awayDismiss(id, a, ap)
		return nil
	}
	return input
}

// layoutAwayPane draws pane id's card docked at the bottom of grid, the
// terminal's rect in the pane, min(grid, 560dp) wide, in the floating
// overlay style: how long the user was away and the agent's state, the
// summary, the facts, Open chat when the pane has a chat view, and the
// x. It never takes the keys.
func (u *ui) layoutAwayPane(gtx gl.Context, st *model.State, id string, grid image.Rectangle) {
	a, ap, ok := u.awayOf(st, id)
	if !ok || grid.Empty() {
		return
	}
	th := u.th
	p := findPane(st, id)
	var ws *model.Workspace
	if p != nil {
		ws = findWorkspace(st, p.WorkspaceID)
	}
	tabChat := terminalChat(st, id)
	chat := tabChat != nil || u.paneChatable(st, ws, id) != nil
	for ap.shut.Clicked(gtx) {
		u.awayDismiss(id, a, ap)
		return
	}
	for ap.open.Clicked(gtx) {
		if chat {
			if tabChat != nil && ws != nil {
				u.toggleChatTerminal(st, ws) // back to the chat tab's chat
			} else {
				u.flipPaneChat(id)
			}
		}
		return
	}
	margin := gtx.Dp(theme.SpaceM)
	w := min(grid.Dx()-2*margin, gtx.Dp(awayPaneW))
	if w < gtx.Dp(200) {
		return // too narrow to say anything
	}
	pad := gtx.Dp(theme.SpaceL)
	inner := w - 2*pad
	head := "Since you left · " + model.AwayFor(a.At.Sub(a.Since))
	if i := slices.IndexFunc(st.Activities, func(x model.Activity) bool { return x.PaneID == a.Chat }); i >= 0 {
		if now := model.AwayNow(st.AwaitPR(st.Activities[i]).State); now != "" {
			head += " · " + now
		}
	}
	needs, needsFg := a.Needs, th.Fg
	if a.Asking {
		needs, needsFg = "Summarizing…", th.Muted
	}
	lines := []struct {
		s  string
		c  color.NRGBA
		sz unit.Sp
	}{{a.Status, th.Fg, th.Sp(theme.Body)}, {needs, needsFg, th.Sp(theme.Body)}, {strings.Join(model.AwayFacts(a), " · "), th.Muted, th.Sp(theme.Small)}}
	// The header row: the clock and the title, then Open chat and the x.
	bs := gtx.Dp(28)
	m := op.Record(gtx.Ops)
	btnW := 0
	if chat {
		bg := gtx
		bg.Constraints.Min = image.Point{}
		btnW = kit.Button(bg, th, &ap.open, kit.Secondary, kit.Small, "Open chat").Size.X
	}
	openCall := m.Stop()
	tg := gtx
	tg.Constraints = gl.Constraints{Max: image.Pt(max(0, inner-gtx.Dp(18)-btnW-bs-gtx.Dp(theme.SpaceS)), gtx.Constraints.Max.Y)}
	tc, ts := textCall(tg, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, head)
	headH := max(bs, ts.Y)
	// The body: the summary's two lines and the facts, each up to two lines.
	bodyM := op.Record(gtx.Ops)
	y, gap := 0, gtx.Dp(theme.Space2XS)
	for _, l := range lines {
		if l.s == "" {
			continue
		}
		lg := gtx
		lg.Constraints = gl.Constraints{Max: image.Pt(inner, gtx.Constraints.Max.Y)}
		o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		d := widget.Label{MaxLines: 2, Truncator: "…"}.Layout(lg, th.Shaper, th.UIFont, l.sz, l.s, colorCall(gtx, l.c))
		o.Pop()
		y += d.Size.Y + gap
	}
	body := bodyM.Stop()
	h := pad + headH + gtx.Dp(theme.SpaceXS) + max(0, y-gap) + pad
	at := image.Pt(grid.Min.X+(grid.Dx()-w)/2, grid.Max.Y-margin-h)
	rect := image.Rectangle{Max: image.Pt(w, h)}
	defer op.Offset(at).Push(gtx.Ops).Pop()
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusCard), kit.Floating, th.BorderSubtle, th.Surface)
	area := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &ap.card)
	pointer.CursorDefault.Add(gtx.Ops)
	area.Pop()
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &ap.card, Kinds: pointer.Press}); !ok {
			break
		}
	}
	o := op.Offset(image.Pt(pad, pad+(headH-gtx.Dp(14))/2)).Push(gtx.Ops)
	sidebar.Icon(gtx, "clock", gtx.Dp(14), th.Muted)
	o.Pop()
	o = op.Offset(image.Pt(pad+gtx.Dp(18), pad+(headH-ts.Y)/2)).Push(gtx.Ops)
	tc.Add(gtx.Ops)
	o.Pop()
	o = op.Offset(image.Pt(w-pad-bs+gtx.Dp(4), pad+(headH-bs)/2)).Push(gtx.Ops)
	kit.IconButton(gtx, th, &ap.shut, kit.Ghost, kit.Small, func(gtx gl.Context, c color.NRGBA, s int) { sidebar.Icon(gtx, "x", s, c) })
	o.Pop()
	if chat {
		o = op.Offset(image.Pt(w-pad-bs-gtx.Dp(theme.SpaceS)-btnW, pad+(headH-gtx.Dp(28))/2)).Push(gtx.Ops)
		openCall.Add(gtx.Ops)
		o.Pop()
	}
	o = op.Offset(image.Pt(pad, pad+headH+gtx.Dp(theme.SpaceXS))).Push(gtx.Ops)
	body.Add(gtx.Ops)
	o.Pop()
}
