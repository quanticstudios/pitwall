package main

import (
	"os"
	"slices"
	"sync"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// backend is app.Backend over a live daemon connection.
type backend struct {
	conn    *proto.Conn
	changed chan struct{}
	focus   chan proto.FocusSession

	mu      sync.Mutex
	session string // the session the window shows, from SessionShow
	state   model.State
	frames  map[string]proto.Frame

	// Send queues for sendLoop, so a daemon busy in a slow request never
	// blocks the window: a few hundred small frames fill a unix socket.
	outMu   sync.Mutex
	out     []any
	outErr  error // the write error that ended sendLoop
	outWake chan struct{}
}

// newBackend queues a FocusSession for the window's first session, and the
// tab $PITWALL_ATTACH names.
func newBackend(c *proto.Conn, session string) *backend {
	b := &backend{conn: c, changed: make(chan struct{}, 1), focus: make(chan proto.FocusSession, 1), frames: map[string]proto.Frame{}, outWake: make(chan struct{}, 1)}
	if f := (proto.FocusSession{WorkspaceID: os.Getenv("PITWALL_ATTACH"), SessionID: session}); f != (proto.FocusSession{}) {
		b.focus <- f
	}
	return b
}

var _ app.Focuser = (*backend)(nil)

func (b *backend) State() model.State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *backend) Frame(pane string) (vt.Grid, vt.Modes, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, ok := b.frames[pane]
	return f.Grid, f.Modes, ok
}

// Send queues msg for sendLoop and returns at once; the error is the one
// that broke the connection. A SessionShow also tells the backend which
// session's frames redraw the window.
func (b *backend) Send(msg any) error {
	if s, ok := msg.(proto.SessionShow); ok {
		b.mu.Lock()
		b.session = s.SessionID
		b.mu.Unlock()
	}
	b.outMu.Lock()
	if err := b.outErr; err != nil {
		b.outMu.Unlock()
		return err
	}
	b.out = enqueue(b.out, msg)
	b.outMu.Unlock()
	select {
	case b.outWake <- struct{}{}:
	default:
	}
	return nil
}

// sendLoop writes queued messages in order until a write fails.
func (b *backend) sendLoop() {
	for range b.outWake {
		for {
			b.outMu.Lock()
			if len(b.out) == 0 {
				b.outMu.Unlock()
				break
			}
			msg := b.out[0]
			b.out[0] = nil
			b.out = b.out[1:]
			b.outMu.Unlock()
			if err := b.conn.Send(msg); err != nil {
				b.outMu.Lock()
				b.outErr, b.out = err, nil
				b.outMu.Unlock()
				return
			}
		}
	}
}

// enqueue appends msg to q, except that a Resize for a pane with one
// queued replaces it in place: only the latest size matters. Nothing is
// dropped, and everything else keeps the order it was sent in.
func enqueue(q []any, msg any) []any {
	if r, ok := msg.(proto.Resize); ok {
		for i, m := range q {
			if o, ok := m.(proto.Resize); ok && o.Pane == r.Pane {
				q[i] = r
				return q
			}
		}
	}
	return append(q, msg)
}

func (b *backend) Changed() <-chan struct{}         { return b.changed }
func (b *backend) Focus() <-chan proto.FocusSession { return b.focus }

func (b *backend) recvLoop() {
	defer close(b.changed)
	for {
		msg, err := b.conn.Recv()
		if err != nil {
			return
		}
		if focus, ok := msg.(proto.FocusSession); ok {
			b.focus <- focus
			select {
			case b.changed <- struct{}{}:
			default:
			}
			continue
		}
		b.mu.Lock()
		switch m := msg.(type) {
		case proto.StateMsg:
			b.state = m.State
			live := map[string]bool{}
			for _, p := range m.State.Panes {
				live[p.ID] = true
			}
			for id := range b.frames {
				if !live[id] {
					delete(b.frames, id)
				}
			}
		case proto.Frame:
			b.frames[m.Pane] = m
			if !shown(&b.state, b.session, m.Pane) {
				// Kept for when its tab is shown; no redraw for it now.
				b.mu.Unlock()
				continue
			}
		}
		b.mu.Unlock()
		select {
		case b.changed <- struct{}{}:
		default:
		}
	}
}

// Scroll implements app.Scroller from the pane's last frame.
func (b *backend) Scroll(pane string) (offset, max int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.frames[pane]
	return f.ScrollOffset, f.ScrollMax
}

// shown reports whether pane is in the active tab of a tab of session the
// window can show, so output in a background tab or another session does
// not redraw the window.
func shown(st *model.State, session, pane string) bool {
	for _, w := range st.Workspaces {
		if w.Detached || w.SessionID != session {
			continue
		}
		for _, t := range w.Tabs {
			if t.ID == w.ActiveTab && slices.Contains(layout.Panes(t.Layout), pane) {
				return true
			}
		}
	}
	return false
}
