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
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/layout"
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
	Stats          func(ctx context.Context, worktree string) (model.BranchStats, error)
	AddWorktree    func(ctx context.Context, repoRoot, name string) (path, branch string, err error)
	RemoveWorktree func(ctx context.Context, repoRoot, path string, deleteBranch bool) error
	Save           func(model.State) error
	Load           func() (model.State, error)
	RestoreCmd     func(model.Pane) []string
	Split          func(root *layout.Node, target, newPane string, dir layout.Dir) *layout.Node
	Remove         func(root *layout.Node, pane string) *layout.Node
	StatsInterval  time.Duration // 0 means 30s
}

type Daemon struct {
	o Options

	mu          sync.Mutex
	st          model.State
	panes       map[string]Pane
	inputs      map[string]chan []byte // per pane, drained by writeInput
	views       map[string]*view       // scroll positions, see scroll.go
	clients     map[*client]struct{}   // gui clients only
	closing     bool
	savePending bool
	live        liveness

	saveMu sync.Mutex // serializes snapshot+write so an old save never lands last
}

// New loads state from store.Path() and relaunches saved panes with
// store.RestoreCmd.
func New() (*Daemon, error) {
	path := store.Path()
	return NewWith(Options{
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
		Stats:          gitstat.Stats,
		AddWorktree:    gitstat.AddWorktree,
		RemoveWorktree: gitstat.RemoveWorktree,
		Save:           func(s model.State) error { return store.Save(path, s) },
		Load:           func() (model.State, error) { return store.Load(path) },
		RestoreCmd:     store.RestoreCmd,
		Split:          layout.Split,
		Remove:         layout.Remove,
	})
}

// NewWith loads state through o.Load and relaunches every saved pane that had
// not exited, using o.RestoreCmd in the pane's saved Cwd. Activities from the
// previous run are dropped: the agents behind them are new processes.
func NewWith(o Options) (*Daemon, error) {
	if o.StatsInterval == 0 {
		o.StatsInterval = 30 * time.Second
	}
	st, err := o.Load()
	if err != nil {
		return nil, err
	}
	if st.Stats == nil {
		st.Stats = map[string]model.BranchStats{}
	}
	st.Activities = nil
	d := &Daemon{o: o, st: st, panes: map[string]Pane{}, inputs: map[string]chan []byte{}, clients: map[*client]struct{}{}}
	d.mu.Lock() // watchers of already started panes read d.panes
	defer d.mu.Unlock()
	for i := range d.st.Panes {
		p := &d.st.Panes[i]
		if p.Exited {
			continue
		}
		if err := d.start(p.ID, o.RestoreCmd(*p), p.Cwd); err != nil {
			log.Printf("pitwall: restore pane %s: %v", p.ID, err)
			p.Exited, p.ExitCode = true, -1
		}
	}
	return d, nil
}

// Serve accepts clients until ctx is done, then saves state and closes panes.
func (d *Daemon) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	context.AfterFunc(ctx, func() { ln.Close() })
	go d.statsLoop(ctx)
	go d.livenessLoop(ctx)

	var wg sync.WaitGroup
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
		log.Printf("pitwall: save state: %v", err)
	}
	d.saveMu.Unlock()

	var wg sync.WaitGroup
	for _, p := range panes {
		wg.Go(func() { p.Close() })
	}
	wg.Wait()
}

// client is one connection's outbound queue. Bursts coalesce: a pending
// StateMsg is built from the current state when the writer gets to it, and a
// newer Frame for a pane replaces an unsent older one.
type client struct {
	conn   *proto.Conn
	nc     net.Conn // for write deadlines
	wake   chan struct{}
	mu     sync.Mutex
	state  bool
	frames map[string]proto.Frame
	msgs   []any
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
// and PaneExited pushes; every kind gets Error replies to failed requests.
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
		conn.Send(proto.Error{Message: fmt.Sprintf("daemon speaks protocol version %d; send Hello{Version: %d} first", proto.Version, proto.Version)})
		return
	}

	c := &client{conn: conn, nc: nc, wake: make(chan struct{}, 1), frames: map[string]proto.Frame{}}
	done := make(chan struct{})
	defer close(done)
	go d.writeLoop(c, done)

	if hello.Kind == "gui" {
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
			d.mu.Unlock()
		}()
	}

	for {
		m, err := conn.Recv()
		if err != nil {
			return
		}
		if err := d.handle(ctx, m); err != nil {
			c.queue(proto.Error{Message: err.Error()})
		}
	}
}

func (d *Daemon) handle(ctx context.Context, m any) error {
	switch m := m.(type) {
	case proto.AddProject:
		return d.addProject(ctx, m)
	case proto.NewWorkspace:
		return d.newWorkspace(ctx, m)
	case proto.RenameWorkspace:
		return d.editWorkspace(m.WorkspaceID, func(w *model.Workspace) { w.Name = m.Name })
	case proto.ArchiveWorkspace:
		return d.editWorkspace(m.WorkspaceID, func(w *model.Workspace) { w.Archived = m.Archived })
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
		return p.Resize(m.Cols, m.Rows)
	case proto.Scroll:
		return d.scroll(m)
	case proto.AgentEvent:
		return d.agentEvent(ctx, m)
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
	for _, p := range d.st.Projects {
		if p.Root == root {
			return fmt.Errorf("project %s is already open", root)
		}
	}
	p := model.Project{ID: newID(), Name: filepath.Base(root), Root: root, Kind: kind, Color: "neutral"}
	d.st.Projects = append(d.st.Projects, p)
	if kind == model.ProjectFolder {
		d.st.Workspaces = append(d.st.Workspaces, model.Workspace{
			ID: newID(), ProjectID: p.ID, Name: p.Name, Path: root, UpdatedAt: time.Now(),
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
	if m.Name == "" {
		m.Name = d.nextWorkspaceName(p.ID)
	}

	path, branch := p.Root, ""
	if p.Kind == model.ProjectGit {
		var err error
		if path, branch, err = d.o.AddWorktree(ctx, p.Root, m.Name); err != nil {
			return err
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Workspaces = append(d.st.Workspaces, model.Workspace{
		ID: newID(), ProjectID: p.ID, Name: m.Name, Branch: branch, Path: path, UpdatedAt: time.Now(),
	})
	d.changed()
	return nil
}

func (d *Daemon) editWorkspace(id string, f func(*model.Workspace)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(id)
	if w == nil {
		return fmt.Errorf("no workspace %s", id)
	}
	f(w)
	d.changed()
	return nil
}

// setLayout takes a layout only when its leaves are exactly the workspace's
// panes: one built from stale state could hide a live pane or bring back a
// closed one. The GUI re-reads state when it gets the error.
func (d *Daemon) setLayout(m proto.SetLayout) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(m.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	var want []string
	for _, p := range d.st.Panes {
		if p.WorkspaceID == w.ID {
			want = append(want, p.ID)
		}
	}
	got := layout.Panes(m.Layout)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) || (m.Layout != nil && !sanitize(m.Layout)) {
		return fmt.Errorf("layout for workspace %s does not match its panes", w.ID)
	}
	w.Layout = m.Layout
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
	var proj model.Project
	if w != nil {
		ws = *w
		if i := slices.IndexFunc(d.st.Projects, func(p model.Project) bool { return p.ID == ws.ProjectID }); i >= 0 {
			proj = d.st.Projects[i]
		}
	}
	d.mu.Unlock()
	if w == nil {
		return fmt.Errorf("no workspace %s", m.WorkspaceID)
	}
	// The main checkout is never removed: only worktrees pitwall could have added.
	var err error
	if proj.Kind == model.ProjectGit && ws.Path != proj.Root {
		// A kept branch is still reported, but the worktree is gone, so the
		// workspace goes too.
		if err = d.o.RemoveWorktree(ctx, proj.Root, ws.Path, m.RemoveBranch); err != nil && !errors.Is(err, gitstat.ErrBranchKept) {
			return err
		}
	}

	d.mu.Lock()
	var closing []Pane
	for _, p := range d.st.Panes {
		if p.WorkspaceID == ws.ID {
			closing = append(closing, d.dropPane(p.ID))
		}
	}
	d.st.Panes = slices.DeleteFunc(d.st.Panes, func(p model.Pane) bool { return p.WorkspaceID == ws.ID })
	d.st.Workspaces = slices.DeleteFunc(d.st.Workspaces, func(w model.Workspace) bool { return w.ID == ws.ID })
	delete(d.st.Stats, ws.ID)
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
	if m.Target != "" && !slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool {
		return p.ID == m.Target && p.WorkspaceID == w.ID
	}) {
		return fmt.Errorf("no pane %s in workspace %s", m.Target, w.ID)
	}
	id := newID()
	if err := d.start(id, m.Cmd, w.Path); err != nil {
		return err
	}
	d.st.Panes = append(d.st.Panes, model.Pane{ID: id, WorkspaceID: w.ID, Cmd: m.Cmd, Cwd: w.Path})
	leaf := &layout.Node{Pane: id}
	switch {
	case w.Layout == nil:
		w.Layout = leaf
	case m.Target == "":
		// No target with panes already open: put the new pane beside the whole tree.
		w.Layout = &layout.Node{Dir: m.Dir, Ratios: []float64{0.5, 0.5}, Children: []*layout.Node{w.Layout, leaf}}
	default:
		w.Layout = d.o.Split(w.Layout, m.Target, id, m.Dir)
	}
	d.changed()
	return nil
}

func (d *Daemon) closePane(id string) error {
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if i < 0 {
		d.mu.Unlock()
		return fmt.Errorf("no pane %s", id)
	}
	if w := d.workspace(d.st.Panes[i].WorkspaceID); w != nil {
		w.Layout = d.o.Remove(w.Layout, id)
	}
	d.st.Panes = slices.Delete(d.st.Panes, i, i+1)
	h := d.dropPane(id)
	d.changed()
	d.mu.Unlock()
	closeAll([]Pane{h})
	return nil
}

// dropPane forgets a pane's handle and activity; the caller removes it from
// st.Panes and closes the returned handle outside the lock.
func (d *Daemon) dropPane(id string) Pane {
	h := d.panes[id]
	delete(d.panes, id)
	delete(d.inputs, id)
	delete(d.views, id)
	d.st.Activities = slices.DeleteFunc(d.st.Activities, func(a model.Activity) bool { return a.PaneID == id })
	return h
}

// closeAll closes in the background: Pane.Close can take 2s to kill a
// process group that ignores SIGHUP.
func closeAll(ps []Pane) {
	for _, p := range ps {
		if p != nil {
			go p.Close()
		}
	}
}

func (d *Daemon) agentEvent(ctx context.Context, m proto.AgentEvent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane })
	if pi < 0 {
		return fmt.Errorf("no pane %s", m.Pane)
	}
	p := &d.st.Panes[pi]
	d.sawHook(p.ID)
	ai := slices.IndexFunc(d.st.Activities, func(a model.Activity) bool { return a.PaneID == p.ID })
	var prev *model.Activity
	if ai >= 0 {
		cp := d.st.Activities[ai]
		prev = &cp
	}
	now := time.Now()
	changed := false

	if next, ok := d.o.Derive(prev, m.Provider, m.Payload, now); ok {
		changed = true
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
		p.Provider, changed = m.Provider, true
	}
	if sid := d.o.SessionID(m.Provider, m.Payload); sid != "" && sid != p.SessionID {
		p.SessionID, changed = sid, true
	}
	if changed {
		if w := d.workspace(p.WorkspaceID); w != nil {
			w.UpdatedAt = now
		}
		d.changed()
	}
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

// refreshStats runs git for every non-archived git workspace, or only for
// workspace only when it is not "".
func (d *Daemon) refreshStats(ctx context.Context, only string) {
	d.mu.Lock()
	paths := map[string]string{}
	for _, w := range d.st.Workspaces {
		if w.Archived || (only != "" && w.ID != only) {
			continue
		}
		if i := slices.IndexFunc(d.st.Projects, func(p model.Project) bool { return p.ID == w.ProjectID }); i >= 0 && d.st.Projects[i].Kind == model.ProjectGit {
			paths[w.ID] = w.Path
		}
	}
	d.mu.Unlock()

	got := map[string]model.BranchStats{}
	for id, path := range paths {
		if s, err := d.o.Stats(ctx, path); err == nil {
			got[id] = s
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	changed := false
	for id, s := range got {
		if old, ok := d.st.Stats[id]; d.workspace(id) != nil && (!ok || old != s) {
			d.st.Stats[id], changed = s, true
		}
	}
	if changed {
		d.changed()
	}
}

// start launches a pane and its watcher. Callers hold d.mu.
func (d *Daemon) start(id string, cmd []string, cwd string) error {
	p, err := d.o.StartPane(pane.Config{ID: id, Cmd: cmd, Cwd: cwd, Cols: defaultCols, Rows: defaultRows, NewVT: d.o.NewVT})
	if err != nil {
		return err
	}
	d.panes[id] = p
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

// watch pushes at most 60 frames a second for one pane. Dirty has capacity
// 1, so signals that arrive during the wait fold into the next snapshot.
func (d *Daemon) watch(id string, p Pane) {
	var last time.Time
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
			d.pushFrame(id, p)
		case <-p.Done():
			d.pushFrame(id, p)
			d.exited(id, p)
			return
		}
	}
}

func (d *Daemon) pushFrame(id string, p Pane) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.panes[id] != p || len(d.clients) == 0 {
		return
	}
	f := d.frame(id, p)
	for c := range d.clients {
		c.push(func() { c.frames[id] = f })
	}
}

func (d *Daemon) exited(id string, p Pane) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing || d.panes[id] != p {
		return // closed on purpose: by ClosePane, DeleteWorkspace or shutdown
	}
	i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == id })
	if i < 0 {
		return
	}
	code := p.ExitCode()
	d.st.Panes[i].Exited, d.st.Panes[i].ExitCode = true, code
	d.dropActivity(id)
	d.changed()
	for c := range d.clients {
		c.queue(proto.PaneExited{Pane: id, ExitCode: code})
	}
}

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
	d.st.Version++
	for c := range d.clients {
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
		log.Printf("pitwall: save state: %v", err)
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
	s.Projects = slices.Clone(s.Projects)
	s.Workspaces = slices.Clone(s.Workspaces)
	for i := range s.Workspaces {
		s.Workspaces[i].Layout = cloneNode(s.Workspaces[i].Layout)
	}
	s.Panes = slices.Clone(s.Panes)
	s.Activities = slices.Clone(s.Activities)
	s.Stats = maps.Clone(s.Stats)
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
