//go:build unix

package pane

import (
	"fmt"
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
	return newPane(c, sys{tty: tty, pid: pid, waiter: w}), nil
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
