package main

import (
	"sync"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// backend is app.Backend over a live daemon connection.
type backend struct {
	conn    *proto.Conn
	changed chan struct{}

	mu     sync.Mutex
	state  model.State
	frames map[string]proto.Frame
}

func newBackend(c *proto.Conn) *backend {
	return &backend{conn: c, changed: make(chan struct{}, 1), frames: map[string]proto.Frame{}}
}

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

func (b *backend) Send(msg any) error       { return b.conn.Send(msg) }
func (b *backend) Changed() <-chan struct{} { return b.changed }

func (b *backend) recvLoop() {
	defer close(b.changed)
	for {
		msg, err := b.conn.Recv()
		if err != nil {
			return
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
		}
		b.mu.Unlock()
		select {
		case b.changed <- struct{}{}:
		default:
		}
	}
}
