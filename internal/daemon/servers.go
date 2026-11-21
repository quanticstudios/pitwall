package daemon

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ports"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// The seams the Servers panel reads the system through; tests swap them.
var (
	procTable   = ports.Procs
	listenPorts = ports.Listening
	procCwd     = ports.Cwd
	signalProc  = ports.Signal
	procRunning = ports.Running
	procExe     = exeOf // the executable, which names an agent whose comm is its version
)

// Stopping waits stopGrace after SIGTERM before the window asks to force
// it, looking every stopPoll; tests shorten them.
var (
	stopGrace = 3 * time.Second
	stopPoll  = 250 * time.Millisecond
)

// serverScan is what one look at the system found: the servers by tab, the
// process table they came from, and the processes Stop never signals as a
// group with a server (the daemon, the panes' leaders, the agents).
type serverScan struct {
	servers map[string][]model.Server
	table   ports.Table
	keep    map[int]bool
}

// scanServers finds every tab's servers: the processes under its panes'
// leaders, in their sessions, and under its chat's agent process, that
// listen on a loopback or wildcard TCP port. Agents' own ports are left
// out. A Codex app-server that several Codex TUIs share runs every turn's
// commands outside the panes, so its servers go to the one Codex pane
// whose folder holds theirs.
func (d *Daemon) scanServers() serverScan {
	type paneRoot struct {
		tab, cwd string
		fg       int
		agent    bool
		codex    bool
	}
	type livePane struct {
		f    foregrounder
		lp   Pane
		tab  string
		prov model.Provider
	}
	type liveChat struct {
		c   *chat
		tab string
	}
	// The panes and drivers are asked outside d.mu, as codexRoute asks them.
	d.mu.Lock()
	var lps []livePane
	for _, p := range d.st.Panes {
		lp := d.panes[p.ID]
		if f, ok := lp.(foregrounder); ok && !p.Exited {
			lps = append(lps, livePane{f, lp, p.WorkspaceID, p.Provider})
		}
	}
	var lcs []liveChat
	for _, c := range d.chats {
		if i := slices.IndexFunc(d.st.Chats, func(m model.Chat) bool { return m.ID == c.id }); i >= 0 {
			lcs = append(lcs, liveChat{c, d.st.Chats[i].WorkspaceID})
		}
	}
	d.mu.Unlock()
	var prs []paneRoot
	for _, p := range lps {
		if fg := p.f.Foreground(); fg > 0 {
			prs = append(prs, paneRoot{tab: p.tab, cwd: p.lp.Cwd(), fg: fg, agent: p.prov != "", codex: p.prov == model.ProviderCodex})
		}
	}
	var roots []ports.Root
	for _, c := range lcs {
		if pid := c.c.pid(); pid > 0 {
			roots = append(roots, ports.Root{Tab: c.tab, PID: pid, Agent: true})
		}
	}
	out := serverScan{keep: map[int]bool{os.Getpid(): true}}
	if len(prs) == 0 && len(roots) == 0 {
		return out
	}
	t, err := procTable()
	if err != nil {
		return out
	}
	out.table = t
	self := os.Getpid()
	// A chat's agent process is the daemon's child; a pid that is not is
	// one the agent's exit freed for another process.
	roots = slices.DeleteFunc(roots, func(r ports.Root) bool { return t[r.PID].PPID != self })
	fgs := map[int]string{}
	for _, p := range prs {
		lead := t[p.fg].SID
		if lead <= 1 || lead == self {
			continue
		}
		roots = append(roots, ports.Root{Tab: p.tab, PID: lead, Session: true, Agent: p.agent})
		out.keep[lead] = true
		fgs[p.fg] = p.tab
	}
	for _, r := range roots {
		out.keep[r.PID] = true
	}
	agents := map[int]bool{}
	isAgent := func(p ports.Proc) bool {
		v, ok := agents[p.PID]
		if !ok {
			v = agent.Identify(p.Name, procExe(p.PID)) != ""
			agents[p.PID] = v
		}
		return v
	}
	var loose []int
	for pid, p := range t {
		// A chat's own app-server is the daemon's child, and its root.
		if p.Name == "codex" && p.PPID != self && isServer(pid, fgs) {
			loose = append(loose, pid)
		}
	}
	place := func(pid int) string {
		cwd := procCwd(pid)
		if !filepath.IsAbs(cwd) {
			return ""
		}
		found := ""
		for _, p := range prs {
			if !p.codex || !within(p.cwd, cwd) {
				continue
			}
			if found != "" && found != p.tab {
				return "" // two Codex tabs over that folder: nothing tells them apart
			}
			found = p.tab
		}
		return found
	}
	skip := func(p ports.Proc) bool { return p.PID == self || isAgent(p) }
	out.servers = ports.Find(t, roots, loose, place, skip, listenPorts)
	d.srvSeen.keep(t, out.servers)
	return out
}

// seenServers remembers which tab each server found was in. A server
// whose launching shell exits is reparented out of the tree that found
// it; one in a pane's session stays there, but one a chat's agent or the
// shared Codex app-server started does not. While the same pid runs under
// the same name it stays its tab's.
type seenServers struct {
	mu  sync.Mutex
	tab map[int]seenServer
}

type seenServer struct {
	tab, name string
	agent     bool
}

// keep adds to found the servers seen before that t still runs and found
// lacks, when they still listen, and remembers found's for next time.
func (s *seenServers) keep(t ports.Table, found map[string][]model.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := map[int]bool{}
	for _, ss := range found {
		for _, sv := range ss {
			in[sv.PID] = true
		}
	}
	var lost []int
	for pid, was := range s.tab {
		switch p, ok := t[pid]; {
		case !ok || p.Name != was.name:
			delete(s.tab, pid) // gone, or the pid went to another program
		case !in[pid]:
			lost = append(lost, pid)
		}
	}
	if len(lost) > 0 {
		slices.Sort(lost)
		for pid, ps := range listenPorts(lost) {
			if was := s.tab[pid]; len(ps) > 0 {
				found[was.tab] = append(found[was.tab], model.Server{PID: pid, Name: was.name, Ports: ps, Agent: was.agent})
				slices.SortFunc(found[was.tab], func(a, b model.Server) int { return a.Ports[0] - b.Ports[0] })
				in[pid] = true
			}
		}
		for _, pid := range lost {
			if !in[pid] {
				delete(s.tab, pid) // it stopped listening
			}
		}
	}
	if s.tab == nil {
		s.tab = map[int]seenServer{}
	}
	for tab, ss := range found {
		for _, sv := range ss {
			s.tab[sv.PID] = seenServer{tab: tab, name: sv.Name, agent: sv.Agent}
		}
	}
}

// pid is c's driver's process, 0 for none.
func (c *chat) pid() int {
	if p := c.procOf.Load(); p != nil {
		return p.p.PID()
	}
	return 0
}

// pider is a driver that names its process.
type pider struct{ p interface{ PID() int } }

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkServers sets State.Servers from a scan, and sends a change only when
// some tab's servers changed.
func (d *Daemon) checkServers() {
	got := d.scanServers().servers
	d.mu.Lock()
	defer d.mu.Unlock()
	maps.DeleteFunc(got, func(id string, _ []model.Server) bool { return d.workspace(id) == nil }) // closed while scanned
	if len(got) == 0 {
		got = nil
	}
	if maps.EqualFunc(got, d.st.Servers, func(a, b []model.Server) bool { return slices.EqualFunc(a, b, serverEqual) }) {
		return
	}
	d.st.Servers = got
	d.changed()
}

// serverEqual is == for a model.Server.
func serverEqual(a, b model.Server) bool {
	return a.PID == b.PID && a.Name == b.Name && a.Agent == b.Agent && slices.Equal(a.Ports, b.Ports)
}

// serverStop answers m: it signals m.PID only when a scan now lists it for
// m.Workspace on m.Port, the pid coming from a client. SIGTERM goes to the
// server's job, its process group, when that group holds no pane leader,
// agent or the daemon; else to the pid alone. It then waits up to
// stopGrace for the process to go. Force sends SIGKILL the same way.
func (d *Daemon) serverStop(ctx context.Context, m proto.ServerStop) proto.ServerStopResult {
	out := proto.ServerStopResult{Workspace: m.Workspace, PID: m.PID, Port: m.Port, Force: m.Force}
	if !ports.Supported {
		out.Err = "Stopping a server needs Linux or macOS"
		return out
	}
	scan := d.scanServers()
	listed := slices.ContainsFunc(scan.servers[m.Workspace], func(s model.Server) bool {
		return s.PID == m.PID && slices.Contains(s.Ports, m.Port)
	})
	if !listed {
		if _, ok := scan.table[m.PID]; !ok && m.PID > 0 {
			d.checkServers()
			return out // already gone
		}
		out.Err = "pitwall no longer finds that server in this tab"
		return out
	}
	// An agent in the server's group would go with it: then the pid alone.
	for pid, p := range scan.table {
		if p.PGID == scan.table[m.PID].PGID && agent.Identify(p.Name, procExe(pid)) != "" {
			scan.keep[pid] = true
		}
	}
	target, group := m.PID, false
	if g := ports.Group(scan.table, m.PID, scan.keep); g > 0 {
		target, group = g, true
	}
	if err := signalProc(target, group, m.Force); err != nil {
		out.Err = "Could not stop it: " + err.Error()
		return out
	}
	wait := stopGrace
	if m.Force {
		wait = stopGrace / 3
	}
	deadline := time.Now().Add(wait)
	for procRunning(m.PID) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return out
		case <-time.After(stopPoll):
		}
	}
	out.Running = procRunning(m.PID)
	d.checkServers() // the panel drops it now, not on the next poll
	return out
}
