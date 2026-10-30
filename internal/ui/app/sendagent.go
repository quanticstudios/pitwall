package app

import (
	"context"
	"image"
	"slices"
	"sync"
	"time"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
)

// fetchFailures and fetchReviews are what Send failures and Send review
// comments run gh through; tests replace them.
var (
	fetchFailures = forge.Failures
	fetchReviews  = forge.Reviews
)

// sendTimeout bounds one fetch: gh pr checks, then a log per failed job.
const sendTimeout = 3 * time.Minute

// agentSends are the fetches of send_failures and send_review: gh runs off
// the UI goroutine and each answer waits in done for a frame.
type agentSends struct {
	mu   sync.Mutex
	busy map[string]bool // tab id and act, while its fetch runs
	done []agentSend
	err  string // the last fetch's failure, in the notice box until closed
}

// agentSend is a fetch's answer: the message for tab ws's agent, "" when
// there is nothing to send, or the error gh gave. A Plan row's Request
// changes… comes as act request_changes with the plan's pane.
type agentSend struct {
	ws, act, text string
	pane          string // the agent's pane, "" for the tab's first
	err           error
}

// startSend fetches what act, send_failures or send_review, sends to tab
// ws's agent. A second ask while one runs does nothing.
func (u *ui) startSend(st *model.State, ws *model.Workspace, act string) {
	s := &u.sends
	key := ws.ID + " " + act
	s.mu.Lock()
	if s.busy[key] {
		s.mu.Unlock()
		return
	}
	if s.busy == nil {
		s.busy = map[string]bool{}
	}
	s.busy[key] = true
	s.mu.Unlock()
	invalidate := u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	dir, number := st.LivePath(*ws), st.PRs[ws.ID].Number
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		res := agentSend{ws: ws.ID, act: act}
		res.text, res.err = forge.SendPrompt(ctx, dir, number, act, fetchFailures, fetchReviews)
		s.mu.Lock()
		delete(s.busy, key)
		s.done = append(s.done, res)
		s.mu.Unlock()
		invalidate()
	}()
}

// takeSends queues each finished fetch's message for its tab's agent, as
// the review view queues comments, unless one already waits for that
// agent, and says how it went: a notice at the bottom of the pane area gtx
// fills, or the error in the notice box.
func (u *ui) takeSends(gtx gl.Context, st *model.State) {
	s := &u.sends
	s.mu.Lock()
	done := s.done
	s.done = nil
	s.mu.Unlock()
	for _, res := range done {
		what := "Send failures to agent"
		switch res.act {
		case "send_review":
			what = "Send review comments to agent"
		case "request_changes":
			what = "Request changes"
		}
		p := agentPane(st, findWorkspace(st, res.ws))
		if res.pane != "" {
			p = findPane(st, res.pane)
		}
		area := image.Rectangle{Max: gtx.Constraints.Max}
		switch {
		case res.err != nil:
			u.sendFailed(what + ": " + res.err.Error())
		case res.text == "" && res.act == "send_review":
			u.showNotice(gtx, "No unresolved review comments", area)
		case res.text == "":
			u.showNotice(gtx, "No failed checks", area)
		case p == nil:
			u.sendFailed(what + ": the tab has no agent to send to")
		case slices.ContainsFunc(u.review.queue, func(d delivery) bool { return d.pane == p.ID && d.send }):
			// why: queued sends would add up to one paste of any size.
			u.showNotice(gtx, "A message already waits for "+agentName(p)+"; this one was dropped", area)
		default:
			u.review.enqueue(p.ID, res.text, nil).send = true
			msg := "Queued until " + agentName(p) + " waits for input"
			if res.act == "request_changes" {
				msg = "Queued: answer No in " + agentName(p) + "'s plan dialog to send it"
			}
			if agentReady(st, p.ID) {
				msg = "Sent to " + agentName(p)
			}
			u.showNotice(gtx, msg, area)
			u.deliver(st, gtx.Now)
		}
	}
}

// sendFailed puts msg in the notice box, again if it was closed before.
func (u *ui) sendFailed(msg string) {
	u.sends.err = msg
	if u.dismissed == msg {
		u.dismissed = ""
	}
}
