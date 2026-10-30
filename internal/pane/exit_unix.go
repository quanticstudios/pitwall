//go:build unix

package pane

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// exitWaiter observes termination and returns an exit code, or -1 for an
// unavailable status or a signal. Only a parent also reaps the process.
type exitWaiter interface {
	Wait() (int, error)
}

type exitWatcher interface {
	exitWaiter
	Close() error
}

type commandExit struct{ cmd *exec.Cmd }

func (w commandExit) Wait() (int, error) {
	err := w.cmd.Wait()
	if w.cmd.ProcessState == nil {
		return -1, err
	}
	return w.cmd.ProcessState.ExitCode(), nil
}

// Adopt takes ownership of a transferred PTY master and watches its existing
// process, which need not be our child. It consumes master even on failure.
// Register before allowing the old daemon to release the child: macOS proc
// events only cover exits after registration. Config.Cmd and Env are ignored;
// the caller restores any emulator state separately through Config.NewVT.
func Adopt(c Config, master *os.File, pid int) (*Pane, error) {
	p, err := Prepare(c, master, pid)
	if err != nil {
		return nil, err
	}
	p.Start()
	return p, nil
}

// Prepare is Adopt without reading: the exit watcher is registered and the
// emulator built, and nothing reads the PTY until Start. Release gives up a
// pane that was never started.
func Prepare(c Config, master *os.File, pid int) (*Pane, error) {
	if master == nil {
		return nil, fmt.Errorf("adopt pane: nil PTY master")
	}
	if pid <= 0 {
		master.Close()
		return nil, fmt.Errorf("adopt pane: invalid PID %d", pid)
	}
	w, err := watchExit(pid)
	if err != nil {
		master.Close()
		return nil, fmt.Errorf("adopt pane %d: %w", pid, err)
	}
	tty, err := pollable(master)
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("adopt pane %d: %w", pid, err)
	}
	return prepared(c, sys{tty: tty, pid: pid, waiter: w}), nil
}

// Release closes a pane from Prepare that was never started: its PTY master,
// exit watcher and emulator. It never signals the process.
func (p *Pane) Release() {
	if w, ok := p.waiter.(exitWatcher); ok {
		w.Close()
	}
	p.tty.Close()
	if c, ok := p.vt.(io.Closer); ok {
		c.Close()
	}
}

// waitChild collects status if reparenting made us the parent. ECHILD means
// init or another subreaper owns reaping; watching never changes parentage.
func waitChild(pid int) (int, error) {
	var status unix.WaitStatus
	for {
		_, err := unix.Wait4(pid, &status, 0, nil)
		if err == unix.EINTR {
			continue
		}
		return status.ExitStatus(), err
	}
}
