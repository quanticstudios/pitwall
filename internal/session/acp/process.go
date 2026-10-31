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
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	emit   func(session.Event)
	stderr *tail
	wake   chan struct{} // a post since emitLoop last looked
	eof    chan struct{} // closed once stdout is read to its end
	exited chan struct{} // closed once the process is gone and session.exited is queued
	done   chan struct{} // closed once emitLoop has emitted everything

	mu      sync.Mutex
	s       *stream
	next    int
	pending map[string]chan rpcMessage
	queue   []session.Event
	dead    bool // the process is gone; nothing is posted after its session.exited
	closing bool // Close was called
}

// post queues e for emitLoop, stamped now. d.mu is held.
func (d *driver) post(e session.Event) {
	e.CreatedAt = session.Stamp(time.Now())
	d.queue = append(d.queue, e)
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

// write sends one JSON line to the CLI. d.mu is held.
func (d *driver) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := d.stdin.Write(append(b, '\n')); err != nil {
		return session.ErrClosed
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
		}
		if err != nil {
			return
		}
	}
}

// wait reports the CLI's exit once its output is read.
func (d *driver) wait(stdout io.Closer) {
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

// Close closes stdin, which ends the CLI, and kills it when ctx ends
// first. It returns once every event is out.
func (d *driver) Close(ctx context.Context) error {
	d.mu.Lock()
	if !d.closing {
		d.closing = true
		if !d.dead {
			d.cancelPermissions()
		}
		d.stdin.Close()
	}
	d.mu.Unlock()
	select {
	case <-d.exited:
	case <-ctx.Done():
		d.cmd.Process.Kill()
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
