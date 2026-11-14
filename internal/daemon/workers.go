package daemon

import (
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
)

// workerClock is transient: a restart starts a fresh delay. Guarded by d.mu.
type workerClock struct {
	since time.Time
	turn  time.Time
}

// touchWorker restarts a tab's clock on focus or input. Callers hold d.mu.
func (d *Daemon) touchWorker(pane string) {
	for _, p := range d.st.Panes {
		if p.ID == pane {
			delete(d.doneWorkers, p.WorkspaceID)
			return
		}
	}
}

// finishedWorker also checks pending input and background work, which can
// outlive a Stop hook. Callers hold d.mu.
func (d *Daemon) finishedWorker(st model.State, w model.Workspace) bool {
	if !st.FinishedWorker(w) {
		return false
	}
	for _, p := range st.Panes {
		if p.WorkspaceID != w.ID {
			continue
		}
		if d.live.background[p.ID] || len(d.mail.queue[p.ID]) > 0 || len(d.inputs[p.ID]) > 0 {
			return false
		}
		if i := d.activityIndex(p.ID); i >= 0 && d.mail.typed[p.ID].After(d.st.Activities[i].UpdatedAt) {
			return false
		}
	}
	return true
}

// checkDoneWorkers runs on the liveness tick. A focused tab waits until
// focus leaves before its uninterrupted delay can start.
func (d *Daemon) checkDoneWorkers(now time.Time) {
	d.mu.Lock()
	after := config.DefaultCloseDoneWorkers
	if d.o.CloseDoneWorkers != nil {
		after = d.o.CloseDoneWorkers()
	}
	if after <= 0 || d.closing || d.frozen {
		d.doneWorkers = nil
		d.mu.Unlock()
		return
	}
	st := d.st
	st.Panes = slices.Clone(st.Panes)
	st.Activities = d.attended()
	next := map[string]workerClock{}
	var closing []Pane
	for _, w := range slices.Clone(st.Workspaces) {
		if !d.finishedWorker(st, w) {
			continue
		}
		focused := false
		var turn time.Time
		for _, p := range st.Panes {
			if p.WorkspaceID == w.ID {
				focused = focused || d.shownFocused(p.ID)
			}
		}
		for _, a := range st.Activities {
			if a.WorkspaceID == w.ID && a.UpdatedAt.After(turn) {
				turn = a.UpdatedAt
			}
		}
		if focused {
			continue
		}
		clock, ok := d.doneWorkers[w.ID]
		if !ok || !clock.turn.Equal(turn) {
			clock = workerClock{since: now, turn: turn}
		}
		if now.Sub(clock.since) < after {
			next[w.ID] = clock
			continue
		}
		closing = append(closing, d.removeWorkspace(w.ID)...)
	}
	d.doneWorkers = next
	if len(closing) > 0 {
		d.changed()
	}
	d.mu.Unlock()
	closeAll(closing)
}

// closeDoneWorkers rechecks the parent's children under the lock, so a new
// turn or PR since the menu opened keeps its tab.
func (d *Daemon) closeDoneWorkers(parent string) error {
	d.mu.Lock()
	st := d.st
	st.Panes = slices.Clone(st.Panes)
	st.Activities = d.attended()
	var closing []Pane
	for _, w := range slices.Clone(st.Workspaces) {
		if w.Parent == parent && d.finishedWorker(st, w) {
			closing = append(closing, d.removeWorkspace(w.ID)...)
		}
	}
	if len(closing) > 0 {
		d.changed()
	}
	d.mu.Unlock()
	closeAll(closing)
	return nil
}

// keepWorkerResult preserves replies alongside the existing bounded closed
// tab history. Only metadata goes to clients; replies stay in the daemon.
// Callers hold d.mu, before the panes are dropped.
func (d *Daemon) keepWorkerResult(c *model.ClosedTab, panes []model.Pane) {
	if !d.finishedWorker(d.st, c.Tab) {
		return
	}
	c.FinishedWorker = true
	if !model.IsAgent(c.Pane.Provider) {
		if agents := d.st.AgentPanes(c.Tab); len(agents) > 0 {
			c.Pane = d.liveCwd(agents[0].Pane)
			c.Pane.History = nil
		}
	}
	if d.workerReplies == nil {
		d.workerReplies = map[string]map[string]string{}
	}
	replies := map[string]string{}
	for _, p := range panes {
		reply, ok := d.replies[p.ID]
		if !ok {
			if h := d.panes[p.ID]; h != nil {
				// Screen-read agents have no final-message hook. Keep the same
				// bounded, redacted text that read --screen could read before closing.
				d.keepReply(p.ID, screenText(h.Snapshot()))
				reply, ok = d.replies[p.ID]
			}
		}
		if ok {
			replies[p.ID] = reply
		}
	}
	d.workerReplies[c.ID] = replies
}

func (d *Daemon) pruneWorkerReplies() {
	for id := range d.workerReplies {
		if !slices.ContainsFunc(d.st.Closed, func(c model.ClosedTab) bool { return c.ID == id }) {
			delete(d.workerReplies, id)
		}
	}
}
