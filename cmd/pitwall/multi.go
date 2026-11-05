package main

import (
	"maps"
	"reflect"
	"runtime"
	"sync"

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
}

// hostLink is one daemon of a multi.
type hostLink struct {
	name string // "" for this machine
	b    *backend
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
	name string
	dial func(session string, restart bool) (*proto.Conn, proto.StateMsg, error)
}

// windowHosts are the [[hosts]] entries a plain window shows next to this
// machine, and a func that drops their forwards. None on Windows, whose
// ssh cannot share a connection.
func windowHosts(hosts []config.Host) ([]hostDial, func()) {
	var out []hostDial
	var close []func()
	for _, h := range hosts {
		if !h.Window || h.Name == "" || runtime.GOOS == "windows" {
			continue
		}
		r, err := newSSHHost(h.Name, hosts)
		d := hostDial{name: h.Name}
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
	m := &multi{changed: make(chan struct{}, 1), focus: make(chan proto.FocusSession, 1), owner: map[string]*hostLink{}, clipSeq: map[*hostLink]uint64{}}
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
		m.hosts = append(m.hosts, &hostLink{name: r.name, b: b})
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
	return m
}

// merge rebuilds the window's state from every host's last one. A host
// that is down keeps its last state on screen, as one window does.
func (m *multi) merge() {
	m.mergeMu.Lock()
	defer m.mergeMu.Unlock()
	var st model.State
	owner := map[string]*hostLink{}
	for _, h := range m.hosts {
		s := h.b.State()
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
	m.state, m.owner = st, owner
	m.mu.Unlock()
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
	for _, h := range m.hosts {
		l := h.b.Link()
		hi := sidebar.HostInfo{Name: h.name, Up: l.State == app.LinkUp && l.Epoch > 0, Note: l.Note}
		switch l.State {
		case app.LinkRestart, app.LinkStale:
			hi.Note = "Runs another pitwall version; open it with pitwall --host " + h.name + " to restart it."
		}
		for _, s := range h.b.State().Sessions {
			hi.Sessions = append(hi.Sessions, s.ID)
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
