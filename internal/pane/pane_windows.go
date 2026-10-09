package pane

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	if len(c.Cmd) == 0 {
		argv, env = reportCwd(argv, env)
	}
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

// Cwd is the folder the shell's prompt last reported with OSC 7 (see
// reportCwd), "" before its first prompt or for a shell that reports none.
// Another process's own directory lives in its PEB, which takes more than
// this is worth.
func (p *Pane) Cwd() string {
	if c, ok := p.vt.(interface{ Cwd() string }); ok {
		return c.Cwd()
	}
	return ""
}

// psPrompt wraps PowerShell's prompt function, after the profile set it,
// to print OSC 7 with the folder before the prompt.
const psPrompt = `$global:__pitwallPrompt = $function:prompt
function global:prompt {
	$p = & $global:__pitwallPrompt
	$l = $executionContext.SessionState.Path.CurrentLocation
	if ($l.Provider.Name -eq 'FileSystem') { "$([char]27)]7;$(([uri]$l.ProviderPath).AbsoluteUri)$([char]27)\" + $p } else { $p }
}`

// reportCwd has the shell a pane opens with print its folder at every
// prompt: PowerShell through psPrompt, run with -NoExit after the profile,
// cmd through PROMPT. Other shells, Git Bash among them, are left as they
// are; they report a folder only if the user's prompt prints OSC 7.
func reportCwd(argv, env []string) ([]string, []string) {
	switch strings.ToLower(strings.TrimSuffix(filepath.Base(argv[0]), filepath.Ext(argv[0]))) {
	case "pwsh", "powershell":
		if len(argv) == 1 {
			argv = append(argv, "-NoExit", "-Command", psPrompt)
		}
	case "cmd":
		prompt := "$P$G" // cmd's default
		env = slices.DeleteFunc(env, func(kv string) bool {
			k, v, _ := strings.Cut(kv, "=")
			if strings.EqualFold(k, "PROMPT") {
				prompt = v
				return true
			}
			return false
		})
		env = append(env, `PROMPT=$E]7;file://localhost/$P$E\`+prompt)
	}
	return argv, env
}

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
