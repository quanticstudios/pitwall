//go:build unix

package pane

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type sys struct {
	tty    *os.File // the PTY master
	pid    int
	waiter exitWaiter
}

// spawnAttempts bounds retries of a start the kernel refused with EPERM.
const spawnAttempts = 3

// Adapted from tuios (MIT): internal/ptyspawn/spawn.go
// The controlling-terminal grab (TIOCSCTTY) sometimes fails with EPERM when a
// just-freed pts index is recycled while its old session is still being torn
// down. A fresh pty a few milliseconds later succeeds, so retry, bounded, and
// never on any other error.
func spawn(c Config, argv, env []string) (sys, error) {
	for attempt := 1; ; attempt++ {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = c.Cwd
		cmd.Env = env
		// pty.StartWithAttrs puts the slave on fd 0, so Ctty 0 is the one to claim.
		attrs := &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
		ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Cols: uint16(c.Cols), Rows: uint16(c.Rows)}, attrs)
		if err == nil {
			if ptmx, err = pollable(ptmx); err != nil {
				cmd.Process.Kill()
				cmd.Wait()
				return sys{}, fmt.Errorf("start %s: %w", argv[0], err)
			}
			return sys{tty: ptmx, pid: cmd.Process.Pid, waiter: commandExit{cmd}}, nil
		}
		if attempt < spawnAttempts && errors.Is(err, syscall.EPERM) {
			time.Sleep(time.Duration(attempt) * time.Millisecond)
			continue
		}
		return sys{}, fmt.Errorf("start %s: %w", argv[0], err)
	}
}

// pollable swaps the master for a non-blocking duplicate on Go's poller.
// creack/pty calls File.Fd, which leaves the master blocking, and a blocking
// read ignores deadlines.
func pollable(f *os.File) (*os.File, error) {
	defer f.Close()
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

func (p *Pane) reap() int {
	code, err := p.waiter.Wait()
	if err != nil {
		// The kernel watcher failed. PTY EOF still proves that all slave
		// holders have gone, but carries no numeric exit status.
		<-p.readDone
		return -1
	}
	return code
}

// drain bounds the read that follows the exit. A paused pane keeps its
// pause deadline; Resume sets this one.
func (p *Pane) drain() {
	p.hold.Lock()
	defer p.hold.Unlock()
	p.drainAt = time.Now().Add(drainTimeout)
	if p.paused == nil {
		p.tty.SetReadDeadline(p.drainAt)
	}
}

// Pause stops the pane for a handoff. It returns once the reader is between
// two reads, so the emulator holds every byte read and the kernel buffer
// the rest. A Write blocked on a program that does not read its input
// returns with what it wrote. Resume undoes it.
func (p *Pane) Pause() {
	p.hold.Lock()
	if p.paused != nil {
		p.hold.Unlock()
		return
	}
	p.paused = make(chan struct{})
	past := time.Unix(1, 0)
	p.tty.SetReadDeadline(past)
	p.tty.SetWriteDeadline(past)
	p.hold.Unlock()
	select {
	case <-p.parked:
	case <-p.readDone:
	}
}

// Resume reads and writes again after Pause.
func (p *Pane) Resume() {
	p.hold.Lock()
	defer p.hold.Unlock()
	if p.paused == nil {
		return
	}
	p.tty.SetReadDeadline(p.drainAt)
	p.tty.SetWriteDeadline(time.Time{})
	close(p.paused)
	p.paused = nil
}

// Master duplicates the PTY master of a running pane for a handoff and
// returns its process's PID. Pause the pane first. The caller closes the
// copy.
func (p *Pane) Master() (*os.File, int, error) {
	select {
	case <-p.exited:
		return nil, 0, fmt.Errorf("pane process %d exited", p.pid)
	default:
	}
	var dup int
	err := p.control(func(fd int) (err error) {
		dup, err = unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		return err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("duplicate PTY master: %w", err)
	}
	return os.NewFile(uintptr(dup), p.tty.Name()), p.pid, nil
}

func (p *Pane) setSize(cols, rows int) error {
	return p.control(func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
	})
}

// Cwd is the live working directory of the foreground process.
func (p *Pane) Cwd() string {
	if pg := p.foreground(); pg > 0 {
		if cwd := procCwd(pg); cwd != "" {
			return cwd
		}
	}
	return procCwd(p.pid)
}

// foreground is the PTY's foreground process group, or 0 if unknown.
func (p *Pane) foreground() int {
	var pgid int
	if p.control(func(fd int) (err error) { pgid, err = unix.IoctlGetInt(fd, unix.TIOCGPGRP); return }) != nil {
		return 0
	}
	return pgid
}

// control runs f on the master's fd through SyscallConn. File.Fd, which
// creack/pty's helpers call, would put the master back in blocking mode, and
// a blocking read ignores the deadline wait sets.
func (p *Pane) control(f func(fd int) error) error {
	rc, err := p.tty.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = f(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

// Close sends SIGHUP and kills the process group after 2s. It returns once the
// process has exited and the master closed; a process that left the group and
// still holds the slave is left running.
func (p *Pane) Close() error {
	p.closeOnce.Do(func() {
		pg := -p.pid // Setsid made the child a group leader
		select {
		case <-p.exited:
			return
		default:
		}
		syscall.Kill(pg, syscall.SIGHUP)
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			// A shell runs its foreground job in a group of its own.
			if fg := p.foreground(); fg > 0 {
				syscall.Kill(-fg, syscall.SIGKILL)
			}
			syscall.Kill(pg, syscall.SIGKILL)
		}
	})
	<-p.done
	return nil
}
