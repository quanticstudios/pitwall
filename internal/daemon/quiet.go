package daemon

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Quiet agents show "quiet 20m" after [agents] quiet_after without screen
// changes or work hooks. A still Claude or Codex input box ends a lost Stop;
// other quiet panes can receive continue with [agents] continue_when_quiet.

// quietPane is what checkQuiet keeps per working pane. Guarded by d.mu.
type quietPane struct {
	p      Pane
	cells  []vt.Cell // the screen at the last poll
	grid   vt.Grid   // cached screen when WriteSeq is unchanged
	seq    uint64
	hasSeq bool
	since  time.Time // when the screen last changed
	shown  bool      // still for quiet_after: clients get QuietSince
	// nudged is when continue was typed, zero for not yet. It holds until
	// the agent stops working or the user types in the pane, so one
	// stretch of work gets one continue, its echo and any turn it starts
	// included.
	nudged time.Time
}

// nudgeable are the agents whose prompts and forms pitwall reads from the
// screen (agent.StateOf), so continue never lands in one.
var nudgeable = []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderPi, model.ProviderGemini, model.ProviderOpenCode}

// quietAfter reads [agents] quiet_after; 0 is off.
func (d *Daemon) quietAfter() time.Duration {
	if d.o.QuietAfter == nil {
		return 0
	}
	return d.o.QuietAfter()
}

// checkQuiet compares each working pane's screen with the last poll's and
// marks the panes still for quiet_after, or completes a hooked turn at an
// input box unless its Stop reported background work.
func (d *Daemon) checkQuiet() {
	after := d.quietAfter()
	nudge := d.o.ContinueWhenQuiet != nil && d.o.ContinueWhenQuiet()
	type look struct {
		id     string
		p      Pane
		prov   model.Provider
		seq    uint64
		cached bool
		grid   vt.Grid
	}
	var looks []look
	d.mu.Lock()
	for _, a := range d.st.Activities {
		if p := d.panes[a.PaneID]; p != nil && after > 0 && a.State == model.StateWorking {
			l := look{id: a.PaneID, p: p, prov: a.Provider, seq: d.live.hookSeq[a.PaneID]}
			if wseq, ok := paneWriteSeq(p); ok {
				if q := d.quiet[a.PaneID]; q != nil && q.p == p && q.hasSeq && q.seq == wseq {
					l.cached, l.grid = true, q.grid
				}
			}
			looks = append(looks, l)
		}
	}
	for id, q := range d.quiet {
		if !slices.ContainsFunc(looks, func(l look) bool { return l.id == id }) {
			delete(d.quiet, id)
			if q.shown {
				d.changed()
			}
		}
	}
	d.mu.Unlock()
	now := time.Now()
	for _, l := range looks {
		var g vt.Grid
		var wseq uint64
		var hasSeq bool
		if l.cached {
			g = l.grid
		} else {
			wseq, hasSeq = paneWriteSeq(l.p)
			g = l.p.Snapshot()
		}
		d.mu.Lock()
		i := d.activityIndex(l.id)
		if d.closing || d.frozen || d.panes[l.id] != l.p || d.live.hookSeq[l.id] != l.seq || i < 0 || d.st.Activities[i].State != model.StateWorking || d.st.Activities[i].Provider != l.prov {
			d.mu.Unlock()
			continue
		}
		if d.quiet == nil {
			d.quiet = map[string]*quietPane{}
		}
		q := d.quiet[l.id]
		if q == nil || q.p != l.p {
			q = &quietPane{p: l.p}
			d.quiet[l.id] = q
		}
		if !l.cached {
			if q.cells == nil || !slices.Equal(q.cells, g.Cells) {
				q.cells, q.since = g.Cells, now
			}
			q.grid, q.seq, q.hasSeq = g, wseq, hasSeq
		}
		if now.Sub(q.since) >= after && !d.live.hookAt[l.id].IsZero() && !d.live.background[l.id] && agent.InputReady(l.prov, g) {
			d.setActivity(context.Background(), l.id, l.prov, model.StateCompleted, "")
			d.recordExplanation(l.id, l.prov, model.StateCompleted, "quiet-input", agent.ExplainScreen(l.prov, g))
			delete(d.quiet, l.id)
			d.mu.Unlock()
			continue
		}
		if !q.nudged.IsZero() && d.mail.keyAt[l.id].After(q.nudged) {
			q.nudged = time.Time{}
		}
		if shown := now.Sub(q.since) >= after; shown != q.shown {
			q.shown = shown
			d.changed()
		}
		fire := nudge && q.shown && q.nudged.IsZero() && !d.live.background[l.id] && quietPrompt(l.prov, g) && !d.drafted(l.id, l.prov, g)
		if fire {
			q.nudged = now
		}
		d.mu.Unlock()
		if fire {
			go d.continueQuiet(l.id, l.p, l.prov)
		}
	}
}

// quietPrompt reports whether continue may be typed on screen g of agent
// prov: pitwall reads that agent's forms, and the screen shows none, nor
// a turn running.
func quietPrompt(prov model.Provider, g vt.Grid) bool {
	return slices.Contains(nudgeable, prov) && agent.StateOf(prov, g) == "" && agent.ReadScreenOf(prov, g) == agent.ScreenNone
}

// continueQuiet types continue into pane id once its screen, read again
// under sendMu, still allows it and the agent still works.
func (d *Daemon) continueQuiet(id string, p Pane, prov model.Provider) {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	g := p.Snapshot()
	d.mu.Lock()
	i := d.activityIndex(id)
	q := d.quiet[id]
	ok := !d.closing && d.panes[id] == p && i >= 0 && d.st.Activities[i].State == model.StateWorking && !d.live.background[id] && q != nil && slices.Equal(q.cells, g.Cells) && !d.drafted(id, prov, g)
	d.mu.Unlock()
	if !ok || !quietPrompt(prov, g) {
		return
	}
	if err := d.typeLine(id, p, "continue", "nothing to type"); err != nil {
		log.Printf("pane %s: continue after a quiet stretch: %q", id, err)
		return
	}
	log.Printf("pane %s: typed continue after a quiet stretch", id)
}
