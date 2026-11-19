package app

import (
	"image"

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

// paneChatKey reports whether chat_terminal is the window's on the shown
// tab: its focused pane is a pane chat.
func (u *ui) paneChatKey(st *model.State) bool {
	ws := findWorkspace(st, u.nav.workspace)
	return ws != nil && ws.Chat == "" && paneChatOf(st, u.nav.focused()) != nil && linkLevel(u.b) >= proto.PaneChatLevel
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
	gtx.Constraints = gl.Exact(rect.Size())
	frame := image.Rectangle{Max: rect.Size()}
	defer clip.Rect(frame).Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, u.th.Bg, clip.Rect(frame).Op())
	if !sole {
		paint.FillShape(gtx.Ops, paneBorder(u.th, focused), clip.Stroke{Path: clip.Rect(frame).Path(), Width: float32(gtx.Dp(1))}.Op())
	}
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
	gtx.Constraints = gl.Exact(image.Pt(rect.Dx(), max(0, rect.Dy()-head)))
	u.chatBody(gtx, st, u.b.(Chatter), id, ws.Path, focused, func() { u.flipPaneChat(id) })
}
