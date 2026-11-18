package app

import (
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Chatter is optionally implemented by a Backend: the transcript of the
// chat the window shows, as last read from the daemon, false before the
// first read comes. Each call may read what came since.
type Chatter interface {
	Chat(id string) (session.Snapshot, bool)
}

// ChatFailer is optionally implemented by a Backend: the message of the
// last Chat* request the daemon refused for chat id, once.
type ChatFailer interface {
	ChatFailed(id string) (string, bool)
}

// ChatPoll is how often the window asks for a live chat's transcript
// while it shows it.
const ChatPoll = 150 * time.Millisecond

// composerWidth is the composer's column.
// shortcut: the standard [chat] width; read the setting once it lands.
const composerWidth unit.Dp = 960

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
	s, ok := ch.Chat(ws.Chat)
	if ok {
		aide := curHost() == "" && findAide() != nil
		cmp := u.composer(st, ws.Chat)
		if f, ok := u.b.(ChatFailer); ok {
			if msg, ok := f.ChatFailed(ws.Chat); ok {
				cmp.Failed(msg, gtx.Now)
			}
		}
		env := u.chatEnv(st, ws.Chat, &s)
		gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions { return u.transcript.Layout(gtx, u.th, &s, aide) }),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return cmp.Layout(gtx, u.th, env) }),
		)
		for _, m := range cmp.Messages() {
			u.send(m)
		}
		for _, a := range u.transcript.Answers() {
			u.send(proto.ChatApprove{Chat: ws.Chat, Request: a.Request, Decision: a.Decision})
		}
		if u.transcript.OpenAide() {
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

// chatEnv is what chat id's composer needs this frame.
func (u *ui) chatEnv(st *model.State, id string, s *session.Snapshot) chatin.Env {
	return chatin.Env{Chat: id, Snap: s, Width: composerWidth,
		Focus:  !u.keysHeld(st) && u.nav.peek == "" && u.find.pane == "",
		Images: u.link().Level >= proto.ChatAttachLevel, ReadImage: readClipImage, Invalidate: u.invalidate}
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

func findChat(st *model.State, id string) *model.Chat {
	for i := range st.Chats {
		if st.Chats[i].ID == id {
			return &st.Chats[i]
		}
	}
	return nil
}
