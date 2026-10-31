package app

import (
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Chatter is optionally implemented by a Backend: the transcript of the
// chat the window shows, as last read from the daemon, false before the
// first read comes. Each call may read what came since.
type Chatter interface {
	Chat(id string) (session.Snapshot, bool)
}

// ChatPoll is how often the window asks for a live chat's transcript
// while it shows it.
const ChatPoll = 150 * time.Millisecond

// layoutChat draws the chat tab ws: the chat's transcript, read-only.
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
		u.transcript.Layout(gtx, u.th, &s)
	} else {
		gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			return para(gtx, u.th, u.th.UIFont, u.th.Sp(theme.Body), u.th.Muted, "Reading the chat…")
		})
	}
	if c := findChat(st, ws.Chat); !ok || c != nil && chatLive(c.State) {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(ChatPoll)})
	}
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
