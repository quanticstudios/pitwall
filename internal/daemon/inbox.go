package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// settleTick is how often the daemon looks for tabs idle past [sidebar]
// settle_after_days. Days are the unit, so minutes late is on time.
const settleTick = 10 * time.Minute

// setTabInbox settles, unsettles, snoozes or wakes a tab. Unsettling
// counts as activity, so settle_after_days starts over.
func (d *Daemon) setTabInbox(m proto.SetTabInbox) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(m.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	now := time.Now()
	switch {
	case m.Settled && w.Settled.IsZero():
		w.Settled = now
	case !m.Settled && !w.Settled.IsZero():
		w.Settled, w.UpdatedAt = time.Time{}, now
	}
	if !m.SnoozedUntil.Equal(w.SnoozedUntil) {
		w.SnoozedUntil, w.SnoozedAt = m.SnoozedUntil, now
		if m.SnoozedUntil.IsZero() {
			w.SnoozedAt = time.Time{}
		}
	}
	d.changed()
	return nil
}

// asks reports a state that wakes a snoozed tab: the agent wants an
// answer or failed.
func asks(s model.AgentState) bool {
	switch s {
	case model.StatePendingApproval, model.StateAwaitingInput, model.StatePlanReady, model.StateError:
		return true
	}
	return false
}

// wakeTab clears w's snooze when one of its activities, acts, asks for
// the user after w was snoozed, and its settle when one asks or starts a
// turn after w settled. It reports whether it changed w.
func wakeTab(w *model.Workspace, acts []model.Activity) bool {
	changed := false
	for _, a := range acts {
		if a.WorkspaceID != w.ID {
			continue
		}
		if !w.SnoozedUntil.IsZero() && asks(a.State) && a.UpdatedAt.After(w.SnoozedAt) {
			w.SnoozedUntil, w.SnoozedAt, changed = time.Time{}, time.Time{}, true
		}
		if !w.Settled.IsZero() && (asks(a.State) || model.Pulses(&a)) && a.UpdatedAt.After(w.Settled) {
			w.Settled, changed = time.Time{}, true
		}
	}
	return changed
}

// wakeTabs runs wakeTab on every tab. Callers hold d.mu.
func (d *Daemon) wakeTabs() {
	for i := range d.st.Workspaces {
		if w := &d.st.Workspaces[i]; !w.SnoozedUntil.IsZero() || !w.Settled.IsZero() {
			wakeTab(w, d.st.Activities)
		}
	}
}

// idleSettles reports whether w, whose agents' activities are acts, has
// gone days without agent activity and should settle. A pinned, settled
// or snoozed tab never does, nor one whose agent works or waits for an
// answer. The end of a snooze counts as activity.
func idleSettles(w model.Workspace, acts []model.Activity, now time.Time, days int) bool {
	if days <= 0 || w.Pinned || w.Detached || !w.Settled.IsZero() || w.SnoozedUntil.After(now) {
		return false
	}
	agent := false
	for _, a := range acts {
		if a.WorkspaceID != w.ID || a.Provider == model.ProviderTerminal {
			continue
		}
		agent = true
		if model.Pulses(&a) || asks(a.State) && a.State != model.StateError {
			return false
		}
	}
	last := w.UpdatedAt
	if w.SnoozedUntil.After(last) {
		last = w.SnoozedUntil
	}
	return agent && now.Sub(last) >= time.Duration(days)*24*time.Hour
}

// settleIdle settles the tabs idleSettles picks, with [sidebar]
// settle_after_days from o.Settle.
func (d *Daemon) settleIdle(now time.Time) {
	if d.o.Settle == nil {
		return
	}
	days := d.o.Settle().SettleAfterDays
	d.mu.Lock()
	defer d.mu.Unlock()
	changed := false
	for i := range d.st.Workspaces {
		if w := &d.st.Workspaces[i]; idleSettles(*w, d.st.Activities, now, days) {
			w.Settled, changed = now, true
		}
	}
	if changed {
		d.changed()
	}
}

// settleLoop runs settleIdle now and every settleTick.
func (d *Daemon) settleLoop(ctx context.Context) {
	t := time.NewTicker(settleTick)
	defer t.Stop()
	for {
		d.settleIdle(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
