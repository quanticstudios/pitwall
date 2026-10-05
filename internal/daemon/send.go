package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	errSendBusy    = errors.New("the agent is working; wait for it first (pitwall wait <tab> --until done)")
)

// write is one input for a pane's writer. check, when set, runs on the
// writer right before the PTY write, holding the pane's gate and d.mu; an
// error drops the write. after, when set, runs once the write succeeded,
// still holding the gate, so no hook lands between the two. done, when set,
// gets the outcome once the write finished or was dropped.
type write struct {
	data  []byte
	check func() error
	after func()
	done  chan<- error
}

// paneLock is a mutex whose lock can give up when a context ends.
type paneLock chan struct{}

func (l paneLock) lock(ctx context.Context) error {
	if l.tryLock() {
		return nil // free: never lost to a context that ended meanwhile
	}
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l paneLock) tryLock() bool {
	select {
	case l <- struct{}{}:
		return true
	default:
		return false
	}
}

func (l paneLock) unlock() { <-l }

// gate is pane id's lock between a checked write and the updates that can
// mark the pane blocked (hooks, screen detection, OSC notifications). It
// is taken before d.mu, never under it.
// ponytail: a checked write the program does not read blocks that pane's
// hook events and notifications until it drains; detection skips the pane.
func (d *Daemon) gate(id string) paneLock {
	g, _ := d.gates.LoadOrStore(id, make(paneLock, 1))
	return g.(paneLock)
}

// sendLock is pane id's lock for a whole Send, paste to Enter, so two
// sends never merge into one prompt. It is not the gate: hooks go on during
// the pause.
func (d *Daemon) sendLock(id string) paneLock {
	l, _ := d.sends.LoadOrStore(id, make(paneLock, 1))
	return l.(paneLock)
}

// writeChecked writes w to p, after w.check when it has one.
func (d *Daemon) writeChecked(id string, p Pane, w write) error {
	if w.check == nil {
		p.Write(w.data) // a pane that cannot take input has exited, which watch reports
		return nil
	}
	g := d.gate(id)
	g.lock(context.Background())
	defer g.unlock()
	d.mu.Lock()
	err := w.check()
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if _, err := p.Write(w.data); err != nil {
		return err
	}
	if w.after != nil {
		w.after()
	}
	return nil
}

// writeNow queues data for pane id behind check and waits until the writer
// wrote it or dropped it, or ctx ends. A write still queued when ctx ends
// is dropped by its check.
func (d *Daemon) writeNow(ctx context.Context, id string, p Pane, data []byte, check func() error, after func()) error {
	done := make(chan error, 1)
	checked := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return check()
	}
	if err := d.queueInput(id, write{data: data, check: checked, after: after, done: done}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-p.Done():
		return fmt.Errorf("pane %s has exited", id)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// send is proto.Send. The writer checks again right before the paste and
// again before the Enter, so a prompt pitwall sees by then gets neither.
// ctx ends with the daemon or the client's connection.
func (d *Daemon) send(ctx context.Context, m proto.Send) error {
	l := d.sendLock(m.Pane)
	if err := l.lock(ctx); err != nil {
		return err
	}
	defer l.unlock()
	d.mu.Lock()
	p, err := d.sendable(m.Pane)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	check := func() error { _, err := d.sendable(m.Pane); return err }
	// why: the prompt is submitted with this write; a turn that ends after it is the new one.
	stamp := func() {
		d.mu.Lock()
		if i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == m.Pane }); i >= 0 {
			d.st.Panes[i].SentAt = time.Now()
			d.changed()
		}
		d.mu.Unlock()
	}
	var pasted func()
	if !m.Enter {
		pasted = stamp
	}
	if err := d.writeNow(ctx, m.Pane, p, input.Paste(m.Text, p.Modes()), check, pasted); err != nil || !m.Enter {
		return err
	}
	select {
	case <-time.After(sendPause):
	case <-ctx.Done():
		return ctx.Err()
	}
	return d.writeNow(ctx, m.Pane, p, []byte("\r"), check, stamp)
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
