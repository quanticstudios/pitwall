package app

import (
	"image"
	"image/color"
	"strings"

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
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// chatHeadHeight is the chat header's and the terminal bar's height.
const chatHeadHeight = theme.Space2XL + theme.SpaceM

// chatHead is the strip over a chat tab: its header in the chat view, its
// bar in the terminal view.
type chatHead struct {
	term, panel, more, back, aide widget.Clickable
	menu                          bool // the more menu is open
	backdrop                      int  // the click outside the menu that closes it
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
	u.chatHead.menu = false
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
	h := &u.chatHead
	for h.term.Clicked(gtx) {
		u.toggleChatTerminal(st, ws)
	}
	for h.panel.Clicked(gtx) {
		u.nav.panelOpen = !u.nav.panelOpen
	}
	for h.more.Clicked(gtx) {
		h.menu = !h.menu
	}
	for h.aide.Clicked(gtx) {
		h.menu = false
		openAide(findAide())
	}
	aide := curHost() == "" && findAide() != nil
	c := findChat(st, ws.Chat)
	title := tabTitle(*ws)
	claude := c != nil && c.Provider == model.ProviderClaude
	prov := model.ProviderClaude
	if c != nil {
		prov = c.Provider
	}
	th := u.th
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions { return sidebar.AgentMark(gtx, prov, gtx.Dp(16), th.Fg) }),
		gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, title)
		}),
	}
	if ws.Branch != "" {
		kids = append(kids,
			gl.Rigid(gl.Spacer{Width: theme.SpaceM}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return sidebar.Icon(gtx, "git-branch", gtx.Dp(12), th.Muted) }),
			gl.Rigid(gl.Spacer{Width: theme.SpaceXS}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, ws.Branch)
			}))
	}
	kids = append(kids, gl.Flexed(1, fill))
	if claude {
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.tipped(gtx, &h.term, withKey("Raw terminal", u.keyOf("chat_terminal")), func(gtx gl.Context) gl.Dimensions {
				return ghostIconButton(gtx, th, &h.term, "terminal", "Terminal")
			})
		}), gl.Rigid(gl.Spacer{Width: theme.SpaceXS}.Layout))
	}
	kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
		return u.tipped(gtx, &h.panel, withKey("Agent panel", u.keyOf("toggle_panel")), func(gtx gl.Context) gl.Dimensions {
			return kit.IconButton(gtx, th, &h.panel, kit.Ghost, kit.Small, func(gtx gl.Context, col color.NRGBA, size int) {
				sidebar.Icon(gtx, "panel-right", size, col)
			})
		})
	}))
	if aide {
		kids = append(kids, gl.Rigid(gl.Spacer{Width: theme.SpaceXS}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return kit.IconButton(gtx, th, &h.more, kit.Ghost, kit.Small, func(gtx gl.Context, col color.NRGBA, size int) {
				sidebar.Icon(gtx, "ellipsis", size, col)
			})
		}))
	} else {
		h.menu = false
	}
	height := u.strip(gtx, th.Bg, kids)
	if h.menu {
		u.chatMenu(gtx, height)
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
		gl.Rigid(func(gtx gl.Context) gl.Dimensions { return sidebar.Icon(gtx, "terminal", gtx.Dp(14), th.Muted) }),
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
			return u.tipped(gtx, &h.back, withKey("Back to chat", u.keyOf("chat_terminal")), func(gtx gl.Context) gl.Dimensions {
				return kit.Button(gtx, th, &h.back, kit.Secondary, kit.Small, "Back to chat")
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
	gl.Inset{Left: theme.SpaceM, Right: theme.SpaceM}.Layout(g, func(gtx gl.Context) gl.Dimensions {
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
		call, sz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), fg, text)
		pad, gap, is := gtx.Dp(theme.SpaceS), gtx.Dp(6), gtx.Dp(14)
		size := image.Pt(pad+is+gap+sz.X+pad, gtx.Dp(24))
		r := gtx.Dp(theme.RadiusControl)
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

// tipped draws w and, while c is hovered, tip under it over everything.
func (u *ui) tipped(gtx gl.Context, c *widget.Clickable, tip string, w gl.Widget) gl.Dimensions {
	d := w(gtx)
	if !c.Hovered() || tip == "" {
		return d
	}
	th := u.th
	call, sz := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, tip)
	pad := image.Pt(gtx.Dp(theme.SpaceS), gtx.Dp(theme.SpaceXS+1))
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
func (u *ui) chatMenu(gtx gl.Context, top int) {
	h := &u.chatHead
	th := u.th
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &h.backdrop, Kinds: pointer.Press}); !ok {
			break
		}
		h.menu = false
	}
	call, sz := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, "Open in aide")
	pad := gtx.Dp(theme.SpaceXS)
	row := image.Pt(max(sz.X+2*gtx.Dp(theme.SpaceM), gtx.Dp(160)), gtx.Dp(28))
	box := row.Add(image.Pt(2*pad, 2*pad))
	right, btn := gtx.Constraints.Max.X-gtx.Dp(theme.SpaceM), gtx.Dp(24)
	anchor := image.Rect(right-btn, (top-btn)/2, right, (top+btn)/2)
	at := kit.Place(anchor, box, image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, 1<<20)}, kit.BelowEnd, gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceS))
	m := op.Record(gtx.Ops)
	bd := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &h.backdrop)
	bd.Pop()
	o := op.Offset(at).Push(gtx.Ops)
	kit.Surface(gtx, image.Rectangle{Max: box}, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderSubtle, th.SurfaceElevated)
	o2 := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	g := gtx
	g.Constraints = gl.Exact(row)
	h.aide.Layout(g, func(gtx gl.Context) gl.Dimensions {
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
	op.Defer(gtx.Ops, m.Stop())
}
