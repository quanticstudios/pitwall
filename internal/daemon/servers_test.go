package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ports"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// fgPane is a live pane with a foreground group and a folder.
type fgPane struct {
	Pane
	fg  int
	cwd string
}

func (p fgPane) Foreground() int { return p.fg }
func (p fgPane) Cwd() string     { return p.cwd }
func (p fgPane) Close() error    { return nil }

// absPath is the slash path p as an absolute path here: on a drive on
// Windows.
func absPath(p string) string {
	if runtime.GOOS == "windows" {
		return `C:` + filepath.FromSlash(p)
	}
	return p
}

type fixedPID int

func (p fixedPID) PID() int { return int(p) }

// sysFake is a process table and its sockets, and the signals sent.
type sysFake struct {
	mu      sync.Mutex
	table   ports.Table
	ports   map[int][]int
	cwd     map[int]string
	signals []string
	// ignore lists the pids SIGTERM leaves running.
	ignore map[int]bool
}

func (f *sysFake) install(t *testing.T) {
	t.Helper()
	oldT, oldL, oldC, oldS, oldR, oldE := procTable, listenPorts, procCwd, signalProc, procRunning, procExe
	oldComm, oldParent := commOf, parentOf
	oldGrace, oldPoll := stopGrace, stopPoll
	t.Cleanup(func() {
		procTable, listenPorts, procCwd, signalProc, procRunning, procExe = oldT, oldL, oldC, oldS, oldR, oldE
		commOf, parentOf = oldComm, oldParent
		stopGrace, stopPoll = oldGrace, oldPoll
	})
	stopGrace, stopPoll = 60*time.Millisecond, 5*time.Millisecond
	procTable = func() (ports.Table, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := ports.Table{}
		for k, v := range f.table {
			out[k] = v
		}
		return out, nil
	}
	listenPorts = func(pids []int) map[int][]int {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := map[int][]int{}
		for _, p := range pids {
			if ps := f.ports[p]; len(ps) > 0 {
				out[p] = slices.Clone(ps)
			}
		}
		return out
	}
	procCwd = func(pid int) string { f.mu.Lock(); defer f.mu.Unlock(); return f.cwd[pid] }
	procExe = func(int) string { return "" }
	commOf = func(pid int) string { f.mu.Lock(); defer f.mu.Unlock(); return f.table[pid].Name }
	parentOf = func(pid int) (int, int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := f.table[pid]
		return p.PPID, p.PGID
	}
	procRunning = func(pid int) bool { f.mu.Lock(); defer f.mu.Unlock(); _, ok := f.table[pid]; return ok }
	signalProc = func(pid int, group, force bool) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		what := "TERM"
		if force {
			what = "KILL"
		}
		target := "pid"
		if group {
			target = "group"
		}
		f.signals = append(f.signals, what+" "+target+" "+strconv.Itoa(pid))
		for q, p := range f.table {
			hit := q == pid
			if group {
				hit = p.PGID == pid
			}
			if hit && (force || !f.ignore[q]) {
				delete(f.table, q)
			}
		}
		return nil
	}
}

// serversDaemon has tab w1 with a shell pane (leader 10), tab w2 with a
// Claude pane (leader 20), tab w3 with a Codex chat whose agent process is
// 30, and tab w4 with a Codex TUI pane (leader 40) whose shared app-server
// is 45.
func serversDaemon(t *testing.T) (*Daemon, *sysFake) {
	t.Helper()
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	self := os.Getpid()
	sys := &sysFake{
		table: ports.Table{
			1:    {PID: 1, PPID: 0, PGID: 1, SID: 1, Name: "systemd"},
			10:   {PID: 10, PPID: 1, PGID: 10, SID: 10, Name: "zsh"},
			11:   {PID: 11, PPID: 10, PGID: 11, SID: 10, Name: "npm"},
			12:   {PID: 12, PPID: 11, PGID: 11, SID: 10, Name: "node"},
			20:   {PID: 20, PPID: 1, PGID: 20, SID: 20, Name: "claude"},
			21:   {PID: 21, PPID: 20, PGID: 20, SID: 20, Name: "python3"}, // in the agent's own group
			30:   {PID: 30, PPID: self, PGID: 30, SID: 5, Name: "codex"},
			31:   {PID: 31, PPID: 30, PGID: 31, SID: 5, Name: "rails"},
			40:   {PID: 40, PPID: 1, PGID: 40, SID: 40, Name: "codex"},
			45:   {PID: 45, PPID: 40, PGID: 45, SID: 45, Name: "codex"},
			46:   {PID: 46, PPID: 45, PGID: 46, SID: 45, Name: "bash"},
			47:   {PID: 47, PPID: 46, PGID: 46, SID: 45, Name: "vite"},
			60:   {PID: 60, PPID: 1, PGID: 60, SID: 60, Name: "postgres"},
			self: {PID: self, PPID: 1, PGID: self, SID: self, Name: "pitwall"},
		},
		ports:  map[int][]int{12: {3000, 3001}, 20: {9999}, 21: {8000}, 31: {3002}, 47: {5173}, 60: {5432}, self: {7777}},
		cwd:    map[int]string{47: absPath("/work/app/web")},
		ignore: map[int]bool{},
	}
	sys.install(t)
	d.mu.Lock()
	d.st.Sessions = []model.Session{{ID: "s", Name: "s"}}
	for _, id := range []string{"w1", "w2", "w3", "w4"} {
		d.st.Workspaces = append(d.st.Workspaces, model.Workspace{ID: id, SessionID: "s"})
	}
	d.st.Panes = []model.Pane{{ID: "p1", WorkspaceID: "w1"}, {ID: "p2", WorkspaceID: "w2", Provider: model.ProviderClaude}, {ID: "p4", WorkspaceID: "w4", Provider: model.ProviderCodex}}
	d.panes = map[string]Pane{"p1": fgPane{fg: 11, cwd: absPath("/work/one")}, "p2": fgPane{fg: 20, cwd: absPath("/work/two")}, "p4": fgPane{fg: 40, cwd: absPath("/work/app")}}
	c := &chat{id: "c3"}
	c.procOf.Store(&pider{fixedPID(30)})
	if d.chats == nil {
		d.chats = map[string]*chat{}
	}
	d.chats["c3"] = c
	t.Cleanup(func() { d.mu.Lock(); delete(d.chats, "c3"); d.mu.Unlock() }) // before shutdown, which would close it
	d.st.Chats = []model.Chat{{ID: "c3", WorkspaceID: "w3", Provider: model.ProviderCodex}}
	d.mu.Unlock()
	return d, sys
}

func TestCheckServers(t *testing.T) {
	d, sys := serversDaemon(t)
	v := d.state().Version
	d.checkServers()
	st := d.state()
	want := map[string][]model.Server{
		"w1": {{PID: 12, Name: "node", Ports: []int{3000, 3001}}},
		"w2": {{PID: 21, Name: "python3", Ports: []int{8000}, Agent: true}},
		"w3": {{PID: 31, Name: "rails", Ports: []int{3002}, Agent: true}},
		"w4": {{PID: 47, Name: "vite", Ports: []int{5173}, Agent: true}},
	}
	if !reflect.DeepEqual(st.Servers, want) {
		t.Fatalf("Servers =\n%+v\nwant\n%+v", st.Servers, want)
	}
	if st.Version == v {
		t.Fatal("no state sent")
	}
	// Nothing changed: nothing sent.
	v = st.Version
	d.checkServers()
	if d.state().Version != v {
		t.Fatal("an unchanged scan sent a state")
	}
	// A server goes: its tab drops out.
	sys.mu.Lock()
	delete(sys.table, 31)
	sys.mu.Unlock()
	d.checkServers()
	if _, ok := d.state().Servers["w3"]; ok {
		t.Fatalf("w3 kept a gone server: %+v", d.state().Servers)
	}
	// A chat's agent pid another process took (its parent is not the
	// daemon) claims nothing.
	sys.mu.Lock()
	sys.table[30] = ports.Proc{PID: 30, PPID: 1, PGID: 30, SID: 30, Name: "x"}
	sys.table[31] = ports.Proc{PID: 31, PPID: 30, PGID: 31, SID: 30, Name: "rails"}
	sys.mu.Unlock()
	d.checkServers()
	if _, ok := d.state().Servers["w3"]; ok {
		t.Fatal("a reused pid's child counted as the chat's")
	}
	// Two Codex tabs over the folder: a new server of the shared
	// app-server goes to neither; vite, placed before, stays in w4.
	d.mu.Lock()
	d.st.Workspaces = append(d.st.Workspaces, model.Workspace{ID: "w5", SessionID: "s"})
	d.st.Panes = append(d.st.Panes, model.Pane{ID: "p5", WorkspaceID: "w5", Provider: model.ProviderCodex})
	d.panes["p5"] = fgPane{fg: 50, cwd: absPath("/work")}
	d.mu.Unlock()
	sys.mu.Lock()
	sys.table[50] = ports.Proc{PID: 50, PPID: 1, PGID: 50, SID: 50, Name: "codex"}
	sys.table[48] = ports.Proc{PID: 48, PPID: 46, PGID: 46, SID: 45, Name: "astro"}
	sys.ports[48] = []int{4321}
	sys.cwd[48] = absPath("/work/app/site")
	sys.mu.Unlock()
	d.checkServers()
	if got := d.state().Servers["w4"]; len(got) != 1 || got[0].PID != 47 {
		t.Fatalf("w4 = %+v, want vite alone", got)
	}
	if got := d.state().Servers["w5"]; len(got) != 0 {
		t.Fatalf("w5 = %+v", got)
	}

	// vite's shell exits: reparented out of the app-server's tree, it stays
	// w4's while it runs under that name and listens.
	sys.mu.Lock()
	delete(sys.table, 46)
	sys.table[47] = ports.Proc{PID: 47, PPID: 1, PGID: 46, SID: 45, Name: "vite"}
	sys.mu.Unlock()
	d.checkServers()
	if got := d.state().Servers["w4"]; len(got) != 1 || got[0].PID != 47 || !got[0].Agent {
		t.Fatalf("reparented: w4 = %+v", got)
	}
	// It stops listening, then the pid goes to another program: forgotten.
	sys.mu.Lock()
	delete(sys.ports, 47)
	sys.mu.Unlock()
	d.checkServers()
	sys.mu.Lock()
	sys.ports[47] = []int{5173}
	sys.table[47] = ports.Proc{PID: 47, PPID: 1, PGID: 47, SID: 47, Name: "nginx"}
	sys.mu.Unlock()
	d.checkServers()
	if got := d.state().Servers["w4"]; len(got) != 0 {
		t.Fatalf("a reused pid stayed w4's: %+v", got)
	}
}

func TestCheckServersNone(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	called := false
	old := procTable
	t.Cleanup(func() { procTable = old })
	procTable = func() (ports.Table, error) { called = true; return nil, errors.New("x") }
	d.checkServers()
	if called || d.state().Servers != nil {
		t.Fatal("no panes or chats still read the process table")
	}
}

func TestServerStop(t *testing.T) {
	if !ports.Supported {
		t.Skip("stopping a server needs Linux or macOS")
	}
	d, sys := serversDaemon(t)
	ctx := context.Background()
	stop := func(m proto.ServerStop) proto.ServerStopResult { return d.serverStop(ctx, m) }

	// Not listed for that tab, or on that port: nothing is signalled.
	for _, m := range []proto.ServerStop{{Workspace: "w2", PID: 12, Port: 3000}, {Workspace: "w1", PID: 12, Port: 9}, {Workspace: "w1", PID: 60, Port: 5432}, {Workspace: "w1", PID: os.Getpid(), Port: 7777}} {
		if r := stop(m); r.Err == "" {
			t.Errorf("stop %+v: %+v", m, r)
		}
	}
	if len(sys.signals) != 0 {
		t.Fatalf("signals for unlisted servers: %v", sys.signals)
	}

	// A job of its own: SIGTERM to its group, npm and node together.
	r := stop(proto.ServerStop{Workspace: "w1", PID: 12, Port: 3001})
	if r.Err != "" || r.Running || !slices.Equal(sys.signals, []string{"TERM group 11"}) {
		t.Fatalf("stop node: %+v, signals %v", r, sys.signals)
	}
	if _, ok := d.state().Servers["w1"]; ok {
		t.Fatal("the stopped server still shows")
	}

	// In the agent's own group: the pid alone, never the agent.
	sys.signals = nil
	r = stop(proto.ServerStop{Workspace: "w2", PID: 21, Port: 8000})
	if r.Err != "" || !slices.Equal(sys.signals, []string{"TERM pid 21"}) {
		t.Fatalf("stop python3: %+v, signals %v", r, sys.signals)
	}
	if _, ok := sys.table[20]; !ok {
		t.Fatal("the agent went with its server")
	}

	// It ignores SIGTERM: Running, then Force sends SIGKILL.
	sys.signals = nil
	sys.ignore[47] = true
	r = stop(proto.ServerStop{Workspace: "w4", PID: 47, Port: 5173})
	if r.Err != "" || !r.Running {
		t.Fatalf("stubborn: %+v", r)
	}
	r = stop(proto.ServerStop{Workspace: "w4", PID: 47, Port: 5173, Force: true})
	if r.Err != "" || r.Running || !r.Force || !slices.Equal(sys.signals, []string{"TERM group 46", "KILL group 46"}) {
		t.Fatalf("force: %+v, signals %v", r, sys.signals)
	}

	// Already gone: no error, nothing signalled.
	sys.signals = nil
	if r := stop(proto.ServerStop{Workspace: "w4", PID: 47, Port: 5173, Force: true}); r.Err != "" || r.Running || len(sys.signals) != 0 {
		t.Fatalf("gone: %+v, %v", r, sys.signals)
	}
}

func TestWithin(t *testing.T) {
	for _, tc := range []struct {
		dir, path string
		want      bool
	}{{"/a", "/a", true}, {"/a", "/a/b", true}, {"/a", "/ab", false}, {"/a/b", "/a", false}, {"", "/a", false}, {"/a/", "/a/b/../c", true}} {
		if got := within(tc.dir, tc.path); got != tc.want {
			t.Errorf("within(%q, %q) = %v", tc.dir, tc.path, got)
		}
	}
}
