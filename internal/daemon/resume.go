package daemon

import (
	"log"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Resume after a usage limit: when Claude Code or Codex ends a turn on its
// plan's usage limit, the daemon types continue into it resumeDelay after
// the limit resets ([agents] resume_after_limit). The schedule lives in
// memory only; a daemon restart drops it.

// resumeDelay is how long after a limit resets continue is typed.
var resumeDelay = time.Minute

// The clock behind resumes; tests replace it.
var (
	resumeNow   = time.Now
	resumeAfter = func(wait time.Duration, f func()) (stop func() bool) { return time.AfterFunc(wait, f).Stop }
)

// resume is a pane's pending continue.
type resume struct {
	at   time.Time      // when continue is typed
	p    Pane           // the pane's process when it was set
	prov model.Provider // the agent it was set for
	turn time.Time      // UpdatedAt of the activity the limit stopped
	stop func() bool
}

// limitResumes is the daemon's resumes. Guarded by d.mu.
type limitResumes struct {
	pending map[string]*resume // pane: its continue
	// seen is, per pane, the limit message last acted on, until it leaves
	// the screen, so one message on screen schedules once.
	seen map[string]string
}

// seenMsg records msg as pane id's limit message acted on.
func (l *limitResumes) seenMsg(id, msg string) {
	if l.seen == nil {
		l.seen = map[string]string{}
	}
	l.seen[id] = msg
}

// trackedLimits reads the plan limits as the GUI's meter does: Claude
// Code's from pitwall statusline's file, Codex's from its rollouts of the
// last two days.
func trackedLimits() func() []flow.Limit {
	var mu sync.Mutex
	var s flow.Scanner
	return func() []flow.Limit {
		mu.Lock()
		defer mu.Unlock()
		home, _ := os.UserHomeDir()
		now := time.Now()
		s.Scan(flow.CodexDays(home, now, 2), now.AddDate(0, 0, -2))
		return s.Limits(config.StateDir())
	}
}

// resumeOn reads [agents] resume_after_limit; nil is off.
func (d *Daemon) resumeOn() bool { return d.o.ResumeAfterLimit != nil && d.o.ResumeAfterLimit() }

// pendingResume is pane id's resume while resumeBlocked lets it type,
// else nil. Callers hold d.mu.
func (d *Daemon) pendingResume(id string) *resume {
	if r := d.lim.pending[id]; r != nil && d.resumeBlocked(id, r) == "" {
		return r
	}
	return nil
}

// checkLimits reads the screen of each Claude Code or Codex agent whose
// turn ended for a usage-limit message, which also catches one no hook
// reported.
func (d *Daemon) checkLimits() {
	if !d.resumeOn() {
		return
	}
	type look struct {
		id   string
		p    Pane
		turn time.Time
	}
	var looks []look
	d.mu.Lock()
	for _, sp := range d.st.Panes {
		p := d.panes[sp.ID]
		if p == nil || sp.Exited || sp.Provider != model.ProviderClaude && sp.Provider != model.ProviderCodex || d.pendingResume(sp.ID) != nil {
			continue
		}
		var turn time.Time
		if i := d.activityIndex(sp.ID); i >= 0 {
			if s := d.st.Activities[i].State; s != model.StateCompleted && s != model.StateError {
				continue
			}
			turn = d.st.Activities[i].UpdatedAt
		}
		looks = append(looks, look{sp.ID, p, turn})
	}
	d.mu.Unlock()
	for _, l := range looks {
		g := l.p.Snapshot()
		if agent.ReadScreen(g) != agent.ScreenNone {
			continue
		}
		msg, resets, ok := agent.UsageLimit(agent.ScreenText(g, 12), resumeNow())
		d.mu.Lock()
		fresh := ok && d.lim.seen[l.id] != msg
		switch {
		case d.panes[l.id] != l.p:
		case !ok:
			delete(d.lim.seen, l.id)
		case fresh:
			d.lim.seenMsg(l.id, msg)
		}
		d.mu.Unlock()
		if fresh {
			d.limitStopped(l.id, l.turn, msg, resets)
		}
	}
}

// limitStopped schedules continue for pane id, whose agent's turn turn
// (the UpdatedAt of its activity, zero for none) ended on limit message
// msg naming reset resets, zero for none. Without one, the reset is the
// one the tracked limits name. The activity becomes an error with msg as
// its Detail. A turn that changed meanwhile, and a reset not ahead, set
// nothing.
func (d *Daemon) limitStopped(id string, turn time.Time, msg string, resets time.Time) {
	now := resumeNow()
	var limits []flow.Limit
	if resets.IsZero() && d.o.Limits != nil {
		limits = d.o.Limits()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	i := d.activityIndex(id)
	var cur time.Time
	if i >= 0 {
		cur = d.st.Activities[i].UpdatedAt
	}
	if d.closing || pi < 0 || !cur.Equal(turn) {
		return
	}
	sp := d.st.Panes[pi]
	if resets.IsZero() {
		resets = limitReset(limits, sp.Provider, now)
	}
	if resets.IsZero() || resets.Before(now.Add(-time.Hour)) {
		log.Printf("pane %s: stopped on a usage limit with no reset ahead; no resume", id)
		return
	}
	if i < 0 || d.st.Activities[i].State != model.StateError {
		a := model.Activity{PaneID: id, WorkspaceID: sp.WorkspaceID, Provider: sp.Provider, State: model.StateError, Detail: msg, UpdatedAt: time.Now()}
		if i < 0 {
			d.st.Activities = append(d.st.Activities, a)
			i = len(d.st.Activities) - 1
		} else {
			a.SessionID = d.st.Activities[i].SessionID
			d.st.Activities[i] = a
		}
	}
	d.dropResume(id)
	r := &resume{at: resets.Add(resumeDelay), p: d.panes[id], prov: sp.Provider, turn: d.st.Activities[i].UpdatedAt}
	if r.at.Before(now) {
		r.at = now // a reset just past
	}
	r.stop = resumeAfter(r.at.Sub(now), func() { d.fireResume(id, r) })
	if d.lim.pending == nil {
		d.lim.pending = map[string]*resume{}
	}
	d.lim.pending[id] = r
	d.changed()
}

// limitReset is when the fullest window of prov's plan limits resets,
// zero when unknown or past.
func limitReset(limits []flow.Limit, prov model.Provider, now time.Time) time.Time {
	var top flow.Window
	for _, l := range limits {
		if w, ok := l.Tightest(now); ok && l.Provider == prov && w.Used >= top.Used {
			top = w
		}
	}
	return top.Resets
}

// fireResume types continue into pane id for r, unless resumeBlocked
// says why not or the screen shows a turn or a prompt.
func (d *Daemon) fireResume(id string, r *resume) {
	d.mu.Lock()
	if d.lim.pending[id] != r {
		d.mu.Unlock()
		return // dropped or replaced
	}
	delete(d.lim.pending, id)
	d.changed()
	why := d.resumeBlocked(id, r)
	d.mu.Unlock()
	if why == "" && agent.ReadScreen(r.p.Snapshot()) != agent.ScreenNone {
		why = "its screen shows a turn or a prompt"
	}
	if why != "" {
		log.Printf("pane %s: no resume after the usage limit: %s", id, why)
		return
	}
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	if err := d.typeLine(id, r.p, "continue", "nothing to type"); err != nil {
		log.Printf("pane %s: resume after the usage limit: %q", id, err)
	}
}

// resumeBlocked is why r must not type into pane id, "" when it may: the
// pane still runs the same process and agent, and its activity is still
// the turn the limit stopped. A key of the user's drops r (noteUserKey).
// Callers hold d.mu.
func (d *Daemon) resumeBlocked(id string, r *resume) string {
	pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	i := d.activityIndex(id)
	switch {
	case d.closing || pi < 0 || d.panes[id] != r.p:
		return "the pane closed or restarted"
	case d.st.Panes[pi].Provider != r.prov:
		return "another agent runs there"
	case i < 0 || !d.st.Activities[i].UpdatedAt.Equal(r.turn):
		return "the turn changed"
	}
	return ""
}

// dropResume drops pane id's resume, reporting whether it had one.
// Callers hold d.mu.
func (d *Daemon) dropResume(id string) bool {
	r := d.lim.pending[id]
	if r == nil {
		return false
	}
	r.stop()
	delete(d.lim.pending, id)
	return true
}

// cancelResume is proto.CancelResume: the user's Cancel resume.
func (d *Daemon) cancelResume(m proto.CancelResume) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dropResume(m.Pane) {
		d.changed()
	}
	return nil
}
