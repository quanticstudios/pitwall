package pane

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/conpty"
	"golang.org/x/sys/windows"
)

type sys struct {
	tty *conpty.ConPty
	h   *handles
}

// handles are the process and a job holding it and its descendants, so Close
// can end every process attached to the console, not only the first.
type handles struct {
	mu   sync.Mutex // drain closes the handles while Close may be killing
	proc windows.Handle
	job  windows.Handle // 0 when the job could not be made, or once closed
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
	// ponytail: the process runs before it joins the job, so a child it
	// starts in that window escapes kill; CREATE_SUSPENDED would close the
	// window, but conpty.Spawn closes the thread handle that would resume it.
	job, err := windows.CreateJobObject(nil, nil)
	if err == nil && windows.AssignProcessToJobObject(job, windows.Handle(h)) != nil {
		windows.CloseHandle(job)
		job = 0
	}
	return sys{tty, &handles{proc: windows.Handle(h), job: job}}, nil
}

func (p *Pane) reap() int {
	windows.WaitForSingleObject(p.h.proc, windows.INFINITE)
	var code uint32
	if windows.GetExitCodeProcess(p.h.proc, &code) != nil {
		return -1
	}
	return int(code)
}

// drain closes the pseudoconsole, which flushes its last output and ends the
// read; ConPTY keeps the pipe open past the process otherwise. Processes still
// attached to it end with it.
func (p *Pane) drain() {
	p.tty.Close()
	p.h.mu.Lock()
	defer p.h.mu.Unlock()
	windows.CloseHandle(p.h.proc)
	if p.h.job != 0 {
		windows.CloseHandle(p.h.job)
	}
	p.h.proc, p.h.job = 0, 0
}

// kill terminates the process and every descendant in its job.
func (p *Pane) kill() {
	p.h.mu.Lock()
	defer p.h.mu.Unlock()
	if p.h.job != 0 {
		windows.TerminateJobObject(p.h.job, 1)
	}
	if p.h.proc != 0 {
		windows.TerminateProcess(p.h.proc, 1)
	}
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
// process and its descendants if the pane is not done 2s later.
func (p *Pane) Close() error {
	p.closeOnce.Do(func() {
		// why: closing the pseudoconsole, and closing its input pipe while a
		// write is stuck in it, wait for conhost, which waits for every
		// attached process to handle its close event or time out. The kill
		// must not wait behind that.
		go p.tty.Close()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			p.kill()
		}
	})
	<-p.done
	return nil
}
