package daemon

import (
	"cmp"
	"context"
	"log"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Screen-based prompt detection for hooked working panes.
//
// Antigravity CLI (agy) has no hooks for permission prompts, questions, folder
// trust or plan approvals; when OSC 7501 is off, missed its startup query, or
// a later work hook clobbered the state back to working, reading agy's screen
// catches the prompt within about two seconds (agyPromptDelay).
//
// For every agent, a 10-second safety net (formFallbackDelay) catches any
// missed hook (Claude, Codex, Gemini, OpenCode, pi, agy) whose screen keeps
// showing a ScreenForm while pitwall still believes the pane is working.
//
// Menus the user opened themselves while typing in the TUI (such as /model or
// /settings) are excluded: when a form first appears within userMenuWindow of
// real TUI keyboard input sent after the working stretch started, it is
// latched as a user-opened menu until it closes.

var (
	// agyPromptDelay is how long an Antigravity prompt must stay on screen
	// while pitwall says working before pitwall promotes it without waiting
	// for OSC 7501. With livePoll = 1s, 1s (two polls) detects within ~1-2s
	// without racing an OSC 7501 report arriving alongside the redraw.
	agyPromptDelay = newTiming(time.Second)

	// formFallbackDelay is how long any agent's screen may show a ScreenForm
	// while pitwall says working before the safety net promotes it, logs why,
	// and notifies.
	formFallbackDelay = newTiming(10 * time.Second)

	// userMenuWindow is how soon after a real TUI keystroke a form must
	// appear to count as a menu the user opened themselves in the TUI.
	userMenuWindow = newTiming(3 * time.Second)
)

// screenWait tracks a form observed on a working pane's screen across polls.
// Guarded by d.mu.
type screenWait struct {
	firstSeen  time.Time
	userMenu   bool
	promoted   bool
	promotedAt time.Time
	rule       string
	state      model.AgentState
	detail     string
	stillCells []vt.Cell
	stillCount int
}

// noteWorkState updates workStart whenever pane id enters or leaves
// StateWorking. Callers hold d.mu.
func (d *Daemon) noteWorkState(id string, prev, next model.AgentState, now time.Time) {
	if next == model.StateWorking {
		if prev != model.StateWorking {
			if d.live.workStart == nil {
				d.live.workStart = map[string]time.Time{}
			}
			d.live.workStart[id] = now
		}
		return
	}
	delete(d.live.workStart, id)
}

// checkWaiting inspects working agent panes (and panes it previously promoted
// to waiting) for prompts and forms on their screens.
func (d *Daemon) checkWaiting(ctx context.Context) {
	type check struct {
		id       string
		p        Pane
		prov     model.Provider
		hookSeq  uint64
		promoted bool
		state    model.AgentState
	}
	var checks []check
	keep := map[string]bool{}
	d.mu.Lock()
	for _, sp := range d.st.Panes {
		p := d.panes[sp.ID]
		if p == nil || sp.Exited {
			continue
		}
		ai := d.activityIndex(sp.ID)
		if ai < 0 {
			continue
		}
		a := d.st.Activities[ai]
		prov := a.Provider
		if prov == "" {
			prov = sp.Provider
		}
		if !model.IsAgent(prov) || prov == model.ProviderTerminal || model.ReportedName(prov) != "" || model.ReportedName(sp.Provider) != "" {
			continue
		}
		sw := d.live.screenWait[sp.ID]
		owned := sw != nil && sw.promoted && waits(a.State) && a.UpdatedAt.Equal(sw.promotedAt)
		if a.State != model.StateWorking && !owned {
			continue
		}
		keep[sp.ID] = true
		checks = append(checks, check{
			id:       sp.ID,
			p:        p,
			prov:     prov,
			hookSeq:  d.live.hookSeq[sp.ID],
			promoted: owned,
			state:    a.State,
		})
	}
	for id := range d.live.screenWait {
		if !keep[id] {
			delete(d.live.screenWait, id)
		}
	}
	d.mu.Unlock()

	for _, c := range checks {
		g := c.p.Snapshot()
		scr := agent.ReadScreenOf(c.prov, g)
		trace := agent.ExplainScreen(c.prov, g)
		screenState := trace.State
		detail := agent.ScreenDetail(c.prov, screenState, g)
		now := time.Now()

		d.mu.Lock()
		if d.closing || d.frozen || d.panes[c.id] != c.p || d.live.hookSeq[c.id] != c.hookSeq {
			d.mu.Unlock()
			continue
		}
		ai := d.activityIndex(c.id)
		if ai < 0 {
			delete(d.live.screenWait, c.id)
			d.mu.Unlock()
			continue
		}
		a := d.st.Activities[ai]
		sw := d.live.screenWait[c.id]

		if sw != nil && sw.promoted && waits(a.State) && a.UpdatedAt.Equal(sw.promotedAt) {
			d.updatePromotedWait(ctx, c.id, c.prov, sw, scr, screenState, detail, trace, g)
			d.mu.Unlock()
			continue
		}
		if a.State != model.StateWorking {
			delete(d.live.screenWait, c.id)
			d.mu.Unlock()
			continue
		}
		if scr != agent.ScreenForm {
			delete(d.live.screenWait, c.id)
			d.mu.Unlock()
			continue
		}
		if d.live.screenWait == nil {
			d.live.screenWait = map[string]*screenWait{}
		}
		if sw == nil || sw.firstSeen.IsZero() {
			tuiKey := d.live.tuiKeyAt[c.id]
			workStart := d.live.workStart[c.id]
			userOpened := !tuiKey.IsZero() && tuiKey.After(workStart) && now.Sub(tuiKey) <= userMenuWindow.get()
			sw = &screenWait{firstSeen: now, userMenu: userOpened}
			d.live.screenWait[c.id] = sw
		}

		// Fast path for Antigravity: real agy prompts (approval, question,
		// folder trust, plan) promote within agyPromptDelay (~1-2s). User TUI
		// menus (/model, /settings) have screenState == "" and never enter
		// this branch.
		if c.prov == model.ProviderAntigravity && (screenState == model.StatePendingApproval ||
			screenState == model.StateAwaitingInput ||
			(screenState == model.StatePlanReady && !sw.userMenu)) {
			if now.Sub(sw.firstSeen) >= agyPromptDelay.get() {
				d.promoteScreenWait(ctx, c.id, c.prov, sw, screenState, detail, "agy-screen", trace)
			}
			d.mu.Unlock()
			continue
		}

		// Safety net for every agent: a ScreenForm still on screen for
		// formFallbackDelay (~10s) while state is working promotes to waiting,
		// unless the user opened the menu themselves in the TUI.
		if sw.userMenu {
			d.mu.Unlock()
			continue
		}
		if tuiKey := d.live.tuiKeyAt[c.id]; tuiKey.After(sw.firstSeen) {
			// The user is actively pressing keys in the TUI right now.
			sw.firstSeen = tuiKey
			d.mu.Unlock()
			continue
		}
		if now.Sub(sw.firstSeen) >= formFallbackDelay.get() {
			target := screenState
			if !waits(target) {
				target = model.StateAwaitingInput
			}
			fallbackDetail := detail
			if fallbackDetail == "" {
				fallbackDetail = agent.ScreenDetail(c.prov, target, g)
			}
			log.Printf("pane %s (%s): screen showed a form (%s) for %s while state was working; treating as %s",
				c.id, c.prov, cmp.Or(trace.Rule, "screen-form"), now.Sub(sw.firstSeen).Round(time.Second), target)
			d.promoteScreenWait(ctx, c.id, c.prov, sw, target, fallbackDetail, "screen-form-fallback", trace)
		}
		d.mu.Unlock()
	}
}

// promoteScreenWait moves pane id into waiting state target from screen
// detection and records ownership on sw so checkWaiting can clear it when the
// prompt leaves. Callers hold d.mu.
func (d *Daemon) promoteScreenWait(ctx context.Context, id string, prov model.Provider, sw *screenWait, target model.AgentState, detail, rule string, trace agent.ScreenTrace) {
	d.setActivity(ctx, id, prov, target, detail)
	d.recordExplanation(id, prov, target, rule, trace)
	if i := d.activityIndex(id); i >= 0 {
		sw.promoted = true
		sw.promotedAt = d.st.Activities[i].UpdatedAt
		sw.rule = rule
		sw.state = target
		sw.detail = detail
		sw.stillCells = nil
		sw.stillCount = 0
	}
}

// updatePromotedWait keeps a screen-promoted waiting state in sync with the
// screen: updates if a different prompt replaced it, resumes working when the
// prompt goes away and work continues, or clears to idle if the screen settles
// with no form and no movement. Callers hold d.mu.
func (d *Daemon) updatePromotedWait(ctx context.Context, id string, prov model.Provider, sw *screenWait, scr agent.Screen, screenState model.AgentState, detail string, trace agent.ScreenTrace, g vt.Grid) {
	stillWaiting := false
	nextState := sw.state
	nextDetail := sw.detail
	switch {
	case sw.rule == "agy-screen":
		if waits(screenState) {
			stillWaiting = true
			nextState, nextDetail = screenState, detail
		}
	case scr == agent.ScreenForm:
		stillWaiting = true
		if waits(screenState) {
			nextState = screenState
			if detail != "" {
				nextDetail = detail
			}
		}
	}
	if stillWaiting {
		sw.stillCells, sw.stillCount = nil, 0
		if nextState != sw.state || nextDetail != sw.detail {
			d.promoteScreenWait(ctx, id, prov, sw, nextState, nextDetail, sw.rule, trace)
		}
		return
	}
	if scr == agent.ScreenBusy || screenState == model.StateWorking {
		delete(d.live.screenWait, id)
		d.setActivity(ctx, id, prov, model.StateWorking, "")
		d.recordExplanation(id, prov, model.StateWorking, "screen-resumed", trace)
		return
	}
	// ScreenNone: wait one poll to distinguish a streaming reply (moving) from
	// a cancelled/dismissed prompt back at an idle input box (still).
	if sw.stillCells == nil {
		sw.stillCells = g.Cells
		sw.stillCount = 0
		return
	}
	if !slices.Equal(sw.stillCells, g.Cells) {
		delete(d.live.screenWait, id)
		d.setActivity(ctx, id, prov, model.StateWorking, "")
		d.recordExplanation(id, prov, model.StateWorking, "screen-resumed", trace)
		return
	}
	delete(d.live.screenWait, id)
	d.dropActivity(id)
	d.recordExplanation(id, prov, "", "screen-prompt-cleared", trace)
}
