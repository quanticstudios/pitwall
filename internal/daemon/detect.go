package daemon

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Status for panes no hook reports on, read on the liveness poll: an agent
// started without hooks gets its state from its screen, and a shell running
// a command, or a pane opened with one, gets terminal-running with the
// command's name.

// Process reads behind detection; tests replace them. Only stat, comm, the
// exe link and the children lists are read: cmdline and the environment carry
// secrets.
var (
	sessionOf = procSession
	identify  = procIdentify
	commOf    = readComm
)

// shellComm is the comm of the shell a pane without a command starts. A
// session leader with another comm exec'd something else.
var shellComm = pathComm(pane.Shell())

// shells are the programs a pane's command may name and still be a shell,
// which at its prompt is no terminal command.
var shells = []string{"sh", "bash", "zsh", "fish", "dash", "ksh", "mksh", "tcsh", "csh", "nu", "elvish", "xonsh"}

// pathComm is the comm the kernel gives a program exec'd from path: its base,
// cut to 15 bytes.
func pathComm(path string) string {
	b := filepath.Base(path)
	return b[:min(len(b), 15)]
}

// paneShell is the comm of the shell a pane runs, or "" when its command is
// no shell.
func paneShell(cmd []string) string {
	if len(cmd) == 0 {
		return shellComm
	}
	if c := pathComm(cmd[0]); c == shellComm || slices.Contains(shells, c) {
		return c
	}
	return ""
}

// detected is what the poll keeps per pane. Guarded by d.mu.
type detected struct {
	sid   int       // the pane's session id, which is the pid of its first process
	cells []vt.Cell // the agent's screen at the last poll, to see it still
}

// look is one pane's poll, read under d.mu and run outside it.
type look struct {
	id     string
	p      Pane
	shell  string // the comm of the shell the pane runs, or "" for a command
	hooked bool   // a hook has reported from the pane: hooks own its agent
	hookFg int    // the foreground group of the last hook
	sid    int
}

// detect polls every live pane. A pane whose foreground is its own shell
// costs one TIOCGPGRP ioctl, a comm read and a cwd readlink; any other foreground adds an exe
// read, and a detected agent without hooks adds a screen snapshot.
func (d *Daemon) detect(ctx context.Context) {
	d.mu.Lock()
	var looks []look
	for _, sp := range d.st.Panes {
		p := d.panes[sp.ID]
		if _, ok := p.(foregrounder); !ok || sp.Exited {
			continue
		}
		l := look{id: sp.ID, p: p, shell: paneShell(sp.Cmd), hooked: !d.live.hookAt[sp.ID].IsZero(), hookFg: d.live.fg[sp.ID]}
		if det := d.live.det[sp.ID]; det != nil {
			l.sid = det.sid
		}
		looks = append(looks, l)
	}
	d.mu.Unlock()
	for _, l := range looks {
		d.lookAt(ctx, l)
	}
}

func (d *Daemon) lookAt(ctx context.Context, l look) {
	fg := l.p.(foregrounder).Foreground()
	if fg <= 0 {
		return
	}
	if l.sid == 0 {
		l.sid = sessionOf(fg) // every process on the terminal shares it
	}
	own := fg == l.sid
	var prov model.Provider
	var comm string
	var g vt.Grid
	switch {
	case own && l.shell != "" && commOf(fg) == l.shell: // the shell at its prompt, not exec'd into something else
	case l.hooked && fg == l.hookFg:
		return // the hooked agent: hooks own it
	default:
		prov, comm = identify(fg)
		if prov != "" && !l.hooked {
			g = l.p.Snapshot()
		}
	}
	var cwd string
	if own && l.shell != "" && prov == "" {
		cwd = l.p.Cwd() // the shell's directory names its tab
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if hooked := !d.live.hookAt[l.id].IsZero(); d.closing || d.panes[l.id] != l.p || hooked != l.hooked {
		return // closed, or a hook came during the read
	}
	if d.live.det == nil {
		d.live.det = map[string]*detected{}
	}
	det := d.live.det[l.id]
	if det == nil {
		det = &detected{}
		d.live.det[l.id] = det
	}
	det.sid = l.sid
	var prev model.AgentState
	if i := d.activityIndex(l.id); i >= 0 {
		a := d.st.Activities[i]
		if l.hooked && a.Provider != model.ProviderTerminal {
			return // a hook's activity, which only hooks and the exit check end
		}
		prev = a.State
	}
	d.setProvider(l.id, prov) // the foreground's agent, idle or busy; "" for a shell or a command
	switch {
	case prov != "" && l.hooked:
		// Another agent took the foreground after this poll's exit check:
		// never a terminal command, and the next poll reads its screen.
		d.setActivity(ctx, l.id, "", "", "")
	case prov != "":
		s := agent.State(g)
		if s == "" && prev == model.StateWorking && !slices.Equal(det.cells, g.Cells) {
			s = model.StateWorking // a reply streaming with the spinner hidden
		} else if s == "" && (prev == model.StateWorking || prev == model.StateCompleted) {
			s = model.StateCompleted
		}
		det.cells = g.Cells
		d.setActivity(ctx, l.id, prov, s, "")
	case !own || l.shell == "": // a job, or the command the pane was opened with
		det.cells = nil
		d.setActivity(ctx, l.id, model.ProviderTerminal, model.StateTerminalRunning, comm)
	default:
		det.cells = nil
		d.setActivity(ctx, l.id, "", "", "")
		if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == l.id }); i >= 0 && cwd != "" && d.st.Panes[i].Cwd != cwd {
			d.st.Panes[i].Cwd = cwd
			d.changed()
			go d.refreshStats(ctx, d.st.Panes[i].WorkspaceID) // a cd can change the repo
		}
	}
}

// setProvider records which agent runs in a pane's foreground now, ""
// for none, pushing state only on a change. Callers hold d.mu.
func (d *Daemon) setProvider(id string, prov model.Provider) {
	if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }); i >= 0 && d.st.Panes[i].Provider != prov {
		d.st.Panes[i].Provider = prov
		d.changed()
	}
}

// setActivity makes a pane's activity the given one, or drops it when state is
// "". An agent's provider is also recorded on the pane. Callers hold d.mu.
func (d *Daemon) setActivity(ctx context.Context, id string, prov model.Provider, state model.AgentState, detail string) {
	if state == "" {
		d.dropActivity(id)
		return
	}
	pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if pi < 0 {
		return
	}
	p := &d.st.Panes[pi]
	now := time.Now()
	next := model.Activity{PaneID: id, WorkspaceID: p.WorkspaceID, Provider: prov, State: state, Detail: detail, UpdatedAt: now}
	i := d.activityIndex(id)
	switch {
	case i < 0:
		d.st.Activities = append(d.st.Activities, next)
	case d.st.Activities[i].Provider == prov && d.st.Activities[i].State == state && d.st.Activities[i].Detail == detail:
		return
	default:
		d.st.Activities[i] = next
	}
	if prov != model.ProviderTerminal {
		p.Provider = prov
		if w := d.workspace(p.WorkspaceID); w != nil {
			w.UpdatedAt = now
		}
		if state == model.StateCompleted {
			go d.refreshStats(ctx, p.WorkspaceID)
		}
	}
	d.changed()
}

// groupWalk bounds the processes read behind a foreground leader that is no
// agent. A wrapper has one child; the bound keeps a build's tree from being
// read every second.
const groupWalk = 16
