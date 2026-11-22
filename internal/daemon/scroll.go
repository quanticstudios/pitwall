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

// text answers m with its selection's text. A Pane without Text, like a
// test fake, has none.
func (d *Daemon) text(m proto.Text) any {
	d.mu.Lock()
	p := d.panes[m.Pane]
	d.mu.Unlock()
	if p == nil {
		return proto.Error{Message: fmt.Sprintf("no pane %s", m.Pane)}
	}
	r := proto.TextResult{Pane: m.Pane, Sel: m.Sel}
	if t, ok := p.(interface{ Text(vt.Selection) string }); ok {
		r.Text = t.Text(m.Sel)
	}
	return r
}

// image answers m with the pixels of an image m.Pane's frames place. A
// Pane without Image, like a test fake, has none.
func (d *Daemon) image(m proto.Image) proto.ImageResult {
	d.mu.Lock()
	p := d.panes[m.Pane]
	d.mu.Unlock()
	r := proto.ImageResult{Pane: m.Pane}
	if i, ok := p.(interface {
		Image(key uint64) (vt.Image, bool)
	}); ok {
		r.Image, _ = i.Image(m.Key)
	}
	r.Image.Key = m.Key
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

type frameCapturer interface{ CaptureFrame(off int) vt.Frame }
type writeSeqer interface{ WriteSeq() uint64 }
type titler interface{ Title() string }

func paneWriteSeq(p Pane) (uint64, bool) {
	if s, ok := p.(writeSeqer); ok {
		if seq := s.WriteSeq(); seq > 0 {
			return seq, true
		}
	}
	return 0, false
}

func paneTitle(p Pane) string {
	if t, ok := p.(titler); ok {
		return t.Title()
	}
	return p.Snapshot().Title
}

func capturePaneFrame(p Pane, off int) vt.Frame {
	if c, ok := p.(frameCapturer); ok {
		return c.CaptureFrame(off)
	}
	marked := false
	if shell, ok := p.(interface{ HasPromptMarks() bool }); ok {
		marked = shell.HasPromptMarks()
	}
	return vt.Frame{
		Grid:         p.SnapshotAt(off),
		Modes:        p.Modes(),
		ScrollMax:    p.ScrollbackLen(),
		ScrollPushed: scrollPushed(p),
		PromptMarks:  marked,
	}
}

// captureViewFrame captures a pane's frame without holding d.mu. When not live
// and scrolled back, it anchors prevOff against ScrollPushed and recaptures if
// new lines scrolled off since prevPushed.
func captureViewFrame(id string, p Pane, live bool, prevOff int, prevPushed uint64) (proto.Frame, int, uint64) {
	if live {
		cf := capturePaneFrame(p, 0)
		return proto.Frame{
			PromptMarks:  cf.PromptMarks,
			Pane:         id,
			Grid:         cf.Grid,
			Modes:        cf.Modes,
			ScrollMax:    cf.ScrollMax,
			ScrollPushed: cf.ScrollPushed,
		}, 0, cf.ScrollPushed
	}
	cf := capturePaneFrame(p, prevOff)
	off := prevOff
	if off > 0 && cf.ScrollPushed > prevPushed {
		off += int(cf.ScrollPushed - prevPushed)
	}
	off = min(off, cf.ScrollMax)
	if off != prevOff {
		cf = capturePaneFrame(p, off)
		off = min(off, cf.ScrollMax)
	}
	return proto.Frame{
		PromptMarks:  cf.PromptMarks,
		Pane:         id,
		Grid:         cf.Grid,
		Modes:        cf.Modes,
		ScrollOffset: off,
		ScrollMax:    cf.ScrollMax,
		ScrollPushed: cf.ScrollPushed,
	}, off, cf.ScrollPushed
}

// viewState reads id's current scroll offset and pushed count. Callers hold d.mu.
func (d *Daemon) viewState(id string) (off int, pushed uint64) {
	if v := d.views[id]; v != nil {
		return v.off, v.pushed
	}
	return 0, 0
}

// updateView records the anchored scroll offset and pushed count from an
// unlocked CaptureFrame, unless a concurrent scroll/unscroll moved the view.
// Callers hold d.mu.
func (d *Daemon) updateView(id string, live bool, prevOff int, prevPushed uint64, newOff int, newPushed uint64) {
	if d.views == nil {
		d.views = map[string]*view{}
	}
	v := d.views[id]
	if v == nil {
		if live {
			d.views[id] = &view{pushed: newPushed}
		} else {
			d.views[id] = &view{off: newOff, pushed: newPushed}
		}
		return
	}
	if live {
		if v.off == 0 && newPushed >= v.pushed {
			v.pushed = newPushed
		}
		return
	}
	if v.off == prevOff && v.pushed == prevPushed {
		v.off = newOff
		v.pushed = newPushed
	}
}

// frame builds id's frame at its scroll offset. Callers hold d.mu.
func (d *Daemon) frame(id string, p Pane) proto.Frame {
	prevOff, prevPushed := d.viewState(id)
	f, newOff, newPushed := captureViewFrame(id, p, false, prevOff, prevPushed)
	d.updateView(id, false, prevOff, prevPushed, newOff, newPushed)
	return f
}

// frameFor is id's frame for c: at the live screen for a pane c asked to
// see live (View.Live), else at the shared scroll offset. Callers hold d.mu.
func (d *Daemon) frameFor(c *client, id string, p Pane) proto.Frame {
	if !c.live[id] {
		return d.frame(id, p)
	}
	prevOff, prevPushed := d.viewState(id)
	f, newOff, newPushed := captureViewFrame(id, p, true, prevOff, prevPushed)
	d.updateView(id, true, prevOff, prevPushed, newOff, newPushed)
	return f
}

// scroll moves the view m.Lines into history (negative goes back toward the
// live screen), clamped to [0, ScrollbackLen], then m.Prompts shell prompts,
// and pushes a frame.
func (d *Daemon) scroll(m proto.Scroll) error {
	d.mu.Lock()
	p := d.panes[m.Pane]
	if p == nil {
		d.mu.Unlock()
		return fmt.Errorf("no pane %s", m.Pane)
	}
	if m.Prompts != 0 {
		if shell, ok := p.(interface{ HasPromptMarks() bool }); ok && !shell.HasPromptMarks() {
			d.mu.Unlock()
			return fmt.Errorf("prompt jumps need shell integration: https://github.com/quanticstudios/pitwall/blob/main/docs/usage.md#shell-integration")
		}
	}
	v, hist := d.view(m.Pane, p)
	v.off = max(0, min(v.off+m.Lines, hist))
	if j, ok := p.(interface{ PromptOffset(off, n int) int }); ok && m.Prompts != 0 {
		v.off = max(0, min(j.PromptOffset(v.off, m.Prompts), hist))
	}
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
