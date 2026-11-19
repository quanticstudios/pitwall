package app

import (
	"image"
	"slices"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// A pane chat is a pane running Claude Code's TUI, drawn as the desktop
// chat in the pane's own rect (the daemon side is daemon/panechat.go).
// Each pane of a split decides for itself; [chat] claude picks the view a
// pane starts in, and chat_terminal flips the focused one. Claude keeps
// running either way, and the pane keeps getting its real size.

// paneChatOf is pane id as a chat, with a state from its activity, nil
// when the pane runs no Claude Code with a transcript.
func paneChatOf(st *model.State, id string) *model.Chat {
	p := findPane(st, id)
	if p == nil || !model.PaneChat(*p) {
		return nil
	}
	c := &model.Chat{ID: id, WorkspaceID: p.WorkspaceID, Provider: model.ProviderClaude, State: session.StateReady}
	for _, a := range st.Activities {
		if a.PaneID != id {
			continue
		}
		switch a.State {
		case model.StateWorking:
			c.State = session.StateRunning
		case model.StatePendingApproval, model.StatePlanReady, model.StateAwaitingInput:
			c.State = session.StateWaiting
		}
	}
	return c
}

// paneChatShown reports whether pane id of tab ws shows as a chat now: a
// pane chat of a tab of panes, on a local daemon that answers for it,
// in the view [chat] claude starts it in unless flipped since.
func (u *ui) paneChatShown(st *model.State, ws *model.Workspace, id string) bool {
	if ws == nil || ws.Chat != "" || curHost() != "" || u.hostOf(id) != "" || linkLevel(u.b) < proto.PaneChatLevel {
		return false
	}
	if paneChatOf(st, id) == nil {
		delete(u.paneFlip, id) // the next claude in the pane starts in the default view
		return false
	}
	_, ok := u.b.(Chatter)
	return ok && u.cfg.Chat.Claude != u.paneFlip[id]
}

// flipPaneChat flips pane id between its chat and the TUI.
func (u *ui) flipPaneChat(id string) {
	if u.paneFlip == nil {
		u.paneFlip = map[string]bool{}
	}
	u.paneFlip[id] = !u.paneFlip[id]
	if u.invalidate != nil { // why: the flip lands after this frame drew the pane
		u.invalidate()
	}
}

// chatFrame draws a chat's frame in size as paneChrome draws a terminal
// pane's, lit when it has the keys, filled with the chat's background, and
// clips to its rounded corners; it returns the rect inside and that clip.
func (u *ui) chatFrame(gtx gl.Context, size image.Point, lit, sole bool) (image.Rectangle, clip.Stack) {
	in := paneChrome(gtx, u.th, image.Rectangle{Max: size}, paneBorder(u.th, lit), sole)
	rr := clip.UniformRRect(in, roundedFor(gtx, sole))
	paint.FillShape(gtx.Ops, u.th.Bg, rr.Op(gtx.Ops))
	return in, rr.Push(gtx.Ops)
}

// paneChatKey reports whether chat_terminal is the window's on the shown
// tab: one of its panes is a pane chat. Not only the focused one: a click
// that focuses a pane chat and a quick Alt+T can come in one batch, before
// a frame registers filters for the new focus, and the key would be lost.
func (u *ui) paneChatKey(st *model.State) bool {
	ws := findWorkspace(st, u.nav.workspace)
	if ws == nil || ws.Chat != "" || linkLevel(u.b) < proto.PaneChatLevel {
		return false
	}
	tab := shownTab(ws)
	return tab != nil && slices.ContainsFunc(panesOf(tab.Layout), func(id string) bool { return paneChatOf(st, id) != nil })
}

// layoutPaneChat draws pane id as a chat in r, where layoutPanes put the
// pane: the pane laid out unseen first, which keeps its PTY at the
// rect's size and the TUI drawn right for a flip back, then the chat's
// header, transcript and composer. focused is whether it takes the keys.
func (u *ui) layoutPaneChat(gtx gl.Context, st *model.State, ws *model.Workspace, p *paneUI, id string, r layout.Rect, focused, sole bool) {
	unseen := op.Record(gtx.Ops)
	u.layoutPane(gtx, st, p, id, r, false, false, sole, nil)
	unseen.Stop()

	rect := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	lit := focused || (u.nav.paneMode || u.hints.on) && id == u.nav.focused()
	in, cl := u.chatFrame(gtx, rect.Size(), lit, sole)
	defer cl.Pop()
	defer op.Offset(in.Min).Push(gtx.Ops).Pop()
	gtx.Constraints = gl.Exact(in.Size())
	// A press anywhere focuses the pane, as on the TUI; layoutPane reads it.
	pass := pointer.PassOp{}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.focusClick)
	pass.Pop()

	if u.paneHeads == nil {
		u.paneHeads = map[string]*chatHead{}
	}
	h := u.paneHeads[id]
	if h == nil {
		h = &chatHead{}
		u.paneHeads[id] = h
	}
	head := u.chatStrip(gtx, h, ws, paneChatOf(st, id), func() { u.flipPaneChat(id) })
	defer op.Offset(image.Pt(0, head)).Push(gtx.Ops).Pop()
	gtx.Constraints = gl.Exact(image.Pt(in.Dx(), max(0, in.Dy()-head)))
	u.chatBody(gtx, st, u.b.(Chatter), id, ws.Path, focused, func() { u.flipPaneChat(id) })
}
