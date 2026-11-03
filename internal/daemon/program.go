package daemon

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
)

// The Program Status Protocol (OSC 7501) in the daemon. A program in a
// pane reports what it does through the pane's emulator, or through
// pitwall status, which sends the same report as a report-agent hook
// (programPayload). Each pane keeps its records in a progstatus.Store;
// Pane.Program is the most urgent one, and attended shows it as the pane's
// activity when no agent owns the pane.
//
// Records end the way tuios ends them, without OSC 133:
//   - working, blocked and idle ones when the process group that reported
//     them has no process left (dropEndedPrograms, each detect poll), or
//     the pane's process exits;
//   - done and error ones when the user types into the pane.
//
// A report from the shell's own group, one typed at the prompt or from a
// program that exited before the daemon read the foreground, has no group
// and stays until it is replaced or cleared, or the pane exits.

// paneProgram is one pane's records and the number of the last report
// applied, so reports applied out of order cannot undo a newer one.
type paneProgram struct {
	store progstatus.Store
	seq   uint64
}

// programPayload is what pitwall status sends as a report-agent hook: the
// body of an OSC 7501 report and the CLI's process group. Name stays empty
// so a daemon without program status refuses it as an agent report.
type programPayload struct {
	Name    string `json:"name"`
	Program string `json:"program"`
	Group   int    `json:"group,omitempty"`
}

// programHook reads a report-agent payload as pitwall status's, and
// reports whether it is one.
func programHook(payload []byte) (programPayload, bool) {
	var p programPayload
	if json.Unmarshal(payload, &p) != nil || p.Program == "" || p.Name != "" {
		return p, false
	}
	return p, true
}

// programFromHook applies pitwall status's report to pane id.
func (d *Daemon) programFromHook(id string, p programPayload) {
	body := []byte(p.Program)
	r, ok := progstatus.Parse(body, progstatus.SequenceLen(len("7501;")+len(body), false))
	if !ok {
		return
	}
	d.programReport(id, r, d.progSeq.Add(1), p.Group)
}

// programFromVT applies a report the emulator of pane id parsed, numbered
// seq in the order it came. The group is the one in the foreground now,
// unless that is the pane's own process.
func (d *Daemon) programFromVT(id string, r progstatus.Report, seq uint64) {
	d.mu.Lock()
	p := d.panes[id]
	d.mu.Unlock()
	group := 0
	if f, ok := p.(foregrounder); ok {
		if fg := f.Foreground(); fg > 0 && sessionOf(fg) != fg {
			group = fg
		}
	}
	d.programReport(id, r, seq, group)
}

// programReport stores r in pane id's records.
func (d *Daemon) programReport(id string, r progstatus.Report, seq uint64, group int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing || d.panes[id] == nil {
		return
	}
	if d.prog == nil {
		d.prog = map[string]*paneProgram{}
	}
	pp := d.prog[id]
	if pp == nil {
		pp = &paneProgram{}
		d.prog[id] = pp
	}
	if seq <= pp.seq {
		return
	}
	pp.seq = seq
	if pp.store.ApplyFrom(r, time.Now().UnixNano(), group) {
		d.syncProgram(id)
	}
}

// syncProgram sets pane id's Program from its records. Callers hold d.mu.
func (d *Daemon) syncProgram(id string) {
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if i < 0 {
		return
	}
	var next *model.Program
	if pp := d.prog[id]; pp != nil {
		if r, ok := progstatus.Summary(pp.store.Records()); ok && r.State != progstatus.Idle {
			msg := r.Msg
			if r.Title != "" && msg != "" {
				msg = r.Title + ": " + msg
			} else if msg == "" {
				msg = r.Title
			}
			next = &model.Program{App: r.EffectiveApp, State: string(r.State), Kind: string(r.Kind), Progress: r.Progress,
				Message: decide.Redact(msg, d.dec.cur.Secrets...), UpdatedAt: time.Unix(0, r.At)}
		}
	}
	p := &d.st.Panes[i]
	if p.Program == nil && next == nil || p.Program != nil && next != nil && *p.Program == *next {
		return
	}
	p.Program = next
	if w := d.workspace(p.WorkspaceID); w != nil && next != nil {
		w.UpdatedAt = next.UpdatedAt
	}
	d.changed()
}

// dropPrograms ends pane id's records that drop picks. Callers hold d.mu.
func (d *Daemon) dropPrograms(id string, drop func(*progstatus.Store) bool) {
	if pp := d.prog[id]; pp != nil && drop(&pp.store) {
		d.syncProgram(id)
	}
}

// dropEndedPrograms ends the working, blocked and idle records whose
// process group has no process left: the program that reported them
// exited. Callers hold d.mu.
func (d *Daemon) dropEndedPrograms() {
	for id := range d.prog {
		d.dropPrograms(id, func(s *progstatus.Store) bool {
			return s.DropEnded(func(g int) bool { return !groupAlive(g) })
		})
	}
}

// programInput ends pane id's done and error records: the user typed into
// the pane. Callers hold d.mu.
func (d *Daemon) programInput(id string) {
	d.dropPrograms(id, func(s *progstatus.Store) bool { return s.HasFinished() && s.DropFinished() })
}

// focusReport reports whether b is only focus in and out reports, which
// the window sends, not the user.
func focusReport(b []byte) bool {
	for len(b) > 0 {
		if len(b) < 3 || b[0] != 0x1b || b[1] != '[' || b[2] != 'I' && b[2] != 'O' {
			return false
		}
		b = b[3:]
	}
	return true
}

// programExited ends pane id's working, blocked and idle records: its
// process exited. Callers hold d.mu.
func (d *Daemon) programExited(id string) {
	d.dropPrograms(id, (*progstatus.Store).DropTransient)
}

// programActivity is pane p's program as its activity, for a pane no agent
// owns: a has the pane's own activity, nil for none. An agent's activity,
// or an agent at its prompt, wins; the row shows the program beside it.
func programActivity(p model.Pane, a *model.Activity) (model.Activity, bool) {
	if p.Program == nil || a != nil && a.Provider != model.ProviderTerminal || model.IsAgent(p.Provider) {
		return model.Activity{}, false
	}
	return model.Activity{PaneID: p.ID, WorkspaceID: p.WorkspaceID, Provider: model.ProviderTerminal,
		State: p.Program.Activity(), Detail: p.Program.Message, UpdatedAt: p.Program.UpdatedAt}, true
}
