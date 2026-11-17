package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// remotes is what one CLI call sees of the current session across
// machines: this machine's state and, dialled on first need, each host
// the session has a part or a worker on. Plain ls and the tab commands
// stay on this machine; ls --children, ls --orphans, wait, read, send,
// prompt, kill and adopt reach the hosts through it.
type remotes struct {
	local   *cliConn // nil when the caller only reads
	state   model.State
	session model.Session
	hosts   map[string]*hostPart
}

// hostPart is one host as a CLI call sees it.
type hostPart struct {
	name  string
	conn  *cliConn
	done  func()
	level int
	state model.State // the host's whole state
	part  string      // the session's part there, "" for none
	// view is what of state belongs to the session here: its part's
	// groups, tabs, panes and their activity, folded into the session, and
	// the closed tabs of the session's workers there.
	view model.State
	err  error // why the host's state is not current; nil once it is
}

func newRemotes(local *cliConn, state model.State, session model.Session) *remotes {
	return &remotes{local: local, state: state, session: session, hosts: map[string]*hostPart{}}
}

func (r *remotes) close() {
	for _, h := range r.hosts {
		if h.done != nil {
			h.done()
		}
	}
}

// names is the hosts the session has a part or a worker on, sorted, and
// none under --host.
func (r *remotes) names() []string {
	if onHost != "" {
		return nil // the hosts named in onHost's state are its own
	}
	var out []string
	for _, p := range r.session.Remotes {
		out = append(out, p.Host)
	}
	for _, w := range r.session.Workers {
		out = append(out, w.Host)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// originOf is what a part on a host at level is made for, for session of
// daemon: zero while either daemon is too old to keep it.
func originOf(daemon, session string, level int) model.Origin {
	if daemon == "" || level < proto.OriginLevel {
		return model.Origin{}
	}
	return model.Origin{Daemon: daemon, Session: session}
}

// host is name, dialled on the first call. Its state is current when err
// is nil: a host that refused the connection or did not answer is
// unresolved, and nothing of it is read from an older copy.
func (r *remotes) host(name string) *hostPart {
	if h := r.hosts[name]; h != nil {
		return h
	}
	h := &hostPart{name: name}
	r.hosts[name] = h
	conn, done, err := dialHost(name, "cli")
	if err != nil {
		h.err = err
		return h
	}
	h.conn, h.done = conn, done
	m, err := syncMsg(conn)
	if err != nil {
		h.err = err
		return h
	}
	h.level = m.Level
	origin := originOf(r.state.Daemon, r.session.ID, m.Level)
	hs := reconcileWorkers(r.local, conn, r.session, name, m.State, origin, m.Level)
	h.state = hs
	if p := partOn(hs, &r.session, name, origin, false); p != nil {
		h.part = p.ID
	}
	h.view = sessionPart(hs, h.part, r.session, name)
	return h
}

// reconcileWorkers sends host the Reparents, and this machine the lost
// records, that workerFixes finds for session's workers there, and
// returns hs, host's state, after them. local nil skips the records.
func reconcileWorkers(local, hc *cliConn, session model.Session, host string, hs model.State, origin model.Origin, level int) model.State {
	if origin == (model.Origin{}) || level < proto.RemoteWorkerLevel {
		return hs
	}
	moves, lost := workerFixes(session, host, hs, origin, time.Now())
	for _, mv := range moves {
		if updated, err := syncCLI(hc, mv); err == nil {
			hs = updated
		}
	}
	if len(lost) > 0 && local != nil {
		syncCLI(local, proto.RemoteWorkers{SessionID: session.ID, Set: lost})
	}
	return hs
}

// sessionPart is what of hs, host's state, belongs to session: part's
// groups, tabs, panes and their activity, and the closed tabs of the
// session's workers there, which may outlive their part, all moved into
// session.
func sessionPart(hs model.State, part string, session model.Session, host string) model.State {
	recorded := map[string]bool{}
	for _, w := range session.Workers {
		if w.Host == host {
			recorded[w.ID] = true
		}
	}
	to := map[string]string{}
	if part != "" {
		to[part] = session.ID
	}
	f := foldIn(hs, to, nil)
	var v model.State
	tabs, panes := map[string]bool{}, map[string]bool{}
	for _, w := range f.Workspaces {
		if w.SessionID == session.ID {
			v.Workspaces = append(v.Workspaces, w)
			tabs[w.ID] = true
		}
	}
	for _, p := range f.Projects {
		if p.SessionID == session.ID {
			v.Projects = append(v.Projects, p)
		}
	}
	for _, p := range f.Panes {
		if tabs[p.WorkspaceID] {
			v.Panes = append(v.Panes, p)
			panes[p.ID] = true
		}
	}
	for _, a := range f.Activities {
		if tabs[a.WorkspaceID] || panes[a.PaneID] {
			v.Activities = append(v.Activities, a)
		}
	}
	for _, c := range f.Closed {
		if c.Tab.SessionID == session.ID || recorded[c.Tab.ID] {
			c.Tab.SessionID = session.ID
			v.Closed = append(v.Closed, c)
		}
	}
	v.PRs, v.Queued = f.PRs, f.Queued
	return v
}

// merged is this machine's state with each current host's part of the
// session folded in, as a window that shows both machines has it.
func (r *remotes) merged() model.State {
	st := r.state
	st.Projects, st.Workspaces, st.Panes = slices.Clone(st.Projects), slices.Clone(st.Workspaces), slices.Clone(st.Panes)
	st.Activities, st.Closed = slices.Clone(st.Activities), slices.Clone(st.Closed)
	for _, name := range r.sorted() {
		h := r.hosts[name]
		if h.err != nil {
			continue
		}
		v := h.view
		st.Projects = append(st.Projects, v.Projects...)
		st.Workspaces = append(st.Workspaces, v.Workspaces...)
		st.Panes = append(st.Panes, v.Panes...)
		st.Activities = append(st.Activities, v.Activities...)
		st.Closed = append(st.Closed, v.Closed...)
		st.PRs = join(st.PRs, v.PRs)
		st.Queued = join(st.Queued, v.Queued)
	}
	return st
}

// sorted is the names of the hosts dialled so far.
func (r *remotes) sorted() []string {
	var out []string
	for name := range r.hosts {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// where is the machine of w, a tab of merged, and its number there as
// pitwall ls on that machine gives it; "" for this machine.
func (r *remotes) where(w model.Workspace) (host string, n int) {
	for _, name := range r.sorted() {
		if h := r.hosts[name]; h.err == nil && h.view.Workspace(w.ID) != nil {
			return name, slices.IndexFunc(h.state.Numbered(h.part), func(x model.Workspace) bool { return x.ID == w.ID }) + 1
		}
	}
	return "", slices.IndexFunc(r.state.Numbered(r.session.ID), func(x model.Workspace) bool { return x.ID == w.ID }) + 1
}

// ownerOf is the current host whose part of the session has id, a tab,
// pane or closed tab's id, or nil for this machine and for an id no
// machine has.
func (r *remotes) ownerOf(id string) *hostPart {
	has := func(s model.State) bool {
		found := false
		eachID(s, func(x string) { found = found || x == id })
		return found
	}
	if has(r.state) {
		return nil
	}
	for _, name := range r.names() {
		if h := r.host(name); h.err == nil && has(h.view) {
			return h
		}
	}
	return nil
}

// tab finds a tab as resolveTab does here, and on the session's hosts:
// box:N or #box:N, or any other address after "box:", among the tabs of
// box's part, and an @id this machine lacks in a host's part. h is nil for
// a tab here.
func (r *remotes) tab(addr string) (h *hostPart, w model.Workspace, err error) {
	h, w, _, err = r.find(addr, false)
	return h, w, err
}

// pane is tab for a pane, as resolvePane finds one.
func (r *remotes) pane(addr string) (*hostPart, model.Workspace, model.Pane, error) {
	return r.find(addr, true)
}

func (r *remotes) find(addr string, pane bool) (*hostPart, model.Workspace, model.Pane, error) {
	addr = hostLocal(addr)
	on := func(st model.State, session, a string) (model.Workspace, model.Pane, error) {
		if pane {
			if id, ok := strings.CutPrefix(a, "@"); ok {
				if w := st.Workspace(id); w != nil {
					if p := mainPane(tabPanes(st, *w)); p != nil {
						return *w, *p, nil
					}
					return *w, model.Pane{}, fmt.Errorf("tab %s has no pane", tabTitle(*w))
				}
			}
			return resolvePane(st, session, a)
		}
		w, err := resolveTab(st, session, a)
		return w, model.Pane{}, err
	}
	// #box:3 is how new --host prints the tab.
	if name, rest, ok := strings.Cut(strings.TrimPrefix(addr, "#"), ":"); ok && slices.Contains(r.names(), name) {
		h := r.host(name)
		switch {
		case h.err != nil:
			return nil, model.Workspace{}, model.Pane{}, fmt.Errorf("%s unreachable: %w", name, h.err)
		case h.part == "" || rest == "":
			return nil, model.Workspace{}, model.Pane{}, fmt.Errorf("no tab %s: this session has no tabs on %s", addr, name)
		}
		w, p, err := on(h.state, h.part, rest)
		if err == nil && w.SessionID != h.part {
			// why: an @id finds a tab in any session there; only the
			// session's own part is the caller's to reach.
			err = fmt.Errorf("no tab %s in session %s", addr, r.session.Name)
		}
		return h, w, p, err
	}
	w, p, err := on(r.state, r.session.ID, addr)
	if err == nil || !strings.HasPrefix(addr, "@") {
		return nil, w, p, err
	}
	var unreached []string
	for _, name := range r.names() {
		h := r.host(name)
		if h.err != nil {
			unreached = append(unreached, fmt.Sprintf("%s unreachable: %v", name, h.err))
			continue
		}
		if h.part == "" {
			continue
		}
		if hw, hp, herr := on(h.state, h.part, addr); herr == nil && hw.SessionID == h.part {
			return h, hw, hp, nil
		}
	}
	if len(unreached) > 0 {
		err = fmt.Errorf("%w; %s", err, strings.Join(unreached, "; "))
	}
	return nil, w, p, err
}

// callerTab is the tab of $PITWALL_PANE in state.
func callerTab(state model.State) (string, error) {
	pane := os.Getenv("PITWALL_PANE")
	i := slices.IndexFunc(state.Panes, func(p model.Pane) bool { return p.ID == pane })
	if pane == "" || i < 0 {
		return "", errors.New("--children must run inside a pitwall pane (PITWALL_PANE is not set)")
	}
	return state.Panes[i].WorkspaceID, nil
}

// workersOf is the records of the workers tab started on other machines,
// in any session of state.
func workersOf(state model.State, tab string) []model.RemoteWorker {
	var out []model.RemoteWorker
	for _, s := range state.Sessions {
		for _, w := range s.Workers {
			if w.Parent == tab {
				out = append(out, w)
			}
		}
	}
	return out
}

// staleRow is a worker this machine recorded on a host it cannot reach
// now: its state is unknown.
func staleRow(w model.RemoteWorker) tabJSON {
	return tabJSON{ID: w.ID, Parent: w.Parent, State: "unknown", Host: w.Host, Addr: "@" + w.ID, Stale: true, PaneList: []paneJSON{}}
}

// listChildren is ls --children: the tabs the calling pane's tab opened
// here and on the hosts it started workers on. A worker on a host that
// cannot be reached shows as stale, its state unknown.
func listChildren(out io.Writer, r *remotes, asJSON, tree bool) error {
	tab, err := callerTab(r.state)
	if err != nil {
		return err
	}
	var stale []model.RemoteWorker
	for _, w := range workersOf(r.state, tab) {
		if w.Lost == "" && r.host(w.Host).err != nil {
			stale = append(stale, w)
		}
	}
	merged := r.merged()
	only, err := childTabs(merged)
	if err != nil {
		return err
	}
	for _, w := range workersOf(r.state, tab) {
		if merged.Workspace(w.ID) != nil {
			only[w.ID] = true // its parent there is the one this machine records
		}
	}
	if asJSON {
		rows := tabRows(merged, r.session.ID, only, r.where)
		for _, w := range stale {
			rows = append(rows, staleRow(w))
		}
		return json.NewEncoder(out).Encode(rows)
	}
	return listTabs(out, merged, r.session.ID, only, tree, r.where, stale)
}

// orphan is a worker of this session, or of a session this machine no
// longer has, whose parent is no tab here.
type orphan struct {
	row  tabJSON
	part string // the part of a gone session it runs in, "" for this session's
	rec  *model.RemoteWorker
}

// orphans lists this session's workers here and elsewhere whose parent is
// no live tab here: its records, and tabs of its parts with no record,
// then the tabs of parts on reachable [[hosts]] that this daemon made for
// a session it no longer has. It names each host it could not reach in
// unreached.
func (r *remotes) orphans() (out []orphan, unreached []string) {
	gone := func(parent string) bool { return r.state.Workspace(parent) == nil }
	seen := map[string]bool{}
	row := func(h *hostPart, id string) tabJSON {
		if h.err == nil {
			if w := h.view.Workspace(id); w != nil {
				t := tabRows(h.view, r.session.ID, map[string]bool{id: true}, r.where)[0]
				t.Parent = w.Parent
				return t
			}
		}
		return tabJSON{}
	}
	for _, w := range control.Orphans(r.state, r.session.ID) {
		t := tabsJSON(r.state, r.session.ID, map[string]bool{w.ID: true})[0]
		t.Parent = w.Parent
		out = append(out, orphan{row: t})
	}
	for _, rec := range r.session.Workers {
		if rec.Lost != "" || !gone(rec.Parent) {
			continue
		}
		seen[rec.ID] = true
		h := r.host(rec.Host)
		t := row(h, rec.ID)
		if t.ID == "" {
			t = staleRow(rec)
			t.Stale = h.err != nil
		}
		out = append(out, orphan{row: t, rec: &rec})
	}
	for _, name := range r.names() {
		h := r.host(name)
		if h.err != nil {
			continue
		}
		for _, w := range h.view.Workspaces {
			if w.RemoteParent && gone(w.Parent) && !seen[w.ID] {
				seen[w.ID] = true
				out = append(out, orphan{row: row(h, w.ID)})
			}
		}
	}
	if r.state.Daemon == "" {
		return out, unreached
	}
	s, _ := config.Load()
	for _, entry := range s.Hosts {
		h := r.host(entry.Name)
		if h.err != nil {
			unreached = append(unreached, fmt.Sprintf("%s unreachable: %v", entry.Name, h.err))
			continue
		}
		for _, p := range h.state.Sessions {
			if p.Origin.Daemon != r.state.Daemon || r.state.Session(p.Origin.Session) != nil {
				continue
			}
			for _, w := range h.state.Numbered(p.ID) {
				t := tabRows(h.state, p.ID, map[string]bool{w.ID: true}, func(model.Workspace) (string, int) { return entry.Name, 0 })[0]
				t.Addr, t.Parent = "@"+w.ID, w.Parent
				out = append(out, orphan{row: t, part: p.ID})
			}
		}
	}
	return out, unreached
}

// listOrphans is ls --orphans.
func listOrphans(out, errOut io.Writer, r *remotes, asJSON bool) error {
	found, unreached := r.orphans()
	for _, u := range unreached {
		fmt.Fprintln(errOut, "pitwall:", u)
	}
	if asJSON {
		rows := []tabJSON{}
		for _, o := range found {
			rows = append(rows, o.row)
		}
		return json.NewEncoder(out).Encode(rows)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ADDRESS\tID\tNAME\tSTATE")
	for _, o := range found {
		fmt.Fprintf(w, "%s\t@%s\t%s\t%s\n", o.row.Addr, o.row.ID, cmp.Or(o.row.Title, "-"), o.row.State)
	}
	return w.Flush()
}

// adopt makes the calling pane's tab the parent of orphans, the ones
// given by @id or all with all: its record here, the parent there, and
// for the part of a gone session, that part, which joins this session.
func adopt(out io.Writer, r *remotes, ids []string, all bool) error {
	if r.local == nil {
		return errors.New("adopt needs this machine's daemon")
	}
	tab, err := callerTab(r.state)
	if err != nil {
		return errors.New("adopt must run inside a pitwall pane, in the tab that takes the workers over")
	}
	if r.state.SessionOf(tab) != r.session.ID {
		return fmt.Errorf("this tab is not in session %s", r.session.Name)
	}
	found, _ := r.orphans()
	var picked []orphan
	if all {
		// The caller and its own ancestors would form a loop; the daemon
		// refuses them, which would stop --all partway.
		picked = slices.DeleteFunc(slices.Clone(found), func(o orphan) bool {
			return o.row.ID == tab || o.row.Host == "" && r.state.AncestorOf(o.row.ID, tab)
		})
	}
	for _, id := range ids {
		id = strings.TrimPrefix(id, "@")
		if id == tab {
			return errors.New("a tab cannot adopt itself")
		}
		i := slices.IndexFunc(found, func(o orphan) bool { return o.row.ID == id })
		if i < 0 {
			return fmt.Errorf("@%s is not an orphan of this session (see pitwall ls --orphans)", id)
		}
		picked = append(picked, found[i])
	}
	if len(picked) == 0 {
		return errors.New("no orphans to adopt (see pitwall ls --orphans)")
	}
	// Parts of a gone session join this one first, one per host.
	joined := map[string]bool{}
	for _, o := range picked {
		if o.part == "" || joined[o.part] {
			continue
		}
		h := r.host(o.row.Host)
		if mine := r.session.Remote(h.name); mine != nil && mine.Session != o.part {
			return fmt.Errorf("session %s has its own part on %s already, so it cannot take over @%s's; adopt it from a session with no tabs on %s", r.session.Name, h.name, o.row.ID, h.name)
		}
		origin := originOf(r.state.Daemon, r.session.ID, h.level)
		if origin == (model.Origin{}) {
			return fmt.Errorf("%s: the pitwall daemon there is too old to move a part to another session; update pitwall there", h.name)
		}
		hs, err := syncCLI(h.conn, proto.SetOrigin{SessionID: o.part, Origin: origin})
		if err != nil {
			return fmt.Errorf("%s: %w", h.name, err)
		}
		if _, err := syncCLI(r.local, proto.SessionLink{SessionID: r.session.ID, Host: h.name, Remote: o.part}); err != nil {
			return err
		}
		h.state, h.part, joined[o.part] = hs, o.part, true
	}
	for _, o := range picked {
		if o.row.Host == "" {
			if _, err := syncCLI(r.local, proto.Reparent{WorkspaceID: o.row.ID, ParentTab: tab}); err != nil {
				return err
			}
			fmt.Fprintf(out, "@%s adopted\n", o.row.ID)
			continue
		}
		h := r.host(o.row.Host)
		rec := model.RemoteWorker{ID: o.row.ID, Host: h.name, Parent: tab, Since: time.Now()}
		if o.rec != nil {
			rec.Since = o.rec.Since
		}
		update := proto.RemoteWorkers{SessionID: r.session.ID, Set: []model.RemoteWorker{rec}}
		if o.rec == nil {
			update.Add = []model.RemoteWorker{rec}
		}
		if _, err := syncCLI(r.local, update); err != nil {
			return err
		}
		if h.err != nil {
			// The record moved; the next call that reaches the host moves
			// the worker there too (reconcileWorkers).
			fmt.Fprintf(out, "%s:@%s adopted here; %s unreachable, its parent there follows once it is back\n", h.name, rec.ID, h.name)
			continue
		}
		origin := originOf(r.state.Daemon, r.session.ID, h.level)
		if _, err := syncCLI(h.conn, proto.Reparent{WorkspaceID: rec.ID, ParentTab: tab, Origin: origin}); err != nil {
			return fmt.Errorf("%s:@%s: %w", h.name, rec.ID, err)
		}
		fmt.Fprintf(out, "%s:@%s adopted\n", h.name, rec.ID)
	}
	return nil
}

// hostWatch is a host as wait --children follows it.
type hostWatch struct {
	state     model.State
	current   bool      // a state came on the live connection
	downSince time.Time // when it last stopped being current, or the wait began
	err       error
	said      string // the unreachable line printed last
}

// hostEvent is a state from a host's watch connection, or why it ended;
// host "" is this machine.
type hostEvent struct {
	host  string
	state model.State
	err   error
}

// hostWait keeps a single-tab wait on its owning machine across drops.
// It owns the watch, including exit messages, so exitOf can finish
// reading an exit without racing a background reader.
type hostWait struct {
	name  string
	watch *watcher
	done  func()
	stop  func() bool
}

func (h *hostWait) close() {
	if h.stop != nil {
		h.stop()
	}
	if h.done != nil {
		h.done()
	}
	h.watch, h.done, h.stop = nil, nil, nil
}

func (h *hostWait) next(ctx context.Context, errOut io.Writer) (model.State, error) {
	var downSince time.Time
	wait, said := hostRetryMin, ""
	for {
		if err := ctx.Err(); err != nil {
			return model.State{}, err
		}
		var err error
		if h.watch == nil {
			var c *cliConn
			c, h.done, err = dialHost(h.name, "watch")
			if err == nil {
				h.watch = &watcher{cliConn: c, exits: map[string]int{}}
				h.stop = context.AfterFunc(ctx, func() { c.Close() })
			}
		}
		if err == nil {
			var st model.State
			if st, err = h.watch.next(); err == nil {
				if err = h.watch.socket.SetDeadline(time.Time{}); err == nil {
					return st, nil
				}
			}
		}
		h.close()
		if downSince.IsZero() {
			downSince = time.Now()
		}
		if msg := err.Error(); msg != said {
			said = msg
			fmt.Fprintf(errOut, "pitwall: %s unreachable: %v\n", h.name, err)
		}
		left := unreachableAfter - time.Since(downSince)
		if left <= 0 {
			return model.State{}, fmt.Errorf("%s unreachable for %s: %w", h.name, unreachableAfter, err)
		}
		timer := time.NewTimer(min(wait, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			return model.State{}, ctx.Err()
		case <-timer.C:
		}
		wait = min(2*wait, hostRetryMax)
	}
}

// unreachableAfter is how long wait --children waits for a host it cannot
// reach before it gives up; tests shorten it. waitTick is how often it
// looks at the clock meanwhile, and it dials again with the window's
// backoff, from hostRetryMin to hostRetryMax.
var (
	unreachableAfter, waitTick = 5 * time.Minute, time.Second
	hostRetryMin, hostRetryMax = retryMin, retryMax
)

// watchHost sends events with name's states, and why its connection
// failed, dialling again with backoff from least to most until ctx ends.
func watchHost(ctx context.Context, dial func(name, kind string) (*cliConn, func(), error), name string, least, most time.Duration, events chan<- hostEvent) {
	wait := least
	for {
		c, done, err := dial(name, "watch")
		if err == nil {
			w := &watcher{cliConn: c, exits: map[string]int{}}
			stop := context.AfterFunc(ctx, func() { c.Close() })
			for err == nil {
				var st model.State
				if st, err = w.next(); err == nil {
					// Keep hello's deadline until the first state arrives.
					if err = c.socket.SetDeadline(time.Time{}); err != nil {
						break
					}
					wait = least
					select {
					case events <- hostEvent{host: name, state: st}:
					case <-ctx.Done():
					}
				}
			}
			stop()
			done()
		}
		select {
		case events <- hostEvent{host: name, err: err}:
		case <-ctx.Done():
			return
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		wait = min(2*wait, most)
	}
}

// remoteHead names a worker on host in wait's lines: box:N and its title
// while its tab is open there, else box:@pane, by which read reaches its
// reply.
func remoteHead(host string, hs model.State, w model.Workspace, pane string) string {
	if n := tabNumber(host, hs.Numbered(w.SessionID), w.ID); n != "" {
		return n + " " + tabTitle(w)
	}
	return fmt.Sprintf("%s:@%s %s", host, pane, tabTitle(w))
}

// lostLine is why wait cannot count rec, a worker on a host whose state
// hs is current, or "" while it can still turn up or be judged: lost when
// the record says so or hs has no such tab lostAfter after its start, and
// its result lost when it closed without being kept as finished, as after
// the host's daemon restarted.
func lostLine(rec model.RemoteWorker, hs model.State, now time.Time) string {
	head := rec.Host + ":@" + rec.ID
	if rec.Lost != "" {
		return head + " lost: " + rec.Lost
	}
	if hs.Workspace(rec.ID) != nil {
		return ""
	}
	if i := slices.IndexFunc(hs.Closed, func(c model.ClosedTab) bool { return c.Tab.ID == rec.ID }); i >= 0 {
		if c := hs.Closed[i]; !c.FinishedWorker {
			line := head + " result lost: " + rec.Host + " restarted or the tab closed unfinished"
			if c.Tab.Branch != "" {
				line += "; its branch is " + c.Tab.Branch
			}
			return line
		}
		return ""
	}
	if now.Sub(rec.Since) > lostAfter {
		return head + " lost: " + rec.Host + " has no tab with this id"
	}
	return ""
}

// hostAddr is how a tab is addressed: #N here, box:N on a host.
var hostAddr = control.Addr
