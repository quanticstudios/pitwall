package daemon

import (
	"log"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Quiet agents: a working agent whose screen has not changed for [agents]
// quiet_after shows "quiet 20m" on its row, as a failed turn that sends no
// hook stays working forever. With [agents] continue_when_quiet the daemon
// types continue into it once.

// quietPane is what checkQuiet keeps per working pane. Guarded by d.mu.
type quietPane struct {
	p     Pane
	cells []vt.Cell // the screen at the last poll
	since time.Time // when the screen last changed
	shown bool      // still for quiet_after: clients get QuietSince
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
// marks the panes still for quiet_after.
func (d *Daemon) checkQuiet() {
	after := d.quietAfter()
	nudge := d.o.ContinueWhenQuiet != nil && d.o.ContinueWhenQuiet()
	type look struct {
		id   string
		p    Pane
		prov model.Provider
	}
	var looks []look
	d.mu.Lock()
	for _, a := range d.st.Activities {
		if p := d.panes[a.PaneID]; p != nil && after > 0 && a.State == model.StateWorking {
			looks = append(looks, look{a.PaneID, p, a.Provider})
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
		g := l.p.Snapshot()
		d.mu.Lock()
		if d.quiet == nil {
			d.quiet = map[string]*quietPane{}
		}
		q := d.quiet[l.id]
		if q == nil || q.p != l.p {
			q = &quietPane{p: l.p}
			d.quiet[l.id] = q
		}
		if q.cells == nil || !slices.Equal(q.cells, g.Cells) {
			q.cells, q.since = g.Cells, now
		}
		if !q.nudged.IsZero() && d.mail.keyAt[l.id].After(q.nudged) {
			q.nudged = time.Time{}
		}
		if shown := now.Sub(q.since) >= after; shown != q.shown {
			q.shown = shown
			d.changed()
		}
		fire := nudge && q.shown && q.nudged.IsZero() && quietPrompt(l.prov, g)
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
	return slices.Contains(nudgeable, prov) && agent.StateOf(prov, g) == "" && agent.ReadScreen(g) == agent.ScreenNone
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
	ok := !d.closing && d.panes[id] == p && i >= 0 && d.st.Activities[i].State == model.StateWorking && q != nil && slices.Equal(q.cells, g.Cells)
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
