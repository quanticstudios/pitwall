package daemon

import (
	"context"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Liveness timings; tests shorten them.
var (
	// livePoll is how often every pane gets its foreground process group
	// read, for the exit check here and for detect.
	livePoll = time.Second
	// settleDelay is how long after a key the screen is judged, and stillFor
	// how long before that the first of the two compared snapshots is taken.
	settleDelay = 1500 * time.Millisecond
	stillFor    = 500 * time.Millisecond
)

// liveness is what the daemon keeps to catch an activity no hook will end:
// a Claude turn the user interrupted and an agent that died. Guarded by d.mu.
type liveness struct {
	hookAt map[string]time.Time // pane: when its agent last sent a hook
	fg     map[string]int       // pane: its foreground process group at that hook
	det    map[string]*detected // pane: what detect saw at its last poll
}

// foregrounder is the optional Pane method behind the exit check;
// *pane.Pane has it.
type foregrounder interface{ Foreground() int }

// sawHook records a hook event from a pane's agent. The terminal's
// foreground group at that moment is the agent's job: Claude Code was
// measured keeping it through its own tool runs. Callers hold d.mu.
func (d *Daemon) sawHook(id string) {
	if d.live.hookAt == nil {
		d.live.hookAt, d.live.fg = map[string]time.Time{}, map[string]int{}
	}
	d.live.hookAt[id] = time.Now()
	if f, ok := d.panes[id].(foregrounder); ok {
		if pg := f.Foreground(); pg > 0 {
			d.live.fg[id] = pg
		}
	}
}

// noteInput starts a settle check when a key may have ended the turn without
// a hook: Esc or Ctrl+C while working, or any key while the agent waits on a
// form (approve, deny, answer). Claude Code fires no hook for an interrupt or
// a denied permission.
func (d *Daemon) noteInput(id string, b []byte) {
	d.mu.Lock()
	i := d.activityIndex(id)
	var s model.AgentState
	if i >= 0 {
		s = d.st.Activities[i].State
	}
	d.mu.Unlock()
	switch {
	case s == model.StateWorking && isInterrupt(b):
	case s == model.StatePendingApproval, s == model.StateAwaitingInput, s == model.StatePlanReady:
	default:
		return
	}
	go d.settle(id, time.Now())
}

// settle judges the screen settleDelay after a key at `at`, unless a hook
// arrived since, which always wins. A form still on screen keeps the state. A
// spinner, or a screen that changed in the last stillFor, means a turn runs:
// the state becomes working (an approved tool can run for minutes before its
// PostToolUse). A still screen with neither means the turn ended, and the
// activity goes, which is the agent idle at its prompt.
func (d *Daemon) settle(id string, at time.Time) {
	p, err := d.pane(id)
	if err != nil {
		return
	}
	time.Sleep(settleDelay - stillFor)
	before := p.Snapshot()
	time.Sleep(stillFor)
	g := p.Snapshot()

	d.mu.Lock()
	defer d.mu.Unlock()
	i := d.activityIndex(id)
	if d.closing || d.panes[id] != p || d.live.hookAt[id].After(at) || i < 0 {
		return
	}
	switch agent.ReadScreen(g) {
	case agent.ScreenForm:
		return
	case agent.ScreenNone:
		if slices.Equal(before.Cells, g.Cells) {
			d.dropActivity(id)
			return
		}
	}
	if a := &d.st.Activities[i]; a.State != model.StateWorking {
		a.State, a.Detail, a.UpdatedAt = model.StateWorking, "", time.Now()
		d.changed()
	}
}

// isInterrupt reports whether input is a bare Esc or Ctrl+C press, in legacy
// or kitty keyboard encoding (Claude Code and Codex push kitty flags).
func isInterrupt(b []byte) bool {
	switch string(b) {
	case "\x1b", "\x03", "\x1b[27u", "\x1b[27;1u", "\x1b[27;1:1u", "\x1b[99;5u", "\x1b[99;5:1u":
		return true
	}
	return false
}

// livenessLoop drops the activity of a pane whose foreground group is no
// longer the one its agent's hooks came from: the agent exited to the shell,
// crashed, was killed or was suspended. SessionEnd covers only clean exits.
func (d *Daemon) livenessLoop(ctx context.Context) {
	t := time.NewTicker(livePoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.checkForeground()
		d.detect(ctx)
	}
}

func (d *Daemon) checkForeground() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id := range d.live.hookAt {
		if d.panes[id] == nil {
			delete(d.live.fg, id)
			delete(d.live.hookAt, id)
		}
	}
	for id := range d.live.det {
		if d.panes[id] == nil {
			delete(d.live.det, id)
		}
	}
	for _, a := range slices.Clone(d.st.Activities) {
		f, ok := d.panes[a.PaneID].(foregrounder)
		if want := d.live.fg[a.PaneID]; ok && want > 0 && f.Foreground() != want {
			d.dropActivity(a.PaneID)
		}
	}
}

// activityIndex is the index of a pane's activity, or -1. Callers hold d.mu.
func (d *Daemon) activityIndex(id string) int {
	return slices.IndexFunc(d.st.Activities, func(a model.Activity) bool { return a.PaneID == id })
}

// dropActivity removes a pane's activity, if any. Callers hold d.mu.
func (d *Daemon) dropActivity(id string) {
	if i := d.activityIndex(id); i >= 0 {
		d.st.Activities = slices.Delete(d.st.Activities, i, i+1)
		d.changed()
	}
}
