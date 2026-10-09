package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// backend is app.Backend over a daemon connection. When the connection is
// lost it keeps the last state on screen and connects again through redial.
type backend struct {
	changed chan struct{}
	focus   chan proto.FocusSession
	retry   chan struct{} // Reconnect cuts the wait before the next try short

	// redial connects as dialOrStart does; nil leaves a lost connection
	// lost. name is the session the window was opened on, -s's.
	redial func(session string, restart bool) (*proto.Conn, proto.StateMsg, error)
	name   string

	mu      sync.Mutex
	session string // the session the window shows, from SessionShow
	state   model.State
	frames  map[string]proto.Frame
	found   map[string]proto.SearchResult // per pane, the reply to its last Search
	tree    proto.WorktreeInfo            // the reply to the last WorktreeQuery, cleared when one is sent
	link    app.Link
	dialing bool // a dial loop runs

	// Send queues for sendLoop, so a daemon busy in a slow request never
	// blocks the window: a few hundred small frames fill a unix socket.
	// conn, done and outWake belong to one connection; attach replaces them.
	outMu   sync.Mutex
	conn    *proto.Conn
	done    chan struct{} // closed by fail
	outWake chan struct{}
	out     []any
	outErr  error     // the error that ended the connection
	backed  time.Time // when the queue went past backedUp, zero when it is not
	peak    int       // the longest queue since then
}

// errNotConnected is Send's error before the first connection.
var errNotConnected = errors.New("not connected to the daemon")

// errOlderDaemon is Send's error for a message type the daemon's
// proto.Level predates; nothing is sent.
var errOlderDaemon = errors.New("the daemon is older than this window")

// The waits between reconnect tries double from retryMin up to retryMax.
var retryMin, retryMax = 500 * time.Millisecond, 30 * time.Second

// newBackend queues a FocusSession for the window's first session, and the
// tab $PITWALL_ATTACH names. With c nil it starts disconnected.
func newBackend(c *proto.Conn, session string) *backend {
	b := &backend{conn: c, changed: make(chan struct{}, 1), focus: make(chan proto.FocusSession, 1), retry: make(chan struct{}, 1),
		frames: map[string]proto.Frame{}, found: map[string]proto.SearchResult{}, outWake: make(chan struct{}, 1), done: make(chan struct{})}
	if c == nil {
		b.outErr = errNotConnected
		close(b.done)
	} else {
		b.link.Epoch = 1
	}
	if f := (proto.FocusSession{WorkspaceID: os.Getenv("PITWALL_ATTACH"), SessionID: session}); f != (proto.FocusSession{}) {
		b.focus <- f
	}
	return b
}

var (
	_ app.Focuser   = (*backend)(nil)
	_ app.Linker    = (*backend)(nil)
	_ app.Finder    = (*backend)(nil)
	_ app.Worktreer = (*backend)(nil)
)

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
// that broke the connection, or errOlderDaemon. A SessionShow also tells the
// backend which session's frames redraw the window.
func (b *backend) Send(msg any) error {
	b.mu.Lock()
	if s, ok := msg.(proto.SessionShow); ok {
		b.session = s.SessionID
	}
	if _, ok := msg.(proto.WorktreeQuery); ok {
		b.tree = proto.WorktreeInfo{}
	}
	level := b.link.Level
	b.mu.Unlock()
	// why: a daemon below proto.Level 1 drops the connection on a message
	// type it does not know.
	if need := proto.Since(msg); need > level {
		return fmt.Errorf("%w: %T needs level %d, the daemon is at %d", errOlderDaemon, msg, need, level)
	}
	b.outMu.Lock()
	if err := b.outErr; err != nil {
		b.outMu.Unlock()
		return err
	}
	b.out = enqueue(b.out, msg)
	if n := len(b.out); n > backedUp {
		if b.backed.IsZero() {
			b.backed = time.Now()
			log.Printf("send queue backed up: %d messages", n)
		}
		b.peak = max(b.peak, n)
	}
	wake := b.outWake
	b.outMu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
	return nil
}

// sendLoop writes queued messages in order until the connection ends.
func (b *backend) sendLoop() {
	b.outMu.Lock()
	conn, done, wake := b.conn, b.done, b.outWake
	b.outMu.Unlock()
	for {
		select {
		case <-done:
			return
		case <-wake:
		}
		for {
			b.outMu.Lock()
			if len(b.out) == 0 || b.conn != conn {
				b.outMu.Unlock()
				break
			}
			msg := b.out[0]
			b.out[0] = nil
			b.out = b.out[1:]
			if !b.backed.IsZero() && len(b.out) < backedUp/10 {
				log.Printf("send queue drained after %v, peak %d messages", time.Since(b.backed).Round(time.Millisecond), b.peak)
				b.backed, b.peak = time.Time{}, 0
			}
			b.outMu.Unlock()
			if err := conn.Send(msg); err != nil {
				b.fail(conn, err, 1)
				return
			}
		}
	}
}

// backedUp is the queue length the log notes, once until it is back under a
// tenth of that.
const backedUp = 1000

// fail ends conn on its first error: it closes it, so recvLoop ends, logs
// how many accepted messages were not sent, counting unsent ones the
// caller holds, makes later Sends return err, and starts reconnecting.
func (b *backend) fail(conn *proto.Conn, err error, unsent int) {
	b.outMu.Lock()
	if b.conn != conn || b.outErr != nil {
		b.outMu.Unlock()
		return
	}
	b.outErr = err
	unsent += len(b.out)
	b.out = nil
	close(b.done)
	b.outMu.Unlock()
	conn.Close()
	log.Printf("daemon connection lost: %q; %d queued messages not sent", err, unsent)
	b.setLink(app.LinkDown, "Reconnecting…")
	if b.redial != nil {
		b.startDialing(false)
	}
}

// startDialing runs dial unless it runs already.
func (b *backend) startDialing(restart bool) {
	b.mu.Lock()
	running := b.dialing
	b.dialing = true
	b.mu.Unlock()
	if !running {
		go b.dial(restart)
	}
}

// dial connects until it succeeds or finds a daemon of another
// proto.Version, waiting longer after each failure. With restart it first
// replaces a daemon of another Version.
func (b *backend) dial(restart bool) {
	wait := retryMin
	for {
		conn, initial, err := b.redial(b.sessionName(), restart)
		restart = false
		var refused incompatible
		switch {
		case err == nil:
			b.attach(conn, initial)
			return
		case errors.As(err, &refused):
			log.Printf("reconnect: %q", err)
			b.mu.Lock()
			b.dialing = false
			// A newer daemon means a newer pitwall restarted it: this window
			// reopens on the binary installed now.
			b.link.State, b.link.Note = app.LinkRestart, ""
			if refused.version > proto.Version && b.link.Epoch > 0 {
				b.link.State = app.LinkStale
			}
			b.mu.Unlock()
			b.notify()
			return
		}
		log.Printf("reconnect: %q; next try in %v", err, wait)
		b.setLink(app.LinkDown, fmt.Sprintf("%s. Trying again in %v.", err, wait.Round(time.Second/10)))
		select {
		case <-time.After(wait):
		case <-b.retry:
		}
		b.setLink(app.LinkDown, "Reconnecting…")
		wait = min(2*wait, retryMax)
	}
}

// attach makes conn the backend's connection, with initial's state.
func (b *backend) attach(conn *proto.Conn, initial proto.StateMsg) {
	b.outMu.Lock()
	b.conn, b.done, b.outWake, b.out, b.outErr = conn, make(chan struct{}), make(chan struct{}, 1), nil, nil
	b.outMu.Unlock()
	b.mu.Lock()
	b.state, b.frames, b.found = initial.State, map[string]proto.Frame{}, map[string]proto.SearchResult{}
	b.dialing = false
	b.link = app.Link{Epoch: b.link.Epoch + 1, Level: initial.Level}
	first := b.session == ""
	b.mu.Unlock()
	log.Printf("connected to the daemon")
	if first { // the window had nothing to show until now
		if s, _ := guiTarget(initial.State, b.name); s.ID != "" {
			b.pushFocus(proto.FocusSession{SessionID: s.ID})
		}
	}
	go b.recvLoop()
	go b.sendLoop()
	b.notify()
}

// sessionName is the name of the session the window shows, for a Hello.
func (b *backend) sessionName() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.state.Session(b.session); s != nil {
		return s.Name
	}
	return b.name
}

func (b *backend) setLink(state app.LinkState, note string) {
	b.mu.Lock()
	b.link.State, b.link.Note = state, note
	b.mu.Unlock()
	b.notify()
}

// Link implements app.Linker.
func (b *backend) Link() app.Link {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.link
}

// Reconnect implements app.Linker: the next try starts now.
func (b *backend) Reconnect() {
	select {
	case b.retry <- struct{}{}:
	default:
	}
}

// Restart implements app.Linker: replace the daemon of another Version.
func (b *backend) Restart() {
	if b.redial == nil {
		return
	}
	b.setLink(app.LinkDown, "Restarting pitwall's background service…")
	b.startDialing(true)
}

// notify wakes the window.
func (b *backend) notify() {
	select {
	case b.changed <- struct{}{}:
	default:
	}
}

// pushFocus replaces any focus request the window has not taken yet.
func (b *backend) pushFocus(f proto.FocusSession) {
	// why: the window takes requests on another goroutine that can be busy
	// raising it; blocking here would stop every later state.
	select {
	case <-b.focus: // the newest request wins
	default:
	}
	select {
	case b.focus <- f:
	default:
	}
	b.notify()
}

// enqueue appends msg to q, except that a Resize for a pane with one
// queued replaces it in place: only the latest size matters. Nothing is
// dropped while the connection is up, and everything else keeps the order
// it was sent in.
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

// recvLoop applies the connection's messages until it fails.
func (b *backend) recvLoop() {
	b.outMu.Lock()
	conn := b.conn
	b.outMu.Unlock()
	for {
		msg, err := conn.Recv()
		if err != nil {
			b.fail(conn, err, 0)
			return
		}
		if focus, ok := msg.(proto.FocusSession); ok {
			b.pushFocus(focus)
			continue
		}
		b.mu.Lock()
		switch m := msg.(type) {
		case proto.StateMsg:
			b.state, b.link.Level = m.State, m.Level
			live := map[string]bool{}
			for _, p := range m.State.Panes {
				live[p.ID] = true
			}
			for id := range b.frames {
				if !live[id] {
					delete(b.frames, id)
					delete(b.found, id)
				}
			}
		case proto.SearchResult:
			b.found[m.Pane] = m
		case proto.WorktreeInfo:
			b.tree = m
		case proto.Frame:
			b.frames[m.Pane] = m
			if !shown(&b.state, b.session, m.Pane) {
				// Kept for when its tab is shown; no redraw for it now.
				b.mu.Unlock()
				continue
			}
		}
		b.mu.Unlock()
		b.notify()
	}
}

// Scroll implements app.Scroller from the pane's last frame.
func (b *backend) Scroll(pane string) (offset, max int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.frames[pane]
	return f.ScrollOffset, f.ScrollMax
}

// Worktree implements app.Worktreer.
func (b *backend) Worktree() proto.WorktreeInfo {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tree
}

// Found implements app.Finder from the pane's last SearchResult and frame.
func (b *backend) Found(pane string) (proto.SearchResult, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.frames[pane]
	return b.found[pane], f.ScrollPushed - uint64(f.ScrollOffset)
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
