// Package pane runs one process in a PTY and feeds its output to a vt
// emulator.
package pane

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

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
	sys // the PTY as tty, and the process

	mu sync.Mutex // guards vt: the reader writes while clients snapshot and resize
	vt vt.Emulator

	dirty     chan struct{}
	exited    chan struct{} // closed once the process is reaped
	readDone  chan struct{}
	done      chan struct{} // closed once the process is reaped and its output read
	exit      int           // written before exited closes
	closeOnce sync.Once
}

// Shell is the program Start runs for a Config without Cmd.
func Shell() string {
	sh := os.Getenv("SHELL")
	if runtime.GOOS == "windows" {
		return windowsShell(sh, exec.LookPath)
	}
	if sh != "" {
		return sh
	}
	return "/bin/sh"
}

// windowsShell is $SHELL when it runs (Git Bash sets a POSIX path that does
// not), else the first of pwsh and Windows PowerShell on PATH, else cmd.
func windowsShell(sh string, look func(string) (string, error)) string {
	for _, s := range []string{sh, "pwsh.exe", "powershell.exe"} {
		if _, err := look(s); s != "" && err == nil {
			return s
		}
	}
	return "cmd.exe"
}

func Start(c Config) (*Pane, error) {
	argv := c.Cmd
	if len(argv) == 0 {
		argv = []string{Shell()}
	}
	env := append(environ(), c.Env...)
	env = append(env, "PITWALL_PANE="+c.ID, "TERM=xterm-256color", "COLORTERM=truecolor")

	s, err := spawn(c, argv, env)
	if err != nil {
		return nil, err
	}
	p := &Pane{
		sys:      s,
		vt:       c.NewVT(c.Cols, c.Rows, s.tty),
		dirty:    make(chan struct{}, 1),
		exited:   make(chan struct{}),
		readDone: make(chan struct{}),
		done:     make(chan struct{}),
	}
	if d, ok := p.vt.(interface{ SetDirtyFunc(func()) }); ok {
		// invariant: the callback holds the channel, not p. A method value
		// would let the emulator keep its own pane alive.
		dirty := p.dirty
		d.SetDirtyFunc(func() { signal(dirty) })
	}
	go p.read()
	go p.wait()
	return p, nil
}

// environ is os.Environ() without the variables that make nested tools think
// they already run inside tmux or zellij, and without a worktree tab's
// ports when pitwall itself started from one of its panes.
func environ() []string {
	var out []string
	_, ported := os.LookupEnv("PITWALL_PORTS")
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "TMUX" || k == "TMUX_PANE" || strings.HasPrefix(k, "ZELLIJ") ||
			k == "PITWALL_PORTS" || k == "PITWALL_PORT_BASE" || ported && k == "PORT" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// drainTimeout is how long output may still be read once the process is
// reaped. EOF normally comes first; this bounds the wait when a detached
// descendant holds the slave open.
const drainTimeout = 500 * time.Millisecond

func (p *Pane) read() {
	defer close(p.readDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := p.tty.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.vt.Write(buf[:n])
			p.mu.Unlock()
			p.signal()
		}
		if err != nil {
			return // EIO once every holder of the slave is gone, or the drain deadline
		}
	}
}

// wait reaps the process without waiting for PTY EOF, which a detached
// descendant holding the slave can delay forever.
func (p *Pane) wait() {
	p.exit = p.reap()
	close(p.exited)
	p.drain()
	<-p.readDone
	p.tty.Close()
	// Nothing reads replies once the PTY is gone, so the emulator's reply
	// goroutines stop here instead of whenever the GC frees it.
	if c, ok := p.vt.(io.Closer); ok {
		c.Close()
	}
	close(p.done)
}

func (p *Pane) signal() { signal(p.dirty) }

func signal(dirty chan struct{}) {
	select {
	case dirty <- struct{}{}:
	default:
	}
}

// Write sends input bytes to the process.
func (p *Pane) Write(b []byte) (int, error) { return p.tty.Write(b) }

// Size limits for Resize: a side fits the kernel's uint16 and the cell count
// bounds the emulator's memory.
const (
	maxSide  = 1000
	maxCells = 500_000
)

// Resize sets the kernel and emulator sizes under one lock, so concurrent
// calls cannot leave the two disagreeing. Once the process has exited only
// the emulator resizes: its PTY is closed, and the screen is still shown.
func (p *Pane) Resize(cols, rows int) error {
	if cols < 1 || rows < 1 || cols > maxSide || rows > maxSide || cols*rows > maxCells {
		return fmt.Errorf("pane size %dx%d out of range", cols, rows)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
	default:
		if err := p.setSize(cols, rows); err != nil {
			return err
		}
	}
	p.vt.Resize(cols, rows)
	p.signal() // the snapshot changed size: a program that does not redraw sends no output
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

func (p *Pane) Search(query string, limit int) ([]vt.Match, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vt.Search(query, limit)
}

func (p *Pane) Text(sel vt.Selection) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vt.Text(sel)
}

// PromptOffset is the emulator's PromptOffset; off for an emulator
// without prompt marks.
func (p *Pane) PromptOffset(off, n int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m, ok := p.vt.(interface{ PromptOffset(off, n int) int }); ok {
		return m.PromptOffset(off, n)
	}
	return off
}

func (p *Pane) Modes() vt.Modes {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.vt.Modes()
}

// Dirty has capacity 1 and is signalled after output lands in the emulator
// and after a resize.
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
