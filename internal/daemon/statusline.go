package daemon

import (
	"cmp"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
)

// statuslineStale is how long a Claude pane's hooks may go on after its
// last statusline report before the report stops counting. Claude Code runs
// the statusline after every reply, so a pane that works this long with no
// report no longer has the statusline installed. An idle pane's report
// stays good however old it is: its model and context do not change.
const statuslineStale = 15 * time.Minute

// statuslineRec is the last statusline report of a pane, and when it came.
type statuslineRec struct {
	agent.StatuslineState
	at time.Time
}

// statuslineFresh reports whether rec still describes a pane whose agent is
// in session and last sent a hook at hookAt (zero for none): false for
// another session, or after statuslineStale of hooks with no report.
func statuslineFresh(rec statuslineRec, session string, hookAt time.Time) bool {
	if rec.SessionID != "" && session != "" && rec.SessionID != session {
		return false
	}
	return hookAt.Sub(rec.at) <= statuslineStale
}

// keepStatusline records a pane's statusline report. Callers hold d.mu.
func (d *Daemon) keepStatusline(id string, st agent.StatuslineState) {
	prev, had := d.statuslines[id]
	if d.statuslines == nil {
		d.statuslines = map[string]statuslineRec{}
	}
	d.statuslines[id] = statuslineRec{st, time.Now()}
	if !had || prev.StatuslineState != st {
		d.changed()
	}
}

// liveStatusline is pane id's statusline report while it is good: a Claude
// pane, in the session the report names. Callers hold d.mu.
func (d *Daemon) liveStatusline(id string) (statuslineRec, bool) {
	rec, ok := d.statuslines[id]
	if !ok || d.paneProvider(id) != model.ProviderClaude {
		return statuslineRec{}, false
	}
	var session string
	for _, p := range d.st.Panes {
		if p.ID == id {
			session = p.SessionID
		}
	}
	return rec, statuslineFresh(rec, session, d.live.hookAt[id])
}

// settingsOf is the model and effort of pane id: what its screen showed
// (keepClaudeSettings), replaced by what its statusline says. Callers hold d.mu.
func (d *Daemon) settingsOf(id string) phoneSettings {
	s := d.phoneSettings[id]
	if rec, ok := d.liveStatusline(id); ok {
		s.model = cmp.Or(rec.ModelName, rec.Model, s.model)
		s.effort = cmp.Or(rec.Effort, s.effort)
	}
	return s
}
