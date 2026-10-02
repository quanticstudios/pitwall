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

	mu     sync.Mutex
	state  model.State
	frames map[string]proto.Frame
}

func newBackend(c *proto.Conn) *backend {
	b := &backend{conn: c, changed: make(chan struct{}, 1), focus: make(chan proto.FocusSession, 1), frames: map[string]proto.Frame{}}
	if id := os.Getenv("PITWALL_ATTACH"); id != "" {
		b.focus <- proto.FocusSession{WorkspaceID: id}
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

func (b *backend) Send(msg any) error               { return b.conn.Send(msg) }
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
			if !shown(&b.state, m.Pane) {
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

// shown reports whether pane is in the active tab of a session the window
// can show, so output in a background tab does not redraw the window.
func shown(st *model.State, pane string) bool {
	for _, w := range st.Workspaces {
		if w.Detached {
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
