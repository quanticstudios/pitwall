package daemon

// repeatAfter is how many failed calls in a row, with one errlog.Signature,
// flag a working pane as repeating an error.
const repeatAfter = 5

// repeatRun is a pane's latest failed calls in a row that share one
// signature. Guarded by d.mu.
type repeatRun struct {
	sig string
	n   int
}

// noteFailure counts a failed call of pane with signature sig: one more in
// the run when sig matches, else a new run. Callers hold d.mu.
func (d *Daemon) noteFailure(pane, sig string) {
	r := d.repeats[pane]
	shown := r.n >= repeatAfter
	if r.sig != sig {
		r = repeatRun{sig: sig}
	}
	r.n++
	if d.repeats == nil {
		d.repeats = map[string]repeatRun{}
	}
	d.repeats[pane] = r
	if shown || r.n >= repeatAfter {
		d.changed() // the count on the row moves
	}
}

// dropRepeat ends pane's run: a call succeeded, the turn ended or the user
// typed. Callers hold d.mu.
func (d *Daemon) dropRepeat(pane string) {
	if d.repeats[pane].n >= repeatAfter {
		d.changed()
	}
	delete(d.repeats, pane)
}
