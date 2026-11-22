package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

type driver struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	stdin   io.WriteCloser
	writeMu sync.Mutex // serializes stdin.Write off d.mu so Close never deadlocks on a full pipe
	emit    func(session.Event)
	stderr  *tail
	wake    chan struct{} // a post since emitLoop last looked
	eof     chan struct{} // closed once stdout is read to its end
	exited  chan struct{} // closed once the process is gone and session.exited is queued
	done    chan struct{} // closed once emitLoop has emitted everything

	mu      sync.Mutex
	s       *stream
	next    int
	pending map[string]chan rpcMessage
	queue   []session.Event
	outbox  [][]byte // lines queued under d.mu and flushed off d.mu
	sending bool     // Send is writing session/prompt; emitLoop waits so EventTurnStarted stays first
	dead    bool     // the process is gone; nothing is posted after its session.exited
	closing bool     // Close was called
}

// post queues e for emitLoop, stamped now. d.mu is held.
func (d *driver) post(e session.Event) {
	e.CreatedAt = session.Stamp(time.Now())
	d.queue = append(d.queue, e)
	d.notifyWake()
}

func (d *driver) notifyWake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// emitLoop calls emit from one goroutine, so emit may call back into the
// driver, until the process is gone and every event is out.
func (d *driver) emitLoop() {
	defer close(d.done)
	for {
		d.mu.Lock()
		if d.sending {
			d.mu.Unlock()
			<-d.wake
			continue
		}
		q, dead := d.queue, d.dead
		d.queue = nil
		d.mu.Unlock()
		for _, e := range q {
			d.emit(e)
		}
		if len(q) > 0 {
			continue
		}
		if dead {
			return
		}
		<-d.wake
	}
}

func encodeLine(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// writeBytes sends one encoded JSON line to the CLI without holding d.mu.
func (d *driver) writeBytes(b []byte) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if _, err := d.stdin.Write(b); err != nil {
		return session.ErrClosed
	}
	return nil
}

// queueWrite appends an encoded JSON line to be written once d.mu is released.
// d.mu is held.
func (d *driver) queueWrite(v any) {
	if b, err := encodeLine(v); err == nil {
		d.outbox = append(d.outbox, b)
	}
}

// flushWrites writes any queued outbox lines without holding d.mu.
func (d *driver) flushWrites() error {
	d.mu.Lock()
	lines := d.outbox
	d.outbox = nil
	d.mu.Unlock()
	for _, b := range lines {
		if err := d.writeBytes(b); err != nil {
			return err
		}
	}
	return nil
}

// waitDelay is how long the driver waits, after the CLI exits, for a
// child it left behind to let go of stdout and stderr.
const waitDelay = 2 * time.Second

// read maps every stdout line to events until stdout ends.
func (d *driver) read(stdout io.Reader) {
	defer close(d.eof)
	r := bufio.NewReader(stdout)
	for {
		b, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(b)) > 0 {
			d.mu.Lock()
			for _, e := range d.line(b) {
				d.post(e)
			}
			d.mu.Unlock()
			_ = d.flushWrites()
		}
		if err != nil {
			return
		}
	}
}

// wait reports the CLI's exit once its output is read.
func (d *driver) wait(stdout io.Closer) {
	defer d.cancel()
	err := d.cmd.Wait()
	select {
	case <-d.eof:
	case <-time.After(waitDelay):
		stdout.Close()
		<-d.eof
	}
	stdout.Close()
	d.mu.Lock()
	var code *int
	if st := d.cmd.ProcessState; st != nil && st.Exited() {
		code = new(int)
		*code = st.ExitCode()
	}
	if !d.closing {
		msg := "ACP agent exited"
		if code != nil {
			msg = fmt.Sprintf("ACP agent exited (code %d)", *code)
		} else if err != nil {
			msg = "ACP agent exited: " + err.Error()
		}
		if t := d.stderr.String(); t != "" {
			msg += "\n" + t
		}
		d.post(session.Event{Type: session.EventError, Message: msg})
	}
	d.post(session.Event{Type: session.EventExited, ExitCode: code})
	d.dead = true
	for _, ch := range d.pending {
		close(ch)
	}
	d.pending = nil
	d.mu.Unlock()
	close(d.exited)
}

// PID is the provider process's id, for the Servers panel: what it
// started is the chat's.
func (d *driver) PID() int { return d.cmd.Process.Pid }

// Close closes stdin, which ends the CLI, and kills its process group when ctx
// ends first. It returns once every event is out.
func (d *driver) Close(ctx context.Context) error {
	d.mu.Lock()
	first := !d.closing
	if first {
		d.closing = true
		if !d.dead {
			d.cancelRequests()
		}
		d.outbox = nil // closing stdin ends the process; do not block Close on a stalled pipe
	}
	d.mu.Unlock()
	if first {
		d.stdin.Close()
	}
	select {
	case <-d.exited:
	case <-ctx.Done():
		d.cancel()
		<-d.exited
	}
	<-d.done
	return nil
}

// tail keeps the last 4 KB written to it, for the exit message.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - 4096; over > 0 {
		t.b = t.b[over:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
