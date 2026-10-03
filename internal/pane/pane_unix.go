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
	tty *os.File // the PTY master
	cmd *exec.Cmd
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
			return sys{ptmx, cmd}, nil
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
	p.cmd.Wait()
	return p.cmd.ProcessState.ExitCode()
}

// drain bounds the read that follows the exit.
func (p *Pane) drain() { p.tty.SetReadDeadline(time.Now().Add(drainTimeout)) }

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
	return procCwd(p.cmd.Process.Pid)
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
// process is reaped and the master closed; a process that left the group and
// still holds the slave is left running.
func (p *Pane) Close() error {
	p.closeOnce.Do(func() {
		pg := -p.cmd.Process.Pid // Setsid made the child a group leader
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
