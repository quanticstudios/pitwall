package daemon

import "github.com/quanticstudios/pitwall/internal/errlog"

// logError keeps the tab identity even after the tab closes. Callers hold d.mu.
func (d *Daemon) logError(tab string, e errlog.Event, secrets ...string) {
	e.Tab = tab
	if w := d.workspace(tab); w != nil {
		e.Name, e.Branch = w.Name, w.Branch
	}
	d.o.Errors.Add(e, secrets...)
}
