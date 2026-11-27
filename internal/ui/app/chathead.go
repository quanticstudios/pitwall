package app

import (
	"image"
	"image/color"
	"path/filepath"
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
	toolsNarrow = 45 * theme.SpaceL             // below it the Tools button drops its label
)

// chatHead is the strip over a chat tab: its header in the chat view, its
// bar in the terminal view.
type chatHead struct {
	term, panel, more, back, aide widget.Clickable
	files                         widget.Clickable // the chat's Files and Changes panel
	servers                       widget.Clickable // its Servers view, while the tab runs one
	menu                          bool             // the more menu is open
	menuAt                        time.Time        // when it last opened or closed, for its fade
	backdrop                      int              // the click outside the menu that closes it

	tipOf    *widget.Clickable // the control hovered for a tooltip
	tipSince time.Time         // since when
	tipShown time.Time         // the last frame a tooltip showed, so a neighbor's shows at once

	find  widget.Clickable // Find in chat
	tools widget.Clickable // opens the [chat] tool_calls dropdown

	toolsOpen     bool                               // the Tools menu is open
	toolsAt       time.Time                          // when it last opened or closed, for its fade
	toolsSel      int                                // highlighted choice in the Tools menu
	toolsHover    int                                // choice hovered last frame, -1 for none
	toolsAnchor   image.Rectangle                    // the Tools button's rect in the chat area
	toolsBackdrop int                                // click outside closes the menu
	toolsPanel    int                                // swallows clicks inside the menu
	toolsTag      int                                // key focus while open
	toolsReq      bool                               // key focus requested since opening
	toolsFocused  bool                               // toolsTag has had focus since opening
	toolsItems    [len(toolChoices)]widget.Clickable // one per toolChoice
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

// terminalAgent reports whether a chat of agent p has a terminal view:
// Claude's, and Codex's and agy's on a daemon that runs theirs.
func (u *ui) terminalAgent(p model.Provider) bool {
	switch p {
	case model.ProviderClaude:
		return true
	case model.ProviderCodex, model.ProviderAntigravity:
		return linkLevel(u.b) >= proto.ChatTerminalAgentsLevel
	}
	return false
}

// toggleChatTerminal flips chat tab ws between the chat and its agent's
// TUI, asking first when that stops a turn.
func (u *ui) toggleChatTerminal(st *model.State, ws *model.Workspace) {
	c := findChat(st, ws.Chat)
	if c == nil || !u.terminalAgent(c.Provider) {
		return
	}
	msg := proto.ChatTerminal{Chat: c.ID, On: c.Terminal == ""}
	if msg.On && chatBusy(c.State) {
		u.modal.open(modalChatTerminal, ws.ID)
		u.modal.then, u.modal.agents = msg, []string{sidebar.AgentName(c.Provider)}
		return
	}
	u.chatHead.setMenu(false, time.Now())
	u.chatHead.closeTools()
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
			agent := "Claude"
			if len(u.modal.agents) > 0 {
				agent = u.modal.agents[0]
			}
			return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Muted, agent+" is mid-turn. The terminal runs the same conversation, so this turn stops first.")
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
	for h.tools.Clicked(gtx) {
		if h.toolsOpen {
			h.setTools(false, gtx.Now)
		} else {
			h.toolsSel = toolsIndex(u.cfg.Chat.ToolCalls)
			h.setTools(true, gtx.Now)
		}
	}
	if tr == nil {
		h.closeTools()
	} else {
		u.toolsEvents(gtx, h)
	}
	for h.servers.Clicked(gtx) {
		if tr != nil {
			tr.ShowServers()
		}
	}
	for h.more.Clicked(gtx) {
		open := !h.menu
		if open {
			h.setTools(false, gtx.Now)
		}
		h.setMenu(open, gtx.Now)
	}
	for h.aide.Clicked(gtx) {
		h.setMenu(false, gtx.Now)
		openAide(findAide())
	}
	aide := curHost() == "" && findAide() != nil
	title := tabTitle(*ws)
	// A pane chat's agent runs in its pane, so it has a terminal whatever
	// the agent.
	term := c != nil && (ws.Chat == "" || u.terminalAgent(c.Provider))
	prov := model.ProviderClaude
	if c != nil {
		prov = c.Provider
	}
	th := u.th
	left := func(gtx gl.Context) gl.Dimensions {
		hgt := gtx.Dp(headControl)
		maxW := gtx.Constraints.Max.X
		if maxW <= 0 {
			return gl.Dimensions{Size: image.Pt(0, hgt)}
		}
		defer clip.Rect{Max: image.Pt(maxW, hgt)}.Push(gtx.Ops).Pop()
		x := 0
		markS := gtx.Dp(headMark)
		o := op.Offset(image.Pt(0, (hgt-markS)/2)).Push(gtx.Ops)
		sidebar.AgentMark(gtx, prov, markS, th.Fg)
		o.Pop()
		x += markS + gtx.Dp(theme.SpaceS)
		if x >= maxW {
			return gl.Dimensions{Size: image.Pt(maxW, hgt)}
		}
		var sizeCall op.CallOp
		var sizeDim gl.Dimensions
		sizeGap := 0
		if l := u.chatSizeLabel(gtx); l != nil {
			m := op.Record(gtx.Ops)
			g := gtx
			g.Constraints = gl.Constraints{Max: image.Pt(maxW-x, hgt)}
			sizeDim = l(g)
			sizeCall = m.Stop()
			sizeGap = gtx.Dp(theme.SpaceM)
		}
		avail := maxW - x
		if sizeDim.Size.X > 0 && sizeDim.Size.X+sizeGap+gtx.Dp(48) <= avail {
			avail -= sizeDim.Size.X + sizeGap
		} else {
			sizeDim = gl.Dimensions{}
			sizeGap = 0
		}
		unbounded := gtx
		unbounded.Constraints = gl.Constraints{Max: image.Pt(1<<20, hgt)}
		_, titleNat := textCall(unbounded, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, title)
		branchIconW, branchGap, branchPad := gtx.Dp(headBranch), gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceM)
		branchNatW := 0
		if ws.Branch != "" {
			_, bs := textCall(unbounded, th, th.UIFont, th.Sp(theme.Small), th.Muted, ws.Branch)
			branchNatW = bs.X
		}
		titleW, branchW := min(titleNat.X, avail), 0
		if ws.Branch != "" && avail > branchPad+branchIconW+branchGap+gtx.Dp(24) {
			need := titleNat.X + branchPad + branchIconW + branchGap + branchNatW
			if need <= avail {
				titleW, branchW = titleNat.X, branchNatW
			} else {
				titleW = min(titleNat.X, max(avail/2, avail-(branchPad+branchIconW+branchGap+branchNatW)))
				branchW = avail - titleW - branchPad - branchIconW - branchGap
				if branchW < gtx.Dp(24) {
					titleW = min(titleNat.X, avail)
					branchW = 0
				}
			}
		}
		tg := gtx
		tg.Constraints = gl.Constraints{Max: image.Pt(max(0, titleW), hgt)}
		m := op.Record(gtx.Ops)
		td := label(tg, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, title)
		tcall := m.Stop()
		o = op.Offset(image.Pt(x, (hgt-td.Size.Y)/2)).Push(gtx.Ops)
		tcall.Add(gtx.Ops)
		o.Pop()
		x += td.Size.X
		if branchW > 0 {
			x += branchPad
			o = op.Offset(image.Pt(x, (hgt-branchIconW)/2)).Push(gtx.Ops)
			sidebar.Icon(gtx, "git-branch", branchIconW, th.Muted)
			o.Pop()
			x += branchIconW + branchGap
			bg := gtx
			bg.Constraints = gl.Constraints{Max: image.Pt(branchW, hgt)}
			m = op.Record(gtx.Ops)
			bd := label(bg, th, th.UIFont, th.Sp(theme.Small), th.Muted, ws.Branch)
			bcall := m.Stop()
			o = op.Offset(image.Pt(x, (hgt-bd.Size.Y)/2)).Push(gtx.Ops)
			bcall.Add(gtx.Ops)
			o.Pop()
			x += bd.Size.X
		}
		if sizeDim.Size.X > 0 {
			x += sizeGap
			o = op.Offset(image.Pt(x, (hgt-sizeDim.Size.Y)/2)).Push(gtx.Ops)
			sizeCall.Add(gtx.Ops)
			o.Pop()
		}
		return gl.Dimensions{Size: image.Pt(maxW, hgt)}
	}
	kids := []gl.FlexChild{
		gl.Flexed(1, left),
		gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
	}
	var toolsW, afterW int
	after := func(w gl.Widget) gl.FlexChild {
		return gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			d := w(gtx)
			afterW += d.Size.X
			return d
		})
	}
	if tr != nil {
		mode := u.cfg.Chat.ToolCalls
		narrow := gtx.Constraints.Max.X < gtx.Dp(toolsNarrow) // a split pane's header keeps the glyph alone
		tip := toolsTip(mode)
		if h.toolsOpen {
			tip = ""
		}
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			d := u.tipped(gtx, h, &h.tools, tip, func(gtx gl.Context) gl.Dimensions {
				if narrow {
					kind := kit.Ghost
					if h.toolsOpen {
						kind = kit.Secondary
					}
					return kit.IconButton(gtx, th, &h.tools, kind, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
						sidebar.Icon(gtx, "wrench", size, col)
					})
				}
				return ghostMenuButton(gtx, th, &h.tools, "wrench", toolsLabel(mode), h.toolsOpen)
			})
			toolsW = d.Size.X
			return d
		}), after(gl.Spacer{Width: theme.SpaceS}.Layout))
	}
	kids = append(kids, after(func(gtx gl.Context) gl.Dimensions {
		return u.tipped(gtx, h, &h.find, withKey("Find in chat", "Ctrl+F"), func(gtx gl.Context) gl.Dimensions {
			return kit.IconButton(gtx, th, &h.find, kit.Ghost, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
				sidebar.Icon(gtx, "search", size, col)
			})
		})
	}), after(gl.Spacer{Width: theme.SpaceS}.Layout))
	if term {
		kids = append(kids, after(func(gtx gl.Context) gl.Dimensions {
			return u.tipped(gtx, h, &h.term, withKey("Raw terminal", u.keyOf("chat_terminal")), func(gtx gl.Context) gl.Dimensions {
				return ghostIconButton(gtx, th, &h.term, "terminal", "Terminal")
			})
		}), after(gl.Spacer{Width: theme.SpaceS}.Layout))
	}
	// What the tab's servers were when the transcript last drew, a frame ago.
	if tr != nil && len(tr.Servers.List) > 0 {
		kind := kit.Ghost
		if tr.ServersOpen() {
			kind = kit.Secondary
		}
		kids = append(kids, after(func(gtx gl.Context) gl.Dimensions {
			return u.tipped(gtx, h, &h.servers, serversTip(tr.Servers.List), func(gtx gl.Context) gl.Dimensions {
				return kit.IconButton(gtx, th, &h.servers, kind, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
					sidebar.IconPath(gtx, panel.IconGlobe, size, col)
				})
			})
		}), after(gl.Spacer{Width: theme.SpaceS}.Layout))
	}
	if tr != nil {
		kind := kit.Ghost
		if tr.FilesOpen() && !tr.ServersOpen() {
			kind = kit.Secondary
		}
		kids = append(kids, after(func(gtx gl.Context) gl.Dimensions {
			return u.tipped(gtx, h, &h.files, "Files and changes", func(gtx gl.Context) gl.Dimensions {
				return kit.IconButton(gtx, th, &h.files, kind, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
					sidebar.IconPath(gtx, panel.IconFileDiff, size, col)
				})
			})
		}), after(gl.Spacer{Width: theme.SpaceS}.Layout))
	}
	kids = append(kids, after(func(gtx gl.Context) gl.Dimensions {
		return u.tipped(gtx, h, &h.panel, withKey("Agent panel", u.keyOf("toggle_panel")), func(gtx gl.Context) gl.Dimensions {
			return kit.IconButton(gtx, th, &h.panel, kit.Ghost, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
				sidebar.Icon(gtx, "panel-right", size, col)
			})
		})
	}))
	if aide {
		kids = append(kids, after(gl.Spacer{Width: theme.SpaceS}.Layout), after(func(gtx gl.Context) gl.Dimensions {
			return kit.IconButton(gtx, th, &h.more, kit.Ghost, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
				sidebar.Icon(gtx, "ellipsis", size, col)
			})
		}))
	} else {
		h.setMenu(false, gtx.Now)
	}
	height := u.strip(gtx, th.Bg, kids)
	if tr != nil {
		btn := gtx.Dp(headControl)
		right := max(toolsW, gtx.Constraints.Max.X-gtx.Dp(theme.SpaceL)-afterW)
		h.toolsAnchor = image.Rect(right-toolsW, (height-btn)/2, right, (height+btn)/2)
		if h.toolsOpen || !anim.Reduced() && !h.toolsAt.IsZero() && gtx.Now.Sub(h.toolsAt) < menuOut {
			u.toolsMenu(gtx, h)
		}
	}
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
	out := []string{strings.TrimSuffix(filepath.Base(cmd[0]), ".exe")}
	for i := 1; i < len(cmd); i++ {
		a := cmd[i]
		if (cmd[i-1] == "--resume" || cmd[i-1] == "--session-id" || cmd[i-1] == "--conversation" || cmd[i-1] == "resume") && len(a) > 4 {
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
	return ghostButton(gtx, th, c, icon, text, false, false)
}

// ghostMenuButton is ghostIconButton with a trailing chevron for a
// dropdown trigger, keeping its hover look while open.
func ghostMenuButton(gtx gl.Context, th *theme.Theme, c *widget.Clickable, icon, text string, open bool) gl.Dimensions {
	return ghostButton(gtx, th, c, icon, text, true, open)
}

func ghostButton(gtx gl.Context, th *theme.Theme, c *widget.Clickable, icon, text string, chevron, open bool) gl.Dimensions {
	return kit.Clickable(gtx, c, func(gtx gl.Context) gl.Dimensions {
		fg, fill := th.Muted, color.NRGBA{}
		switch {
		case c.Pressed():
			fg, fill = th.Fg, th.Pressed
		case c.Hovered() || open:
			fg, fill = th.Fg, th.Hover
		}
		call, sz := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Body), fg, text)
		pad, gap, is := gtx.Dp(headPad), gtx.Dp(theme.SpaceS), gtx.Dp(headIcon)
		w := pad + is + gap + sz.X + pad
		chevGap, chevSz := gtx.Dp(theme.SpaceXS), gtx.Dp(theme.SpaceM)
		if chevron {
			w += chevGap + chevSz
		}
		size := image.Pt(w, gtx.Dp(headControl))
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
		if chevron {
			o = op.Offset(image.Pt(pad+is+gap+sz.X+chevGap, (size.Y-chevSz)/2)).Push(gtx.Ops)
			sidebar.Icon(gtx, "chevron-down", chevSz, th.Muted)
			o.Pop()
		}
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
	kit.Clickable(g, &h.aide, func(gtx gl.Context) gl.Dimensions {
		if gtx.Focused(&h.aide) {
			kit.FocusRing(gtx, th, image.Rectangle{Max: row}, gtx.Dp(theme.RadiusControl))
		}
		if h.aide.Hovered() {
			paint.FillShape(gtx.Ops, th.HoverElevated, clip.UniformRRect(image.Rectangle{Max: row}, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		t := op.Offset(image.Pt(gtx.Dp(theme.SpaceM), (row.Y-sz.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		t.Pop()
		return gl.Dimensions{Size: row}
	})
	o2.Pop()
	o.Pop()
	grow.Pop()
	fade.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
