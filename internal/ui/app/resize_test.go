package app

import (
	"errors"
	"image"
	"reflect"
	"sync"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// laggy holds the fake's messages in a queue, as the window's send queue
// holds them while the daemon works through an input flood. A newer Resize
// for a pane replaces its queued one, as cmd/pitwall's enqueue does. The
// first Resize sent for each pane fails.
type laggy struct {
	*FakeBackend
	mu     sync.Mutex
	queue  []any
	failed map[string]bool
}

func (l *laggy) Send(msg any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r, ok := msg.(proto.Resize); ok {
		if !l.failed[r.Pane] {
			l.failed[r.Pane] = true
			return errors.New("send failed")
		}
		for i, m := range l.queue {
			if o, ok := m.(proto.Resize); ok && o.Pane == r.Pane {
				l.queue[i] = r
				return nil
			}
		}
	}
	l.queue = append(l.queue, msg)
	return nil
}

// deliver hands the fake the queued messages up to and including the first
// one stop matches, or all of them.
func (l *laggy) deliver(stop func(any) bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(l.queue) > 0 {
		m := l.queue[0]
		l.queue = l.queue[1:]
		l.FakeBackend.Send(m)
		if stop != nil && stop(m) {
			return
		}
	}
}

func (l *laggy) queued() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.queue) }

// TestNewPaneSizeUnderFlood drives the real window layout while its send
// queue is backed up with typing: a split and a new tab made behind the flood
// draw their new panes at the fake's 80x24 until their Resize gets through,
// one Resize each fails to send, and once the queue drains every pane on
// screen has the size the window fits it to.
func TestNewPaneSizeUnderFlood(t *testing.T) {
	f := NewFakeBackend()
	b := &laggy{FakeBackend: f, failed: map[string]bool{}}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	keys := func(es ...key.Event) {
		for _, e := range es {
			r.Queue(e)
			frame()
			r.Queue(key.Event{Name: e.Name, Modifiers: e.Modifiers, State: key.Release})
			frame()
		}
	}
	flood := func() {
		for range 50 {
			r.Queue(key.EditEvent{Text: "y"})
			frame()
		}
		for range 1000 {
			b.Send(proto.Input{Pane: u.nav.focused(), Data: []byte("y")})
		}
	}
	isA := func(want any) func(any) bool {
		return func(m any) bool { return reflect.TypeOf(m) == reflect.TypeOf(want) }
	}
	for range 3 {
		frame()
		b.deliver(nil)
	}

	flood()
	keys(press("N", key.ModAlt)) // split_right
	flood()
	b.deliver(isA(proto.OpenPane{})) // the daemon gets to the split
	frame()
	frame()
	split := u.nav.focused()
	if g, _, _ := f.Frame(split); split == "a" || g.Cols != 80 || g.Rows != 24 {
		t.Fatalf("split pane %s: %dx%d, want a new pane at the fake's 80x24", split, g.Cols, g.Rows)
	}

	flood()
	keys(press("T", key.ModAlt|key.ModShift)) // new_tab
	flood()
	b.deliver(isA(proto.NewTab{}))
	for range 3 {
		frame()
	}
	tab := u.nav.focused()
	if tab == split || tab == "" {
		t.Fatalf("new tab's pane %q", tab)
	}
	flood()

	for range 5 {
		b.deliver(nil)
		frame()
	}
	if n := b.queued(); n != 0 {
		t.Fatalf("%d messages still queued after the drain", n)
	}
	sent := map[string]proto.Resize{}
	for _, m := range f.Sent() {
		if r, ok := m.(proto.Resize); ok {
			sent[r.Pane] = r
		}
	}
	for _, id := range []string{tab} {
		g, _, _ := f.Frame(id)
		if r, ok := sent[id]; !ok || g.Cols != r.Cols || g.Rows != r.Rows || g.Cols == 80 && g.Rows == 24 {
			t.Fatalf("pane %s stuck at %dx%d; its last Resize %+v", id, g.Cols, g.Rows, r)
		}
	}
	// Back on the split's tab, it has its size too.
	st := f.State()
	u.nav.attachSession(&st, "w1")
	frame()
	frame()
	b.deliver(nil)
	g, _, _ := f.Frame(split)
	if r := sent[split]; g.Cols == 80 && g.Rows == 24 || r.Pane != "" && (g.Cols != r.Cols || g.Rows != r.Rows) {
		t.Fatalf("split pane %s stuck at %dx%d; its last Resize %+v", split, g.Cols, g.Rows, r)
	}
}
