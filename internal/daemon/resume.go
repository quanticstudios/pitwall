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
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Resume after a usage limit: when Claude Code or Codex ends a turn on its
// plan's usage limit, the daemon types continue into it resumeDelay after
// the limit resets ([agents] resume_after_limit), or sends it to a chat.
// Panes keep their schedule and the limit acted on in the saved state.

// resumeDelay is how long after a limit resets continue is typed.
var resumeDelay = time.Minute

// The clock behind resumes; tests replace it.
var (
	resumeNow   = time.Now
	resumeAfter = func(wait time.Duration, f func()) (stop func() bool) { return time.AfterFunc(wait, f).Stop }
)

// resume is a pane's or a chat's pending continue.
type resume struct {
	at       time.Time // when continue is typed
	p        Pane      // the pane's process when it was set; nil for a chat
	send     func(string) error
	prov     model.Provider // the agent it was set for
	turn     time.Time      // UpdatedAt of the activity the limit stopped
	session  string
	restored bool
	// since is when the turn stopped, zero when unknown: a key of the
	// user's after it, such as a draft, drops the resume.
	since time.Time
	stop  func() bool
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
	var s flow.ProfileScanner
	return func() []flow.Limit {
		mu.Lock()
		defer mu.Unlock()
		home, _ := os.UserHomeDir()
		now := time.Now()
		cfg, _ := config.Load()
		return s.Read(home, config.StateDir(), cfg.Profiles, now, 2)
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
		d.mu.Lock()
		changed := false
		for _, p := range d.st.Panes {
			changed = d.dropResume(p.ID) || changed
		}
		for id := range d.lim.pending {
			changed = d.dropResume(id) || changed
		}
		if changed {
			d.changed()
		}
		d.mu.Unlock()
		if changed {
			d.save()
		}
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
		fresh := ok && d.lim.seen[l.id] != msg && !d.actedOnLimit(l.id, l.turn, msg)
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

// agentOf is the tab and agent of id, a pane or a chat, and the pane, nil
// for a chat. Callers hold d.mu.
func (d *Daemon) agentOf(id string) (ws string, prov model.Provider, p *model.Pane, ok bool) {
	if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }); i >= 0 {
		return d.st.Panes[i].WorkspaceID, d.st.Panes[i].Provider, &d.st.Panes[i], true
	}
	if i := slices.IndexFunc(d.st.Chats, func(c model.Chat) bool { return c.ID == id }); i >= 0 {
		return d.st.Chats[i].WorkspaceID, d.st.Chats[i].Provider, nil, true
	}
	return "", "", nil, false
}

// isChat reports whether id is a live chat's. Callers hold d.mu.
func (d *Daemon) isChat(id string) bool {
	_, _, p, ok := d.agentOf(id)
	return ok && p == nil
}

// limitStopped schedules continue for pane or chat id, whose agent's turn turn
// (the UpdatedAt of its activity, zero for none) ended on limit message
// msg naming reset resets, zero for none. Without one, the reset is the
// one the tracked limits name. The activity becomes an error with msg as
// its Detail. A turn that changed meanwhile, a key of the user's since it
// stopped, and a reset more than resumeDelay past set nothing.
func (d *Daemon) limitStopped(id string, turn time.Time, msg string, resets time.Time) {
	now := resumeNow()
	var limits []flow.Limit
	if resets.IsZero() && d.o.Limits != nil {
		limits = d.o.Limits()
	}
	d.mu.Lock()
	armed := false
	defer func() {
		d.mu.Unlock()
		if armed {
			d.save()
		}
	}()
	ws, prov, sp, ok := d.agentOf(id)
	i := d.activityIndex(id)
	var cur time.Time
	if i >= 0 {
		cur = d.st.Activities[i].UpdatedAt
	}
	if d.closing || !ok || !cur.Equal(turn) || d.mail.keyAt[id].After(turn) {
		return
	}
	auto := sp != nil && d.claudeContinues(*sp)
	if resets.IsZero() {
		profile := ""
		if w := d.workspace(ws); w != nil {
			profile = w.Profile
		}
		limits = slices.DeleteFunc(slices.Clone(limits), func(l flow.Limit) bool { return l.Profile != profile })
		resets = limitReset(limits, prov, now)
	}
	if !auto && (resets.IsZero() || resets.Before(now.Add(-resumeDelay))) {
		log.Printf("pane %s: stopped on a usage limit with no reset ahead; no resume", id)
		return
	}
	if i < 0 || d.st.Activities[i].State != model.StateError {
		a := model.Activity{PaneID: id, WorkspaceID: ws, Provider: prov, State: model.StateError, Detail: msg, UpdatedAt: time.Now()}
		if i < 0 {
			d.st.Activities = append(d.st.Activities, a)
			i = len(d.st.Activities) - 1
		} else {
			a.SessionID = d.st.Activities[i].SessionID
			d.st.Activities[i] = a
		}
	}
	d.st.Activities[i].Detail = msg
	d.dropResume(id)
	if auto {
		d.changed()
		return
	}
	r := &resume{at: resets.Add(resumeDelay), p: d.panes[id], prov: prov, turn: d.st.Activities[i].UpdatedAt, since: turn}
	if sp != nil {
		r.session = sp.SessionID
		sp.LimitResume = &model.LimitResume{PaneID: id, Provider: prov, SessionID: r.session,
			Turn: r.turn, Since: turn, Resets: resets, Message: msg, Pending: true, Drafting: d.mail.drafting[id]}
	}
	d.armResume(id, r, now)
	armed = true
	d.changed()
}

// armResume binds the timer to the current process. Callers hold d.mu.
func (d *Daemon) armResume(id string, r *resume, now time.Time) {
	if p := r.p; p != nil {
		r.send = func(text string) error { return d.typeLine(id, p, text, "nothing to type") }
	} else {
		r.send = func(text string) error { return d.chatResume(id, text) }
	}
	r.stop = resumeAfter(max(0, r.at.Sub(now)), func() { d.fireResume(id, r) })
	if d.lim.pending == nil {
		d.lim.pending = map[string]*resume{}
	}
	d.lim.pending[id] = r
}

// actedOnLimit prevents a cancelled or consumed limit from arming again when
// its message reappears during startup. Callers hold d.mu.
func (d *Daemon) actedOnLimit(id string, turn time.Time, msg string) bool {
	_, _, p, ok := d.agentOf(id)
	if !ok || p == nil || p.LimitResume == nil {
		return false
	}
	r := p.LimitResume
	return (r.Turn.Equal(turn) || turn.IsZero()) && r.Message == msg
}

// restoreLimitResume runs after the pane's agent and stopped activity return.
// Callers hold d.mu.
func (d *Daemon) restoreLimitResume(id string, restarted bool) {
	_, prov, p, ok := d.agentOf(id)
	if !ok || p == nil || p.LimitResume == nil || !p.LimitResume.Pending {
		return
	}
	saved := *p.LimitResume
	i := d.activityIndex(id)
	r := &resume{at: saved.Resets.Add(resumeDelay), p: d.panes[id], prov: saved.Provider,
		turn: saved.Turn, since: saved.Since, session: saved.SessionID, restored: restarted}
	if saved.PaneID != id || saved.SessionID == "" || !store.Resumes(*p) || p.Exited ||
		prov != model.ProviderClaude && prov != model.ProviderCodex || saved.Resets.IsZero() ||
		i < 0 || d.st.Activities[i].Detail != saved.Message || d.resumeBlocked(id, r) != "" {
		saved.Pending = false
		p.LimitResume = &saved
		return
	}
	d.mail.drafting[id] = saved.Drafting
	d.lim.seenMsg(id, saved.Message)
	d.armResume(id, r, resumeNow())
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

// fireResume types continue into pane id for r, or sends it to chat id,
// unless resumeBlocked says why not or the screen shows a turn or a
// prompt. It holds sendMu
// before it takes r, so a key or a cancel while another typing runs still
// drops it. Once typed, the activity is working with model.ResumedDetail,
// which the GUI notifies about.
func (d *Daemon) fireResume(id string, r *resume) {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	var g vt.Grid
	if r.p != nil {
		g = r.p.Snapshot()
	}
	busy := r.p != nil && agent.ReadScreen(g) != agent.ScreenNone
	d.mu.Lock()
	if d.lim.pending[id] != r {
		d.mu.Unlock()
		return // dropped or replaced
	}
	why := d.resumeBlocked(id, r)
	if why == "" && r.restored && !d.resumeReady(id, r, g) {
		r.stop = resumeAfter(time.Second, func() { d.fireResume(id, r) })
		d.mu.Unlock()
		return
	}
	if why == "" && busy {
		why = "its screen shows a turn or a prompt"
	}
	if why == "" && r.p != nil && d.drafted(id, r.prov, g) {
		why = "the user has a draft in its input box"
	}
	d.consumeResume(id)
	d.changed()
	if why != "" {
		d.dropResume(id)
	}
	d.mu.Unlock()
	if why != "" {
		log.Printf("pane %s: no resume after the usage limit: %s", id, why)
		d.save()
		return
	}
	// Persist consumption before typing: a crash must never replay this limit.
	// shortcut: a crash before typing can lose the send; provider receipts would close that gap.
	if err := d.saveState(false); err != nil {
		d.mu.Lock()
		d.dropResume(id)
		d.mu.Unlock()
		return
	}
	if r.p != nil {
		g = r.p.Snapshot()
	}
	d.mu.Lock()
	valid := d.lim.pending[id] == r && d.resumeBlocked(id, r) == ""
	if r.p != nil && (agent.ReadScreen(g) != agent.ScreenNone || d.drafted(id, r.prov, g)) {
		valid = false
	}
	d.dropResume(id)
	d.mu.Unlock()
	if !valid {
		return
	}
	if err := r.send("continue"); err != nil {
		log.Printf("pane %s: resume after the usage limit: %q", id, err)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if i := d.activityIndex(id); i >= 0 && d.st.Activities[i].UpdatedAt.Equal(r.turn) {
		a := &d.st.Activities[i]
		a.State, a.Detail, a.UpdatedAt = model.StateWorking, model.ResumedDetail, time.Now()
		d.changed()
	}
}

// resumeBlocked is why r must not type into pane id, or send to chat id,
// "" when it may: the setting is on, the pane still runs the same process
// and agent, its activity is still the turn the limit stopped, and the
// user typed nothing in it since that turn stopped. Callers hold d.mu.
func (d *Daemon) resumeBlocked(id string, r *resume) string {
	_, prov, sp, ok := d.agentOf(id)
	i := d.activityIndex(id)
	switch {
	case !d.resumeOn():
		return "resume_after_limit is off"
	case d.closing || !ok || d.panes[id] != r.p:
		return "the pane closed or restarted"
	case prov != r.prov:
		return "another agent runs there"
	case sp != nil && sp.SessionID != r.session:
		return "the agent session changed"
	case sp != nil && d.claudeContinues(*sp):
		return "Claude will continue on its own"
	case i < 0 || !d.st.Activities[i].UpdatedAt.Equal(r.turn) || !stoppedState(d.st.Activities[i].State):
		// why: Windows clocks can give a new turn the stopped one's UpdatedAt, so the state counts too.
		return "the turn changed"
	case d.mail.keyAt[id].After(r.since):
		return "the user typed in it"
	}
	return ""
}

// stoppedState reports a state a usage limit leaves a turn in.
func stoppedState(s model.AgentState) bool {
	return s == model.StateError || s == model.StateCompleted
}

// dropResume drops pane id's resume, reporting whether it had one.
// Callers hold d.mu.
func (d *Daemon) dropResume(id string) bool {
	r := d.lim.pending[id]
	dropped := d.consumeResume(id) || r != nil
	if r != nil {
		r.stop()
		delete(d.lim.pending, id)
	}
	return dropped
}

// consumeResume replaces the saved record without mutating a snapshot that
// another goroutine may be writing. Callers hold d.mu.
func (d *Daemon) consumeResume(id string) bool {
	if _, _, p, ok := d.agentOf(id); ok && p != nil && p.LimitResume != nil && p.LimitResume.Pending {
		saved := *p.LimitResume
		saved.Pending = false
		p.LimitResume = &saved
		return true
	}
	return false
}

// cancelResume is proto.CancelResume: the user's Cancel resume.
func (d *Daemon) cancelResume(m proto.CancelResume) error {
	d.mu.Lock()
	dropped := d.dropResume(m.Pane)
	if dropped {
		d.changed()
	}
	d.mu.Unlock()
	if dropped {
		return d.saveState(false)
	}
	return nil
}

// resumeReady waits for the resumed agent's hook to settle. Codex has no
// startup hook, so it must draw its input box after the startup grace.
// Callers hold d.mu.
func (d *Daemon) resumeReady(id string, r *resume, g vt.Grid) bool {
	if hook := d.live.hookAt[id]; !hook.IsZero() {
		return time.Since(hook) >= sendSettle.get()
	}
	return time.Since(d.resumed[id]) >= restartWait && agent.InputReady(r.prov, g)
}

// restartLine is what a resumed agent that was mid-turn gets typed once it
// is back after a daemon restart.
const restartLine = "pitwall restarted and resumed this session. Continue the task where you left off; your uncommitted work is in the worktree, don't checkout, reset or stash it."

// restartWait is how long after a restart restartLine waits for the
// agent's first hook. Codex sends none until its first prompt, so past it
// the line goes once the agent sits at its prompt anyway.
const restartWait = 20 * time.Second

// restartNudge carries pane id's state over a restart from saved, the
// activities of the run before: a finished turn stays finished, so pitwall
// wait counts it, and a turn that was running gets restartLine queued like
// a pitwall send. The agent's first hook (Claude's resume SessionStart, pi's
// session_start) delivers it, or restartWait. Callers hold d.mu.
// A consumed limit continuation needs a new turn hook before another nudge.
func (d *Daemon) restartNudge(id string, saved []model.Activity) {
	i := slices.IndexFunc(saved, func(a model.Activity) bool { return a.PaneID == id })
	if i < 0 {
		return
	}
	switch a := saved[i]; a.State {
	case model.StateCompleted, model.StateError:
		d.st.Activities = append(d.st.Activities, a)
	case model.StateWorking:
		if _, _, p, ok := d.agentOf(id); ok && p != nil && p.LimitResume != nil && !p.LimitResume.Pending && a.Detail == model.ResumedDetail {
			// No hook has confirmed a new turn since the consumed continuation.
			a.State, a.Detail, a.UpdatedAt = model.StateError, p.LimitResume.Message, p.LimitResume.Turn
			d.st.Activities = append(d.st.Activities, a)
			return
		}
		d.mail.queue[id] = append(d.mail.queue[id], letter{text: restartLine, plain: true})
		d.wake(id, restartWait)
	}
}
