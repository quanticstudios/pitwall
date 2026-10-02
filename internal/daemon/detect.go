package daemon

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Status for panes no hook reports on, read on the liveness poll: an agent
// started without hooks gets its state from its screen, and a shell running
// a command gets terminal-running with the command's name.

// Process reads behind detection; tests replace them. Only stat, comm, the
// exe link and the children lists are read: cmdline and the environment carry
// secrets.
var (
	sessionOf = procSession
	identify  = procIdentify
)

// detected is what the poll keeps per pane. Guarded by d.mu.
type detected struct {
	sid   int       // the pane's session id, which is the pid of its first process
	cells []vt.Cell // the agent's screen at the last poll, to see it still
}

// look is one pane's poll, read under d.mu and run outside it.
type look struct {
	id     string
	p      Pane
	shell  bool // the pane runs the user's shell, not a command of its own
	hooked bool // a hook has reported from the pane: hooks own its agent
	hookFg int  // the foreground group of the last hook
	sid    int
}

// detect polls every live pane. A pane whose foreground is its own shell
// costs one TIOCGPGRP ioctl; any other foreground adds a comm and exe read,
// and a detected agent without hooks adds a screen snapshot.
func (d *Daemon) detect(ctx context.Context) {
	d.mu.Lock()
	var looks []look
	for _, sp := range d.st.Panes {
		p := d.panes[sp.ID]
		if _, ok := p.(foregrounder); !ok || sp.Exited {
			continue
		}
		l := look{id: sp.ID, p: p, shell: len(sp.Cmd) == 0, hooked: !d.live.hookAt[sp.ID].IsZero(), hookFg: d.live.fg[sp.ID]}
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
	case own && l.shell: // the shell at its prompt
	case l.hooked && fg == l.hookFg:
		return // the hooked agent: hooks own it
	default:
		prov, comm = identify(fg)
		if prov != "" && !l.hooked {
			g = l.p.Snapshot()
		}
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
	case !own:
		det.cells = nil
		d.setActivity(ctx, l.id, model.ProviderTerminal, model.StateTerminalRunning, comm)
	default:
		det.cells = nil
		d.setActivity(ctx, l.id, "", "", "")
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

// procSession is the session id of pid, or 0.
func procSession(pid int) int { return statField(pid, 6) }

// procIdentify names the agent in the foreground group pg, and returns the
// comm of its leader. A wrapper runs its agent as a child in the same group:
// node for an npm install of Codex, a launcher script that does not exec. So
// when the leader is no agent, its group is walked, at most groupWalk
// processes and three levels deep.
func procIdentify(pg int) (model.Provider, string) {
	comm := readComm(pg)
	if p := agent.Identify(comm, readExe(pg)); p != "" {
		return p, comm
	}
	n := 0
	var walk func(pid, depth int) model.Provider
	walk = func(pid, depth int) model.Provider {
		for _, c := range children(pid) {
			if n++; n > groupWalk {
				return ""
			}
			if statField(c, 5) != pg {
				continue // a background job or a daemon the leader started
			}
			if p := agent.Identify(readComm(c), readExe(c)); p != "" {
				return p
			}
			if depth < 3 {
				if p := walk(c, depth+1); p != "" {
					return p
				}
			}
		}
		return ""
	}
	return walk(pg, 1), comm
}

// groupWalk bounds the processes read behind a foreground leader that is no
// agent. A wrapper has one child; the bound keeps a build's tree from being
// read every second.
const groupWalk = 16

func readComm(pid int) string {
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	return strings.TrimSpace(string(b))
}

// Adapted from tuios (MIT): internal/session/agent_detect_linux.go
func readExe(pid int) string {
	s, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	return strings.TrimSuffix(s, " (deleted)")
}

// statField is field n of /proc/<pid>/stat as proc(5) numbers them, or 0.
// comm, field 2, may hold spaces and parentheses, so fields are counted from
// after the last ')'.
//
// Adapted from tuios (MIT): internal/session/agent_detect_linux.go
func statField(pid, n int) int {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if n < 3 || len(f) <= n-3 {
		return 0
	}
	v, _ := strconv.Atoi(f[n-3])
	return v
}

// children lists pid's children across its threads: a child is listed under
// the thread that forked it. At most 64 threads are read.
//
// Adapted from tuios (MIT): internal/session/agent_detect_linux.go
func children(pid int) []int {
	dir := "/proc/" + strconv.Itoa(pid) + "/task/"
	tids, _ := os.ReadDir(dir)
	var out []int
	for _, t := range tids[:min(len(tids), 64)] {
		b, _ := os.ReadFile(dir + t.Name() + "/children")
		for _, f := range strings.Fields(string(b)) {
			if c, err := strconv.Atoi(f); err == nil {
				out = append(out, c)
			}
		}
	}
	return out
}
