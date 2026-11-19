package app

import (
	"image"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Chatter is optionally implemented by a Backend: the transcript of the
// chat the window shows, as last read from the daemon, false before the
// first read comes. Each call may read what came since.
type Chatter interface {
	Chat(id string) (session.Snapshot, bool)
}

// ChatResulter is optionally implemented by a Backend: the daemon's
// answers to the window's ChatSend requests and its refusals of any other
// Chat* request on chat id, each once.
type ChatResulter interface {
	ChatResults(id string) []chatin.Result
}

// ChatPoll is how often the window asks for a live chat's transcript
// while it shows it.
const ChatPoll = 150 * time.Millisecond

// layoutChat draws the chat tab ws: the chat's transcript with the
// composer under it, and sends the approvals answered on it and the
// composer's messages.
func (u *ui) layoutChat(gtx gl.Context, st *model.State, ws *model.Workspace) {
	ch, ok := u.b.(Chatter)
	if !ok {
		gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			return para(gtx, u.th, u.th.UIFont, u.th.Sp(theme.Body), u.th.Muted, "Chat: read it with pitwall chat read "+ws.Chat)
		})
		return
	}
	// The tab's frame sits pane_margin in, as a tab of one terminal's does.
	m := gtx.Dp(unit.Dp(u.cfg.PaneMargin))
	paint.FillShape(gtx.Ops, u.th.Surface, clip.Rect{Max: gtx.Constraints.Max}.Op())
	defer op.Offset(image.Pt(m, m)).Push(gtx.Ops).Pop()
	size := image.Pt(max(0, gtx.Constraints.Max.X-2*m), max(0, gtx.Constraints.Max.Y-2*m))
	in, cl := u.chatFrame(gtx, size, !u.keysHeld(st) && u.nav.peek == "", m == 0)
	defer cl.Pop()
	defer op.Offset(in.Min).Push(gtx.Ops).Pop()
	gtx.Constraints = gl.Exact(in.Size())
	head := u.layoutChatHead(gtx, st, ws)
	defer op.Offset(image.Pt(0, head)).Push(gtx.Ops).Pop()
	gtx.Constraints.Max.Y = max(0, gtx.Constraints.Max.Y-head)
	gtx.Constraints.Min.Y = min(gtx.Constraints.Min.Y, gtx.Constraints.Max.Y)
	u.chatBody(gtx, st, ch, ws.Chat, ws.Path, true)
}

// chatBody draws chat id's transcript with the composer under it, and
// sends the composer's messages and the requests answered on it. dir is
// where the transcript's paths are; focus is whether the composer may
// take the keys, as a pane chat in an unfocused split may not.
func (u *ui) chatBody(gtx gl.Context, st *model.State, ch Chatter, id, dir string, focus bool) {
	s, ok := ch.Chat(id)
	if ok {
		t := u.chatTranscript(st, id)
		t.Width, t.Copy, t.Dir = unit.Dp(u.cfg.Chat.Width), putClipboard, dir
		t.PRs, t.Open = st.PRs, func(target string) { u.openChatTarget(st, id, dir, target) }
		cmp := u.composer(st, id)
		if u.drawn == nil {
			u.drawn = map[string]bool{}
		}
		u.drawn[id] = true
		if f, ok := u.b.(ChatResulter); ok {
			for _, r := range f.ChatResults(id) {
				cmp.Result(r, gtx.Now)
			}
		}
		env := u.chatEnv(st, id, &s, t)
		env.Focus = env.Focus && focus && !t.Zoomed()
		gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			// Open in aide is the header's menu; the transcript's own copy
			// floated over the reply's top right.
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions { return t.Layout(gtx, u.th, &s, false) }),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return cmp.Layout(gtx, u.th, env) }),
		)
		for _, m := range cmp.Messages() {
			if _, ok := m.(proto.ChatSend); ok {
				t.ToEnd()
				gtx.Execute(op.InvalidateCmd{}) // the transcript laid out before this
			}
			u.send(m)
		}
		for _, a := range t.Answers() {
			u.send(proto.ChatApprove{Chat: id, Request: a.Request, Decision: a.Decision})
		}
		for _, a := range t.Inputs() {
			u.send(proto.ChatAnswer{Chat: id, Request: a.Request, Answers: a.Answers})
		}
	} else {
		gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			return para(gtx, u.th, u.th.UIFont, u.th.Sp(theme.Body), u.th.Muted, "Reading the chat…")
		})
	}
	if c := findChat(st, id); !ok || c != nil && chatLive(c.State) {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(ChatPoll)})
	}
}

// chatEnv is what chat id's composer needs this frame; t is the chat's
// transcript, which answers its pending request by the keys typed into
// an empty composer.
func (u *ui) chatEnv(st *model.State, id string, s *session.Snapshot, t *panel.Transcript) chatin.Env {
	width := unit.Dp(u.cfg.Chat.Width) // the transcript's column, so their edges line up
	if width == 0 {                    // full: the whole tab, less its sides
		width = 1 << 16
	}
	env := chatin.Env{Chat: id, Snap: s, Width: width,
		Focus:  !u.keysHeld(st) && u.nav.peek == "" && u.find.pane == "",
		Images: u.link().Level >= proto.ChatAttachLevel, ReadImage: readClipImage, Invalidate: u.invalidate}
	env.Key = t.Key
	return env
}

// composer is chat id's composer, kept in window state so its draft
// survives tab switches; composers of chats that closed go.
func (u *ui) composer(st *model.State, id string) *chatin.Composer {
	if u.composers == nil {
		u.composers = map[string]*chatin.Composer{}
	}
	for k := range u.composers {
		if k != id && findChat(st, k) == nil {
			delete(u.composers, k)
		}
	}
	c := u.composers[id]
	if c == nil {
		c = new(chatin.Composer)
		u.composers[id] = c
	}
	return c
}

// chatLive is whether a chat in state may still add to its transcript
// without a change that pushes a new State.
func chatLive(state string) bool {
	return state == session.StateConnecting || state == session.StateRunning || state == session.StateWaiting
}

// chatTranscript is chat id's transcript, kept while the chat is in st,
// so a tab switch keeps its folds, scroll, picks and answered lines.
func (u *ui) chatTranscript(st *model.State, id string) *panel.Transcript {
	if u.transcripts == nil {
		u.transcripts = map[string]*panel.Transcript{}
	}
	for c := range u.transcripts {
		if c != id && findChat(st, c) == nil {
			delete(u.transcripts, c)
		}
	}
	t := u.transcripts[id]
	if t == nil {
		t = &panel.Transcript{Open: openLink}
		u.transcripts[id] = t
	}
	return t
}

// findChat is chat id: a chat tab's, or a pane chat's (paneChatOf).
func findChat(st *model.State, id string) *model.Chat {
	for i := range st.Chats {
		if st.Chats[i].ID == id {
			return &st.Chats[i]
		}
	}
	return paneChatOf(st, id)
}
