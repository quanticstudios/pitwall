package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
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
	errSendBlocked = errors.New("the agent is waiting on a permission prompt or a question; answer it in the tab")
	errSendBusy    = errors.New("the agent is working; wait for it first (pitwall wait <tab> --until done)")
)

// sendLock is pane id's lock for a whole Send (paste, pause, Enter), so two
// sends never merge into one prompt. Nothing under it blocks for long.
func (d *Daemon) sendLock(id string) *sync.Mutex {
	l, _ := d.sends.LoadOrStore(id, new(sync.Mutex))
	return l.(*sync.Mutex)
}

// send is proto.Send. It checks the pane right before queueing the paste
// and again before the Enter. Once the Enter is queued the pane's agent
// shows working ("prompt sent"): send runs only while no turn does, so the
// next done the agent reports ends the turn the send started.
func (d *Daemon) send(m proto.Send) error {
	l := d.sendLock(m.Pane)
	l.Lock()
	defer l.Unlock()
	paste := func(p Pane) []byte { return input.Paste(m.Text, p.Modes()) }
	if err := d.sendStep(m.Pane, paste, false); err != nil || !m.Enter {
		return err
	}
	time.Sleep(sendPause)
	return d.sendStep(m.Pane, func(Pane) []byte { return []byte("\r") }, true)
}

// sendStep checks pane id and queues data(pane) on its input channel,
// which drops what the program does not read instead of blocking. With
// submit, the pane's agent then shows working.
func (d *Daemon) sendStep(id string, data func(Pane) []byte, submit bool) error {
	d.mu.Lock()
	p, err := d.sendable(id)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	select {
	case d.inputs[id] <- data(p):
	default:
		d.mu.Unlock()
		return fmt.Errorf("pane %s is not reading its input", id)
	}
	if i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == id }); submit && i >= 0 {
		if prov := d.st.Panes[i].Provider; prov != "" && prov != model.ProviderTerminal {
			d.setActivity(context.Background(), id, prov, model.StateWorking, "prompt sent")
		}
	}
	d.mu.Unlock()
	d.unscroll(id, p)
	return nil
}

// sendable returns pane id when Send may type into it: it runs, it waits
// on no answer pitwall can see, by hook state, OSC notification or on
// screen, and its agent is not working. The screen counts for every pane,
// as an agent draws a prompt before its hook arrives, or before pitwall
// knows it is an agent. Callers hold d.mu.
func (d *Daemon) sendable(id string) (Pane, error) {
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
	if a := d.attn[id]; a != nil && a.notice != nil {
		state = a.notice.State // what attended shows in its place
	}
	switch state {
	case model.StatePendingApproval, model.StateAwaitingInput, model.StatePlanReady:
		return nil, errSendBlocked
	case model.StateWorking, model.StateConnecting:
		return nil, errSendBusy
	}
	if agent.ReadScreen(p.Snapshot()) == agent.ScreenForm {
		return nil, errSendBlocked
	}
	return p, nil
}
