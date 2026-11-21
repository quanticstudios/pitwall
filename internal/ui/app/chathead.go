package app

import (
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// chatHeadHeight is the chat header's and the terminal bar's height.
const chatHeadHeight = theme.Space2XL + theme.SpaceM

// The header's sizes, on the spacing scale.
const (
	headControl = theme.SpaceXL + theme.SpaceXS // a control's height, kit.Medium's
	headPad     = theme.SpaceS + theme.Space2XS // a text button's side padding, kit.Medium's
	headIcon    = theme.SpaceM + theme.Space2XS // a control's glyph, kit.IconButton's
	headMark    = theme.SpaceL                  // the agent mark
	headBranch  = theme.SpaceM                  // the branch glyph
	menuWidth   = 10 * theme.SpaceL             // the more menu at its narrowest
)

// chatHead is the strip over a chat tab: its header in the chat view, its
// bar in the terminal view.
type chatHead struct {
	term, panel, more, back, aide widget.Clickable
	files                         widget.Clickable // the chat's Files and Changes panel
	menu                          bool             // the more menu is open
	menuAt                        time.Time        // when it last opened or closed, for its fade
	backdrop                      int              // the click outside the menu that closes it

	tipOf    *widget.Clickable // the control hovered for a tooltip
	tipSince time.Time         // since when
	tipShown time.Time         // the last frame a tooltip showed, so a neighbor's shows at once

	find widget.Clickable // Find in chat
}

// Tooltip timing: the first waits tipDelay, a neighbor hovered within
// tipChain of the last one shows at once.
const (
	tipDelay = 500 * time.Millisecond
	tipChain = 300 * time.Millisecond
)

// Menu fade: in from the trigger's edge, out quicker.
const (
	menuIn  = 150 * time.Millisecond
	menuOut = 100 * time.Millisecond
)

// tipReady reports whether a tooltip hovered since since shows at now,
// the last one having shown at shown.
func tipReady(now, since, shown time.Time) bool {
	return now.Sub(since) >= tipDelay || now.Sub(shown) <= tipChain
}

// setMenu opens or closes the more menu at now.
func (h *chatHead) setMenu(on bool, now time.Time) {
	if h.menu != on {
		h.menu, h.menuAt = on, now
	}
}

// chatBusy reports whether chat state c has a turn running or waiting on
// someone, which going to the terminal would stop.
func chatBusy(state string) bool {
	return state == session.StateRunning || state == session.StateWaiting
}

// toggleChatTerminal flips chat tab ws between the chat and Claude Code's
// TUI, asking first when that stops a turn.
func (u *ui) toggleChatTerminal(st *model.State, ws *model.Workspace) {
	c := findChat(st, ws.Chat)
	if c == nil || c.Provider != model.ProviderClaude {
		return
	}
	msg := proto.ChatTerminal{Chat: c.ID, On: c.Terminal == ""}
	if msg.On && chatBusy(c.State) {
		u.modal.open(modalChatTerminal, ws.ID)
		u.modal.then = msg
		return
	}
	u.chatHead.setMenu(false, time.Now())
	u.send(msg)
}

// chatTerminalBody is the dialog that asks before a running turn stops
// for the terminal.
func (u *ui) chatTerminalBody(gtx gl.Context) gl.Dimensions {
	th := u.th
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "Stop the turn and open the terminal?")
		}),
		gl.Rigid(gl.Spacer{Height: theme.SpaceM}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Muted, "Claude is mid-turn. The terminal runs the same conversation, so this turn stops first.")
		}),
		gl.Rigid(gl.Spacer{Height: theme.SpaceXL}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions { return u.buttons(gtx, "Cancel", "Stop and open", kit.Primary) }),
	)
}

// keyOf is the first key of action, "" when it is unbound.
func (u *ui) keyOf(action string) string { return firstChord(u.nav.bind().Global[action]) }

// withKey is a tooltip: what, and its key in parentheses when it has one.
func withKey(what, key string) string {
	if key == "" {
		return what
	}
	return what + " (" + key + ")"
}

// layoutChatHead draws chat tab ws's header strip and returns its height:
// the Claude mark, the title, the branch, and Terminal, the agent panel
// and a more menu with Open in aide when aide is installed.
func (u *ui) layoutChatHead(gtx gl.Context, st *model.State, ws *model.Workspace) int {
	return u.chatStrip(gtx, &u.chatHead, ws, findChat(st, ws.Chat), func() { u.toggleChatTerminal(st, ws) })
}

// chatStrip is layoutChatHead's strip with header state h, for chat c in
// tab ws, where Terminal runs toggle: a chat tab's, or a pane chat's.
func (u *ui) chatStrip(gtx gl.Context, h *chatHead, ws *model.Workspace, c *model.Chat, toggle func()) int {
	for h.term.Clicked(gtx) {
		toggle()
	}
	for h.panel.Clicked(gtx) {
		u.nav.panelOpen = !u.nav.panelOpen
	}
	var tr *panel.Transcript
	if c != nil {
		tr = u.transcripts[c.ID]
	}
	for h.files.Clicked(gtx) {
		if tr != nil {
			tr.ToggleFiles()
		}
	}
	for h.find.Clicked(gtx) {
		if tr != nil {
			tr.OpenFind()
		}
	}
	for h.more.Clicked(gtx) {
		h.setMenu(!h.menu, gtx.Now)
	}
	for h.aide.Clicked(gtx) {
		h.setMenu(false, gtx.Now)
		openAide(findAide())
	}
	aide := curHost() == "" && findAide() != nil
	title := tabTitle(*ws)
	// A pane chat's agent runs in its pane: Codex's has a terminal too.
	claude := c != nil && (c.Provider == model.ProviderClaude || ws.Chat == "")
	prov := model.ProviderClaude
	if c != nil {
		prov = c.Provider
	}
	th := u.th
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions { return sidebar.AgentMark(gtx, prov, gtx.Dp(headMark), th.Fg) }),
		gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, title)
		}),
	}
	if ws.Branch != "" {
		kids = append(kids,
			gl.Rigid(gl.Spacer{Width: theme.SpaceM}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return sidebar.Icon(gtx, "git-branch", gtx.Dp(headBranch), th.Muted)
			}),
			gl.Rigid(gl.Spacer{Width: theme.SpaceXS}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, ws.Branch)
			}))
	}
	if l := u.chatSizeLabel(gtx); l != nil {
		kids = append(kids, gl.Rigid(gl.Spacer{Width: theme.SpaceM}.Layout), gl.Rigid(l))
	}
	kids = append(kids, gl.Flexed(1, fill), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
		return u.tipped(gtx, h, &h.find, withKey("Find in chat", "Ctrl+F"), func(gtx gl.Context) gl.Dimensions {
			return kit.IconButton(gtx, th, &h.find, kit.Ghost, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
				sidebar.Icon(gtx, "search", size, col)
			})
		})
	}), gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout))
	if claude {
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.tipped(gtx, h, &h.term, withKey("Raw terminal", u.keyOf("chat_terminal")), func(gtx gl.Context) gl.Dimensions {
				return ghostIconButton(gtx, th, &h.term, "terminal", "Terminal")
			})
		}), gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout))
	}
	if tr != nil {
		kind := kit.Ghost
		if tr.FilesOpen() {
			kind = kit.Secondary
		}
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.tipped(gtx, h, &h.files, "Files and changes", func(gtx gl.Context) gl.Dimensions {
				return kit.IconButton(gtx, th, &h.files, kind, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
					sidebar.IconPath(gtx, panel.IconFileDiff, size, col)
				})
			})
		}), gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout))
	}
	kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
		return u.tipped(gtx, h, &h.panel, withKey("Agent panel", u.keyOf("toggle_panel")), func(gtx gl.Context) gl.Dimensions {
			return kit.IconButton(gtx, th, &h.panel, kit.Ghost, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
				sidebar.Icon(gtx, "panel-right", size, col)
			})
		})
	}))
	if aide {
		kids = append(kids, gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return kit.IconButton(gtx, th, &h.more, kit.Ghost, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
				sidebar.Icon(gtx, "ellipsis", size, col)
			})
		}))
	} else {
		h.setMenu(false, gtx.Now)
	}
	height := u.strip(gtx, th.Bg, kids)
	if aide && (h.menu || !anim.Reduced() && gtx.Now.Sub(h.menuAt) < menuOut) {
		u.chatMenu(gtx, h, height)
	}
	return height
}

// layoutTerminalBar draws the bar over chat tab ws's terminal view and
// returns its height: the terminal icon, the title, the command the pane
// runs, and Back to chat.
func (u *ui) layoutTerminalBar(gtx gl.Context, st *model.State, ws *model.Workspace) int {
	h := &u.chatHead
	for h.back.Clicked(gtx) {
		u.toggleChatTerminal(st, ws)
	}
	th := u.th
	what := "terminal view"
	if c := findChat(st, ws.Chat); c != nil {
		if p := findPane(st, c.Terminal); p != nil {
			what += " · " + terminalLine(p.Cmd)
		}
	}
	return u.strip(gtx, th.Surface, []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions { return sidebar.Icon(gtx, "terminal", gtx.Dp(headIcon), th.Muted) }),
		gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, tabTitle(*ws))
		}),
		gl.Rigid(gl.Spacer{Width: theme.SpaceM}.Layout),
		gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
			d := label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, what)
			return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, d.Size.Y)}
		}),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.tipped(gtx, h, &h.back, withKey("Back to chat", u.keyOf("chat_terminal")), func(gtx gl.Context) gl.Dimensions {
				return kit.Button(gtx, th, &h.back, kit.Secondary, kit.Medium, "Back to chat")
			})
		}),
	})
}

// terminalLine is how the bar names the terminal's command: the program's
// name and its flags, a session id cut to its first four characters.
func terminalLine(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	out := []string{"claude"}
	for i := 1; i < len(cmd); i++ {
		a := cmd[i]
		if (cmd[i-1] == "--resume" || cmd[i-1] == "--session-id") && len(a) > 4 {
			a = a[:4] + "…"
		}
		out = append(out, a)
	}
	return strings.Join(out, " ")
}

// strip draws kids in a row over fill, chatHeadHeight tall with a border
// under it, and returns its height.
func (u *ui) strip(gtx gl.Context, fill color.NRGBA, kids []gl.FlexChild) int {
	th := u.th
	hgt := gtx.Dp(chatHeadHeight)
	size := image.Pt(gtx.Constraints.Max.X, hgt)
	paint.FillShape(gtx.Ops, fill, clip.Rect{Max: size}.Op())
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, hgt-gtx.Dp(1)), Max: size}.Op())
	g := gtx
	g.Constraints = gl.Exact(size)
	gl.Inset{Left: theme.SpaceL, Right: theme.SpaceL}.Layout(g, func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Alignment: gl.Middle}.Layout(gtx, kids...)
	})
	return hgt
}

// fill takes the width it is given, as a Flexed spacer must.
func fill(gtx gl.Context) gl.Dimensions {
	return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
}

// label is one line of s, cut with an ellipsis to the width it gets.
func label(gtx gl.Context, th *theme.Theme, f font.Font, size unit.Sp, c color.NRGBA, s string) gl.Dimensions {
	gtx.Constraints.Min = image.Point{}
	call, sz := textCall(gtx, th, f, size, c, s)
	defer clip.Rect{Max: image.Pt(min(sz.X, gtx.Constraints.Max.X), sz.Y)}.Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return gl.Dimensions{Size: image.Pt(min(sz.X, gtx.Constraints.Max.X), sz.Y)}
}

// ghostIconButton is a ghost button with an icon before its label, as the
// header's Terminal.
func ghostIconButton(gtx gl.Context, th *theme.Theme, c *widget.Clickable, icon, text string) gl.Dimensions {
	return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		fg, fill := th.Muted, color.NRGBA{}
		switch {
		case c.Pressed():
			fg, fill = th.Fg, th.Pressed
		case c.Hovered():
			fg, fill = th.Fg, th.Hover
		}
		call, sz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), fg, text)
		pad, gap, is := gtx.Dp(headPad), gtx.Dp(theme.SpaceS), gtx.Dp(headIcon)
		size := image.Pt(pad+is+gap+sz.X+pad, gtx.Dp(headControl))
		r := gtx.Dp(theme.RadiusControl)
		if gtx.Focused(c) {
			kit.FocusRing(gtx, th, image.Rectangle{Max: size}, r)
		}
		if fill.A > 0 {
			paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: size}, r).Op(gtx.Ops))
		}
		o := op.Offset(image.Pt(pad, (size.Y-is)/2)).Push(gtx.Ops)
		sidebar.Icon(gtx, icon, is, fg)
		o.Pop()
		o = op.Offset(image.Pt(pad+is+gap, (size.Y-sz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: size}
	})
}

// tipped draws w and, while c is hovered, tip under it over everything:
// after tipDelay, or at once when another tooltip showed within tipChain.
func (u *ui) tipped(gtx gl.Context, h *chatHead, c *widget.Clickable, tip string, w gl.Widget) gl.Dimensions {
	d := w(gtx)
	if !c.Hovered() || tip == "" {
		if h.tipOf == c {
			h.tipOf = nil
		}
		return d
	}
	if h.tipOf != c {
		h.tipOf, h.tipSince = c, gtx.Now
	}
	if !tipReady(gtx.Now, h.tipSince, h.tipShown) {
		gtx.Execute(op.InvalidateCmd{At: h.tipSince.Add(tipDelay)})
		return d
	}
	h.tipShown = gtx.Now
	th := u.th
	call, sz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, tip)
	pad := image.Pt(gtx.Dp(theme.SpaceS), gtx.Dp(theme.SpaceXS))
	box := sz.Add(pad.Mul(2))
	at := image.Pt(d.Size.X-box.X, d.Size.Y+gtx.Dp(theme.SpaceXS))
	if d.Size.X > box.X {
		at.X = (d.Size.X - box.X) / 2
	}
	m := op.Record(gtx.Ops)
	o := op.Offset(at).Push(gtx.Ops)
	kit.Surface(gtx, image.Rectangle{Max: box}, gtx.Dp(theme.RadiusControl), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	o2 := op.Offset(pad).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o2.Pop()
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
	return d
}

// chatMenu draws the header's more menu under its button, the strip's last,
// with a press anywhere else closing it. top is the strip's height.
func (u *ui) chatMenu(gtx gl.Context, h *chatHead, top int) {
	th := u.th
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &h.backdrop, Kinds: pointer.Press}); !ok {
			break
		}
		h.setMenu(false, gtx.Now)
	}
	call, sz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, "Open in aide")
	pad := gtx.Dp(theme.SpaceXS)
	row := image.Pt(max(sz.X+2*gtx.Dp(theme.SpaceM), gtx.Dp(menuWidth)), gtx.Dp(headControl))
	box := row.Add(image.Pt(2*pad, 2*pad))
	right, btn := gtx.Constraints.Max.X-gtx.Dp(theme.SpaceL), gtx.Dp(headControl)
	anchor := image.Rect(right-btn, (top-btn)/2, right, (top+btn)/2)
	at := kit.Place(anchor, box, image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, 1<<20)}, kit.BelowEnd, gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceS))
	t := anim.At(gtx, h.menuAt, menuIn)
	if !h.menu {
		t = 1 - anim.At(gtx, h.menuAt, menuOut)
	}
	m := op.Record(gtx.Ops)
	if h.menu {
		bd := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
		event.Op(gtx.Ops, &h.backdrop)
		bd.Pop()
	}
	// Fade and scale from 97% at the trigger's edge, the menu's top right.
	fade := paint.PushOpacity(gtx.Ops, t)
	sc := 0.97 + 0.03*t
	edge := f32.Pt(float32(at.X+box.X), float32(at.Y))
	grow := op.Affine(f32.AffineId().Scale(edge, f32.Pt(sc, sc))).Push(gtx.Ops)
	o := op.Offset(at).Push(gtx.Ops)
	kit.Surface(gtx, image.Rectangle{Max: box}, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderSubtle, th.SurfaceElevated)
	o2 := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	g := gtx
	g.Constraints = gl.Exact(row)
	h.aide.Layout(g, func(gtx gl.Context) gl.Dimensions {
		if gtx.Focused(&h.aide) {
			kit.FocusRing(gtx, th, image.Rectangle{Max: row}, gtx.Dp(theme.RadiusControl))
		}
		if h.aide.Hovered() {
			paint.FillShape(gtx.Ops, th.Hover, clip.UniformRRect(image.Rectangle{Max: row}, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		t := op.Offset(image.Pt(gtx.Dp(theme.SpaceM), (row.Y-sz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		t.Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: row}
	})
	o2.Pop()
	o.Pop()
	grow.Pop()
	fade.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
