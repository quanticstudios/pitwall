package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/skillshare"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

var (
	_ app.Skillsharer = (*backend)(nil)
	_ app.Skillsharer = (*multi)(nil)
)

// ssWait is a Skillshare waiting for its reply.
type ssWait struct {
	step func(string)
	done chan skillshare.Reply
}

// errLost is Skillshare's error when the connection ends before the reply.
var errLost = errors.New("lost the connection to the daemon")

// Skillshare implements app.Skillsharer: the daemon does m's call, and
// its steps go to step as they come.
func (b *backend) Skillshare(ctx context.Context, _ string, m proto.Skillshare, step func(string)) (skillshare.Reply, error) {
	w := &ssWait{step: step, done: make(chan skillshare.Reply, 1)}
	b.mu.Lock()
	b.ssID++
	m.ID = b.ssID
	if b.ssWaits == nil {
		b.ssWaits = map[uint64]*ssWait{}
	}
	b.ssWaits[m.ID] = w
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.ssWaits, m.ID)
		b.mu.Unlock()
	}()
	b.outMu.Lock()
	gone := b.done
	b.outMu.Unlock()
	if err := b.Send(m); err != nil {
		return skillshare.Reply{}, err
	}
	select {
	case r := <-w.done:
		return r, nil
	case <-gone:
		return skillshare.Reply{}, errLost
	case <-ctx.Done():
		return skillshare.Reply{}, ctx.Err()
	}
}

// takeSkillshare hands a SkillshareResult to its waiter, and returns the
// step to report after b.mu is released. Called with b.mu held.
func (b *backend) takeSkillshare(m proto.SkillshareResult) func() {
	w := b.ssWaits[m.ID]
	switch {
	case w == nil:
	case m.Done:
		select {
		case w.done <- m.Reply:
		default:
		}
	case w.step != nil:
		return func() { w.step(m.Step) }
	}
	return nil
}

// SkillshareOn implements app.Skillsharer from the daemon's state.
func (b *backend) SkillshareOn(string) (installed, old bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.link.State != app.LinkUp || b.link.Epoch == 0 {
		return false, false
	}
	if b.link.Level < proto.SkillshareLevel {
		return false, true
	}
	return b.state.Skillshare, false
}

// Skillshare implements app.Skillsharer: a call for a pane goes to the
// pane's host, as route sends every message that names one, and one for
// a machine picked by name to that host.
func (m *multi) Skillshare(ctx context.Context, host string, msg proto.Skillshare, step func(string)) (skillshare.Reply, error) {
	to := m.byName(host)
	if msg.Pane != "" {
		to = m.route(msg)
	}
	if to == nil {
		return skillshare.Reply{}, fmt.Errorf("%s is not one of this window's machines", host)
	}
	return to.b.Skillshare(ctx, host, msg, step)
}

// SkillshareOn implements app.Skillsharer for host's daemon.
func (m *multi) SkillshareOn(host string) (installed, old bool) {
	if h := m.byName(host); h != nil {
		return h.b.SkillshareOn(host)
	}
	return false, false
}
