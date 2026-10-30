package daemon

// A new daemon takes over this one's panes over the socket; see
// docs/handoff.md for the states and what moves.

import (
	"encoding/gob"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// handoffVersion changes whenever the messages below do; a daemon refuses
// another version and the caller restarts instead.
const handoffVersion = 1

// handoffTimeout bounds each wait before Commit, on both sides; tests
// shorten it.
var handoffTimeout = 10 * time.Second

// handoffFault, nil outside tests, runs at each step named in
// docs/handoff.md; an error fails that step.
var handoffFault func(step string) error

func fault(step string) error {
	if handoffFault == nil {
		return nil
	}
	if err := handoffFault(step); err != nil {
		return fmt.Errorf("%s: %w", step, err)
	}
	return nil
}

// The handoff's messages, after Hello{Kind: "handoff"}.
type (
	handoffRequest struct{ Version int }
	// handoffBundle is followed by Panes handoffPanes, then FDs
	// descriptors: the listener, then each running pane's PTY master in
	// order.
	handoffBundle struct {
		Version int
		State   model.State
		Panes   int
		FDs     int
		Chats   []handoffChat
	}
	handoffPane struct {
		ID         string
		PID        int // 0 for a held pane whose process exited: no master
		ExitCode   int // when PID is 0
		Cols, Rows int
		Snapshot   []byte   // vt.Save
		Input      [][]byte // queued for the PTY and not written yet, in order
	}
	// handoffChat is the seam for managed sessions (M4): each will move
	// with its stdio pipes as FDs descriptors after the panes' masters.
	// This build sends none and refuses a bundle that has any.
	handoffChat struct {
		ID  string
		FDs int
	}
	handoffAbort   struct{ Reason string }
	handoffReady   struct{}
	handoffCommit  struct{}
	handoffStarted struct{}
)

func init() {
	for _, m := range []any{handoffRequest{}, handoffBundle{}, handoffPane{}, handoffAbort{}, handoffReady{}, handoffCommit{}, handoffStarted{}} {
		gob.Register(m)
	}
}

// acceptGate pauses Serve's accept loop while a handoff holds the listener.
type acceptGate struct {
	mu   sync.Mutex
	open chan struct{} // non-nil while paused; closed when the handoff ends
	gone bool          // the handoff succeeded, so Serve accepts no more
}

// pause also keeps the socket path when Serve closes ln meanwhile: the
// new daemon may be about to serve it.
func (g *acceptGate) pause(ln *net.UnixListener) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open = make(chan struct{})
	ln.SetUnlinkOnClose(false)
	ln.SetDeadline(time.Unix(1, 0))
}

// end lets Serve accept again, or with gone stop.
func (g *acceptGate) end(ln *net.UnixListener, gone bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !gone {
		ln.SetUnlinkOnClose(true)
		ln.SetDeadline(time.Time{})
	}
	g.gone = gone
	close(g.open)
	g.open = nil
}

// wait reports, once any handoff ends, whether Accept's err came from a
// pause and Serve should accept again.
func (g *acceptGate) wait(err error) bool {
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		return false
	}
	g.mu.Lock()
	open := g.open
	g.mu.Unlock()
	if open != nil {
		<-open
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.gone
}
