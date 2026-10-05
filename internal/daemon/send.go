package daemon

import (
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

// sendPause is how long Send waits after the paste is written before it
// presses Enter, so Claude Code and Codex see the Enter as a key and not
// part of the paste; tests shorten it.
var sendPause = 300 * time.Millisecond

var (
	errSendBlocked = errors.New("the agent is waiting on a permission prompt or a question; answer it in the tab (pitwall send never answers one it can see)")
	errSendBusy    = errors.New("the agent is working; wait for it (pitwall wait <tab> --until done) or send -f")
)

// write is one input for a pane's writer. check, when set, runs on the
// writer right before the PTY write, holding the pane's gate and d.mu; an
// error drops the write. done, when set, gets the outcome once the write
// finished or was dropped.
type write struct {
	data  []byte
	check func() error
	done  chan<- error
}

// gate is pane id's lock between a checked write and the updates that can
// mark the pane blocked (hooks, screen detection, OSC notifications). It
// is taken before d.mu, never under it.
// ponytail: a checked write the program does not read blocks that pane's
// hook events and notifications until it drains; detection skips the pane.
func (d *Daemon) gate(id string) *sync.Mutex {
	g, _ := d.gates.LoadOrStore(id, new(sync.Mutex))
	return g.(*sync.Mutex)
}

// writeChecked writes w to p, after w.check when it has one.
func (d *Daemon) writeChecked(id string, p Pane, w write) error {
	if w.check == nil {
		p.Write(w.data) // a pane that cannot take input has exited, which watch reports
		return nil
	}
	g := d.gate(id)
	g.Lock()
	defer g.Unlock()
	d.mu.Lock()
	err := w.check()
	d.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = p.Write(w.data)
	return err
}

// writeNow queues data for pane id behind check and waits until the writer
// wrote it or dropped it.
func (d *Daemon) writeNow(id string, p Pane, data []byte, check func() error) error {
	done := make(chan error, 1)
	if err := d.queueInput(id, write{data: data, check: check, done: done}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-p.Done():
		return fmt.Errorf("pane %s has exited", id)
	}
}

// sendLock is pane id's lock for a whole Send, paste to Enter, so two
// sends never merge into one prompt. It is not the gate: hooks go on during
// the pause.
func (d *Daemon) sendLock(id string) *sync.Mutex {
	l, _ := d.sends.LoadOrStore(id, new(sync.Mutex))
	return l.(*sync.Mutex)
}

// send is proto.Send. The writer checks again right before the paste and
// again before the Enter, so a prompt pitwall sees by then gets neither.
func (d *Daemon) send(m proto.Send) error {
	l := d.sendLock(m.Pane)
	l.Lock()
	defer l.Unlock()
	d.mu.Lock()
	p, err := d.sendable(m.Pane, m.Force)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	check := func(force bool) func() error {
		return func() error { _, err := d.sendable(m.Pane, force); return err }
	}
	if err := d.writeNow(m.Pane, p, input.Paste(m.Text, p.Modes()), check(m.Force)); err != nil {
		return err
	}
	if m.Enter {
		time.Sleep(sendPause)
		if err := d.writeNow(m.Pane, p, []byte("\r"), check(true)); err != nil {
			return err
		}
	}
	// why: the prompt is submitted now; a turn that ends before this is an older one.
	d.mu.Lock()
	if i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == m.Pane }); i >= 0 {
		d.st.Panes[i].SentAt = time.Now()
		d.changed()
	}
	d.mu.Unlock()
	return nil
}

// sendable returns pane id when Send may type into it: it runs, it waits
// on no answer pitwall can see, by hook state or on screen, and, unless
// force, its agent is not working. The screen counts for every pane, as an
// agent draws a prompt before its hook arrives, or before pitwall knows it
// is an agent. Callers hold d.mu.
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
	if agent.ReadScreen(p.Snapshot()) == agent.ScreenForm {
		return nil, errSendBlocked
	}
	return p, nil
}
