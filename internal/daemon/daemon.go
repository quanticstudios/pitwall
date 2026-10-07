// Package daemon owns all state: projects, workspaces, panes, agent activity
// and branch stats. Clients connect over proto and get StateMsg and Frame
// pushes.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/decisionlog"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const (
	frameInterval = time.Second / 60
	saveDelay     = time.Second
	// New panes start at this size; the GUI sends Resize once it lays them out.
	defaultCols, defaultRows = 80, 24
	// inputQueue is how many Input messages may wait behind a PTY write.
	inputQueue = 64
	// maxQueued is how many Error and PaneExited messages a client may have
	// unsent before it is disconnected. StateMsg and Frame coalesce instead.
	maxQueued = 1024
)

// writeTimeout is how long one message may take to reach a client before it
// is disconnected; tests shorten it.
var writeTimeout = 10 * time.Second

// Pane is what the daemon needs from a running pane; *pane.Pane has it.
type Pane interface {
	Write(b []byte) (int, error)
	Resize(cols, rows int) error
	Snapshot() vt.Grid
	SnapshotAt(off int) vt.Grid
	ScrollbackLen() int
	Modes() vt.Modes
	Dirty() <-chan struct{}
	Done() <-chan struct{}
	ExitCode() int
	Cwd() string
	Close() error
}

// Options are the daemon's collaborators. New wires the real packages; tests
// pass fakes to NewWith.
type Options struct {
	StartPane      func(pane.Config) (Pane, error)
	NewVT          vt.NewFunc
	Derive         func(prev *model.Activity, provider model.Provider, payload []byte, now time.Time) (model.Activity, bool)
	SessionID      func(provider model.Provider, payload []byte) string
	RepoRoot       func(ctx context.Context, path string) (string, bool)
	Branch         func(ctx context.Context, path string) (branch string, inRepo bool)
	Stats          func(ctx context.Context, worktree string) (model.BranchStats, error)
	AddWorktree    func(ctx context.Context, repoRoot, name string) (path, branch string, err error)
	RemoveWorktree func(ctx context.Context, repoRoot, path string, deleteBranch bool) error
	// Decisions reads the decision settings and provider; nil leaves every
	// decision feature off.
	Decisions func() Decisions
	// Journal is decisions.jsonl; nil logs nothing.
	Journal       *decisionlog.Log
	Save          func(model.State) error
	Load          func() (model.State, error)
	RestoreCmd    func(model.Pane) []string
	Split         func(root *layout.Node, target, newPane string, dir layout.Dir) *layout.Node
	Remove        func(root *layout.Node, pane string) *layout.Node
	StatsInterval time.Duration // 0 means 30s
}

type Daemon struct {
	o Options

	mu          sync.Mutex
	st          model.State
	panes       map[string]Pane
	sizes       map[string][2]int      // per pane, the last size it took, for the log
	inputs      map[string]chan []byte // per pane, drained by writeInput
	views       map[string]*view       // scroll positions, see scroll.go
	clients     map[*client]struct{}   // gui clients only
	watchers    map[*client]struct{}   // watch clients: StateMsg and PaneExited, no frames
	closing     bool
	savePending bool
	live        liveness
	resumed     map[string]time.Time  // pane: when NewWith relaunched it with a resume command
	attn        map[string]*attention // pane: seen time and OSC notification, see attention.go
	dec         decisions             // see decide.go

	saveMu  sync.Mutex // serializes snapshot+write so an old save never lands last
	helloMu sync.Mutex // see firstSession
}

// New loads state from store.Path() and relaunches saved panes with
// store.RestoreCmd.
func New() (*Daemon, error) {
	path := store.Path()
	journal, err := decisionlog.Open(filepath.Join(filepath.Dir(path), "decisions.jsonl"))
	if err != nil {
		log.Printf("decisions log: %q", err) // decisions work without it
	}
	return NewWith(Options{
		Journal: journal,
		StartPane: func(c pane.Config) (Pane, error) {
			p, err := pane.Start(c)
			if err != nil {
				return nil, err // a nil *pane.Pane must not become a non-nil Pane
			}
			return p, nil
		},
		NewVT:          vt.New,
		Derive:         agent.Derive,
		SessionID:      agent.SessionID,
		RepoRoot:       gitstat.RepoRoot,
		Branch:         gitBranch,
		Stats:          gitstat.Stats,
		AddWorktree:    gitstat.AddWorktree,
		RemoveWorktree: gitstat.RemoveWorktree,
		Save:           func(s model.State) error { return store.Save(path, s) },
		Load:           func() (model.State, error) { return store.Load(path) },
		RestoreCmd:     store.RestoreCmd,
		Decisions:      loadDecisions(config.Path(), decide.CredentialsPath(config.Dir())),
		Split:          layout.Split,
		Remove:         layout.Remove,
	})
}

// NewWith loads state through o.Load and relaunches every saved pane that had
// not exited, using o.RestoreCmd in the pane's saved Cwd. Activities from the
// previous run are dropped: the agents behind them are new processes.
//
// A held pane (model.Pane.Held) is relaunched only when its agent session
// can be resumed (store.Resumes). One whose command had exited comes back exited with its code,
// and any other one comes back exited with ExitUnknown, because running its
// command twice may not be safe. Either shows a notice in place of its
// screen, which is not saved.
func NewWith(o Options) (*Daemon, error) {
	if o.StatsInterval == 0 {
		o.StatsInterval = 30 * time.Second
	}
	if o.Branch == nil {
		o.Branch = gitBranch
	}
	st, err := o.Load()
	if err != nil {
		return nil, err
	}
	if st.Stats == nil {
		st.Stats = map[string]model.BranchStats{}
	}
	st.Activities = nil
	d := &Daemon{o: o, st: st, panes: map[string]Pane{}, sizes: map[string][2]int{}, inputs: map[string]chan []byte{}, clients: map[*client]struct{}{}, watchers: map[*client]struct{}{}, resumed: map[string]time.Time{}}
	if o.Decisions != nil {
		d.dec.cur = o.Decisions()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	for i := range d.st.Workspaces {
		d.st.Workspaces[i].RepoRoot = d.repoRoot(ctx, d.st.Workspaces[i].Path)
	}
	cancel()
	d.mu.Lock() // watchers of already started panes read d.panes
	defer d.mu.Unlock()
	for i := range d.st.Workspaces {
		w := &d.st.Workspaces[i]
		w.Tabs = slices.DeleteFunc(w.Tabs, func(t model.Tab) bool { return t.Layout == nil })
		if tabIndex(w, w.ActiveTab) < 0 && len(w.Tabs) > 0 {
			w.ActiveTab = w.Tabs[0].ID
		}
	}
	var gone []string
	for i := range d.st.Panes {
		p := &d.st.Panes[i]
		if p.Exited && !p.Held {
			gone = append(gone, p.ID) // saved by an older daemon, which kept exited panes
			continue
		}
		// why: argv cannot tell, since `claude --resume s` restores to the same argv.
		resumes := store.Resumes(*p)
		if p.Held && (p.Exited || !resumes) {
			d.panes[p.ID] = d.stoppedPane(p)
			d.sizes[p.ID] = [2]int{defaultCols, defaultRows}
			continue
		}
		cmd := o.RestoreCmd(*p)
		if err := d.start(p.ID, cmd, p.Cwd); err != nil {
			log.Printf("restore pane %s: %q", p.ID, err)
			gone = append(gone, p.ID)
			continue
		}
		if resumes {
			d.resumed[p.ID] = time.Now()
			log.Printf("pane %s: resuming %q, mode %q", p.ID, p.Provider, p.AgentMode)
		}
	}
	for _, id := range gone {
		closeAll(d.removePane(id))
	}
	d.adopt()
	d.st.Sessions = slices.DeleteFunc(d.st.Sessions, func(s model.Session) bool {
		return !slices.ContainsFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.SessionID == s.ID }) &&
			!slices.ContainsFunc(d.st.Projects, func(p model.Project) bool { return p.SessionID == s.ID })
	})
	d.retitle()
	d.fixOrders()
	return d, nil
}

// Serve accepts clients until ctx is done, then saves state and closes panes.
func (d *Daemon) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	context.AfterFunc(ctx, func() { ln.Close() })
	go d.statsLoop(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { d.livenessLoop(ctx) }) // waited for: tests swap the globals it reads

	var acceptErr error
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				acceptErr = err
			}
			break
		}
		wg.Go(func() { d.serveConn(ctx, nc) })
	}
	cancel()
	wg.Wait()
	d.shutdown()
	return acceptErr
}

func (d *Daemon) shutdown() {
	d.saveMu.Lock()
	d.mu.Lock()
	d.closing = true
	s := d.saveSnapshot()
	panes := slices.Collect(maps.Values(d.panes))
	d.mu.Unlock()
	if err := d.o.Save(s); err != nil {
		log.Printf("save state: %q", err)
	}
	d.saveMu.Unlock()

	var wg sync.WaitGroup
	for _, p := range panes {
		wg.Go(func() { p.Close() })
	}
	wg.Wait()
	d.o.Journal.Close(time.Second)
}

// client is one connection's outbound queue. Bursts coalesce: a pending
// StateMsg is built from the current state when the writer gets to it, and a
// newer Frame for a pane replaces an unsent older one.
type client struct {
	conn    *proto.Conn
	nc      net.Conn // for write deadlines
	session string   // the session a GUI shows, from SessionShow; guarded by Daemon.mu
	wake    chan struct{}
	mu      sync.Mutex
	state   bool
	frames  map[string]proto.Frame
	msgs    []any
}

func (c *client) push(f func()) {
	c.mu.Lock()
	f()
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// queue adds m to the unsent messages, or disconnects a client that has let
// maxQueued pile up.
func (c *client) queue(m any) {
	c.push(func() {
		if len(c.msgs) >= maxQueued {
			c.conn.Close()
			return
		}
		c.msgs = append(c.msgs, m)
	})
}

func (d *Daemon) writeLoop(c *client, done <-chan struct{}) {
	for {
		select {
		case <-c.wake:
		case <-done:
			return
		}
		c.mu.Lock()
		state, frames, msgs := c.state, c.frames, c.msgs
		c.state, c.frames, c.msgs = false, map[string]proto.Frame{}, nil
		c.mu.Unlock()
		var out []any
		if state {
			d.mu.Lock()
			out = append(out, proto.StateMsg{State: d.snapshot()})
			d.mu.Unlock()
		}
		for _, f := range frames {
			out = append(out, f)
		}
		for _, m := range append(out, msgs...) {
			c.nc.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.conn.Send(m); err != nil {
				c.conn.Close()
				return
			}
		}
	}
}

// serveConn requires Hello first. Only Kind "gui" receives StateMsg, Frame
// and PaneExited pushes, and "watch" the first and last of those; every kind
// gets Error replies to failed requests.
func (d *Daemon) serveConn(ctx context.Context, nc net.Conn) {
	conn := proto.NewConn(nc)
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()

	m, err := conn.Recv()
	if err != nil {
		return
	}
	hello, ok := m.(proto.Hello)
	if !ok || hello.Version != proto.Version {
		if ok, held := refusals.Allow(fmt.Sprint(hello.Version), time.Now()); ok {
			log.Printf("client refused: protocol version %d, want %d%s", hello.Version, proto.Version, heldNote(held))
		}
		conn.Send(proto.Error{Message: fmt.Sprintf("daemon speaks protocol version %d; send Hello{Version: %d} first", proto.Version, proto.Version)})
		return
	}

	c := &client{conn: conn, nc: nc, wake: make(chan struct{}, 1), frames: map[string]proto.Frame{}}
	cid := newID()[:6]
	// errs keeps an input flood into a pane that stopped reading from
	// logging a line per message.
	errs := logs.Limiter{Every: 10 * time.Second}
	if hello.Kind != "hook" { // one connection per agent event
		kind := clientKind(hello.Kind)
		log.Printf("client %s: %s connected", cid, kind)
		defer func() { log.Printf("client %s: %s disconnected: %q", cid, kind, err) }()
	}
	done := make(chan struct{})
	defer close(done)
	go d.writeLoop(c, done)

	if hello.Kind == "gui" {
		if err := d.firstSession(ctx, hello.Cwd, hello.Session); err != nil {
			c.queue(proto.Error{Message: err.Error()})
		}
		d.mu.Lock()
		d.clients[c] = struct{}{}
		c.push(func() {
			c.state = true
			for id, p := range d.panes {
				c.frames[id] = d.frame(id, p)
			}
		})
		d.mu.Unlock()
		defer func() {
			d.mu.Lock()
			delete(d.clients, c)
			if c.session != "" && !d.closing {
				d.changed() // Session.Windows drops
			}
			d.mu.Unlock()
		}()
	}
	if hello.Kind == "watch" {
		d.mu.Lock()
		d.watchers[c] = struct{}{}
		c.push(func() { c.state = true })
		d.mu.Unlock()
		defer func() {
			d.mu.Lock()
			delete(d.watchers, c)
			d.mu.Unlock()
		}()
	}

	for {
		var m any
		m, err = conn.Recv()
		if err != nil {
			return
		}
		if _, ok := m.(proto.Sync); ok {
			// Every earlier request on this connection is handled: replies
			// are synchronous.
			d.mu.Lock()
			s := d.snapshot()
			d.mu.Unlock()
			c.queue(proto.StateMsg{State: s})
			continue
		}
		if show, ok := m.(proto.SessionShow); ok && hello.Kind == "gui" {
			if err := d.sessionShow(c, show.SessionID); err != nil {
				c.queue(proto.Error{Message: err.Error()})
			}
			continue
		}
		start := time.Now()
		herr := d.handle(ctx, m)
		if took := time.Since(start); took > slowHandler {
			log.Printf("client %s: %T took %v", cid, m, took.Round(time.Millisecond))
		}
		if herr != nil {
			if ok, held := errs.Allow(fmt.Sprintf("%T", m), start); ok {
				log.Printf("client %s: %T: %q%s", cid, m, herr, heldNote(held))
			}
			c.queue(proto.Error{Message: herr.Error()})
		}
	}
}

// clientKind is a Hello's Kind for the log: a kind pitwall sends, else
// "other", since a client can send any text.
func clientKind(k string) string {
	switch k {
	case "gui", "cli", "watch", "hook":
		return k
	}
	return "other"
}

// slowHandler is how long one request may take before the log notes it.
const slowHandler = 500 * time.Millisecond

// refusals keeps hook processes from a newer or older binary, one
// connection per agent event, to a log line a minute per version.
var refusals = logs.Limiter{Every: time.Minute}

// resizes keeps a window drag to a log line a second per pane.
var resizes = logs.Limiter{Every: time.Second}

// heldNote is the suffix for a line that a Limiter held others back for.
func heldNote(held int) string {
	if held == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d more since the last line)", held)
}

func (d *Daemon) handle(ctx context.Context, m any) error {
	switch m := m.(type) {
	case proto.AddProject:
		return d.addProject(ctx, m)
	case proto.NewWorkspace:
		return d.newWorkspace(ctx, m)
	case proto.RenameWorkspace:
		return d.renameWorkspace(m)
	case proto.SetProjectAppearance:
		return d.setAppearance(m)
	case proto.DetachSession:
		return d.editWorkspace(m.WorkspaceID, func(w *model.Workspace) error { w.Detached = m.Detached; return nil })
	case proto.KillSession:
		return d.killSession(m)
	case proto.FocusSession:
		return d.focusSession(m)
	case proto.NewTab:
		return d.newTab(ctx, m)
	case proto.CloseTab:
		return d.killSession(proto.KillSession{WorkspaceID: m.WorkspaceID})
	case proto.SelectTab:
		return nil // a session has one tab
	case proto.RenameTab:
		return d.renameTab(m)
	case proto.GroupByFolder:
		return d.groupByFolder(ctx, m)
	case proto.SetLayout:
		return d.setLayout(m)
	case proto.DeleteWorkspace:
		return d.deleteWorkspace(ctx, m)
	case proto.OpenPane:
		return d.openPane(m)
	case proto.ClosePane:
		return d.closePane(m.Pane)
	case proto.Input:
		d.mu.Lock()
		p, in := d.panes[m.Pane], d.inputs[m.Pane]
		d.mu.Unlock()
		if p == nil {
			return fmt.Errorf("no pane %s", m.Pane)
		}
		d.unscroll(m.Pane, p)
		select {
		case in <- m.Data:
			d.noteInput(m.Pane, m.Data)
			return nil
		default:
			return fmt.Errorf("pane %s is not reading its input; dropped %d bytes", m.Pane, len(m.Data))
		}
	case proto.Resize:
		p, err := d.pane(m.Pane)
		if err != nil {
			return err
		}
		if err := p.Resize(m.Cols, m.Rows); err != nil {
			return err
		}
		size := [2]int{m.Cols, m.Rows}
		d.mu.Lock()
		exited := d.inputs[m.Pane] == nil
		old := d.sizes[m.Pane]
		if d.panes[m.Pane] == p {
			d.sizes[m.Pane] = size
		}
		d.mu.Unlock()
		if old != size {
			if ok, held := resizes.Allow(m.Pane, time.Now()); ok {
				log.Printf("pane %s: resized %dx%d to %dx%d%s", m.Pane, old[0], old[1], m.Cols, m.Rows, heldNote(held))
			}
		}
		if exited {
			d.pushFrame(m.Pane, p) // a held pane's watcher has stopped
		}
		return nil
	case proto.Scroll:
		return d.scroll(m)
	case proto.AgentEvent:
		if settled, ok := agent.PiSettled(m.Payload); ok && m.Provider == model.ProviderPi {
			// why: a shutdown that carries the run's result is that agent_settled, then the shutdown.
			if err := d.agentEvent(ctx, proto.AgentEvent{Pane: m.Pane, Provider: m.Provider, Payload: settled}); err != nil {
				return err
			}
		}
		return d.agentEvent(ctx, m)
	case proto.SeePane:
		return d.seePane(m.Pane)
	case proto.NewSession:
		return d.newSession(ctx, m)
	case proto.SessionNew:
		return d.sessionNew(ctx, m)
	case proto.SessionRename:
		return d.sessionRename(m)
	case proto.SessionKill:
		return d.sessionKill(m)
	case proto.SessionShow:
		return errors.New("only a GUI shows a session")
	case proto.SetSessionGroup:
		return d.setSessionGroup(m)
	case proto.MoveSession:
		return d.moveSession(m)
	case proto.MoveGroup:
		return d.moveGroup(m)
	case proto.NewGroup:
		return d.newGroup(m)
	case proto.RenameGroup:
		return d.renameGroup(m)
	case proto.DeleteGroup:
		return d.deleteGroup(m)
	}
	return fmt.Errorf("unexpected message %T", m)
}

func (d *Daemon) addProject(ctx context.Context, m proto.AddProject) error {
	path, err := filepath.Abs(m.Path)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(path); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	root, isGit := d.o.RepoRoot(ctx, path)
	kind := model.ProjectGit
	if !isGit {
		root, kind = path, model.ProjectFolder
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	session, err := d.targetSession(m.SessionID, "")
	if err != nil {
		return err
	}
	for _, p := range d.st.Projects {
		if p.SessionID == session && p.Root == root {
			return fmt.Errorf("project %s is already open", root)
		}
	}
	p := model.Project{ID: newID(), SessionID: session, Name: filepath.Base(root), Root: root, Kind: kind, Color: "neutral"}
	d.st.Projects = append(d.st.Projects, p)
	if kind == model.ProjectFolder {
		d.st.Workspaces = append(d.st.Workspaces, model.Workspace{
			ID: newID(), SessionID: session, ProjectID: p.ID, Name: p.Name, Path: root, RepoRoot: root, UpdatedAt: time.Now(),
		})
	}
	d.changed()
	return nil
}

func (d *Daemon) newWorkspace(ctx context.Context, m proto.NewWorkspace) error {
	d.mu.Lock()
	pi := slices.IndexFunc(d.st.Projects, func(p model.Project) bool { return p.ID == m.ProjectID })
	var p model.Project
	if pi >= 0 {
		p = d.st.Projects[pi]
	}
	d.mu.Unlock()
	if pi < 0 {
		return fmt.Errorf("no project %s", m.ProjectID)
	}
	named := m.Name != ""
	if !named {
		m.Name = d.nextWorkspaceName(p.ID)
	}

	path, branch := p.Root, ""
	if p.Kind == model.ProjectGit {
		var err error
		if path, branch, err = d.o.AddWorktree(ctx, p.Root, m.Name); err != nil {
			return err
		}
	}
	root := d.repoRoot(ctx, path)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Workspaces = append(d.st.Workspaces, model.Workspace{
		ID: newID(), SessionID: p.SessionID, ProjectID: p.ID, Name: m.Name, NameSet: named, Branch: branch, Path: path, UpdatedAt: time.Now(),
		WorktreeRoot: worktreeRoot(p, path), RepoRoot: root,
	})
	d.changed()
	return nil
}

func (d *Daemon) editWorkspace(id string, f func(*model.Workspace) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(id)
	if w == nil {
		return fmt.Errorf("no workspace %s", id)
	}
	if err := f(w); err != nil {
		return err
	}
	d.changed()
	return nil
}

// setLayout takes a layout only when its leaves are exactly the tab's panes:
// one built from stale state could hide a live pane or bring back a closed
// one. The GUI re-reads state when it gets the error.
func (d *Daemon) setLayout(m proto.SetLayout) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(m.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	ti := tabIndex(w, m.TabID)
	if ti < 0 {
		return fmt.Errorf("no tab %q in session %s", m.TabID, w.ID)
	}
	t := &w.Tabs[ti]
	want, got := layout.Panes(t.Layout), layout.Panes(m.Layout)
	slices.Sort(want)
	slices.Sort(got)
	if m.Layout == nil || !slices.Equal(got, want) || !sanitize(m.Layout) {
		return fmt.Errorf("layout for tab %s does not match its panes", t.ID)
	}
	t.Layout = m.Layout
	d.changed()
	return nil
}

// minRatio is the smallest share of a split SetLayout keeps; the GUI clamps
// drags to the same value.
const minRatio = 0.05

// sanitize reports whether every split under n has two or more children and
// no leaf has any, and rewrites each split's ratios to sum to 1 with none
// below minRatio before normalizing. A missing or invalid ratio becomes an
// equal share.
func sanitize(n *layout.Node) bool {
	if n.Pane != "" {
		return len(n.Children) == 0
	}
	if len(n.Children) < 2 {
		return false
	}
	r := make([]float64, len(n.Children))
	total := 0.0
	for i, c := range n.Children {
		if c == nil || !sanitize(c) {
			return false
		}
		r[i] = 1 / float64(len(r))
		if len(n.Ratios) == len(r) && n.Ratios[i] >= 0 && !math.IsInf(n.Ratios[i], 0) {
			r[i] = max(n.Ratios[i], minRatio)
		}
		total += r[i]
	}
	for i := range r {
		r[i] /= total
	}
	n.Ratios = r
	return true
}

func (d *Daemon) deleteWorkspace(ctx context.Context, m proto.DeleteWorkspace) error {
	d.mu.Lock()
	w := d.workspace(m.WorkspaceID)
	var ws model.Workspace
	if w != nil {
		ws = *w
	}
	d.mu.Unlock()
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	// Only a worktree pitwall created for this session is removed. Sessions
	// can be regrouped anywhere, so neither the group nor the path decides.
	var err error
	if ws.WorktreeRoot != "" {
		// A kept branch is still reported, but the worktree is gone, so the
		// workspace goes too.
		if err = d.o.RemoveWorktree(ctx, ws.WorktreeRoot, ws.Path, m.RemoveBranch); err != nil && !errors.Is(err, gitstat.ErrBranchKept) {
			return err
		}
	}

	d.mu.Lock()
	closing := d.removeWorkspace(ws.ID)
	d.changed()
	d.mu.Unlock()
	closeAll(closing)
	return err
}

func (d *Daemon) openPane(m proto.OpenPane) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(m.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	var t *model.Tab
	if ti := tabIndex(w, m.TabID); ti >= 0 {
		t = &w.Tabs[ti]
	} else if m.TabID != "" {
		return fmt.Errorf("no tab %s in session %s", m.TabID, w.ID)
	}
	if m.Target != "" && (t == nil || !slices.Contains(layout.Panes(t.Layout), m.Target)) {
		return fmt.Errorf("no pane %s in tab %q of session %s", m.Target, m.TabID, w.ID)
	}
	id := newID()
	if err := d.start(id, m.Cmd, w.Path); err != nil {
		return err
	}
	d.st.Panes = append(d.st.Panes, model.Pane{ID: id, WorkspaceID: w.ID, Cmd: m.Cmd, Cwd: w.Path})
	leaf := &layout.Node{Pane: id}
	switch {
	case t == nil: // a project's workspace gets its first tab with its first pane
		w.Tabs = append(w.Tabs, model.Tab{ID: newID(), Layout: leaf})
		w.ActiveTab = w.Tabs[len(w.Tabs)-1].ID
	case m.Target == "":
		// No target with panes already open: put the new pane beside the whole tree.
		t.Layout = &layout.Node{Dir: m.Dir, Ratios: []float64{0.5, 0.5}, Children: []*layout.Node{t.Layout, leaf}}
	default:
		t.Layout = d.o.Split(t.Layout, m.Target, id, m.Dir)
	}
	d.changed()
	return nil
}

// closePane closes a pane, and its tab and session when it was their last.
func (d *Daemon) closePane(id string) error {
	d.mu.Lock()
	if !slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }) {
		d.mu.Unlock()
		return fmt.Errorf("no pane %s", id)
	}
	closing := d.removePane(id)
	d.changed()
	d.mu.Unlock()
	closeAll(closing)
	return nil
}

// dropPane forgets a pane's handle and activity; the caller removes it from
// st.Panes and closes the returned handle outside the lock.
func (d *Daemon) dropPane(id string) Pane {
	h := d.panes[id]
	delete(d.panes, id)
	delete(d.sizes, id)
	delete(d.inputs, id)
	delete(d.views, id)
	delete(d.resumed, id)
	delete(d.live.hookAt, id)
	delete(d.live.fg, id)
	delete(d.live.started, id)
	delete(d.live.fresh, id)
	delete(d.live.resumeSeen, id)
	delete(d.live.det, id)
	delete(d.live.piRuntime, id)
	delete(d.live.piRetired, id)
	delete(d.attn, id)
	d.forgetDecisions(id)
	d.st.Activities = slices.DeleteFunc(d.st.Activities, func(a model.Activity) bool { return a.PaneID == id })
	return h
}

// closeAll closes in the background: Pane.Close can take 2s to kill a
// process group that ignores SIGHUP.
func closeAll(ps []Pane) {
	for _, p := range ps {
		if p != nil {
			go func() {
				start := time.Now()
				p.Close()
				if took := time.Since(start); took > slowClose {
					log.Printf("pane close took %v", took.Round(time.Millisecond))
				}
			}()
		}
	}
}

// slowClose is how long closing a pane may take before the log notes it:
// longer than the SIGKILL that follows an ignored SIGHUP.
const slowClose = 3 * time.Second

// agentEvent applies one hook event and starts, in the background, the
// decisions it calls for.
func (d *Daemon) agentEvent(ctx context.Context, m proto.AgentEvent) error {
	d.mu.Lock()
	pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane })
	if pi < 0 {
		d.mu.Unlock()
		return fmt.Errorf("no pane %s", m.Pane)
	}
	p := &d.st.Panes[pi]
	// The pane's pi runtime is the one of its latest session_start; a report
	// from any other, such as one /reload replaced, changes nothing, and
	// neither does one a runtime sends after its session_shutdown.
	piEvent := ""
	if m.Provider == model.ProviderPi {
		piEvent = agent.PiEvent(m.Payload)
	}
	if rt, start := agent.PiRuntime(m.Payload); rt != "" {
		if d.live.piRetired[p.ID][rt] {
			d.mu.Unlock()
			log.Printf("pane %s: dropped a report from a pi runtime that shut down", p.ID)
			return nil
		}
		if piEvent == "session_shutdown" {
			if d.live.piRetired == nil {
				d.live.piRetired = map[string]map[string]bool{}
			}
			if d.live.piRetired[p.ID] == nil {
				d.live.piRetired[p.ID] = map[string]bool{}
			}
			d.live.piRetired[p.ID][rt] = true
		}
		if cur := d.live.piRuntime[p.ID]; !start && cur != "" && cur != rt {
			d.mu.Unlock()
			return nil
		}
		if start {
			if d.live.piRuntime == nil {
				d.live.piRuntime = map[string]string{}
			}
			d.live.piRuntime[p.ID] = rt
		}
	}
	d.sawHook(p.ID)
	ai := slices.IndexFunc(d.st.Activities, func(a model.Activity) bool { return a.PaneID == p.ID })
	var prev *model.Activity
	if ai >= 0 {
		cp := d.st.Activities[ai]
		prev = &cp
	}
	now := time.Now()
	changed := false
	// why: a held `pi -p` pane shuts down right after its result; the tab keeps showing it.
	keep := p.Held && piEvent == "session_shutdown" && prev != nil && ended(prev.State)
	if keep {
		log.Printf("pane %s: pi shut down; held, keeping %q", p.ID, prev.State)
	}

	if next, ok := d.o.Derive(prev, m.Provider, m.Payload, now); ok && !keep {
		changed = true
		clearDecisions(&next) // a new state needs new answers
		if secrets := d.dec.cur.Secrets; len(secrets) > 0 {
			// The known keys, from the whole message before it is cut.
			if msg := agent.LastMessage(m.Payload); next.State == model.StateCompleted && msg != "" {
				next.Detail = agent.Summary(msg, secrets...)
			} else {
				next.Detail = decide.Redact(next.Detail, secrets...)
			}
		}
		switch {
		case next.State == "" && ai >= 0:
			d.st.Activities = slices.Delete(d.st.Activities, ai, ai+1)
		case next.State == "":
		default:
			next.PaneID, next.WorkspaceID = p.ID, p.WorkspaceID
			if next.Provider == "" {
				next.Provider = m.Provider
			}
			if ai >= 0 {
				d.st.Activities[ai] = next
			} else {
				d.st.Activities = append(d.st.Activities, next)
			}
			if next.State == model.StateCompleted && (prev == nil || prev.State != model.StateCompleted) {
				go d.refreshStats(ctx, p.WorkspaceID)
			}
		}
	}
	if p.Provider != m.Provider {
		if p.Provider != "" {
			p.AgentMode = "" // a permission mode belongs to the agent that reported it
		}
		p.Provider, changed = m.Provider, true
	}
	prevMode := p.AgentMode
	keepMode := d.agentMode(p, m.Payload)
	if p.AgentMode != prevMode {
		changed = true
	}
	if sid := d.o.SessionID(m.Provider, m.Payload); sid != "" && sid != p.SessionID {
		p.SessionID, p.Prompt, p.Transcript, changed = sid, "", "", true // a new session reports its own file
		if !keepMode {
			p.AgentMode = "" // and its own mode
		}
	}
	if t := agent.Transcript(m.Payload); t != "" && t != p.Transcript {
		p.Transcript, changed = t, true
	}
	if mode := agent.PermissionMode(m.Payload); mode != "" && mode != p.AgentMode && !keepMode {
		p.AgentMode, changed = mode, true
	}
	if agent.PermissionMode(m.Payload) != "" {
		delete(d.live.fresh, p.ID)
	}
	if p.Prompt == "" {
		// Secrets go before the cut, so no part of a key names the tab.
		if s := promptTitle(decide.Redact(agent.Prompt(m.Provider, m.Payload), d.dec.cur.Secrets...)); s != "" {
			p.Prompt, changed = s, true
		}
	}
	d.noteOutcomes(p.ID, m.Payload, now)
	job := d.planDecisions(*p, m, now)
	if changed {
		if w := d.workspace(p.WorkspaceID); w != nil {
			w.UpdatedAt = now
		}
		d.changed()
	}
	d.mu.Unlock()
	d.runDecisions(ctx, job)
	return nil
}

// nextWorkspaceName is "workspace-N", the first N not taken in the project,
// so it is also a valid, unused branch name.
func (d *Daemon) nextWorkspaceName(projectID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for n := 1; ; n++ {
		name := fmt.Sprintf("workspace-%d", n)
		if !slices.ContainsFunc(d.st.Workspaces, func(w model.Workspace) bool {
			return w.ProjectID == projectID && (w.Name == name || w.Branch == name)
		}) {
			return name
		}
	}
}

func (d *Daemon) statsLoop(ctx context.Context) {
	t := time.NewTicker(d.o.StatsInterval)
	defer t.Stop()
	for {
		d.refreshStats(ctx, "")
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshStats reads the branch and stats of every session in a git repo,
// detached ones included, or of session only when it is not "". Sessions
// outside a repo have no branch and no stats.
func (d *Daemon) refreshStats(ctx context.Context, only string) {
	d.mu.Lock()
	paths := map[string]string{}
	for _, w := range d.st.Workspaces {
		if only == "" || w.ID == only {
			paths[w.ID] = d.st.LivePath(w) // branch and stats follow the shell's cd
		}
	}
	d.mu.Unlock()

	type git struct {
		branch string
		repo   bool
		stats  *model.BranchStats
	}
	got := map[string]git{}
	for id, path := range paths {
		var g git
		g.branch, g.repo = d.o.Branch(ctx, path)
		if g.repo {
			if s, err := d.o.Stats(ctx, path); err == nil {
				g.stats = &s
			}
		}
		got[id] = g
	}
	if ctx.Err() != nil {
		return // git was cut short, not answering "not a repo"
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	changed := false
	for id, g := range got {
		w := d.workspace(id)
		if w == nil {
			continue
		}
		if w.Branch != g.branch {
			w.Branch, changed = g.branch, true
		}
		old, had := d.st.Stats[id]
		switch {
		case !g.repo && had:
			delete(d.st.Stats, id)
			changed = true
		case g.stats != nil && (!had || old != *g.stats):
			d.st.Stats[id], changed = *g.stats, true
		}
	}
	if changed {
		d.changed()
	}
}

// start launches a pane and its watcher. Callers hold d.mu.
func (d *Daemon) start(id string, cmd []string, cwd string) error {
	p, err := d.o.StartPane(pane.Config{ID: id, Cmd: cmd, Cwd: cwd, Cols: defaultCols, Rows: defaultRows, NewVT: d.notifyingVT(id)})
	if err != nil {
		return err
	}
	d.panes[id] = p
	d.sizes[id] = [2]int{defaultCols, defaultRows}
	program, provider := "shell", model.Provider("")
	if len(cmd) > 0 {
		program = filepath.Base(cmd[0]) // never its arguments
	}
	if i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == id }); i >= 0 {
		provider = d.st.Panes[i].Provider
	}
	log.Printf("pane %s: started %q, provider %q, %dx%d", id, program, provider, defaultCols, defaultRows)
	in := make(chan []byte, inputQueue)
	d.inputs[id] = in
	go d.watch(id, p)
	go writeInput(p, in)
	return nil
}

// writeInput writes one pane's input on a goroutine of its own: a program
// that stops reading stdin blocks this goroutine, never a client connection
// or shutdown.
// ponytail: the PTY fd is in blocking mode, so a write stuck when the pane
// closes leaks this goroutine and the fd until the daemon exits; a
// non-blocking fd in package pane would let Close interrupt it.
func writeInput(p Pane, in <-chan []byte) {
	for {
		select {
		case b := <-in:
			p.Write(b) // a pane that cannot take input has exited, which watch reports
		case <-p.Done():
			return
		}
	}
}

// titlePoll is how long a pane's title may lag its output while no GUI is
// connected; with one, every frame carries the title.
const titlePoll = 500 * time.Millisecond

// watch pushes at most 60 frames a second for one pane and keeps its title.
// Dirty has capacity 1, so signals that arrive during the wait fold into the
// next snapshot.
func (d *Daemon) watch(id string, p Pane) {
	var last time.Time
	var titleDue <-chan time.Time
	for {
		select {
		case <-p.Dirty():
			if wait := time.Until(last.Add(frameInterval)); wait > 0 {
				select {
				case <-time.After(wait):
				case <-p.Done():
				}
			}
			last = time.Now()
			if title, ok := d.pushFrame(id, p); ok {
				d.setTitle(id, p, title)
			} else if titleDue == nil {
				titleDue = time.After(titlePoll)
			}
		case <-titleDue:
			titleDue = nil
			d.setTitle(id, p, p.Snapshot().Title)
		case <-p.Done():
			d.pushFrame(id, p)
			d.exited(id, p)
			return
		}
	}
}

// pushFrame sends GUIs a frame of id and returns its title, or reports false
// when there was no GUI to build one for.
func (d *Daemon) pushFrame(id string, p Pane) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.panes[id] != p || len(d.clients) == 0 {
		return "", false
	}
	f := d.frame(id, p)
	for c := range d.clients {
		c.push(func() { c.frames[id] = f })
	}
	return f.Grid.Title, true
}

// resumeGrace is how soon after a restart a resumed agent may fail and get a
// shell instead of closing its pane; tests shorten it.
var resumeGrace = 3 * time.Second

// exited handles a pane whose process ended. A held pane stays, Exited with
// the exit code, and keeps a completed or error activity, so its tab still
// shows how the agent's run ended; a held pane whose resume fails keeps that
// failure's exit code too, with no shell. Any other pane closes, and its tab
// and session when it was their last, except that an agent resumed in a pane
// that was a shell gets the shell back whenever it exits, as it would have
// before the restart, and a pane opened with a command whose resume exits
// non-zero within resumeGrace of the restart (the session expired, the
// binary moved) gets a shell in its place too. After that grace, its exit
// closes the pane like any other command's.
func (d *Daemon) exited(id string, p Pane) {
	d.mu.Lock()
	if d.closing || d.panes[id] != p {
		d.mu.Unlock()
		return // closed on purpose: by ClosePane, DeleteWorkspace or shutdown
	}
	i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == id })
	if i < 0 {
		d.mu.Unlock()
		return
	}
	code := p.ExitCode()
	sp := &d.st.Panes[i]
	// A held pane keeps a failed resume's exit, which pitwall wait reports.
	if at, ok := d.resumed[id]; ok && (len(sp.Cmd) == 0 || !sp.Held && code != 0 && time.Since(at) < resumeGrace) {
		log.Printf("pane %s: exited %d; resumed %q session, opening a shell", id, code, sp.Provider)
		closeAll([]Pane{d.dropPane(id)})
		sp.Cmd, sp.Provider, sp.SessionID, sp.Title, sp.Prompt, sp.AgentMode, sp.Transcript = nil, "", "", "", "", "", ""
		err := d.start(id, nil, sp.Cwd)
		if err == nil {
			d.changed()
			d.mu.Unlock()
			return
		}
		log.Printf("pane %s: shell after failed resume: %q", id, err)
	}
	var closing []Pane
	if sp.Held {
		// why: the tab keeps the command's output on screen, and pitwall wait reads its status.
		d.st.Panes[i].Exited, d.st.Panes[i].ExitCode = true, code
		log.Printf("pane %s: exited %d; held", id, code)
		delete(d.inputs, id)
		delete(d.live.hookAt, id)
		delete(d.live.fg, id)
		delete(d.live.started, id)
		delete(d.live.fresh, id)
		delete(d.live.resumeSeen, id)
		d.st.Activities = slices.DeleteFunc(d.st.Activities, func(a model.Activity) bool { return a.PaneID == id && !ended(a.State) })
	} else {
		log.Printf("pane %s: exited %d; closed", id, code)
		closing = d.removePane(id)
	}
	d.changed()
	for c := range d.clients {
		c.queue(proto.PaneExited{Pane: id, ExitCode: code})
	}
	for c := range d.watchers {
		c.queue(proto.PaneExited{Pane: id, ExitCode: code})
	}
	d.mu.Unlock()
	closeAll(closing)
}

// ended reports whether s is how an agent's run ends: done or error.
func ended(s model.AgentState) bool { return s == model.StateCompleted || s == model.StateError }

func (d *Daemon) pane(id string) (Pane, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p := d.panes[id]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("no pane %s", id)
}

// workspace returns a pointer into d.st; callers hold d.mu.
func (d *Daemon) workspace(id string) *model.Workspace {
	if i := slices.IndexFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.ID == id }); i >= 0 {
		return &d.st.Workspaces[i]
	}
	return nil
}

// changed records a mutation: callers hold d.mu. GUI clients get a StateMsg
// and a save runs saveDelay after the first unsaved change, so a steady
// stream of agent events cannot postpone it forever.
func (d *Daemon) changed() {
	d.pruneNotices()
	d.retitle()
	d.fixOrders()
	d.st.Version++
	for c := range d.clients {
		c.push(func() { c.state = true })
	}
	for c := range d.watchers {
		c.push(func() { c.state = true })
	}
	if !d.savePending && !d.closing {
		d.savePending = true
		time.AfterFunc(saveDelay, d.save)
	}
}

func (d *Daemon) save() {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		return
	}
	d.savePending = false
	s := d.saveSnapshot()
	d.mu.Unlock()
	if err := d.o.Save(s); err != nil {
		log.Printf("save state: %q", err)
	}
}

// saveSnapshot is snapshot with each live pane's current directory, so a
// restart relaunches the shell where the user left it.
func (d *Daemon) saveSnapshot() model.State {
	s := d.snapshot()
	for i, p := range s.Panes {
		if h := d.panes[p.ID]; h != nil && !p.Exited {
			if cwd := h.Cwd(); cwd != "" {
				s.Panes[i].Cwd = cwd
			}
		}
	}
	return s
}

// snapshot deep-copies the state so it can be encoded outside d.mu.
func (d *Daemon) snapshot() model.State {
	s := d.st
	s.Sessions = slices.Clone(s.Sessions)
	for i := range s.Sessions {
		s.Sessions[i].Order = slices.Clone(s.Sessions[i].Order)
		s.Sessions[i].Windows = 0
		for c := range d.clients {
			if c.session == s.Sessions[i].ID {
				s.Sessions[i].Windows++
			}
		}
	}
	s.Projects = slices.Clone(s.Projects)
	s.Workspaces = slices.Clone(s.Workspaces)
	for i := range s.Workspaces {
		tabs := slices.Clone(s.Workspaces[i].Tabs)
		for j := range tabs {
			tabs[j].Layout = cloneNode(tabs[j].Layout)
		}
		s.Workspaces[i].Tabs = tabs
	}
	s.Panes = slices.Clone(s.Panes)
	s.Activities = d.attended()
	s.Stats = maps.Clone(s.Stats)
	s.Decide = d.decideInfo()
	return s
}

func cloneNode(n *layout.Node) *layout.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Ratios = slices.Clone(n.Ratios)
	c.Children = make([]*layout.Node, len(n.Children))
	for i, ch := range n.Children {
		c.Children[i] = cloneNode(ch)
	}
	if n.Children == nil {
		c.Children = nil
	}
	return &c
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// worktreeRoot is p.Root when path is a worktree AddWorktree made for p,
// which puts them in <root>/.worktrees/<name>.
func worktreeRoot(p model.Project, path string) string {
	if p.Kind == model.ProjectGit && filepath.Dir(path) == filepath.Join(p.Root, ".worktrees") {
		return p.Root
	}
	return ""
}
