package daemon

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// sendPause is how long Send waits between the paste and Enter, so Claude
// Code and Codex see the Enter as a key and not part of the paste; tests
// shorten it.
var sendPause = 300 * time.Millisecond

var (
	errSendBlocked = errors.New("the agent is waiting on a permission prompt or a question; answer it in the tab (pitwall never answers for you)")
	errSendBusy    = errors.New("the agent is working; wait for it (pitwall wait <tab> --until done) or send -f")
)

// send is proto.Send. The checks run again before Enter: a prompt that
// appeared during the pause must never get that Enter as its answer.
func (d *Daemon) send(m proto.Send) error {
	d.mu.Lock()
	p, err := d.sendable(m.Pane, m.Force)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if err := d.input(m.Pane, input.Paste(m.Text, p.Modes())); err != nil || !m.Enter {
		return err
	}
	time.Sleep(sendPause)
	d.mu.Lock()
	_, err = d.sendable(m.Pane, true)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	return d.input(m.Pane, []byte("\r"))
}

// sendable returns pane id when Send may type into it: it runs, its agent
// waits on no answer, and, unless force, it is not working. An agent's
// screen counts too, as a prompt shows before its hook arrives. Callers
// hold d.mu.
func (d *Daemon) sendable(id string, force bool) (Pane, error) {
	p := d.panes[id]
	if p == nil {
		return nil, fmt.Errorf("no pane %s", id)
	}
	if d.inputs[id] == nil {
		return nil, fmt.Errorf("pane %s has exited", id)
	}
	var state model.AgentState
	if i := d.activityIndex(id); i >= 0 {
		state = d.st.Activities[i].State
	}
	switch state {
	case model.StatePendingApproval, model.StateAwaitingInput, model.StatePlanReady:
		return nil, errSendBlocked
	case model.StateWorking, model.StateConnecting:
		if !force {
			return nil, errSendBusy
		}
	}
	i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == id })
	if i >= 0 && d.st.Panes[i].Provider != "" && agent.ReadScreen(p.Snapshot()) == agent.ScreenForm {
		return nil, errSendBlocked
	}
	return p, nil
}
