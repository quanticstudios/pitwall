package daemon

import (
	"fmt"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// maxMatches caps a SearchResult: one letter can match most of a 10,000-line
// history.
const maxMatches = 10000

// search answers m with its pane's newest maxMatches matches. A Pane
// without Search, like a test fake, has none.
func (d *Daemon) search(m proto.Search) any {
	d.mu.Lock()
	p := d.panes[m.Pane]
	d.mu.Unlock()
	if p == nil {
		return proto.Error{Message: fmt.Sprintf("no pane %s", m.Pane)}
	}
	r := proto.SearchResult{Pane: m.Pane, Query: m.Query}
	if s, ok := p.(interface {
		Search(query string, limit int) ([]vt.Match, bool)
	}); ok {
		r.Matches, r.More = s.Search(m.Query, maxMatches)
	}
	return r
}

// view is where a pane's view sits in its scrollback. It is view state shared
// by every client and never saved.
type view struct {
	off    int    // lines above the live screen
	pushed uint64 // scrollPushed at the last anchor
}

// scrollPushed counts lines that ever entered p's history. A Pane without
// ScrollbackPushed falls back to the history length, which stops growing
// once history is full.
func scrollPushed(p Pane) uint64 {
	if c, ok := p.(interface{ ScrollbackPushed() uint64 }); ok {
		return c.ScrollbackPushed()
	}
	return uint64(p.ScrollbackLen())
}

// view returns id's view anchored to p's current history, and the history
// length: while scrolled back, the offset grows by the lines that scrolled
// off since the last call, so the same lines stay in view. Callers hold d.mu.
func (d *Daemon) view(id string, p Pane) (*view, int) {
	if d.views == nil {
		d.views = map[string]*view{}
	}
	n, hist := scrollPushed(p), p.ScrollbackLen()
	v := d.views[id]
	if v == nil {
		v = &view{pushed: n}
		d.views[id] = v
	}
	if v.off > 0 && n > v.pushed {
		v.off += int(n - v.pushed)
	}
	v.pushed = n
	v.off = min(v.off, hist)
	return v, hist
}

// frame builds id's frame at its scroll offset. Callers hold d.mu.
// ponytail: output landing between view and SnapshotAt shows one frame off by
// those lines; the next frame corrects it.
func (d *Daemon) frame(id string, p Pane) proto.Frame {
	v, hist := d.view(id, p)
	return proto.Frame{Pane: id, Grid: p.SnapshotAt(v.off), Modes: p.Modes(), ScrollOffset: v.off, ScrollMax: hist, ScrollPushed: v.pushed}
}

// scroll moves the view m.Lines into history (negative goes back toward the
// live screen), clamped to [0, ScrollbackLen], and pushes a frame.
func (d *Daemon) scroll(m proto.Scroll) error {
	d.mu.Lock()
	p := d.panes[m.Pane]
	if p == nil {
		d.mu.Unlock()
		return fmt.Errorf("no pane %s", m.Pane)
	}
	v, hist := d.view(m.Pane, p)
	v.off = max(0, min(v.off+m.Lines, hist))
	d.mu.Unlock()
	d.pushFrame(m.Pane, p)
	return nil
}

// unscroll snaps id's view to the live screen, as any input does.
func (d *Daemon) unscroll(id string, p Pane) {
	d.mu.Lock()
	v := d.views[id]
	moved := v != nil && v.off != 0
	if moved {
		v.off = 0
	}
	d.mu.Unlock()
	if moved {
		d.pushFrame(id, p)
	}
}
