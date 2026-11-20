package daemon

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/timelog"
)

// syncTurns moves each pane's turn to its activity's state and ends the
// turns of panes that closed, writing what ends to timing.jsonl. Only a
// pane whose agent sent a hook starts a turn: without hooks there are no
// tool times. Callers hold d.mu.
func (d *Daemon) syncTurns(now time.Time) {
	for i := range d.st.Panes {
		d.syncTurn(&d.st.Panes[i], now)
	}
	for id := range d.turns.Panes() {
		if !slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }) {
			if e, ok := d.turns.Close(id, now); ok {
				d.o.Timing.Add(e)
			}
			delete(d.turnCwd, id)
		}
	}
}

// hookTurn keeps the agent's folder for timing without changing the
// shell's saved folder. Callers hold d.mu.
func (d *Daemon) hookTurn(p *model.Pane, payload []byte, now time.Time) {
	var hook struct {
		Cwd            string          `json:"cwd"`
		TranscriptPath json.RawMessage `json:"transcript_path"`
		ConversationID string          `json:"conversation_id"`
	}
	// A Claude or Codex /side fork is outside the pane's main turn.
	if json.Unmarshal(payload, &hook) == nil && hook.Cwd != "" && (string(hook.TranscriptPath) != "null" || hook.ConversationID != "") {
		if d.turnCwd == nil {
			d.turnCwd = map[string]string{}
		}
		d.turnCwd[p.ID] = hook.Cwd
	}
	d.syncTurn(p, now)
}

// syncTurn is syncTurns for one pane. Callers hold d.mu.
func (d *Daemon) syncTurn(p *model.Pane, now time.Time) {
	var s model.AgentState
	if i := d.activityIndex(p.ID); i >= 0 {
		s = turnState(d.st.Activities[i])
	}
	// Hooks, screen detection and process exit can each end a turn.
	// changed already publishes this transition, so no extra notification.
	if p.Exited || !timelog.Running(s) {
		delete(d.repeats, p.ID)
	}
	if !d.turns.Has(p.ID) && (d.live.hookAt[p.ID].IsZero() || !timelog.Running(s)) {
		delete(d.turnCwd, p.ID)
		return
	}
	for _, e := range d.turns.State(d.where(p), s, now) {
		d.o.Timing.Add(e)
	}
	if !d.turns.Has(p.ID) {
		delete(d.turnCwd, p.ID)
	}
}

// turnState is a's state as a timed turn sees it: an agent that ended its
// turn with background work still running is done, or the hours until the
// next prompt would count as model time.
func turnState(a model.Activity) model.AgentState {
	if a.State == model.StateWorking && a.Background {
		return model.StateCompleted
	}
	return a.State
}

// where names p and its tab for a timing line. Callers hold d.mu.
func (d *Daemon) where(p *model.Pane) timelog.Where {
	w := timelog.Where{Pane: p.ID, Tab: p.WorkspaceID, Provider: string(p.Provider), Cwd: cmp.Or(d.turnCwd[p.ID], p.Cwd)}
	if ws := d.workspace(p.WorkspaceID); ws != nil {
		w.Project, w.Name, w.Branch = ws.ProjectID, ws.Name, ws.Branch
	}
	return w
}

// prMilestones logs what changed in tab w's pull request, from old, when
// had, to pr: it opened, its checks finished or it merged. Only a change
// the daemon saw counts, so a restart does not log a merge again.
// Callers hold d.mu.
func (d *Daemon) prMilestones(w model.Workspace, old model.PR, had bool, pr model.PR, now time.Time) {
	same := had && old.Number == pr.Number
	e := timelog.Event{T: now, Tab: w.ID, Project: w.ProjectID, Name: w.Name, Branch: w.Branch, PR: pr.Number}
	add := func(kind string) {
		e.Kind = kind
		d.o.Timing.Add(e)
	}
	if !same && pr.State == model.PROpen {
		add(timelog.PR)
	}
	if ci := pr.CI(); same && ci != old.CI() && (ci == model.CheckPass || ci == model.CheckFail) {
		e.CI = string(ci)
		add(timelog.Checks)
		e.CI = ""
	}
	if same && old.State == model.PROpen && pr.State == model.PRMerged {
		add(timelog.Merged)
	}
}

// tabClosed logs that tab w closed. Callers hold d.mu.
func (d *Daemon) tabClosed(w model.Workspace) {
	d.o.Timing.Add(timelog.Event{T: time.Now(), Kind: timelog.Closed, Tab: w.ID, Project: w.ProjectID, Name: w.Name, Branch: w.Branch})
	d.turns.Forget(w.ID)
}
