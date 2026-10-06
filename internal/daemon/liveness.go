package daemon

import (
	"context"
	"encoding/json"
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
	// started marks a pane whose agent sends SessionStart (pitwall's hooks
	// since alpha.14), fresh one whose agent process started and has not
	// reported a permission mode yet, and resumeSeen a pane NewWith resumed
	// whose agent has sent its resume SessionStart; see Daemon.agentMode.
	started, fresh, resumeSeen map[string]bool
	det                        map[string]*detected // pane: what detect saw at its last poll
	// piRuntime is, per pane, the extension nonce of pi's latest
	// session_start (see agent.PiRuntime).
	piRuntime map[string]string
	// piRetired is, per pane, the nonces of pi runtimes that sent their
	// session_shutdown; their later reports are dropped.
	piRetired map[string]map[string]bool
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
		clearDecisions(a)
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
		d.refreshDecisions()
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
	for id := range d.live.piRuntime {
		if d.panes[id] == nil {
			delete(d.live.piRuntime, id)
		}
	}
	for id := range d.live.piRetired {
		if d.panes[id] == nil {
			delete(d.live.piRetired, id)
		}
	}
	// Hooks own a pane only while their agent's group is in the foreground.
	// Once it leaves, an agent started later without hooks is detect's.
	for id, want := range d.live.fg {
		select {
		case <-d.panes[id].Done():
			continue // why: the process ended; exited decides what its activity does
		default:
		}
		if f, ok := d.panes[id].(foregrounder); ok && f.Foreground() != want {
			delete(d.live.fg, id)
			delete(d.live.hookAt, id)
			d.dropActivity(id)
			d.setProvider(id, "") // detect names the next agent, if one took over
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

// agentMode tracks which agent process a hook comes from, for the
// permission mode a resume starts with, and reports whether p.AgentMode
// must stay as it is. An agent that reported bypassPermissions was started
// with bypass allowed (cdang); it keeps bypass across shift+tab, /clear and
// compaction for as long as it runs, so a resume starts it the same way.
// SessionStart tells processes apart: "startup" or "resume" is a new
// process, which counts only once it reports its own mode, and "startup"
// is a new session that never ran with bypass. A resumed session keeps its
// saved mode only in the process pitwall started for it with that mode
// (store.RestoreCmd); any other resume, typed by the user or an in-process
// /resume, starts with no mode until it reports one. Without SessionStart
// hooks (older installs, Codex) the mode follows every report. Callers hold
// d.mu.
func (d *Daemon) agentMode(p *model.Pane, payload []byte) (keep bool) {
	if d.live.started == nil {
		d.live.started, d.live.fresh, d.live.resumeSeen = map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	if started, resumed := agent.NewProcess(payload); started || resumed {
		d.live.started[p.ID], d.live.fresh[p.ID] = true, true
		// Only pitwall's own relaunch, once, and only if the saved mode went
		// into its command: a saved command's own --permission-mode wins
		// over AgentMode in RestoreCmd.
		_, own := d.resumed[p.ID]
		own = own && resumed && !d.live.resumeSeen[p.ID] && d.modeInRestore(*p)
		if resumed {
			d.live.resumeSeen[p.ID] = true
		}
		switch {
		case agent.PermissionMode(payload) != "":
			return false // the process reports its own mode in this hook
		case own:
			return true
		}
		p.AgentMode = "" // a new process has no mode until it reports one
		return false
	}
	if agent.PiEvent(payload) == "" && sessionEnd(payload) {
		// The process is gone: the next one starts over, and pitwall's own
		// relaunch, if this was it, is used up.
		delete(d.live.started, p.ID)
		d.live.resumeSeen[p.ID] = true
		return false
	}
	// Only Claude sends SessionStart; another agent in the pane reports its
	// own mode.
	return p.Provider == model.ProviderClaude && d.live.started[p.ID] && !d.live.fresh[p.ID] && p.AgentMode == "bypassPermissions"
}

// modeInRestore reports whether p's resume command carries p.AgentMode:
// it differs from the one RestoreCmd builds without a mode.
func (d *Daemon) modeInRestore(p model.Pane) bool {
	bare := p
	bare.AgentMode = ""
	return !slices.Equal(d.o.RestoreCmd(p), d.o.RestoreCmd(bare))
}

// sessionEnd reports whether a hook is Claude's or Codex's SessionEnd.
func sessionEnd(payload []byte) bool {
	var p struct {
		Event string `json:"hook_event_name"`
	}
	return json.Unmarshal(payload, &p) == nil && p.Event == "SessionEnd"
}
