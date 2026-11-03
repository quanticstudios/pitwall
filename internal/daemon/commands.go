package daemon

import (
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func (d *Daemon) commands(q proto.Commands) proto.CommandResult {
	d.mu.Lock()
	p := d.panes[q.Pane]
	d.mu.Unlock()
	r := proto.CommandResult{Pane: q.Pane, ID: q.ID}
	if p, ok := p.(interface {
		Commands() ([]vt.CommandBlock, bool)
	}); ok {
		r.Blocks, r.Integrated = p.Commands()
	}
	// Stay below the wire frame limit even when a shell prints large rows.
	size, first := 0, len(r.Blocks)
	for first > 0 {
		b := r.Blocks[first-1]
		n := len(b.Command) + len(b.Output) + 256
		if size+n > 16<<20 {
			break
		}
		size += n
		first--
	}
	r.More = first > 0
	r.Blocks = r.Blocks[first:]
	return r
}
