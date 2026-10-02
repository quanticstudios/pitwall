// Package pane runs one process in a PTY and feeds its output to a vt
// emulator.
package pane

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/creack/pty"

	"github.com/quanticstudios/pitwall/internal/vt"
)

type Config struct {
	ID         string
	Cmd        []string // empty means $SHELL
	Cwd        string
	Env        []string // added on top of os.Environ(); Start also sets PITWALL_PANE, TERM, COLORTERM
	Cols, Rows int
	NewVT      vt.NewFunc
}

type Pane struct {
	ptmx *os.File
	cmd  *exec.Cmd

	mu sync.Mutex // guards vt: the reader writes while clients snapshot and resize
	vt vt.Emulator

	dirty     chan struct{}
	done      chan struct{}
	exit      int // written before done closes
	closeOnce sync.Once
}

func Start(c Config) (*Pane, error) {
	argv := c.Cmd
	if len(argv) == 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		argv = []string{sh}
	}
	env := append(environ(), c.Env...)
	env = append(env, "PITWALL_PANE="+c.ID, "TERM=xterm-256color", "COLORTERM=truecolor")

	ptmx, cmd, err := spawn(c, argv, env)
	if err != nil {
		return nil, err
	}
	p := &Pane{
		ptmx:  ptmx,
		cmd:   cmd,
		vt:    c.NewVT(c.Cols, c.Rows, ptmx),
		dirty: make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	go p.read()
	return p, nil
}

// spawnAttempts bounds retries of a start the kernel refused with EPERM.
const spawnAttempts = 3

// Adapted from tuios (MIT): internal/ptyspawn/spawn.go
// The controlling-terminal grab (TIOCSCTTY) sometimes fails with EPERM when a
// just-freed pts index is recycled while its old session is still being torn
// down. A fresh pty a few milliseconds later succeeds, so retry, bounded, and
// never on any other error.
func spawn(c Config, argv, env []string) (*os.File, *exec.Cmd, error) {
	for attempt := 1; ; attempt++ {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = c.Cwd
		cmd.Env = env
		// pty.StartWithAttrs puts the slave on fd 0, so Ctty 0 is the one to claim.
		attrs := &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
		ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Cols: uint16(c.Cols), Rows: uint16(c.Rows)}, attrs)
		if err == nil {
			return ptmx, cmd, nil
		}
		if attempt < spawnAttempts && errors.Is(err, syscall.EPERM) {
			time.Sleep(time.Duration(attempt) * time.Millisecond)
			continue
		}
		return nil, nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
}

// environ is os.Environ() without the variables that make nested tools think
// they already run inside tmux or zellij.
func environ() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "TMUX" || k == "TMUX_PANE" || strings.HasPrefix(k, "ZELLIJ") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (p *Pane) read() {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.vt.Write(buf[:n])
			p.mu.Unlock()
			select {
			case p.dirty <- struct{}{}:
			default:
			}
		}
		if err != nil {
			break // EIO once every holder of the slave is gone
		}
	}
	p.cmd.Wait()
	p.exit = p.cmd.ProcessState.ExitCode()
	p.ptmx.Close()
	close(p.done)
}

// Write sends input bytes to the process.
func (p *Pane) Write(b []byte) (int, error) { return p.ptmx.Write(b) }

// Size limits for Resize: a side fits the kernel's uint16 and the cell count
// bounds the emulator's memory.
const (
	maxSide  = 1000
	maxCells = 500_000
)

// Resize sets the kernel and emulator sizes under one lock, so concurrent
// calls cannot leave the two disagreeing.
func (p *Pane) Resize(cols, rows int) error {
	if cols < 1 || rows < 1 || cols > maxSide || rows > maxSide || cols*rows > maxCells {
		return fmt.Errorf("pane size %dx%d out of range", cols, rows)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ws := pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}
	if err := p.ioctl(syscall.TIOCSWINSZ, unsafe.Pointer(&ws)); err != nil {
		return err
	}
	p.vt.Resize(cols, rows)
	return nil
}

func (p *Pane) Snapshot() vt.Grid {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vt.Snapshot()
}

func (p *Pane) SnapshotAt(off int) vt.Grid {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vt.SnapshotAt(off)
}

func (p *Pane) ScrollbackLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vt.ScrollbackLen()
}

func (p *Pane) ScrollbackPushed() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vt.ScrollbackPushed()
}

func (p *Pane) Modes() vt.Modes {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vt.Modes()
}

// Dirty has capacity 1 and is signalled after output lands in the emulator.
func (p *Pane) Dirty() <-chan struct{} { return p.dirty }
func (p *Pane) Done() <-chan struct{}  { return p.done }

// ExitCode is the process's exit status once Done is closed, and -1 before
// that or when a signal killed it.
func (p *Pane) ExitCode() int {
	select {
	case <-p.done:
		return p.exit
	default:
		return -1
	}
}

// Cwd is the live working directory of the foreground process.
func (p *Pane) Cwd() string {
	if pg := p.foreground(); pg > 0 {
		if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pg)); err == nil {
			return cwd
		}
	}
	cwd, _ := os.Readlink(fmt.Sprintf("/proc/%d/cwd", p.cmd.Process.Pid))
	return cwd
}

// foreground is the PTY's foreground process group, or 0 if unknown.
func (p *Pane) foreground() int {
	var pgid int32
	if p.ioctl(syscall.TIOCGPGRP, unsafe.Pointer(&pgid)) != nil {
		return 0
	}
	return int(pgid)
}

// ioctl runs req on the master through SyscallConn. File.Fd, which
// creack/pty's helpers call, would put the master back in blocking mode, and
// a blocking read ignores the deadline wait sets.
func (p *Pane) ioctl(req uintptr, arg unsafe.Pointer) error {
	rc, err := p.ptmx.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// Close sends SIGHUP and kills the process group after 2s. It returns once the
// process is reaped.
// ponytail: a process that left the session's process group and still holds
// the slave keeps the reader, and so Close, waiting until it exits.
func (p *Pane) Close() error {
	p.closeOnce.Do(func() {
		pg := -p.cmd.Process.Pid // Setsid made the child a group leader
		select {
		case <-p.done:
			return
		default:
		}
		syscall.Kill(pg, syscall.SIGHUP)
		select {
		case <-p.done:
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
