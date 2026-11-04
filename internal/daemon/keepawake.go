package daemon

import (
	"context"
	"io"
	"log"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

type awakeState struct {
	working   bool
	idleSince time.Time
}

// next decides from daemon-local activity; a handoff starts with a fresh state.
func (s awakeState) next(on, working bool, now time.Time, grace time.Duration) (awakeState, bool) {
	if !on {
		return awakeState{}, false
	}
	if working {
		return awakeState{working: true}, true
	}
	if s.working {
		s = awakeState{idleSince: now}
	}
	return s, !s.idleSince.IsZero() && now.Sub(s.idleSince) < grace
}

type awakeLock struct {
	state  awakeState
	lock   io.Closer
	failed bool
}

func (a *awakeLock) update(ctx context.Context, on, working bool, now time.Time, grace time.Duration, inhibit func(context.Context) (io.Closer, error)) error {
	if l, ok := a.lock.(interface{ Err() error }); ok {
		if err := l.Err(); err != nil {
			a.close()
			a.failed = true
			return err
		}
	}
	var want bool
	a.state, want = a.state.next(on, working, now, grace)
	if !want {
		a.close()
	} else if a.lock == nil && !a.failed {
		var err error
		a.lock, err = inhibit(ctx)
		if err != nil {
			a.failed = true
			return err
		}
	}
	return nil
}

func (a *awakeLock) close() {
	if a.lock != nil {
		a.lock.Close()
		a.lock = nil
	}
}

func (d *Daemon) agentsWorking() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.st.Activities {
		if a.State == model.StateWorking && (model.IsAgent(a.Provider) || d.chats[a.PaneID] != nil) {
			return true
		}
	}
	return false
}

func (d *Daemon) keepAwakeLoop(ctx context.Context) {
	if d.o.KeepAwake == nil || d.o.InhibitIdle == nil {
		return
	}
	var a awakeLock
	defer a.close()
	t := time.NewTicker(time.Second) // config changes apply even without agent events
	defer t.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		on, grace := d.o.KeepAwake()
		if err := a.update(ctx, on, d.agentsWorking(), time.Now(), grace, d.o.InhibitIdle); err != nil && ctx.Err() == nil {
			log.Printf("keep awake: %q; disabled until the daemon restarts", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-d.awakeWake:
		case <-t.C:
		}
	}
}
