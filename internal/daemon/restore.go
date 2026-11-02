package daemon

import (
	"log"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/store"
)

// startupRestores holds agent launches waiting for their gap. Guarded by d.mu.
type startupRestores struct {
	pending []string
	saved   []model.Activity
	delay   time.Duration
	timer   *time.Timer
}

// orderRestores puts the most recently shown session first and keeps pane
// order within each session. Callers hold d.mu.
func (d *Daemon) orderRestores() {
	used := map[string]time.Time{}
	for _, p := range d.st.Panes {
		if s := d.st.Session(d.st.SessionOf(p.WorkspaceID)); s != nil {
			used[p.ID] = s.UsedAt
		}
	}
	slices.SortStableFunc(d.restoring.pending, func(a, b string) int { return used[b].Compare(used[a]) })
}

// restorePane keeps command selection and restart nudges the same for
// immediate and queued launches. Callers hold d.mu.
func (d *Daemon) restorePane(p model.Pane, saved []model.Activity) error {
	if err := d.start(p.ID, d.o.RestoreCmd(p), p.Cwd, d.portsOf(p.WorkspaceID), p.Env); err != nil {
		return err
	}
	if store.Resumes(p) {
		d.resumed[p.ID] = time.Now()
		log.Printf("pane %s: resuming %q, mode %q", p.ID, p.Provider, p.AgentMode)
		d.restartNudge(p.ID, saved)
	}
	return nil
}

// restoreNext launches at most one queued agent and waits a full gap
// after each attempt, including failures. Callers hold d.mu.
func (d *Daemon) restoreNext() {
	if d.closing {
		d.stopRestores()
		return
	}
	for len(d.restoring.pending) > 0 {
		id := d.restoring.pending[0]
		d.restoring.pending = d.restoring.pending[1:]
		i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
		if i < 0 {
			continue
		}
		if err := d.restorePane(d.st.Panes[i], d.restoring.saved); err != nil {
			log.Printf("restore pane %s: %q", id, err)
			closeAll(d.removePane(id))
		}
		d.changed()
		break
	}
	if len(d.restoring.pending) == 0 {
		d.stopRestores()
		return
	}
	d.restoring.timer = time.AfterFunc(d.restoring.delay, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.restoreNext()
	})
}

// stopRestores cancels queued launches. A callback already waiting on d.mu
// sees an empty queue. Callers hold d.mu.
func (d *Daemon) stopRestores() {
	if d.restoring.timer != nil {
		d.restoring.timer.Stop()
	}
	d.restoring.timer = nil
	d.st.Activities = append(d.st.Activities, d.restoreActivities()...)
	d.restoring.pending, d.restoring.saved = nil, nil
}

// restoreActivities keeps queued agents' turns for another restart.
// Callers hold d.mu.
func (d *Daemon) restoreActivities() []model.Activity {
	var saved []model.Activity
	for _, a := range d.restoring.saved {
		if slices.Contains(d.restoring.pending, a.PaneID) && d.activityIndex(a.PaneID) < 0 {
			saved = append(saved, a)
		}
	}
	return saved
}
