package main

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/app"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// multi is app.Backend over several daemons: this machine's first, then
// each [[hosts]] entry with window = true. Each keeps its own backend,
// which connects, queues and reconnects on its own, so a host that is
// down or slow holds up no other. A daemon's ids are 64 random bits, so
// the states merge as they are, and a message goes to the daemon whose
// state holds the first id it names.
type multi struct {
	hosts   []*hostLink // hosts[0] is this machine
	changed chan struct{}
	focus   chan proto.FocusSession // every host's, newest first
	mergeMu sync.Mutex              // one merge at a time, so an older one never lands last

	mu    sync.Mutex
	state model.State
	owner map[string]*hostLink // every session, group, tab, pane, chat and task id
	cur   *hostLink            // the host of the session the window shows
	tree  *hostLink            // where the last WorktreeQuery went
	// searching is the hosts the last SearchAll went to that have not
	// sent its last batch.
	searching map[*hostLink]bool
	seen      *hostLink // the host told of the pane the window shows
	// clipSeq is each host's last Clipboard.Seq, clip the window's, and
	// clipFrom the host whose write it is.
	clipSeq  map[*hostLink]uint64
	clip     model.Clipboard
	clipFrom *hostLink
	// folded is the sessions on other hosts that are parts of a shared
	// session here, shared the shared sessions here.
	folded, shared map[string]bool
	// pending is the shared sessions whose part on a host a SessionNew is
	// making: the name it was asked for, by host and session here.
	pending map[[2]string]string
	// asked is the SessionLink last sent for a session and host, and when,
	// so a merge before this machine's state shows it does not send it
	// again within a few seconds.
	asked map[[2]string]asked
}

type asked struct {
	sig string
	at  time.Time
}

// hostLink is one daemon of a multi.
type hostLink struct {
	name  string // "" for this machine
	ssh   string // the ssh destination, "" for this machine
	color string // its color in a shared session, "" for the palette's turn
	b     *backend
}

var (
	_ app.Focuser         = (*multi)(nil)
	_ app.Linker          = (*multi)(nil)
	_ app.Finder          = (*multi)(nil)
	_ app.Worktreer       = (*multi)(nil)
	_ app.Texter          = (*multi)(nil)
	_ app.Viewer          = (*multi)(nil)
	_ app.Chatter         = (*multi)(nil)
	_ app.Hoster          = (*multi)(nil)
	_ app.Mover           = (*multi)(nil)
	_ app.Scroller        = (*multi)(nil)
	_ app.Iconer          = (*multi)(nil)
	_ app.Paster          = (*multi)(nil)
	_ app.SessionSearcher = (*multi)(nil)
)

// hostDial is one host of a multi and how to reach its daemon.
type hostDial struct {
	name, ssh, color string
	dial             func(session string, restart bool) (*proto.Conn, proto.StateMsg, error)
}

// windowHosts are the [[hosts]] entries a plain window shows next to this
// machine, and a func that drops their forwards. None on Windows, whose
// ssh cannot share a connection.
func windowHosts(hosts []config.Host, colors map[string]string) ([]hostDial, func()) {
	var out []hostDial
	var close []func()
	for _, h := range hosts {
		if !h.Window || h.Name == "" || runtime.GOOS == "windows" {
			continue
		}
		r, err := newSSHHost(h.Name, hosts)
		d := hostDial{name: h.Name, ssh: cmp.Or(h.SSH, h.Name), color: colors[h.Name]}
		if err != nil {
			d.dial = func(string, bool) (*proto.Conn, proto.StateMsg, error) { return nil, proto.StateMsg{}, err }
		} else {
			d.dial = r.dial
			close = append(close, r.close)
		}
		out = append(out, d)
	}
	return out, func() {
		for _, c := range close {
			c()
		}
	}
}

// newMulti joins local, this machine's backend, and a backend per host in
// remotes, which connect in the background.
func newMulti(local *backend, remotes []hostDial) *multi {
	m := &multi{changed: make(chan struct{}, 1), focus: make(chan proto.FocusSession, 1), owner: map[string]*hostLink{}, clipSeq: map[*hostLink]uint64{},
		pending: map[[2]string]string{}, asked: map[[2]string]asked{}}
	m.hosts = append(m.hosts, &hostLink{b: local})
	// The window takes its first session before its first frame.
	select {
	case f := <-local.focus:
		m.focus <- f
	default:
	}
	for _, r := range remotes {
		b := newBackend(nil, "")
		// why: the window's first session is this machine's; a host
		// connecting must not take the window over. pitwall attach run on
		// the host still focuses its tab.
		b.guest = true
		select {
		case <-b.focus:
		default:
		}
		b.redial = r.dial
		b.link.Note = "Connecting…"
		m.hosts = append(m.hosts, &hostLink{name: r.name, ssh: r.ssh, color: r.color, b: b})
	}
	m.cur = m.hosts[0]
	m.merge()
	for _, h := range m.hosts {
		go func() {
			for range h.b.changed {
				m.merge()
				m.notify()
			}
		}()
		go func() {
			for f := range h.b.focus {
				// The newest request wins, as in a backend.
				select {
				case <-m.focus:
				default:
				}
				select {
				case m.focus <- f:
				default:
				}
			}
		}()
	}
	for _, h := range m.hosts[1:] {
		h.b.startDialing(false)
	}
	if len(m.hosts) > 1 {
		go m.pings()
	}
	return m
}

// pings measures each host's latency every few seconds, for the host row.
func (m *multi) pings() {
	for {
		for _, h := range m.hosts[1:] {
			if up(h) {
				h.b.ping()
			}
		}
		time.Sleep(5 * time.Second)
	}
}

// up reports whether h is connected.
func up(h *hostLink) bool {
	l := h.b.Link()
	return l.State == app.LinkUp && l.Epoch > 0
}

// byName is the host called name, "" for this machine, or nil.
func (m *multi) byName(name string) *hostLink {
	for _, h := range m.hosts {
		if h.name == name {
			return h
		}
	}
	return nil
}

// foldIn is s with the sessions in to, each a part of a shared session
// here, folded into that session: their groups, tabs and tasks move to it,
// the sessions themselves go, and so do the tabs in forget.
func foldIn(s model.State, to map[string]string, forget map[string]bool) model.State {
	if len(to) == 0 {
		return s
	}
	s.Sessions = slices.DeleteFunc(slices.Clone(s.Sessions), func(x model.Session) bool { return to[x.ID] != "" })
	s.Projects = slices.Clone(s.Projects)
	for i := range s.Projects {
		if id := to[s.Projects[i].SessionID]; id != "" {
			s.Projects[i].SessionID = id
		}
	}
	s.Workspaces = slices.DeleteFunc(slices.Clone(s.Workspaces), func(w model.Workspace) bool { return forget[w.ID] })
	for i := range s.Workspaces {
		if id := to[s.Workspaces[i].SessionID]; id != "" {
			s.Workspaces[i].SessionID = id
		}
	}
	s.Tasks = slices.Clone(s.Tasks)
	for i := range s.Tasks {
		if id := to[s.Tasks[i].SessionID]; id != "" {
			s.Tasks[i].SessionID = id
		}
	}
	return s
}

// merge rebuilds the window's state from every host's last one. A host
// that is down keeps its last state on screen, as one window does.
func (m *multi) merge() {
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()
	var st model.State
	owner := map[string]*hostLink{}
	local := m.hosts[0].b.State()
	fold := map[string]map[string]string{} // by host: its session, the shared one here
	forget, folded, shared := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, ss := range local.Sessions {
		for _, r := range ss.Remotes {
			if fold[r.Host] == nil {
				fold[r.Host] = map[string]string{}
			}
			fold[r.Host][r.Session] = ss.ID
			folded[r.Session], shared[ss.ID] = true, true
			for _, id := range r.Forget {
				forget[id] = true
			}
		}
	}
	for i, h := range m.hosts {
		s := h.b.State()
		if i == 0 {
			// The window has the other hosts' tabs, so TopOrder may drop
			// the ids it does not know: they are of closed tabs.
			s.Sessions = slices.Clone(s.Sessions)
			for j := range s.Sessions {
				s.Sessions[j].Remotes = nil
			}
		}
		own := func(id string) {
			if id != "" {
				owner[id] = h
			}
		}
		for _, x := range s.Sessions {
			own(x.ID)
		}
		for _, x := range s.Projects {
			own(x.ID)
		}
		for _, x := range s.Workspaces {
			own(x.ID)
			for _, t := range x.Tabs {
				own(t.ID)
			}
		}
		for _, x := range s.Panes {
			own(x.ID)
		}
		for _, x := range s.Chats {
			own(x.ID)
		}
		for _, x := range s.Tasks {
			own(x.ID)
		}
		for _, x := range s.Closed {
			own(x.ID)
		}
		if i > 0 {
			s = foldIn(s, fold[h.name], forget)
		}
		st.Version += s.Version
		st.Sessions = append(st.Sessions, s.Sessions...)
		st.Projects = append(st.Projects, s.Projects...)
		st.Workspaces = append(st.Workspaces, s.Workspaces...)
		st.Panes = append(st.Panes, s.Panes...)
		st.Activities = append(st.Activities, s.Activities...)
		st.Tasks = append(st.Tasks, s.Tasks...)
		st.Chats = append(st.Chats, s.Chats...)
		st.Closed = append(st.Closed, s.Closed...)
		st.Stats = join(st.Stats, s.Stats)
		st.PRs = join(st.PRs, s.PRs)
		st.Overlaps = join(st.Overlaps, s.Overlaps)
		st.Queued = join(st.Queued, s.Queued)
		st.Drafting = join(st.Drafting, s.Drafting)
		st.DraftHeld = join(st.DraftHeld, s.DraftHeld)
		st.Delivered = join(st.Delivered, s.Delivered)
		st.Listening = join(st.Listening, s.Listening)
		if st.Notice == "" {
			st.Notice = s.Notice
		}
		// A host whose Seq moved wrote last; the text is its own, which its
		// daemon later leaves out of its states.
		if s.Clipboard.Seq != m.clipSeq[h] {
			m.clipSeq[h] = s.Clipboard.Seq
			m.clip.Seq++
			m.clipFrom = h
		}
		if h == m.clipFrom {
			m.clip.Text = s.Clipboard.Text
		}
	}
	st.Clipboard = m.clip
	m.mu.Lock()
	m.state, m.owner, m.folded, m.shared = st, owner, folded, shared
	m.mu.Unlock()
	m.reconcile(local)
}

// reconcile keeps this machine's record of its shared sessions true to the
// hosts it reaches: a part whose session ended there leaves, tabs closed
// while their host was down close there, and a part a SessionNew made
// joins once its host's state has it. Callers hold mergeMu.
func (m *multi) reconcile(local model.State) {
	link := func(l proto.SessionLink) {
		key := [2]string{l.SessionID, l.Host}
		sig := fmt.Sprint(l.Remote, l.Forget)
		if a := m.asked[key]; a.sig == sig && time.Since(a.at) < 5*time.Second {
			return
		}
		if m.hosts[0].b.Send(l) == nil {
			m.asked[key] = asked{sig, time.Now()}
		}
	}
	for _, ss := range local.Sessions {
		for _, r := range ss.Remotes {
			h := m.byName(r.Host)
			if h == nil || h == m.hosts[0] || !up(h) {
				continue
			}
			hs := h.b.State()
			switch {
			case hs.Session(r.Session) == nil:
				link(proto.SessionLink{SessionID: ss.ID, Host: r.Host})
			case len(r.Forget) > 0:
				for _, id := range r.Forget {
					if slices.ContainsFunc(hs.Workspaces, func(w model.Workspace) bool { return w.ID == id }) {
						h.b.Send(proto.CloseTab{WorkspaceID: id})
					}
				}
				link(proto.SessionLink{SessionID: ss.ID, Host: r.Host, Remote: r.Session})
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, name := range m.pending {
		ss, h := local.Session(key[1]), m.byName(key[0])
		if ss == nil || h == nil || ss.Remote(key[0]) != nil {
			delete(m.pending, key)
			continue
		}
		hs := h.b.State()
		if x := hs.SessionNamed(name); x != nil && up(h) {
			link(proto.SessionLink{SessionID: ss.ID, Host: h.name, Remote: x.ID})
			delete(m.pending, key)
		}
	}
}

// join adds b's entries to a, which it copies first when b has any.
func join[V any](a, b map[string]V) map[string]V {
	if len(b) == 0 {
		return a
	}
	out := make(map[string]V, len(a)+len(b))
	maps.Copy(out, a)
	maps.Copy(out, b)
	return out
}

func (m *multi) notify() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

func (m *multi) State() model.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *multi) Changed() <-chan struct{}         { return m.changed }
func (m *multi) Focus() <-chan proto.FocusSession { return m.focus }

// of is the host that owns id, nil for none.
func (m *multi) of(id string) *hostLink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.owner[id]
}

// route is the host msg goes to: the owner of the first id among its
// fields, else the host of the session the window shows, where a new
// session or tab with no id to go by is made.
func (m *multi) route(msg any) *hostLink {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := msg.(proto.DeleteWorktree); ok {
		if m.tree != nil {
			return m.tree
		}
		return m.cur
	}
	var to *hostLink
	eachString(reflect.ValueOf(msg), func(s string) bool {
		to = m.owner[s]
		return to == nil
	})
	if to == nil {
		to = m.cur
	}
	return to
}

// eachString calls f with v's strings, fields in order, until f returns
// false. It reports whether f never did.
func eachString(v reflect.Value, f func(string) bool) bool {
	switch v.Kind() {
	case reflect.String:
		return f(v.String())
	case reflect.Pointer, reflect.Interface:
		return v.IsNil() || eachString(v.Elem(), f)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() && !eachString(v.Field(i), f) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return true // bytes typed into a pane
		}
		for i := range v.Len() {
			if !eachString(v.Index(i), f) {
				return false
			}
		}
	}
	return true
}

// Send goes to the daemon that owns what msg names; see route. A
// SessionShow also makes its host the window's, for messages that name
// no id.
func (m *multi) Send(msg any) error {
	switch v := msg.(type) {
	case app.NewTabOn:
		return m.newTabOn(v)
	case proto.SessionRename:
		m.eachPart(v.SessionID, func(h *hostLink, r model.RemoteSession) {
			h.b.Send(proto.SessionRename{SessionID: r.Session, Name: v.Name})
		})
	case proto.SessionKill:
		m.eachPart(v.SessionID, func(h *hostLink, r model.RemoteSession) {
			h.b.Send(proto.SessionKill{SessionID: r.Session})
		})
	case proto.CloseTab:
		if m.forget(v.WorkspaceID) {
			return nil
		}
	case proto.KillSession:
		if m.forget(v.WorkspaceID) {
			return nil
		}
	case proto.NewGroup:
		hosts := map[*hostLink]bool{}
		for _, id := range v.WorkspaceIDs {
			hosts[m.of(id)] = true
		}
		if len(hosts) > 1 {
			return errors.New("a group holds tabs of one machine; these are on several")
		}
	case proto.MoveSession:
		if v.GroupID != "" && m.of(v.GroupID) != m.of(v.WorkspaceID) {
			return errors.New("a group holds tabs of one machine; this tab is on another")
		}
		if sh := m.sharedOf(v.WorkspaceID); sh != "" && v.GroupID == "" {
			var ungroup any
			if st := m.State(); st.Workspace(v.WorkspaceID) != nil && st.Workspace(v.WorkspaceID).ProjectID != "" {
				ungroup = proto.MoveSession{WorkspaceID: v.WorkspaceID}
			}
			return m.moveShared(sh, v.WorkspaceID, v.Before, ungroup)
		}
	case proto.MoveGroup:
		if sh := m.sharedOf(v.GroupID); sh != "" {
			return m.moveShared(sh, v.GroupID, v.Before, nil)
		}
	case proto.SessionShow:
		if h := m.of(v.SessionID); h != nil {
			m.mu.Lock()
			m.cur = h
			m.mu.Unlock()
		}
	case proto.SeePane:
		// The host that showed the last pane hears that it no longer does.
		to := m.cur
		if v.Pane != "" {
			to = m.route(msg)
		}
		m.mu.Lock()
		was := m.seen
		m.seen = to
		m.mu.Unlock()
		if was != nil && was != to {
			was.b.Send(proto.SeePane{})
		}
		return to.b.Send(msg)
	case proto.SearchAll:
		// Every host searches its own sessions.
		waiting := map[*hostLink]bool{}
		for _, h := range m.hosts {
			if h.b.Send(msg) == nil && v.Query != "" {
				waiting[h] = true
			}
		}
		m.mu.Lock()
		m.searching = waiting
		m.mu.Unlock()
		return nil
	case proto.DismissNotice:
		for _, h := range m.hosts {
			if h.b.State().Notice == v.Notice {
				return h.b.Send(msg)
			}
		}
	}
	to := m.route(msg)
	switch msg.(type) {
	case proto.WorktreeQuery:
		m.mu.Lock()
		m.tree = to
		m.mu.Unlock()
	}
	return to.b.Send(msg)
}

// sharedOf is the shared session here that the tab or group id is in, "".
func (m *multi) sharedOf(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sh := m.state.SessionOf(id); m.shared[sh] {
		return sh
	}
	return ""
}

// eachPart calls f with each part on a host that is up of the shared
// session id.
func (m *multi) eachPart(id string, f func(*hostLink, model.RemoteSession)) {
	st := m.hosts[0].b.State()
	ss := st.Session(id)
	if ss == nil {
		return
	}
	for _, r := range ss.Remotes {
		if h := m.byName(r.Host); h != nil && h != m.hosts[0] && up(h) {
			f(h, r)
		}
	}
}

// forget closes a tab of a shared session whose host is down: the window
// stops showing it at once, and this machine's daemon keeps it in the
// part's Forget until a window reaches the host and closes it there. It
// reports whether the tab was such a tab.
func (m *multi) forget(tab string) bool {
	h, sh := m.of(tab), m.sharedOf(tab)
	if h == nil || h == m.hosts[0] || sh == "" || up(h) {
		return false
	}
	st := m.hosts[0].b.State()
	r := st.Session(sh).Remote(h.name)
	if r == nil || slices.Contains(r.Forget, tab) {
		return r != nil
	}
	m.hosts[0].b.Send(proto.SessionLink{SessionID: sh, Host: h.name, Remote: r.Session, Forget: append(slices.Clone(r.Forget), tab)})
	return true
}

// moveShared places the top-level item id of the shared session sh before
// before, or last: this machine's daemon keeps the order. A tab that
// leaves its group is ungrouped on its own host first, by move.
func (m *multi) moveShared(sh, id, before string, move any) error {
	if move != nil {
		if err := m.route(move).b.Send(move); err != nil {
			return err
		}
	}
	m.mu.Lock()
	st := m.state
	st.Sessions = slices.Clone(st.Sessions)
	st.Workspaces = slices.Clone(st.Workspaces)
	for i := range st.Workspaces {
		if st.Workspaces[i].ID == id {
			st.Workspaces[i].ProjectID = ""
		}
	}
	m.mu.Unlock()
	st.PlaceTop(id, before)
	return m.hosts[0].b.Send(proto.SessionOrder{SessionID: sh, Order: st.Session(sh).Order})
}

// newTabOn opens a tab on a host, in that host's part of the window's
// shared session: the part it has, else a session there of the same
// name, which joins, else a new one, which joins once it shows up.
func (m *multi) newTabOn(v app.NewTabOn) error {
	h := m.byName(v.Host)
	if h == nil {
		return fmt.Errorf("no host %s in this window", v.Host)
	}
	tab := v.Tab
	if p := cmp.Or(tab.FromPane, tab.ParentPane); p != "" && m.of(p) != h {
		tab.FromPane, tab.ParentPane = "", "" // a pane of another machine
	}
	if h == m.hosts[0] {
		return h.b.Send(tab)
	}
	if !up(h) {
		return fmt.Errorf("%s is offline", h.name)
	}
	local := m.hosts[0].b.State()
	ss := local.Session(tab.SessionID)
	if ss == nil {
		return fmt.Errorf("no session %s on this machine", tab.SessionID)
	}
	hs := h.b.State()
	if r := ss.Remote(h.name); r != nil && hs.Session(r.Session) != nil {
		tab.SessionID = r.Session
		return h.b.Send(tab)
	}
	if x := hs.SessionNamed(ss.Name); x != nil {
		if err := m.hosts[0].b.Send(proto.SessionLink{SessionID: ss.ID, Host: h.name, Remote: x.ID}); err != nil {
			return err
		}
		tab.SessionID = x.ID
		return h.b.Send(tab)
	}
	m.mu.Lock()
	m.pending[[2]string{h.name, ss.ID}] = ss.Name
	m.mu.Unlock()
	if len(tab.Cmd) > 0 || tab.GroupID != "" {
		// SessionNew opens a shell; the tab follows once the part joins.
		// ponytail: only the CLI sends a command, and it makes the part itself.
		return errors.New("open a tab on " + h.name + " first, then run the command there")
	}
	return h.b.Send(proto.SessionNew{Name: ss.Name, Cwd: tab.Cwd})
}

// Frame implements app.Backend from the pane's host.
func (m *multi) Frame(pane string) (vt.Grid, vt.Modes, bool) {
	if h := m.of(pane); h != nil {
		return h.b.Frame(pane)
	}
	return vt.Grid{}, vt.Modes{}, false
}

// Show implements app.Viewer: each host hears which of its panes the
// window draws, so a host the window does not show sends no frames.
func (m *multi) Show(panes, live []string) {
	by, lv := map[*hostLink][]string{}, map[*hostLink][]string{}
	for _, p := range panes {
		if h := m.of(p); h != nil {
			by[h] = append(by[h], p)
		}
	}
	for _, p := range live {
		if h := m.of(p); h != nil {
			lv[h] = append(lv[h], p)
		}
	}
	for _, h := range m.hosts {
		h.b.Show(by[h], lv[h])
	}
}

// Link implements app.Linker. Only this machine's daemon being down
// covers the window, and only while the window shows one of its
// sessions: a host's state shows in its sidebar heading instead. Epoch
// counts every host's connections, so a reconnect anywhere sends the
// sizes again.
func (m *multi) Link() app.Link {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	l := m.hosts[0].b.Link()
	c := cur.b.Link()
	out := app.Link{State: l.State, Note: l.Note, Level: c.Level, Build: c.Build}
	if cur != m.hosts[0] {
		out.State, out.Note = app.LinkUp, ""
	}
	for _, h := range m.hosts {
		out.Epoch += h.b.Link().Epoch
	}
	return out
}

func (m *multi) Reconnect() {
	for _, h := range m.hosts {
		h.b.Reconnect()
	}
}

// Restart implements app.Linker for this machine's daemon only: a host's
// daemon is restarted by pitwall --host on it, which asks first.
func (m *multi) Restart() { m.hosts[0].b.Restart() }

// HostOf implements app.Hoster.
func (m *multi) HostOf(id string) string {
	if h := m.of(id); h != nil {
		return h.name
	}
	return ""
}

// SessionHost implements app.Hoster.
func (m *multi) SessionHost(session string) string {
	if h := m.of(session); h != nil {
		return h.name
	}
	return ""
}

// Hosts implements app.Hoster.
func (m *multi) Hosts() []sidebar.HostInfo {
	out := make([]sidebar.HostInfo, 0, len(m.hosts))
	m.mu.Lock()
	folded := m.folded
	m.mu.Unlock()
	for _, h := range m.hosts {
		l := h.b.Link()
		hi := sidebar.HostInfo{Name: h.name, Up: up(h), Note: l.Note, SSH: h.ssh, Latency: l.RTT, Retry: l.Retry, Since: l.Since, Color: h.color}
		switch l.State {
		case app.LinkRestart, app.LinkStale:
			hi.Note = "Runs another pitwall version; open it with pitwall --host " + h.name + " to restart it."
		}
		for _, s := range h.b.State().Sessions {
			if !folded[s.ID] {
				hi.Sessions = append(hi.Sessions, s.ID)
			}
		}
		out = append(out, hi)
	}
	return out
}

func (m *multi) Scroll(pane string) (offset, max int, pushed uint64) {
	if h := m.of(pane); h != nil {
		return h.b.Scroll(pane)
	}
	return 0, 0, 0
}

func (m *multi) Found(pane string) (proto.SearchResult, uint64) {
	if h := m.of(pane); h != nil {
		return h.b.Found(pane)
	}
	return proto.SearchResult{}, 0
}

func (m *multi) TakeText(pane string) (proto.TextResult, bool) {
	if h := m.of(pane); h != nil {
		return h.b.TakeText(pane)
	}
	return proto.TextResult{}, false
}

func (m *multi) TakePasted(pane string) (proto.PasteImageResult, bool) {
	if h := m.of(pane); h != nil {
		return h.b.TakePasted(pane)
	}
	return proto.PasteImageResult{}, false
}

func (m *multi) Chat(id string) (session.Snapshot, bool) {
	if h := m.of(id); h != nil {
		return h.b.Chat(id)
	}
	return session.Snapshot{}, false
}

// Favicon asks the host of the session the window shows: the sidebar
// draws that session's repos.
func (m *multi) Favicon(dir string) (proto.FaviconResult, bool) {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	return cur.b.Favicon(dir)
}

func (m *multi) TakeMoves() []proto.FocusDir {
	var out []proto.FocusDir
	for _, h := range m.hosts {
		out = append(out, h.b.TakeMoves()...)
	}
	return out
}

func (m *multi) Worktree() proto.WorktreeInfo {
	m.mu.Lock()
	h := m.tree
	m.mu.Unlock()
	if h == nil {
		return proto.WorktreeInfo{}
	}
	return h.b.Worktree()
}

// SearchResults implements app.SessionSearcher: every host's batches, with
// Done on the last one only once each host sent its last.
//
// ponytail: a host that drops mid-search never finishes it, so the
// palette keeps saying it searches until the next query.
func (m *multi) SearchResults() []proto.SearchAllResult {
	var out []proto.SearchAllResult
	for _, h := range m.hosts {
		for _, r := range h.b.SearchResults() {
			m.mu.Lock()
			if r.Done {
				delete(m.searching, h)
			}
			r.Done = len(m.searching) == 0
			m.mu.Unlock()
			out = append(out, r)
		}
	}
	return out
}
