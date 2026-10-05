package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

type fakePane struct {
	cfg   pane.Config
	dirty chan struct{}
	done  chan struct{}
	snaps atomic.Int64
	code  int

	mu     sync.Mutex
	input  []byte
	size   [2]int
	closed bool
	hist   int    // ScrollbackLen
	pushed uint64 // ScrollbackPushed
	off    int    // the last SnapshotAt offset
	title  string // the emulator's title
}

func (p *fakePane) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.input = append(p.input, b...)
	return len(b), nil
}
func (p *fakePane) Resize(c, r int) error {
	p.mu.Lock()
	p.size = [2]int{c, r}
	p.mu.Unlock()
	return nil
}
func (p *fakePane) Snapshot() vt.Grid {
	p.snaps.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	return vt.Grid{Cols: 1, Rows: 1, Cells: []vt.Cell{{Content: "x", Width: 1}}, Title: p.title}
}
func (p *fakePane) SnapshotAt(off int) vt.Grid {
	p.mu.Lock()
	p.off = off
	p.mu.Unlock()
	return p.Snapshot()
}
func (p *fakePane) ScrollbackLen() int       { p.mu.Lock(); defer p.mu.Unlock(); return p.hist }
func (p *fakePane) ScrollbackPushed() uint64 { p.mu.Lock(); defer p.mu.Unlock(); return p.pushed }
func (p *fakePane) Modes() vt.Modes          { return vt.Modes{} }
func (p *fakePane) Dirty() <-chan struct{}   { return p.dirty }
func (p *fakePane) Done() <-chan struct{}    { return p.done }
func (p *fakePane) ExitCode() int            { return p.code }
func (p *fakePane) Cwd() string              { return p.cfg.Cwd }
func (p *fakePane) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	return nil
}
func (p *fakePane) got() string { p.mu.Lock(); defer p.mu.Unlock(); return string(p.input) }

// fakes is the shared world behind one or more daemons: started panes, the
// saved state, git calls.
type fakes struct {
	mu         sync.Mutex
	panes      []*fakePane
	saved      model.State
	statsCalls map[string]int
}

func (f *fakes) options() Options {
	return Options{
		StartPane: func(c pane.Config) (Pane, error) {
			p := &fakePane{cfg: c, dirty: make(chan struct{}, 1), done: make(chan struct{})}
			f.mu.Lock()
			f.panes = append(f.panes, p)
			f.mu.Unlock()
			return p, nil
		},
		Derive: func(prev *model.Activity, pr model.Provider, payload []byte, now time.Time) (model.Activity, bool) {
			s := string(payload)
			if s == "clear" {
				return model.Activity{}, true
			}
			return model.Activity{State: model.AgentState(s), UpdatedAt: now}, true
		},
		SessionID: func(model.Provider, []byte) string { return "sess-1" },
		RepoRoot: func(_ context.Context, path string) (string, bool) {
			return path, strings.HasSuffix(path, "repo")
		},
		Branch: func(_ context.Context, path string) (string, bool) {
			if strings.Contains(path, "repo") {
				return filepath.Base(path), true
			}
			return "", false
		},
		Stats: func(_ context.Context, wt string) (model.BranchStats, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.statsCalls[wt]++
			return model.BranchStats{Additions: 5, Ahead: f.statsCalls[wt]}, nil
		},
		AddWorktree: func(_ context.Context, root, name string) (string, string, error) {
			return filepath.Join(root, ".worktrees", name), name, nil
		},
		RemoveWorktree: func(context.Context, string, string, bool) error { return nil },
		Save: func(s model.State) error {
			f.mu.Lock()
			f.saved = s
			f.mu.Unlock()
			return nil
		},
		Load: func() (model.State, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.saved, nil
		},
		RestoreCmd: func(p model.Pane) []string {
			if p.SessionID != "" {
				return []string{"claude", "--resume", p.SessionID}
			}
			return p.Cmd
		},
		Split: func(root *layout.Node, target, id string, dir layout.Dir) *layout.Node {
			return &layout.Node{Dir: dir, Ratios: []float64{0.5, 0.5}, Children: []*layout.Node{root, {Pane: id}}}
		},
		Remove: func(root *layout.Node, id string) *layout.Node {
			if root.Pane == id {
				return nil
			}
			var keep []*layout.Node
			for _, c := range root.Children {
				if c.Pane != id {
					keep = append(keep, c)
				}
			}
			if len(keep) == 1 {
				return keep[0]
			}
			root.Children = keep
			return root
		},
		StatsInterval: time.Hour,
	}
}

func (f *fakes) pane(i int) *fakePane {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.panes[i]
}

// run starts a daemon on a socket in a temp dir and returns the socket path
// and a stop func that waits for Serve to return.
func run(t *testing.T, f *fakes) (string, func()) {
	t.Helper()
	return runWith(t, f, func(*Options) {})
}

// runWith is run with Options changed by edit.
func runWith(t *testing.T, f *fakes, edit func(*Options)) (string, func()) {
	t.Helper()
	o := f.options()
	edit(&o)
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()
	stop := func() {
		cancel()
		if err := <-served; err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { cancel() })
	return sock, stop
}

type testClient struct {
	t    *testing.T
	conn *proto.Conn
	in   chan any
}

func dial(t *testing.T, sock, kind string) *testClient {
	t.Helper()
	return dialIn(t, sock, kind, t.TempDir())
}

// dialIn says Hello as if launched in cwd.
func dialIn(t *testing.T, sock, kind, cwd string) *testClient {
	t.Helper()
	return dialHello(t, sock, proto.Hello{Version: proto.Version, Kind: kind, Cwd: cwd})
}

// dialHello connects and says hello.
func dialHello(t *testing.T, sock string, hello proto.Hello) *testClient {
	t.Helper()
	c, err := proto.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Send(hello); err != nil {
		t.Fatal(err)
	}
	tc := &testClient{t: t, conn: c, in: make(chan any, 1024)}
	go func() {
		defer close(tc.in)
		for {
			m, err := c.Recv()
			if err != nil {
				return
			}
			tc.in <- m
		}
	}()
	return tc
}

func (c *testClient) send(m any) {
	c.t.Helper()
	if err := c.conn.Send(m); err != nil {
		c.t.Fatal(err)
	}
}

// waitFor reads messages until ok returns true for one of them.
func (c *testClient) waitFor(what string, ok func(any) bool) any {
	c.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, open := <-c.in:
			if !open {
				c.t.Fatalf("connection closed waiting for %s", what)
			}
			if e, isErr := m.(proto.Error); isErr {
				c.t.Fatalf("daemon error waiting for %s: %s", what, e.Message)
			}
			if ok(m) {
				return m
			}
		case <-timeout:
			c.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (c *testClient) waitState(what string, ok func(model.State) bool) model.State {
	c.t.Helper()
	m := c.waitFor(what, func(m any) bool {
		s, is := m.(proto.StateMsg)
		return is && ok(s.State)
	})
	return m.(proto.StateMsg).State
}

func TestDaemonFlow(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	gui := dial(t, sock, "gui")
	// Workspace 0 and pane 0 are the session the GUI's Hello opened.
	gui.waitState("first session", func(s model.State) bool { return len(s.Workspaces) == 1 })

	folder := t.TempDir()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	gui.send(proto.AddProject{Path: folder})
	gui.send(proto.AddProject{Path: repo})
	st := gui.waitState("two projects", func(s model.State) bool { return len(s.Projects) == 2 })
	if len(st.Workspaces) != 2 || st.Workspaces[1].Path != folder || st.Projects[0].Kind != model.ProjectFolder || st.Projects[1].Kind != model.ProjectGit {
		t.Fatalf("folder project should get one workspace at the folder: %+v", st)
	}
	folderWS, gitProj := st.Workspaces[1].ID, st.Projects[1].ID

	gui.send(proto.NewWorkspace{ProjectID: gitProj, Name: "feat"})
	st = gui.waitState("git workspace", func(s model.State) bool { return len(s.Workspaces) == 3 })
	gw := st.Workspaces[2]
	if gw.Branch != "feat" || gw.Path != filepath.Join(repo, ".worktrees", "feat") {
		t.Fatalf("git workspace: %+v", gw)
	}

	gui.send(proto.OpenPane{WorkspaceID: folderWS, Cmd: []string{"claude"}})
	st = gui.waitState("pane", func(s model.State) bool { return len(s.Panes) == 2 })
	p0 := st.Panes[1].ID
	if f.pane(1).cfg.Cwd != folder || f.pane(1).cfg.ID != p0 || lay(st.Workspaces[1]).Pane != p0 {
		t.Fatalf("pane start: cfg %+v layout %+v", f.pane(1).cfg, lay(st.Workspaces[1]))
	}

	gui.send(proto.OpenPane{WorkspaceID: folderWS, Target: p0, Dir: layout.Vertical})
	st = gui.waitState("split", func(s model.State) bool { return len(s.Panes) == 3 })
	if l := lay(st.Workspaces[1]); len(l.Children) != 2 || l.Dir != layout.Vertical {
		t.Fatalf("split layout: %+v", l)
	}

	gui.send(proto.Input{Pane: p0, Data: []byte("ls\r")})
	gui.send(proto.Resize{Pane: p0, Cols: 120, Rows: 40})
	waitUntil(t, "input reaches pane", func() bool { return f.pane(1).got() == "ls\r" })

	hook := dial(t, sock, "hook")
	hook.send(proto.AgentEvent{Pane: p0, Provider: model.ProviderClaude, Payload: []byte("working")})
	st = gui.waitState("activity", func(s model.State) bool { return len(s.Activities) == 1 })
	a, p := st.Activities[0], st.Panes[1]
	if a.State != model.StateWorking || a.PaneID != p0 || a.WorkspaceID != folderWS || p.SessionID != "sess-1" || p.Provider != model.ProviderClaude {
		t.Fatalf("activity %+v pane %+v", a, p)
	}

	// A git workspace pane going completed refreshes that workspace's stats.
	gui.send(proto.OpenPane{WorkspaceID: gw.ID})
	st = gui.waitState("git pane", func(s model.State) bool { return len(s.Panes) == 4 })
	hook.send(proto.AgentEvent{Pane: st.Panes[3].ID, Provider: model.ProviderCodex, Payload: []byte("completed")})
	gui.waitState("stats after completed", func(s model.State) bool { return s.Stats[gw.ID].Ahead == 1 })

	hook.send(proto.AgentEvent{Pane: p0, Provider: model.ProviderClaude, Payload: []byte("clear")})
	gui.waitState("activity removed", func(s model.State) bool { return len(s.Activities) == 1 })

	gui.send(proto.ClosePane{Pane: p0})
	st = gui.waitState("pane closed", func(s model.State) bool { return len(s.Panes) == 3 })
	if l := lay(st.Workspaces[1]); l.Pane == "" || l.Pane == p0 {
		t.Fatalf("layout after close: %+v", l)
	}
	waitUntil(t, "closed pane handle", func() bool { f.pane(1).mu.Lock(); defer f.pane(1).mu.Unlock(); return f.pane(1).closed })

	gui.send(proto.RenameWorkspace{WorkspaceID: gw.ID, Name: "renamed"})
	gui.send(proto.DeleteWorkspace{WorkspaceID: gw.ID})
	st = gui.waitState("workspace deleted", func(s model.State) bool { return len(s.Workspaces) == 2 })
	if len(st.Panes) != 2 || len(st.Stats) != 0 {
		t.Fatalf("delete left panes or stats: %+v", st)
	}
}

func TestFrameThrottle(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	gui := dial(t, sock, "gui")
	gui.waitState("first session's pane", func(s model.State) bool { return len(s.Panes) == 1 })
	p := f.pane(0)

	const burst = 500 * time.Millisecond
	start := time.Now()
	for time.Since(start) < burst {
		select {
		case p.dirty <- struct{}{}:
		default:
		}
		time.Sleep(200 * time.Microsecond)
	}
	time.Sleep(50 * time.Millisecond)
	n := p.snaps.Load()
	// 60/s over the burst, plus the frame sent on Hello and the first one
	// that goes out immediately.
	if max := int64(burst/frameInterval) + 3; n < 5 || n > max {
		t.Fatalf("%d frames in %v, want 5..%d", n, burst, max)
	}
	gui.waitFor("frame", func(m any) bool { fr, ok := m.(proto.Frame); return ok && fr.Grid.Cells[0].Content == "x" })
}

func TestVersionMismatch(t *testing.T) {
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	c, err := proto.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Send(proto.Hello{Version: proto.Version + 1, Kind: "gui"})
	if m, err := c.Recv(); err != nil {
		t.Fatal(err)
	} else if _, ok := m.(proto.Error); !ok {
		t.Fatalf("got %T, want Error", m)
	}
	if _, err := c.Recv(); err != io.EOF {
		t.Fatalf("connection should close, got %v", err)
	}
}

func TestRestoreOnRestart(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	gui := dial(t, sock, "gui")
	folder := t.TempDir()
	gui.send(proto.AddProject{Path: folder})
	st := gui.waitState("project", func(s model.State) bool { return len(s.Workspaces) == 2 })
	ws := st.Workspaces[1].ID
	gui.send(proto.OpenPane{WorkspaceID: ws, Cmd: []string{"claude"}})
	gui.send(proto.OpenPane{WorkspaceID: ws, Cmd: []string{"make"}})
	st = gui.waitState("panes", func(s model.State) bool { return len(s.Panes) == 3 })
	agentPane, donePane := st.Panes[1].ID, st.Panes[2].ID
	dial(t, sock, "hook").send(proto.AgentEvent{Pane: agentPane, Provider: model.ProviderClaude, Payload: []byte("working")})
	gui.waitState("session", func(s model.State) bool { return s.Panes[1].SessionID != "" })

	f.pane(2).code = 3
	f.pane(2).Close() // the process exits on its own
	gui.waitState("exited pane closed", func(s model.State) bool { return len(s.Panes) == 2 })
	// The writer sends the pending StateMsg before PaneExited.
	gui.waitFor("PaneExited", func(m any) bool { e, ok := m.(proto.PaneExited); return ok && e.Pane == donePane && e.ExitCode == 3 })
	stop()

	f.mu.Lock()
	saved, started := f.saved, len(f.panes)
	f.mu.Unlock()
	if len(saved.Panes) != 2 || saved.Panes[1].Exited {
		t.Fatalf("shutdown must save live panes as not exited: %+v", saved.Panes)
	}

	sock, stop = run(t, f)
	defer stop()
	f.mu.Lock()
	restarted := f.panes[started:]
	f.mu.Unlock()
	if len(restarted) != 2 {
		t.Fatalf("restarted %d panes, want the two live ones", len(restarted))
	}
	if c := restarted[1].cfg; c.ID != agentPane || c.Cwd != folder || !slices.Equal(c.Cmd, []string{"claude", "--resume", "sess-1"}) {
		t.Fatalf("restore config: %+v", c)
	}
	st = dial(t, sock, "gui").waitState("restored state", func(model.State) bool { return true })
	// A restored session means no new one.
	if len(st.Workspaces) != 2 || len(st.Panes) != 2 || len(st.Activities) != 0 {
		t.Fatalf("restored state: %+v", st)
	}
}

// lay is the layout of w's active tab.
func lay(w model.Workspace) *layout.Node { return w.Tabs[tabIndex(&w, "")].Layout }

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

// A paste into a program that never reads stdin blocks the handler in the PTY
// write; shutdown must still save and close the pane.
func TestShutdownWithBlockedWrite(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.NewVT = vt.New
	o.StartPane = func(c pane.Config) (Pane, error) {
		c.Cmd = []string{"sh", "-c", "stty raw -echo; echo ready; exec sleep 60"} // raw: a full input queue blocks the writer
		p, err := pane.Start(c)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- d.Serve(ctx, ln) }()

	gui := dial(t, sock, "gui")
	folder := t.TempDir()
	gui.send(proto.AddProject{Path: folder})
	st := gui.waitState("project", func(s model.State) bool { return len(s.Workspaces) == 2 })
	gui.send(proto.OpenPane{WorkspaceID: st.Workspaces[1].ID})
	st = gui.waitState("pane", func(s model.State) bool { return len(s.Panes) == 2 })
	gui.waitFor("raw mode", func(m any) bool {
		fr, ok := m.(proto.Frame)
		return ok && fr.Pane == st.Panes[1].ID && len(fr.Grid.Cells) > 0 && fr.Grid.Cells[0].Content == "r"
	})
	gui.send(proto.Input{Pane: st.Panes[1].ID, Data: make([]byte, 1<<20)})
	time.Sleep(200 * time.Millisecond) // the handler is now stuck in the write

	cancel()
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return with a PTY write pending")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saved.Panes) != 2 || f.saved.Panes[1].Exited || f.saved.Panes[1].Cwd != folder {
		t.Fatalf("final save: %+v", f.saved.Panes)
	}
}

// A Codex /side fork's hooks must not replace the session a restart resumes.
func TestSideForkKeepsSession(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.AddProject{Path: t.TempDir()}))
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: d.st.Workspaces[0].ID}))
	id := d.st.Panes[0].ID
	must(t, d.handle(ctx, proto.AgentEvent{Pane: id, Provider: model.ProviderCodex,
		Payload: []byte(`{"session_id":"main","transcript_path":"/t.jsonl","hook_event_name":"Stop"}`)}))
	must(t, d.handle(ctx, proto.AgentEvent{Pane: id, Provider: model.ProviderCodex,
		Payload: []byte(`{"session_id":"side","transcript_path":null,"hook_event_name":"Stop"}`)}))
	d.mu.Lock()
	defer d.mu.Unlock()
	if sid := d.st.Panes[0].SessionID; sid != "main" {
		t.Fatalf("SessionID = %q, want main", sid)
	}
}

// A pi shutdown report that arrives after the next pi session started, as
// after /new or /reload, must not end the new session's activity or change
// the session a restart resumes.
func TestPiStaleShutdownIgnored(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.AddProject{Path: t.TempDir()}))
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: d.st.Workspaces[0].ID}))
	id := d.st.Panes[0].ID
	send := func(payload string) {
		t.Helper()
		must(t, d.handle(ctx, proto.AgentEvent{Pane: id, Provider: model.ProviderPi, Payload: []byte(payload)}))
	}
	send(`{"event":"session_start","session_id":"old"}`)
	send(`{"event":"session_start","session_id":"new"}`)
	send(`{"event":"agent_start","session_id":"new"}`)
	send(`{"event":"session_shutdown","session_id":"old"}`)
	if a := d.activityOf(id); a.State != model.StateWorking || a.SessionID != "new" {
		t.Fatalf("stale shutdown changed the activity: %+v", a)
	}
	d.mu.Lock()
	sid := d.st.Panes[0].SessionID
	d.mu.Unlock()
	if sid != "new" {
		t.Fatalf("SessionID = %q, want new", sid)
	}
	send(`{"event":"session_shutdown","session_id":"new"}`)
	if a := d.activityOf(id); a.State != "" {
		t.Fatalf("current shutdown kept the activity: %+v", a)
	}
}

// /reload loads pi's extension again in the same session, as a new runtime
// that starts with session_start. Any report from the runtime it replaced
// that reaches the daemon later changes nothing; reports before any
// session_start are taken.
func TestPiReloadOrdering(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.AddProject{Path: t.TempDir()}))
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: d.st.Workspaces[0].ID}))
	id := d.st.Panes[0].ID
	send := func(event, runtime string) {
		t.Helper()
		b := fmt.Sprintf(`{"event":%q,"runtime":%q,"session_id":"s1","stop_reason":"stop","message":"done"}`, event, runtime)
		must(t, d.handle(ctx, proto.AgentEvent{Pane: id, Provider: model.ProviderPi, Payload: []byte(b)}))
	}
	state := func() model.AgentState { return d.activityOf(id).State }
	send("agent_start", "early")
	if s := state(); s != model.StateWorking {
		t.Fatalf("a report before any session_start was dropped: %q", s)
	}
	send("session_start", "old")
	send("agent_settled", "old")
	send("session_shutdown", "old")
	if s := state(); s != "" {
		t.Fatalf("the current runtime's shutdown kept the activity: %q", s)
	}
	send("agent_settled", "old")
	send("session_start", "new")
	send("agent_start", "new")
	send("agent_settled", "old") // late, from the runtime /reload replaced
	if s := state(); s != model.StateWorking {
		t.Fatalf("the old runtime's late report changed the state to %q", s)
	}
	send("session_shutdown", "old")
	if s := state(); s != model.StateWorking {
		t.Fatalf("the old runtime's late shutdown changed the state to %q", s)
	}
	send("agent_settled", "new")
	if s := state(); s != model.StateCompleted {
		t.Fatalf("the new runtime's report was dropped: %q", s)
	}
	send("session_shutdown", "new")
	if s := state(); s != "" {
		t.Fatalf("the new runtime's shutdown kept the activity: %q", s)
	}
}

// A pi runtime that sent its session_shutdown is retired: a report it sends
// later, its own session_start included, changes nothing.
func TestPiRetiredRuntime(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	d := newDaemon(t, o)
	ctx := context.Background()
	must(t, d.handle(ctx, proto.AddProject{Path: t.TempDir()}))
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: d.st.Workspaces[0].ID}))
	id := d.st.Panes[0].ID
	send := func(event string) {
		t.Helper()
		b := fmt.Sprintf(`{"event":%q,"runtime":"r","session_id":"s1"}`, event)
		must(t, d.handle(ctx, proto.AgentEvent{Pane: id, Provider: model.ProviderPi, Payload: []byte(b)}))
	}
	send("session_start")
	send("agent_start")
	send("session_shutdown")
	for _, late := range []string{"agent_start", "session_start", "tool_call"} {
		send(late)
		if a := d.activityOf(id); a.State != "" {
			t.Fatalf("a late %s from the retired runtime set %q", late, a.State)
		}
	}
}

// A SetLayout built from stale state is rejected; a current one is taken with
// its ratios made sane.
func TestSetLayoutValidates(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.AddProject{Path: t.TempDir()}))
	ws := d.st.Workspaces[0].ID
	for range 3 {
		must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: ws}))
	}
	a, b, c := d.st.Panes[0].ID, d.st.Panes[1].ID, d.st.Panes[2].ID
	must(t, d.handle(ctx, proto.ClosePane{Pane: c}))
	before := cloneNode(lay(d.st.Workspaces[0]))

	split := func(r []float64, leaves ...string) *layout.Node {
		n := &layout.Node{Ratios: r}
		for _, id := range leaves {
			n.Children = append(n.Children, &layout.Node{Pane: id})
		}
		return n
	}
	for name, l := range map[string]*layout.Node{
		"resurrects closed": split(nil, a, b, c),
		"hides live":        {Pane: a},
		"duplicate leaf":    split(nil, a, b, b),
		"empty":             nil,
		"one-child split":   {Children: []*layout.Node{split(nil, a, b)}},
	} {
		if err := d.handle(ctx, proto.SetLayout{WorkspaceID: ws, Layout: l}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got := lay(d.st.Workspaces[0]); !reflect.DeepEqual(got, before) {
		t.Fatalf("rejected layouts changed the layout: %+v", got)
	}

	must(t, d.handle(ctx, proto.SetLayout{WorkspaceID: ws, Layout: split([]float64{3, -1}, b, a)}))
	r := lay(d.st.Workspaces[0]).Ratios
	if len(r) != 2 || math.Abs(r[0]+r[1]-1) > 1e-9 || r[1] <= 0 {
		t.Fatalf("ratios %v", r)
	}
}

// A branch that git refuses to delete must not keep a workspace whose
// worktree is already gone.
func TestDeleteWorkspaceBranchKept(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.RemoveWorktree = func(context.Context, string, string, bool) error {
		return fmt.Errorf("%w: branch not fully merged", gitstat.ErrBranchKept)
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()})) // the session the project joins
	must(t, d.handle(ctx, proto.AddProject{Path: repo}))
	must(t, d.handle(ctx, proto.NewWorkspace{ProjectID: d.st.Projects[0].ID, Name: "feat"}))
	ws := d.st.Workspaces[1].ID
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: ws}))

	err = d.handle(ctx, proto.DeleteWorkspace{WorkspaceID: ws, RemoveBranch: true})
	if !errors.Is(err, gitstat.ErrBranchKept) {
		t.Fatalf("got %v, want the kept branch reported", err)
	}
	d.mu.Lock()
	n, np := len(d.st.Workspaces), len(d.st.Panes)
	d.mu.Unlock()
	if n != 1 || np != 1 {
		t.Fatalf("workspace or pane left: %d %d", n, np)
	}
	waitUntil(t, "pane closed", func() bool { p := f.pane(1); p.mu.Lock(); defer p.mu.Unlock(); return p.closed })
}

// A GUI client that stops reading is disconnected; the others keep getting
// state.
func TestSlowClientDisconnected(t *testing.T) {
	old := writeTimeout
	writeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { writeTimeout = old })
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error)
	defer func() { cancel(); <-served }() // its liveness loop reads globals later tests set
	go func() { served <- d.Serve(ctx, ln) }()

	good := dial(t, sock, "gui")
	good.waitState("initial", func(model.State) bool { return true })
	slow, err := proto.Dial(sock) // never reads
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	slow.Send(proto.Hello{Version: proto.Version, Kind: "gui"})
	waitUntil(t, "slow client registered", func() bool { d.mu.Lock(); defer d.mu.Unlock(); return len(d.clients) == 2 })
	go func() {
		bad := proto.Input{Pane: strings.Repeat("x", 512)} // each earns an Error reply
		for slow.Send(bad) == nil {
		}
	}()
	waitUntil(t, "slow client dropped", func() bool { d.mu.Lock(); defer d.mu.Unlock(); return len(d.clients) == 1 })

	good.send(proto.AddProject{Path: t.TempDir()})
	good.waitState("project", func(s model.State) bool { return len(s.Projects) == 1 })
}

// A restored agent in a pane that was a shell drops to a shell when it
// exits, at any time and with any code. A pane opened with a command closes
// as before; only a failed resume within resumeGrace would get a shell.
func TestRestoredAgentExitKeepsShell(t *testing.T) {
	old := resumeGrace
	resumeGrace = 0
	t.Cleanup(func() { resumeGrace = old })
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	gui := dial(t, sock, "gui")
	gui.send(proto.AddProject{Path: t.TempDir()})
	st := gui.waitState("project", func(s model.State) bool { return len(s.Workspaces) == 2 })
	gui.send(proto.OpenPane{WorkspaceID: st.Workspaces[1].ID, Cmd: []string{"claude"}})
	st = gui.waitState("panes", func(s model.State) bool { return len(s.Panes) == 2 })
	shellPane, cmdPane := st.Panes[0].ID, st.Panes[1].ID
	if st.Panes[0].Cmd != nil {
		t.Fatalf("first pane is no shell: %+v", st.Panes[0])
	}
	hook := dial(t, sock, "hook")
	hook.send(proto.AgentEvent{Pane: shellPane, Provider: model.ProviderClaude, Payload: []byte(`{"permission_mode":"bypassPermissions"}`)})
	hook.send(proto.AgentEvent{Pane: cmdPane, Provider: model.ProviderClaude, Payload: []byte("working")})
	gui.waitState("sessions", func(s model.State) bool {
		return s.Panes[0].SessionID != "" && s.Panes[1].SessionID != "" && s.Panes[0].AgentMode == "bypassPermissions"
	})
	stop()

	f.mu.Lock()
	started := len(f.panes)
	f.mu.Unlock()
	sock, stop = run(t, f)
	defer stop()
	gui = dial(t, sock, "gui")
	gui.waitState("restored", func(s model.State) bool { return len(s.Panes) == 2 })
	for _, p := range f.panes[started : started+2] {
		p.Close() // exit 0, after the grace period
	}
	st = gui.waitState("agents exited", func(s model.State) bool { return len(s.Panes) == 1 && s.Panes[0].Provider == "" })
	// The command pane's tab closed with it; the shell pane's stays.
	if p := st.Panes[0]; p.ID != shellPane || p.Cmd != nil || p.SessionID != "" || p.AgentMode != "" || len(st.Workspaces) != 1 || st.Workspaces[0].ID != p.WorkspaceID {
		t.Fatalf("after exit: panes %+v, workspaces %+v", st.Panes, st.Workspaces)
	}
	f.mu.Lock()
	last := f.panes[len(f.panes)-1].cfg
	f.mu.Unlock()
	if last.ID != shellPane || last.Cmd != nil {
		t.Fatalf("shell start: %+v", last)
	}
}

// A new session starts without the old one's permission mode until a hook
// of its own reports one; an unknown mode changes nothing.
func TestAgentModeFollowsSession(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := runWith(t, f, func(o *Options) { o.SessionID = agent.SessionID })
	defer stop()
	gui := dial(t, sock, "gui")
	st := gui.waitState("pane", func(s model.State) bool { return len(s.Panes) == 1 })
	id := st.Panes[0].ID
	hook := dial(t, sock, "hook")
	for _, step := range []struct{ payload, sid, mode string }{
		{`{"session_id":"a","hook_event_name":"Stop","permission_mode":"bypassPermissions"}`, "a", "bypassPermissions"},
		{`{"session_id":"b","hook_event_name":"Stop"}`, "b", ""},
		{`{"session_id":"b","hook_event_name":"Stop","permission_mode":"plan"}`, "b", "plan"},
		{`{"session_id":"b","hook_event_name":"Stop","permission_mode":"auto"}`, "b", "plan"},
		{`{"session_id":"c","hook_event_name":"Stop","permission_mode":"default"}`, "c", "default"},
	} {
		hook.send(proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte(step.payload)})
		// The fake Derive makes the payload the activity's state, so the
		// wait ends only once this hook was applied.
		st := gui.waitState(step.payload, func(s model.State) bool {
			return len(s.Activities) == 1 && string(s.Activities[0].State) == step.payload
		})
		if p := st.Panes[0]; p.SessionID != step.sid || p.AgentMode != step.mode {
			t.Fatalf("after %s: session %q, mode %q", step.payload, p.SessionID, p.AgentMode)
		}
	}
}
