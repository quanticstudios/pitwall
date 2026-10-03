package pane

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/charmbracelet/x/conpty"
	"golang.org/x/sys/windows"
)

type sys struct {
	tty  *conpty.ConPty
	proc windows.Handle
}

func spawn(c Config, argv, env []string) (sys, error) {
	name, err := exec.LookPath(argv[0])
	if err != nil {
		return sys{}, fmt.Errorf("start %s: %w", argv[0], err)
	}
	tty, err := conpty.New(c.Cols, c.Rows, 0)
	if err != nil {
		return sys{}, fmt.Errorf("start %s: %w", argv[0], err)
	}
	_, h, err := tty.Spawn(name, argv, &syscall.ProcAttr{Dir: c.Cwd, Env: env})
	if err != nil {
		tty.Close()
		return sys{}, fmt.Errorf("start %s: %w", argv[0], err)
	}
	return sys{tty, windows.Handle(h)}, nil
}

func (p *Pane) reap() int {
	windows.WaitForSingleObject(p.proc, windows.INFINITE)
	var code uint32
	if windows.GetExitCodeProcess(p.proc, &code) != nil {
		return -1
	}
	return int(code)
}

// drain closes the pseudoconsole, which flushes its last output and ends the
// read; ConPTY keeps the pipe open past the process otherwise. Processes still
// attached to it end with it.
func (p *Pane) drain() {
	p.tty.Close()
	windows.CloseHandle(p.proc)
}

func (p *Pane) setSize(cols, rows int) error { return p.tty.Resize(cols, rows) }

// Cwd is "" on Windows: another process's directory lives in its PEB, which
// takes more than this is worth. Tabs keep the directory they opened in.
func (p *Pane) Cwd() string { return "" }

// foreground is 0 on Windows, which has no foreground process group: agent
// status there comes from hooks only.
func (p *Pane) foreground() int { return 0 }

// Close closes the pseudoconsole, which ends its processes, and terminates the
// process if it is still running 2s later.
func (p *Pane) Close() error {
	p.closeOnce.Do(func() {
		select {
		case <-p.exited:
			return
		default:
		}
		p.tty.Close()
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			windows.TerminateProcess(p.proc, 1)
		}
	})
	<-p.done
	return nil
}
