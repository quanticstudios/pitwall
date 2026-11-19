package app

import (
	"image"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
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
	head := u.layoutChatHead(gtx, st, ws)
	defer op.Offset(image.Pt(0, head)).Push(gtx.Ops).Pop()
	gtx.Constraints.Max.Y = max(0, gtx.Constraints.Max.Y-head)
	gtx.Constraints.Min.Y = min(gtx.Constraints.Min.Y, gtx.Constraints.Max.Y)
	s, ok := ch.Chat(ws.Chat)
	if ok {
		aide := curHost() == "" && findAide() != nil
		t := u.chatTranscript(st, ws.Chat)
		t.Width, t.Copy, t.Dir = unit.Dp(u.cfg.Chat.Width), putClipboard, ws.Path
		cmp := u.composer(st, ws.Chat)
		if f, ok := u.b.(ChatResulter); ok {
			for _, r := range f.ChatResults(ws.Chat) {
				cmp.Result(r, gtx.Now)
			}
		}
		env := u.chatEnv(st, ws.Chat, &s, t)
		gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions { return t.Layout(gtx, u.th, &s, aide) }),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return cmp.Layout(gtx, u.th, env) }),
		)
		for _, m := range cmp.Messages() {
			u.send(m)
		}
		for _, a := range t.Answers() {
			u.send(proto.ChatApprove{Chat: ws.Chat, Request: a.Request, Decision: a.Decision})
		}
		for _, a := range t.Inputs() {
			u.send(proto.ChatAnswer{Chat: ws.Chat, Request: a.Request, Answers: a.Answers})
		}
		if t.OpenAide() {
			openAide(findAide())
		}
	} else {
		gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			return para(gtx, u.th, u.th.UIFont, u.th.Sp(theme.Body), u.th.Muted, "Reading the chat…")
		})
	}
	if c := findChat(st, ws.Chat); !ok || c != nil && chatLive(c.State) {
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
		t = new(panel.Transcript)
		u.transcripts[id] = t
	}
	return t
}

func findChat(st *model.State, id string) *model.Chat {
	for i := range st.Chats {
		if st.Chats[i].ID == id {
			return &st.Chats[i]
		}
	}
	return nil
}
