//go:build unix

package daemon

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
	"golang.org/x/sys/unix"
)

// pausable is a running pane that can move: *pane.Pane.
type pausable interface {
	Pause()
	Resume()
	Save() ([]byte, error)
	Master() (*os.File, int, error)
}

// serveHandoff is the old daemon's side. Any failure before the new daemon
// starts leaves this one serving as before.
func (d *Daemon) serveHandoff(nc net.Conn, conn *proto.Conn) {
	if err := d.handOff(nc, conn); err != nil {
		log.Printf("handoff: %q; still serving", err)
		conn.Send(handoffAbort{Reason: err.Error()}) // the new daemon may be gone
		// why: closing first would fail the new daemon's next write with
		// EPIPE before it reads why.
		nc.SetReadDeadline(time.Now().Add(time.Second))
		io.Copy(io.Discard, nc)
		return
	}
	log.Printf("handoff: the new daemon took over")
}

func (d *Daemon) handOff(nc net.Conn, conn *proto.Conn) error {
	uc, _ := nc.(*net.UnixConn)
	d.mu.Lock()
	ln, _ := d.ln.(*net.UnixListener)
	d.mu.Unlock()
	if uc == nil || ln == nil {
		return errors.New("not serving a unix socket")
	}
	nc.SetReadDeadline(time.Now().Add(handoffTimeout))
	m, err := conn.Recv()
	if err != nil {
		return err
	}
	if r, ok := m.(handoffRequest); !ok || r.Version != handoffVersion {
		return fmt.Errorf("handoff request %#v, want version %d", m, handoffVersion)
	}
	// why: d.mu stays held until the end, so no request, exit or timer
	// changes state or panes after they were sent.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing {
		return errors.New("shutting down")
	}
	log.Printf("handoff: freezing %d panes", len(d.st.Panes))
	f := &frozen{ln: ln}
	d.gate.pause(ln)
	err = d.freeze(f)
	if err == nil {
		err = fault("old:freeze")
	}
	if err == nil {
		err = f.send(uc, conn)
	}
	if err != nil {
		f.thaw()
		d.gate.end(ln, false)
		return err
	}
	// Nothing here may close or signal the panes again: forget them.
	d.handedOff, d.closing = true, true
	d.panes, d.inputs, d.holds = map[string]Pane{}, map[string]chan []byte{}, map[string]chan chan []byte{}
	d.stop() // before the gate ends, so Serve sees its context done
	d.gate.end(ln, true)
	return nil
}

// frozen is what a handoff paused, to send or to resume.
type frozen struct {
	ln    *net.UnixListener
	state model.State
	out   []handoffPane
	files []*os.File // the listener, then each running pane's master
	held  []heldPane
}

type heldPane struct {
	p      pausable
	in     chan []byte
	hold   chan []byte // writeInput's, nil when it had stopped
	rest   []byte      // what writeInput took and did not write
	queued [][]byte    // what was left in in
}

// freeze pauses every pane, takes their unwritten input and snapshots
// them, all into f, which thaw undoes even after an error. Callers hold
// d.mu.
func (d *Daemon) freeze(f *frozen) error {
	lf, err := f.ln.File()
	if err != nil {
		return err
	}
	f.files = append(f.files, lf)
	for _, sp := range d.st.Panes {
		size := d.sizes[sp.ID]
		hp := handoffPane{ID: sp.ID, Cols: size[0], Rows: size[1]}
		h := d.panes[sp.ID]
		p, ok := h.(pausable)
		switch s, held := h.(*stopped); {
		case held:
			hp.ExitCode = s.code
			hp.Snapshot, err = s.Save()
		case !ok:
			return fmt.Errorf("pane %s cannot be handed off", sp.ID)
		case exited(h) && !sp.Exited:
			return fmt.Errorf("pane %s is exiting", sp.ID)
		case exited(h):
			hp.ExitCode = h.ExitCode()
			hp.Snapshot, err = p.Save()
		default:
			p.Pause()
			hp.PID, hp.Snapshot, hp.Input, err = d.take(f, sp.ID, h, p)
		}
		if err != nil {
			return fmt.Errorf("pane %s: %w", sp.ID, err)
		}
		f.out = append(f.out, hp)
	}
	if len(d.panes) != len(d.st.Panes) {
		return errors.New("panes and state disagree")
	}
	f.state = d.saveSnapshot()
	return nil
}

// take collects a paused pane's input, master and snapshot.
func (d *Daemon) take(f *frozen, id string, h Pane, p pausable) (pid int, snap []byte, input [][]byte, err error) {
	held := heldPane{p: p, in: d.inputs[id]}
	if hold := d.holds[id]; hold != nil {
		r := make(chan []byte)
		select {
		case hold <- r:
			held.hold, held.rest = r, <-r
		case <-h.Done():
		}
	}
	for len(held.in) > 0 {
		held.queued = append(held.queued, <-held.in)
	}
	f.held = append(f.held, held)
	if len(held.rest) > 0 {
		input = append(input, held.rest)
	}
	input = append(input, held.queued...)
	master, pid, err := p.Master()
	if err != nil {
		return 0, nil, nil, err
	}
	f.files = append(f.files, master)
	snap, err = p.Save()
	return pid, snap, input, err
}

func exited(p Pane) bool {
	select {
	case <-p.Done():
		return true
	default:
		return false
	}
}

// thaw resumes what freeze paused: each pane reads again, and its writer
// goes on with the input it held, then its queue. Callers hold d.mu, so no
// new input lands in between.
func (f *frozen) thaw() {
	for _, file := range f.files {
		file.Close()
	}
	for _, h := range f.held {
		h.p.Resume()
		for _, b := range h.queued {
			h.in <- b
		}
		if h.hold != nil {
			h.hold <- h.rest
		}
	}
}

// send runs the handoff from the old side's send step to Started.
func (f *frozen) send(uc *net.UnixConn, conn *proto.Conn) error {
	b := handoffBundle{Version: handoffVersion, State: f.state, Panes: len(f.out), FDs: len(f.files)}
	if err := conn.Send(b); err != nil {
		return err
	}
	for _, hp := range f.out {
		if err := conn.Send(hp); err != nil {
			return err
		}
	}
	err := sendFDs(uc, f.files)
	for _, file := range f.files {
		file.Close()
	}
	f.files = nil
	if err != nil {
		return err
	}
	if err := fault("old:send"); err != nil {
		return err
	}
	uc.SetReadDeadline(time.Now().Add(handoffTimeout))
	if err := expect[handoffReady](conn); err != nil {
		return err
	}
	if err := fault("old:ready"); err != nil {
		return err
	}
	if err := conn.Send(handoffCommit{}); err != nil {
		return err
	}
	// Committed. The new daemon only starts goroutines before Started;
	// a connection closed without it means it died before reading.
	uc.SetReadDeadline(time.Time{})
	if err := expect[handoffStarted](conn); err != nil {
		return fmt.Errorf("new daemon stopped before it started: %w", err)
	}
	return nil
}

// expect reads the next message, which must be a T.
func expect[T any](conn *proto.Conn) error {
	m, err := conn.Recv()
	if err != nil {
		return err
	}
	switch m := m.(type) {
	case T:
		return nil
	case handoffAbort:
		return fmt.Errorf("the other daemon gave up: %s", m.Reason)
	case proto.Error:
		return fmt.Errorf("the other daemon refused: %s", m.Message)
	default:
		var want T
		return fmt.Errorf("got %T, want %T", m, want)
	}
}

// fdsPerMessage keeps each SCM_RIGHTS message well under the kernels'
// limits (253 on Linux).
const fdsPerMessage = 64

// sendFDs sends files in messages of one byte each, so no read on the
// other side spans two of them. It reads each descriptor through
// SyscallConn: File.Fd would put the shared file description, still the
// old daemon's, into blocking mode.
func sendFDs(uc *net.UnixConn, files []*os.File) error {
	for chunk := range slices.Chunk(files, fdsPerMessage) {
		fds := make([]int, 0, len(chunk))
		for _, f := range chunk {
			rc, err := f.SyscallConn()
			if err != nil {
				return err
			}
			if err := rc.Control(func(fd uintptr) { fds = append(fds, int(fd)) }); err != nil {
				return err
			}
		}
		if _, _, err := uc.WriteMsgUnix([]byte{1}, unix.UnixRights(fds...), nil); err != nil {
			return fmt.Errorf("send descriptors: %w", err)
		}
	}
	return nil
}

// recvFDs receives n descriptors from sendFDs. Package net marks them
// close-on-exec.
func recvFDs(uc *net.UnixConn, n int) ([]*os.File, error) {
	var files []*os.File
	fail := func(err error) ([]*os.File, error) {
		for _, f := range files {
			f.Close()
		}
		return nil, fmt.Errorf("receive descriptors: %w", err)
	}
	for len(files) < n {
		want := min(n-len(files), fdsPerMessage)
		oob := make([]byte, unix.CmsgSpace(want*4))
		var b [1]byte
		_, oobn, flags, _, err := uc.ReadMsgUnix(b[:], oob)
		if err != nil {
			return fail(err)
		}
		msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			return fail(err)
		}
		got := 0
		for _, m := range msgs {
			fds, err := unix.ParseUnixRights(&m)
			if err != nil {
				return fail(err)
			}
			for _, fd := range fds {
				files = append(files, os.NewFile(uintptr(fd), "handoff"))
			}
			got += len(fds)
		}
		if flags&unix.MSG_CTRUNC != 0 || got != want {
			return fail(fmt.Errorf("got %d descriptors, want %d", got, want))
		}
	}
	return files, nil
}

// Takeover takes over the panes and listening socket of the daemon serving
// path, as docs/handoff.md describes, and returns a daemon that holds them
// and the listener to Serve. On an error the old daemon keeps serving and
// nothing taken stays open here.
func Takeover(path string, o Options) (*Daemon, net.Listener, error) {
	nc, err := net.Dial("unix", path)
	if err != nil {
		return nil, nil, err
	}
	defer nc.Close()
	conn := proto.NewConn(nc)
	d, ln, err := takeover(nc.(*net.UnixConn), conn, o)
	if err != nil {
		conn.Send(handoffAbort{Reason: err.Error()}) // the old daemon may be gone
		return nil, nil, fmt.Errorf("handoff: %w", err)
	}
	return d, ln, nil
}

// arrival is a pane that came over: prepared and not reading yet, or held.
type arrival struct {
	handoffPane
	p *pane.Pane
	s *stopped
}

func takeover(uc *net.UnixConn, conn *proto.Conn, o Options) (*Daemon, *net.UnixListener, error) {
	if err := conn.Send(proto.Hello{Version: proto.Version, Kind: "handoff"}); err != nil {
		return nil, nil, err
	}
	if err := conn.Send(handoffRequest{Version: handoffVersion}); err != nil {
		return nil, nil, err
	}
	uc.SetReadDeadline(time.Now().Add(handoffTimeout))
	m, err := conn.Recv()
	if err != nil {
		return nil, nil, err
	}
	b, ok := m.(handoffBundle)
	if !ok {
		switch m := m.(type) {
		case handoffAbort:
			return nil, nil, fmt.Errorf("the old daemon refused: %s", m.Reason)
		case proto.Error:
			return nil, nil, fmt.Errorf("the old daemon cannot hand off: %s", m.Message)
		}
		return nil, nil, fmt.Errorf("the old daemon cannot hand off: it sent %T", m)
	}
	if b.Version != handoffVersion {
		return nil, nil, fmt.Errorf("handoff version %d, want %d", b.Version, handoffVersion)
	}
	var panes []handoffPane
	for range b.Panes {
		m, err := conn.Recv()
		if err != nil {
			return nil, nil, err
		}
		hp, ok := m.(handoffPane)
		if !ok {
			return nil, nil, fmt.Errorf("got %T, want a pane", m)
		}
		panes = append(panes, hp)
	}
	files, err := recvFDs(uc, b.FDs)
	if err != nil {
		return nil, nil, err
	}
	d := fromState(o, b.State)
	var ln *net.UnixListener
	var in []arrival
	release := func() {
		for _, f := range files {
			if f != nil {
				f.Close()
			}
		}
		for _, a := range in {
			if a.p != nil {
				a.p.Release()
			} else if c, ok := a.s.vt.(io.Closer); ok {
				c.Close()
			}
		}
		if ln != nil {
			ln.SetUnlinkOnClose(false) // the old daemon still serves the path
			ln.Close()
		}
	}
	ln, in, err = d.prepare(b, panes, files)
	if err == nil {
		err = fault("new:receive")
	}
	if err == nil {
		err = fault("new:prepare")
	}
	if err == nil {
		err = conn.Send(handoffReady{})
	}
	if err == nil {
		err = fault("new:ready")
	}
	if err == nil {
		uc.SetReadDeadline(time.Now().Add(handoffTimeout))
		err = expect[handoffCommit](conn)
	}
	if err == nil {
		err = fault("new:commit")
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	// Committed: nothing below fails.
	d.mu.Lock()
	for _, a := range in {
		size := [2]int{a.Cols, a.Rows}
		if a.s != nil {
			d.panes[a.ID], d.sizes[a.ID] = a.s, size
			continue
		}
		a.p.Start()
		d.run(a.ID, a.p, size, a.Input)
	}
	d.tidy()
	d.mu.Unlock()
	ln.SetUnlinkOnClose(true)
	uc.SetDeadline(time.Time{})
	if err := conn.Send(handoffStarted{}); err != nil {
		log.Printf("handoff: tell the old daemon: %q", err) // it exits all the same once its peer is gone
	}
	queued := 0
	for _, a := range in {
		for _, b := range a.Input {
			queued += len(b)
		}
	}
	log.Printf("handoff: took over %d panes and %d bytes of unwritten input", len(in), queued)
	return d, ln, nil
}

// prepare rebuilds the listener and panes from what came over, without
// reading any PTY. It consumes files: each it uses is set to nil.
func (d *Daemon) prepare(b handoffBundle, panes []handoffPane, files []*os.File) (*net.UnixListener, []arrival, error) {
	var in []arrival
	if len(b.Chats) > 0 {
		return nil, in, errors.New("this build cannot take over managed sessions")
	}
	if len(files) == 0 {
		return nil, in, errors.New("no listener came over")
	}
	l, err := net.FileListener(files[0])
	files[0].Close()
	files[0] = nil
	if err != nil {
		return nil, in, err
	}
	ln, ok := l.(*net.UnixListener)
	if !ok {
		l.Close()
		return nil, in, fmt.Errorf("listener is a %T", l)
	}
	ln.SetUnlinkOnClose(false)
	if len(panes) != len(d.st.Panes) {
		return ln, in, fmt.Errorf("%d panes came over for %d in the state", len(panes), len(d.st.Panes))
	}
	next := 1
	for _, hp := range panes {
		i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == hp.ID })
		if i < 0 || slices.ContainsFunc(in, func(a arrival) bool { return a.ID == hp.ID }) {
			return ln, in, fmt.Errorf("pane %s is not in the state once", hp.ID)
		}
		a := arrival{handoffPane: hp}
		if hp.PID == 0 {
			e, err := vt.Restore(hp.Snapshot, io.Discard)
			if err != nil {
				return ln, in, fmt.Errorf("pane %s: %w", hp.ID, err)
			}
			a.s = &stopped{vt: e, done: make(chan struct{}), code: hp.ExitCode}
			close(a.s.done)
			in = append(in, a)
			continue
		}
		if next >= len(files) {
			return ln, in, fmt.Errorf("no PTY master came over for pane %s", hp.ID)
		}
		var rerr error
		restore := func(cols, rows int, reply io.Writer) vt.Emulator {
			e, err := vt.Restore(hp.Snapshot, reply)
			if err != nil {
				rerr = err
				return vt.New(cols, rows, reply)
			}
			return e
		}
		cfg := pane.Config{ID: hp.ID, Cols: hp.Cols, Rows: hp.Rows, NewVT: d.paneVT(hp.ID, d.portsOf(d.st.Panes[i].WorkspaceID), restore)}
		p, err := pane.Prepare(cfg, files[next], hp.PID)
		files[next] = nil
		next++
		if err != nil {
			return ln, in, fmt.Errorf("pane %s: %w", hp.ID, err)
		}
		a.p = p
		in = append(in, a)
		if rerr != nil {
			return ln, in, fmt.Errorf("pane %s: %w", hp.ID, rerr)
		}
	}
	if next != len(files) {
		return ln, in, fmt.Errorf("%d descriptors came over, used %d", len(files), next)
	}
	return ln, in, nil
}
