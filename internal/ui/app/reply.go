package app

import (
	"context"
	"errors"
	"image"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// The reply sheet's sends and images. A reply goes into the review
// view's delivery queue, as Request changes does: pasted once the agent
// waits for input, then Enter. An image is read off this machine's
// clipboard and sent to the daemon, as a pane's image paste is; the
// daemon saves it and answers with its path, which the reply pastes
// before its text.

// replyImage is the reply sheet's one image on its way: the clipboard
// read, then the daemon's answer.
type replyImage struct {
	ws, pane string
	read     chan pasteRead // the clipboard read's answer, nil once taken
	wait     bool           // a PasteImage is out
}

// reply queues e, a reply from the sidebar, for its tab's agent.
func (u *ui) reply(st *model.State, e sidebar.Reply) {
	pane := e.PaneID
	if pane == "" {
		if p := agentPane(st, findWorkspace(st, e.WorkspaceID)); p != nil {
			pane = p.ID
		}
	}
	u.sends.mu.Lock()
	u.sends.done = append(u.sends.done, agentSend{ws: e.WorkspaceID, pane: pane, act: "reply", text: e.Text, images: e.Images})
	u.sends.mu.Unlock()
	if u.invalidate != nil {
		u.invalidate()
	}
}

// queueReply queues a reply for p, the agent's pane, and says how it went
// at the bottom of area.
func (u *ui) queueReply(gtx gl.Context, st *model.State, p *model.Pane, res agentSend, area image.Rectangle) {
	if p == nil || p.Exited {
		u.sendFailed("Reply: the tab has no agent to send to")
		return
	}
	d := u.review.enqueue(p.ID, res.text, nil)
	d.images = append(d.images, res.images...)
	msg := "Queued until " + agentName(p) + " waits for input"
	if agentReady(st, p.ID) {
		msg = "Sent to " + agentName(p)
	}
	u.showNotice(gtx, msg, area)
	u.deliver(st, gtx.Now)
}

// attachImage starts reading the clipboard's image for the reply sheet on
// tab ws, for its agent in pane.
func (u *ui) attachImage(st *model.State, e sidebar.AttachImage) {
	fail := func(msg string) { u.sidebar.ReplyImage(e.WorkspaceID, "", errors.New(msg)) }
	pane := e.PaneID
	if pane == "" {
		if p := agentPane(st, findWorkspace(st, e.WorkspaceID)); p != nil {
			pane = p.ID
		}
	}
	switch {
	case pane == "":
		fail("the tab has no agent")
		return
	case u.link().Level < proto.Since(proto.PasteImage{}):
		fail("pitwall's background service is too old; restart pitwall")
		return
	case u.replyImg != nil:
		return // one at a time
	}
	if _, ok := u.b.(Paster); !ok {
		fail("this window cannot paste images")
		return
	}
	ri := &replyImage{ws: e.WorkspaceID, pane: pane, read: make(chan pasteRead, 1)}
	u.replyImg = ri
	invalidate := u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pasteTimeout)
		defer cancel()
		b, err := readClipImage(ctx)
		ri.read <- pasteRead{b, err}
		invalidate()
	}()
}

// tickReplyImage moves the reply's image along each frame: the read sent
// to the daemon, the daemon's answer handed to the sidebar.
// ponytail: a pane's own image paste to the same pane at the same moment
// can take this answer; a request id on PasteImage would keep them apart.
func (u *ui) tickReplyImage() {
	ri := u.replyImg
	if ri == nil {
		return
	}
	done := func(path string, err error) {
		u.replyImg = nil
		u.sidebar.ReplyImage(ri.ws, path, err)
	}
	if ri.read != nil {
		select {
		case rd := <-ri.read:
			ri.read = nil
			switch {
			case rd.err != nil:
				done("", rd.err)
			case !u.send(proto.PasteImage{Pane: ri.pane, Data: rd.data}):
				done("", errors.New("the connection to the background service is down"))
			default:
				ri.wait = true
			}
		default:
		}
		return
	}
	if pr, ok := u.b.(Paster); ok && ri.wait {
		if res, ok := pr.TakePasted(ri.pane); ok {
			var err error
			if res.Err != "" {
				err = errors.New(res.Err)
			}
			done(res.Path, err)
		}
	}
}
