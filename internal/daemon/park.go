package daemon

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Parking: a Claude Code or Codex agent idle at its input box for [agents]
// park_idle_after loses its process to free its memory. Its tab, layout
// and screen stay, and its activity reads model.StateParked. The next key,
// message or prompt resumes it with the command a restart uses, and what
// came meanwhile waits until the agent is back at its input box.

// parkPoll is the longest gap between two looks at idle screens; tests
// shorten it.
var parkPoll = newTiming(10 * time.Second)

// parking is the daemon's idle, parked and waking panes. Guarded by d.mu.
type parking struct {
	polled time.Time
	idle   map[string]*idlePane // a pane that may park: its screen and since when it is still
	waking map[string]time.Time // an unparked pane: when its agent started again
	keys   map[string][][]byte  // a parked or waking pane: keys typed in it, oldest first
	// stalled marks a waking pane past resumeReadyWait: what waited still
	// waits, and new keys go straight in so the user can answer its screen.
	stalled map[string]bool
	// promptAt is the first prompt of each pane's agent process, and
	// launched marks one whose start (SessionStart) this daemon saw: what
	// the agent starts before its first prompt, such as MCP servers, is no
	// tool's process. See toolsSince.
	promptAt map[string]time.Time
	launched map[string]bool
}

// idlePane is a pane's screen at the last look and when it last changed.
type idlePane struct {
	p     Pane
	cells []vt.Cell
	since time.Time
}

// parkAfter reads [agents] park_idle_after; 0 is off.
func (d *Daemon) parkAfter() time.Duration {
	if d.o.ParkIdleAfter == nil {
		return 0
	}
	return d.o.ParkIdleAfter()
}

// isParked reports whether pane id is parked. Callers hold d.mu.
func (d *Daemon) isParked(id string) bool {
	s, ok := d.panes[id].(*stopped)
	return ok && s.parked
}

// parkVeto is why pane sp must not park now, "" when it may: a Claude Code
// or Codex agent whose process runs with a session to resume, hidden from
// every window, at a finished turn or with none, with nothing waiting to
// go in or come out. Callers hold d.mu.
func (d *Daemon) parkVeto(sp model.Pane) string {
	id := sp.ID
	var a model.Activity
	i := d.activityIndex(id)
	if i >= 0 {
		a = d.st.Activities[i]
	}
	switch {
	case d.closing || d.frozen:
		return "the daemon is stopping"
	case sp.Provider != model.ProviderClaude && sp.Provider != model.ProviderCodex:
		return "not Claude Code or Codex"
	case sp.Exited || d.panes[id] == nil || d.inputs[id] == nil:
		return "no running process"
	case !store.Resumes(sp):
		return "no session to resume"
	case d.st.IsPopup(id):
		return "a popup"
	case d.inView(id):
		return "a window shows it"
	case i >= 0 && a.State != model.StateCompleted && a.State != model.StateError:
		return "its turn has not ended"
	case d.live.hookAt[id].IsZero():
		return "no hook since it started, so pitwall cannot tell when a resume is back"
	case d.live.background[id]:
		return "its subagents or workflows run"
	case d.lim.pending[id] != nil:
		return "a usage-limit resume waits"
	case len(d.mail.queue[id]) > 0:
		return "a message waits"
	case d.attn[id] != nil && d.attn[id].notice != nil:
		return "a notification waits"
	}
	return ""
}

// inView reports whether a window draws pane id. Callers hold d.mu.
func (d *Daemon) inView(id string) bool {
	for c := range d.clients {
		if c.shows(id) {
			return true
		}
	}
	return false
}

// checkPark parks each agent idle for park_idle_after: no veto, its input
// box empty on a screen with no form, no key, hook or turn and no screen
// change in that time, and no shell or later process of its own running.
func (d *Daemon) checkPark(now time.Time) {
	after := d.parkAfter()
	d.mu.Lock()
	if after <= 0 {
		d.park.idle = nil
	}
	if after <= 0 || now.Sub(d.park.polled) < min(parkPoll.get(), after/4) {
		d.mu.Unlock()
		return
	}
	d.park.polled = now
	type look struct {
		id    string
		p     Pane
		prov  model.Provider
		tools time.Time
	}
	var looks []look
	for _, sp := range d.st.Panes {
		if d.parkVeto(sp) != "" {
			delete(d.park.idle, sp.ID)
			continue
		}
		looks = append(looks, look{sp.ID, d.panes[sp.ID], sp.Provider, d.toolsSince(sp.ID, now)})
	}
	d.mu.Unlock()
	var parked []string
	for _, l := range looks {
		g := l.p.Snapshot()
		still := agent.InputReady(l.prov, g) && agent.ReadScreen(g) == agent.ScreenNone && !agent.Draft(l.prov, g)
		// why: reads /proc, so outside d.mu. A busy agent waits for the next
		// look and keeps its idle time: a hook's shell lives a moment.
		busy := still && agentBusy(l.p, l.tools)
		d.mu.Lock()
		i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == l.id })
		if i < 0 || d.panes[l.id] != l.p {
			d.mu.Unlock()
			continue
		}
		if d.park.idle == nil {
			d.park.idle = map[string]*idlePane{}
		}
		w := d.park.idle[l.id]
		if !still || w == nil || w.p != l.p || !slices.Equal(w.cells, g.Cells) {
			w = &idlePane{p: l.p, cells: g.Cells, since: now}
			d.park.idle[l.id] = w
		}
		since := w.since
		for _, t := range []time.Time{d.mail.keyAt[l.id], d.mail.typed[l.id], d.live.hookAt[l.id]} {
			since = latest(since, t)
		}
		if ai := d.activityIndex(l.id); ai >= 0 {
			since = latest(since, d.st.Activities[ai].UpdatedAt)
		}
		if still && !busy && now.Sub(since) >= after && d.parkVeto(d.st.Panes[i]) == "" {
			log.Printf("pane %s: parked %q after %s idle", l.id, l.prov, idleText(after))
			d.parkPane(l.id, l.p, "Parked after "+idleText(after)+" idle. Press Enter to resume.")
			parked = append(parked, l.id)
		}
		d.mu.Unlock()
	}
	for _, id := range parked {
		d.pushParked(id)
	}
	if len(parked) > 0 {
		d.save()
	}
}

// agentBusy reports whether pane p's agent runs a process it started at
// or after tools, a tool's process such as a dev server (see procBusy). It
// is true when that cannot be read or tools is zero. Tests replace it.
var agentBusy = func(p Pane, tools time.Time) bool {
	f, ok := p.(foregrounder)
	if !ok || tools.IsZero() {
		return true
	}
	pg := f.Foreground()
	return pg <= 0 || procBusy(pg, tools)
}

// toolsSince is when pane id's agent process could first start a tool's
// process: its first prompt, or now when it has had none since a start
// this daemon saw. Zero when unknown, as after an update's handoff.
// Callers hold d.mu.
func (d *Daemon) toolsSince(id string, now time.Time) time.Time {
	if t, ok := d.park.promptAt[id]; ok {
		return t
	}
	if d.park.launched[id] {
		return now
	}
	return time.Time{}
}

// noteParkHook records a hook's new agent process or first prompt for
// toolsSince. Callers hold d.mu.
func (d *Daemon) noteParkHook(id string, payload []byte, now time.Time) {
	if started, resumed := agent.NewProcess(payload); started || resumed {
		if d.park.launched == nil {
			d.park.launched = map[string]bool{}
		}
		d.park.launched[id] = true
		delete(d.park.promptAt, id)
	}
	if _, ok := d.park.promptAt[id]; !ok && agent.PromptSubmitted(payload) {
		if d.park.promptAt == nil {
			d.park.promptAt = map[string]time.Time{}
		}
		d.park.promptAt[id] = now
	}
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// idleText is d as 1h, 20m or 1h30m0s.
func idleText(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0 && d < time.Hour:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

// parkPane puts a parked handle showing live's screen and note in place of
// live, whose process closeAll then ends, and marks the pane parked with
// note as its activity's Detail. Callers hold d.mu.
func (d *Daemon) parkPane(id string, live Pane, note string) {
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if i < 0 {
		return
	}
	sp := d.st.Panes[i]
	newVT := d.o.NewVT
	if sv, ok := live.(interface{ Save() ([]byte, error) }); ok {
		// why: the emulator's own snapshot keeps the screen exactly, and in
		// memory only; history follows historyLines for what is saved.
		if b, err := sv.Save(); err == nil {
			newVT = func(cols, rows int, reply io.Writer) vt.Emulator {
				e, err := vt.Restore(b, reply)
				if err != nil {
					return vt.New(cols, rows, reply)
				}
				e.Resize(cols, rows)
				return e
			}
		}
	}
	h := d.parkedHandle(id, newVT, note)
	if ht, ok := live.(interface{ HistoryText(int) (int, string) }); ok && d.historyLines(sp) > 0 {
		h.cols, h.text = ht.HistoryText(d.historyLines(sp))
	}
	d.panes[id] = h
	delete(d.inputs, id)
	delete(d.holds, id)
	delete(d.resumed, id)
	delete(d.quiet, id)
	delete(d.park.idle, id)
	delete(d.park.waking, id)
	delete(d.park.stalled, id)
	delete(d.park.promptAt, id)
	delete(d.park.launched, id)
	delete(d.live.hookAt, id)
	delete(d.live.hookSeq, id)
	delete(d.live.fg, id)
	delete(d.live.background, id)
	delete(d.live.started, id)
	delete(d.live.fresh, id)
	delete(d.live.resumeSeen, id)
	d.programExited(id)
	d.setParked(id, sp, note)
	closeAll([]Pane{live})
	d.changed()
}

// setParked makes pane sp's activity parked with note. Callers hold d.mu.
func (d *Daemon) setParked(id string, sp model.Pane, note string) {
	a := model.Activity{PaneID: id, WorkspaceID: sp.WorkspaceID, Provider: sp.Provider, SessionID: sp.SessionID,
		State: model.StateParked, Detail: note, UpdatedAt: time.Now()}
	if i := d.activityIndex(id); i >= 0 {
		d.st.Activities[i] = a
	} else {
		d.st.Activities = append(d.st.Activities, a)
	}
}

// parkedHandle is a parked pane's handle: no process, newVT's screen, nil
// for an empty one, and note under it, dimmed. Callers hold d.mu.
func (d *Daemon) parkedHandle(id string, newVT vt.NewFunc, note string) *stopped {
	if newVT == nil {
		newVT = vt.New
	}
	size, ok := d.sizes[id]
	if !ok || size[0] <= 0 || size[1] <= 0 {
		size = [2]int{defaultCols, defaultRows}
	}
	s := &stopped{vt: newVT(size[0], size[1], io.Discard), done: make(chan struct{}), parked: true}
	close(s.done)
	// why: the agent leaves the cursor in its input box; the note goes under everything.
	s.vt.Write(fmt.Appendf(nil, "\x1b[%d;1H\r\n\x1b[2m%s\x1b[0m", size[1], note))
	return s
}

// pushParked sends windows parked pane id's screen: it has no watcher.
func (d *Daemon) pushParked(id string) {
	d.mu.Lock()
	p := d.panes[id]
	d.mu.Unlock()
	if p != nil {
		d.pushFrame(id, p)
	}
}

// unpark starts parked pane id's agent with the command a restart resumes
// it with; checkWaking lets what reaches the pane meanwhile go in once the
// agent is back. A failed start leaves the pane parked with the error. It
// reports whether the pane was parked. Callers hold d.mu.
func (d *Daemon) unpark(id string) bool {
	old, ok := d.panes[id].(*stopped)
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if !ok || !old.parked || i < 0 {
		return false
	}
	if d.closing || d.frozen {
		return true // the input waits in keys or the mail queue
	}
	sp := d.st.Panes[i]
	if err := d.startVT(id, d.o.RestoreCmd(sp), sp.Cwd, d.portsOf(sp.WorkspaceID), sp.Env, d.carryVT(sp, old)); err != nil {
		log.Printf("pane %s: resume a parked agent: %q", id, err)
		d.setParked(id, sp, "Could not resume: "+err.Error()+". Press Enter to try again.")
		d.changed()
		return true
	}
	log.Printf("pane %s: resuming parked %q", id, sp.Provider)
	now := time.Now()
	d.resumed[id] = now
	if d.park.waking == nil {
		d.park.waking = map[string]time.Time{}
	}
	d.park.waking[id] = now
	if ai := d.activityIndex(id); ai >= 0 && d.st.Activities[ai].State == model.StateParked {
		d.st.Activities = slices.Delete(d.st.Activities, ai, ai+1)
	}
	d.changed()
	return true
}

// holdParked takes input b for a parked or waking pane, reporting whether
// it did: a key resumes a parked pane and waits, with any after it, until
// the agent is back, except a lone Enter, which only resumes it. Focus and
// mouse reports are dropped; they resume nothing.
func (d *Daemon) holdParked(id string, b []byte) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, waking := d.park.waking[id]
	if !waking && !d.isParked(id) || waking && d.park.stalled[id] {
		return false
	}
	if s := string(b); focusReport(b) || strings.HasPrefix(s, "\x1b[<") || strings.HasPrefix(s, "\x1b[M") {
		return true
	}
	if !waking {
		d.unpark(id)
		if s := string(b); s == "\r" || s == "\x1b[13u" {
			return true
		}
	}
	if d.park.keys == nil {
		d.park.keys = map[string][][]byte{}
	}
	d.park.keys[id] = append(d.park.keys[id], b)
	return true
}

// queueParked queues l for parked or waking pane id and resumes it,
// reporting whether id was one. Callers hold d.mu.
func (d *Daemon) queueParked(id string, l letter) bool {
	_, waking := d.park.waking[id]
	if !waking && !d.unpark(id) {
		return false
	}
	d.mail.queue[id] = append(d.mail.queue[id], l)
	d.changed()
	return true
}

// waitsToWake reports whether pane id is parked or waking, so a letter
// for it waits for checkWaking. Callers hold d.mu.
func (d *Daemon) waitsToWake(id string) bool {
	_, waking := d.park.waking[id]
	return waking || d.isParked(id)
}

// checkWaking lets each waking pane's held keys, then its messages, go in
// once its agent is back at its input box. Past resumeReadyWait it reports
// the resume as stuck and keeps holding them.
func (d *Daemon) checkWaking() {
	type look struct {
		id   string
		p    Pane
		prov model.Provider
		at   time.Time
	}
	var looks []look
	d.mu.Lock()
	for id, at := range d.park.waking {
		if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }); i >= 0 && d.panes[id] != nil {
			looks = append(looks, look{id, d.panes[id], d.st.Panes[i].Provider, at})
		}
	}
	d.mu.Unlock()
	for _, l := range looks {
		g := l.p.Snapshot()
		d.mu.Lock()
		if d.panes[l.id] != l.p || d.park.waking[l.id] != l.at {
			d.mu.Unlock()
			continue
		}
		if !d.resumeReady(l.id, &resume{prov: l.prov}, g) || !agent.InputReady(l.prov, g) {
			if time.Since(l.at) >= resumeReadyWait && !d.park.stalled[l.id] {
				d.stallWaking(l.id)
			}
			d.mu.Unlock()
			continue
		}
		if keys := d.park.keys[l.id]; len(keys) > 0 {
			select {
			case d.inputs[l.id] <- bytes.Join(keys, nil):
				// why: the keys reach the agent now; a message waits sendQuiet, then for the draft they leave.
				d.mail.keyAt[l.id] = time.Now()
				d.mail.drafting[l.id] = true
				delete(d.park.keys, l.id)
			default:
				d.mu.Unlock()
				continue // the input queue is full: the next look tries again
			}
		}
		delete(d.park.waking, l.id)
		delete(d.park.stalled, l.id)
		if len(d.mail.queue[l.id]) > 0 {
			d.wake(l.id, 0)
		}
		d.changed()
		d.mu.Unlock()
	}
}

// stallWaking reports waking pane id's agent not at its input box after
// resumeReadyWait, and lets new keys through so the user can answer what
// its screen shows. Callers hold d.mu.
func (d *Daemon) stallWaking(id string) {
	if d.park.stalled == nil {
		d.park.stalled = map[string]bool{}
	}
	d.park.stalled[id] = true
	where := "a pane"
	if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }); i >= 0 {
		if a := d.paneAddr(d.st.Panes[i]); a != "" {
			where = a
		}
	}
	log.Printf("pane %s: the resumed agent is not at its input box after %s; what waits for it stays held", id, resumeReadyWait)
	d.st.Notice = fmt.Sprintf("The agent resumed in %s is not at its input box after %s. What was typed or sent to it waits until it is; answer what its screen shows.", where, resumeReadyWait)
	d.changed()
}

// reparkFailed parks waking pane id again when its resumed agent exits
// before it is back, so nothing waiting for it goes to a shell. Callers
// hold d.mu; when it reports true, they push the pane's frame.
func (d *Daemon) reparkFailed(id string, p Pane, code int) bool {
	if _, ok := d.park.waking[id]; !ok {
		return false
	}
	log.Printf("pane %s: the parked agent exited %d while resuming; parked again", id, code)
	d.parkPane(id, p, fmt.Sprintf("The agent exited with code %d while resuming. Press Enter to try again.", code))
	return true
}

// adoptParking takes over hp's held keys and resume from the daemon that
// handed it off; its agent's readiness counts from now, as after a
// restart. Callers hold d.mu.
func (d *Daemon) adoptParking(hp handoffPane) {
	if len(hp.Held) > 0 {
		if d.park.keys == nil {
			d.park.keys = map[string][][]byte{}
		}
		d.park.keys[hp.ID] = hp.Held
	}
	if hp.Waking {
		if d.park.waking == nil {
			d.park.waking = map[string]time.Time{}
		}
		now := time.Now()
		d.resumed[hp.ID], d.park.waking[hp.ID] = now, now
	}
}

// restoreParked keeps pane p parked across a daemon restart when saved,
// the last run's activities, has it parked and its session still resumes.
// Callers hold d.mu.
func (d *Daemon) restoreParked(p *model.Pane, saved []model.Activity) bool {
	i := slices.IndexFunc(saved, func(a model.Activity) bool { return a.PaneID == p.ID && a.State == model.StateParked })
	if i < 0 || !store.Resumes(*p) {
		return false
	}
	newVT := d.o.NewVT
	if newVT == nil {
		newVT = vt.New
	}
	h := d.parkedHandle(p.ID, d.restoredVT(*p, newVT), saved[i].Detail)
	h.history, p.History = p.History, nil
	d.panes[p.ID] = h
	d.st.Activities = append(d.st.Activities, saved[i])
	log.Printf("pane %s: restored parked %q", p.ID, p.Provider)
	return true
}
