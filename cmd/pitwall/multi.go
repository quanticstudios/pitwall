package main

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"strconv"
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
	clipHost string // clipFrom's name for state's Clipboard, under mu
	// folded is the sessions on other hosts that are parts of a shared
	// session here, shared the shared sessions here.
	folded, shared map[string]bool
	// pending is the shared sessions whose part on a host a SessionNew is
	// making: the name it was asked for, by host and session here.
	pending map[[2]string]string
	// pendingHere is the sessions of other hosts a SessionNew here is making
	// shared: by name, the host and its session there.
	pendingHere map[string][2]string
	// asked is the SessionLink last sent for a session and host, and when,
	// so a merge before this machine's state shows it does not send it
	// again within a few seconds.
	asked map[[2]string]asked
	// shown is the session here the window shows, told the part each
	// host last heard it shows, on which connection.
	shown string
	told  map[string]told
}

type told struct {
	session string
	epoch   int
}

type asked struct {
	sig, from string // what was asked, and the record it was asked against
	at        time.Time
}

// hostLink is one daemon of a multi.
type hostLink struct {
	name string // "" for this machine
	ssh  string // the ssh destination, "" for this machine
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
	_ app.Monitorer       = (*multi)(nil)
	_ app.Hoster          = (*multi)(nil)
	_ app.Numberer        = (*multi)(nil)
	_ app.Mover           = (*multi)(nil)
	_ app.Scroller        = (*multi)(nil)
	_ app.Iconer          = (*multi)(nil)
	_ app.Paster          = (*multi)(nil)
	_ app.SessionSearcher = (*multi)(nil)
	_ app.ClipboardHoster = (*multi)(nil)
)

// hostDial is one host of a multi and how to reach its daemon.
type hostDial struct {
	name, ssh string
	dial      func(session string, restart bool) (*proto.Conn, proto.StateMsg, error)
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
		d := hostDial{name: h.Name, ssh: cmp.Or(h.SSH, h.Name)}
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
		pending: map[[2]string]string{}, pendingHere: map[string][2]string{}, asked: map[[2]string]asked{},
		told: map[string]told{}}
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
		m.hosts = append(m.hosts, &hostLink{name: r.name, ssh: r.ssh, b: b})
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
				m.pushFocus(f)
			}
		}()
	}
	for _, h := range m.hosts[1:] {
		h.b.startDialing(false)
	}
	if len(m.hosts) > 1 {
		go m.pings(pingEvery)
	}
	return m
}

// pushFocus asks the window to show f; the newest request wins, as in a
// backend.
func (m *multi) pushFocus(f proto.FocusSession) {
	select {
	case <-m.focus:
	default:
	}
	select {
	case m.focus <- f:
	default:
	}
}

// pings measures each host's latency every few seconds, for the host row.
// ponytail: it runs as long as the process, which has one window.
func (m *multi) pings(every time.Duration) {
	for {
		time.Sleep(every)
		for _, h := range m.hosts[1:] {
			if up(h) {
				h.b.ping()
			}
		}
	}
}

// pingEvery is how often the window measures each host's latency.
var pingEvery = 3 * time.Second

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
			// the ids it does not know: they are of closed tabs. A part
			// whose host has not sent a state yet keeps its ids, which
			// TopOrder keeps for a session with Remotes.
			s.Sessions = slices.Clone(s.Sessions)
			for j := range s.Sessions {
				s.Sessions[j].Remotes = slices.DeleteFunc(slices.Clone(s.Sessions[j].Remotes), func(r model.RemoteSession) bool {
					h := m.byName(r.Host)
					return h != nil && h.b.Link().Epoch > 0
				})
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
	m.clipHost = ""
	if m.clipFrom != nil {
		m.clipHost = m.clipFrom.name
	}
	m.mu.Unlock()
	m.reconcile(local)
}

// ClipboardHost is the host whose pane made the clipboard write numbered
// seq, "" for this machine, while it is the latest.
func (m *multi) ClipboardHost(seq uint64) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clipHost, seq == m.state.Clipboard.Seq
}

// reconcile keeps this machine's record of its shared sessions true to the
// hosts it reaches: a part whose session ended there leaves, tabs closed
// while their host was down close there, and a part a SessionNew made
// joins once its host's state has it. Callers hold mergeMu.
func (m *multi) reconcile(local model.State) {
	link := func(l proto.SessionLink) {
		key := [2]string{l.SessionID, l.Host}
		sig, from := fmt.Sprint(l.Remote, l.Forget, l.Unforget), ""
		if ss := local.Session(l.SessionID); ss != nil {
			if r := ss.Remote(l.Host); r != nil {
				from = fmt.Sprint(r.Session, r.Forget)
			}
		}
		if a := m.asked[key]; a.sig == sig && a.from == from && time.Since(a.at) < 5*time.Second {
			return
		}
		if m.hosts[0].b.Send(l) == nil {
			m.asked[key] = asked{sig, from, time.Now()}
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
				// An id leaves Forget only once the host's state shows its
				// tab gone, so a close a dropped link lost is sent again.
				var gone []string
				for _, id := range r.Forget {
					w := hs.Workspace(id)
					if w == nil {
						gone = append(gone, id)
						continue
					}
					key := [2]string{id, "close"}
					if time.Since(m.asked[key].at) < 5*time.Second {
						continue
					}
					if w.Pinned {
						// why: the window asked before it forgot a pinned
						// tab, and the unpin could not reach a host that
						// was down.
						h.b.Send(proto.SetTabOptions{WorkspaceID: id, Color: w.Color})
					}
					if h.b.Send(proto.CloseTab{WorkspaceID: id}) == nil {
						m.asked[key] = asked{at: time.Now()}
					}
				}
				if len(gone) > 0 {
					link(proto.SessionLink{SessionID: ss.ID, Host: r.Host, Remote: r.Session, Unforget: gone})
				}
			}
		}
	}
	// A shared session's order names every top-level item, so another
	// host's tabs keep their places whatever order that host lists them in.
	m.mu.Lock()
	merged := m.state
	m.mu.Unlock()
	for _, ss := range local.Sessions {
		if len(ss.Remotes) == 0 {
			continue
		}
		top := merged.TopOrder(ss.ID)
		if !slices.Equal(top, ss.Order) && slices.ContainsFunc(top, func(id string) bool { return !slices.Contains(ss.Order, id) }) {
			key := [2]string{ss.ID, "order"}
			if a := m.asked[key]; a.sig != fmt.Sprint(top) || time.Since(a.at) > 5*time.Second {
				if m.hosts[0].b.Send(proto.SessionOrder{SessionID: ss.ID, Order: top}) == nil {
					m.asked[key] = asked{sig: fmt.Sprint(top), at: time.Now()}
				}
			}
		}
	}
	m.tellShown()
	m.mu.Lock()
	defer m.mu.Unlock()
	// A killed shared session ends here once no part is left, which
	// happens as each host's state shows its part gone (above); until
	// then the kill goes again to each host that still has its part.
	for _, ss := range local.Sessions {
		if !ss.Killing {
			continue
		}
		if len(ss.Remotes) == 0 {
			key := [2]string{ss.ID, "kill"}
			if time.Since(m.asked[key].at) > 5*time.Second && m.hosts[0].b.Send(proto.SessionKill{SessionID: ss.ID}) == nil {
				m.asked[key] = asked{at: time.Now()}
			}
			continue
		}
		for _, r := range ss.Remotes {
			h := m.byName(r.Host)
			if h == nil || h == m.hosts[0] || !up(h) {
				continue
			}
			hs := h.b.State()
			key := [2]string{r.Session, "kill"}
			if hs.Session(r.Session) != nil && time.Since(m.asked[key].at) > 5*time.Second {
				if h.b.Send(proto.SessionKill{SessionID: r.Session}) == nil {
					m.asked[key] = asked{at: time.Now()}
				}
			}
		}
	}
	for name, ref := range m.pendingHere {
		if x := local.SessionNamed(name); x != nil {
			link(proto.SessionLink{SessionID: x.ID, Host: ref[0], Remote: ref[1]})
			m.pushFocus(proto.FocusSession{SessionID: x.ID})
			delete(m.pendingHere, name)
		}
	}
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
// no id. The window shows why a tab, task, group, move or kill failed.
func (m *multi) Send(msg any) error {
	err := m.send(msg)
	if err != nil {
		switch msg.(type) {
		case app.NewTabOn, app.NewTaskOn, proto.NewSession, proto.SessionKill, proto.NewGroup, proto.MoveSession, proto.MoveGroup:
			return app.Refusal{Err: err}
		}
	}
	return err
}

func (m *multi) send(msg any) error {
	switch v := msg.(type) {
	case app.NewTabOn:
		return m.newTabOn(v)
	case app.NewTaskOn:
		return m.newTaskOn(v)
	case proto.NewSession:
		// A group's + in a shared session: the group's host opens it.
		if g := m.of(v.GroupID); g != nil && g != m.hosts[0] && m.of(v.SessionID) == m.hosts[0] {
			return m.newTabOn(app.NewTabOn{Host: g.name, Tab: v})
		}
	case proto.SessionRename:
		m.eachPart(v.SessionID, func(h *hostLink, r model.RemoteSession) {
			h.b.Send(proto.SessionRename{SessionID: r.Session, Name: v.Name})
		})
	case proto.SessionKill:
		// why: a part on a host the window cannot reach would keep running
		// there, and nothing here would remember to stop it.
		if down := m.downPart(v.SessionID); down != "" {
			return fmt.Errorf("%s is offline; kill this session once it is back, so its tabs there stop too", down)
		}
		if st := m.hosts[0].b.State(); st.Session(v.SessionID) != nil && len(st.Session(v.SessionID).Remotes) > 0 {
			// why: a kill sent over a link that drops is lost, so the tabs
			// here stay until each part's host shows its part gone, and
			// this machine's daemon keeps the kill for the next window;
			// see reconcile.
			if err := m.hosts[0].b.Send(proto.SessionLink{SessionID: v.SessionID, Kill: true}); err != nil {
				return err
			}
			m.eachPart(v.SessionID, func(h *hostLink, r model.RemoteSession) {
				h.b.Send(proto.SessionKill{SessionID: r.Session})
			})
			return nil
		}
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
		m.mu.Lock()
		m.shown, m.told = v.SessionID, map[string]told{}
		m.mu.Unlock()
		m.tellShown()
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

// tellShown sends SessionShow for each part of the session the window
// shows to its host, once per connection and part. The host then counts
// the window as showing its part, so pitwall attach there focuses this
// window instead of opening one, also for a part that joins later.
func (m *multi) tellShown() {
	m.mu.Lock()
	id := m.shown
	m.mu.Unlock()
	m.eachPart(id, func(h *hostLink, r model.RemoteSession) {
		t := told{r.Session, h.b.Link().Epoch}
		m.mu.Lock()
		same := m.told[h.name] == t
		m.told[h.name] = t
		m.mu.Unlock()
		if !same {
			h.b.Send(proto.SessionShow{SessionID: r.Session})
		}
	})
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

// downPart is the host of a part of the shared session id that the window
// cannot reach, "" when it reaches them all.
func (m *multi) downPart(id string) string {
	st := m.hosts[0].b.State()
	if ss := st.Session(id); ss != nil {
		for _, r := range ss.Remotes {
			if h := m.byName(r.Host); h == nil || !up(h) {
				return r.Host
			}
		}
	}
	return ""
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
	m.hosts[0].b.Send(proto.SessionLink{SessionID: sh, Host: h.name, Remote: r.Session, Forget: []string{tab}})
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
	if owner := m.of(tab.SessionID); owner != nil && owner != m.hosts[0] {
		return m.shareHere(owner, h, tab)
	}
	if h == m.hosts[0] {
		return h.b.Send(tab)
	}
	if err := m.canShare(); err != nil {
		return err
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
	if len(tab.Cmd) > 0 || tab.GroupID != "" {
		// ponytail: SessionNew opens a shell, so a command waits for a tab
		// there; the CLI, which sends commands, makes the part itself.
		return errors.New("open a tab on " + h.name + " first, then run the command there")
	}
	if err := firstPartProfile(h.name, tab); err != nil {
		return err
	}
	m.mu.Lock()
	m.pending[[2]string{h.name, ss.ID}] = ss.Name
	m.mu.Unlock()
	return h.b.Send(proto.SessionNew{Name: ss.Name, Cwd: tab.Cwd})
}

// newTaskOn starts or queues a task on a host, in that host's part of
// the window's shared session; the session's id here would start it on
// this machine. A host with no part yet, or one down, refuses it.
func (m *multi) newTaskOn(v app.NewTaskOn) error {
	h := m.byName(v.Host)
	if h == nil {
		return fmt.Errorf("no host %s in this window", v.Host)
	}
	t := v.Task
	if h != m.hosts[0] {
		if !up(h) {
			return fmt.Errorf("%s is offline", h.name)
		}
		local := m.hosts[0].b.State()
		if ss := local.Session(t.Task.SessionID); ss != nil {
			r := ss.Remote(h.name)
			if r == nil {
				return fmt.Errorf("open a tab on %s first, then start the task", h.name)
			}
			t.Task.SessionID = r.Session
		}
		level := h.b.Link().Level
		if err := varsLevel(t.Task.Env, level); err != nil {
			return fmt.Errorf("%s: %w", h.name, err)
		}
		// why: an older daemon drops what it does not know, so the task
		// would start at once, or under the default account.
		if (!t.Task.NextStart.IsZero() || t.Task.Repeat > 0) && level < proto.ScheduleLevel {
			return fmt.Errorf("pitwall's daemon on %s is too old for task schedules; update pitwall there or restart its daemon", h.name)
		}
		if t.Task.Profile != "" && level < proto.ProfileLevel {
			return fmt.Errorf("pitwall's daemon on %s is too old for account profiles; update pitwall there or restart its daemon", h.name)
		}
	}
	if m.of(t.FromPane) != h {
		t.FromPane = ""
	}
	if m.of(t.Task.GroupID) != h {
		t.Task.GroupID = ""
	}
	return h.b.Send(t)
}

// canShare refuses a tab that would make or grow a shared session while
// this machine's daemon is too old to keep one, before any host makes a
// part it could not link.
func (m *multi) canShare() error {
	if m.hosts[0].b.Link().Level < proto.SharedLevel {
		return errors.New("pitwall's background service here is older than this pitwall and cannot share a session; restart it")
	}
	return nil
}

// shareHere opens tab on h while the window shows a session of another
// host, owner. On owner it is a tab like any other. On this machine it
// makes the session shared: a session here of the same name, made when
// there is none, takes owner's session as its part, and the window
// shows it. A shared session is this machine's, so a third host waits
// until this machine has a tab in it.
func (m *multi) shareHere(owner, h *hostLink, tab proto.NewSession) error {
	if h == owner {
		return h.b.Send(tab)
	}
	if h != m.hosts[0] {
		return errors.New("open a tab on this machine first: it keeps the shared session")
	}
	if err := m.canShare(); err != nil {
		return err
	}
	ost := owner.b.State()
	ss := ost.Session(tab.SessionID)
	if ss == nil {
		return fmt.Errorf("no session %s on %s", tab.SessionID, owner.name)
	}
	local := m.hosts[0].b.State()
	if x := local.SessionNamed(ss.Name); x != nil {
		if err := h.b.Send(proto.SessionLink{SessionID: x.ID, Host: owner.name, Remote: ss.ID}); err != nil {
			return err
		}
		tab.SessionID = x.ID
		m.pushFocus(proto.FocusSession{SessionID: x.ID})
		return h.b.Send(tab)
	}
	if err := firstPartProfile("this machine", tab); err != nil {
		return err
	}
	m.mu.Lock()
	m.pendingHere[ss.Name] = [2]string{owner.name, ss.ID}
	m.mu.Unlock()
	return h.b.Send(proto.SessionNew{Name: ss.Name, Cwd: tab.Cwd})
}

// firstPartProfile refuses tab when it names an account profile and
// would be the first tab of the session's part on host: SessionNew,
// which makes the part, carries no profile, so its shell would start
// under the default account.
func firstPartProfile(host string, tab proto.NewSession) error {
	if tab.Profile == "" {
		return nil
	}
	return fmt.Errorf("%s has no part of this session yet: open its first tab there with the Default account, then pick %s for the next", host, tab.Profile)
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

// TabNumber implements app.Numberer: the tab's number as its host's
// pitwall ls gives it, from that host's own order, "box:3" on another
// host; "" for an unknown tab.
func (m *multi) TabNumber(id string) string {
	h := m.of(id)
	if h == nil {
		return ""
	}
	st := h.b.State()
	w := st.Workspace(id)
	if w == nil {
		return ""
	}
	return tabNumber(h.name, st.Numbered(w.SessionID), id)
}

// tabNumber is id's number among tabs, from 1, prefixed "host:" on
// another host; "" when tabs lacks it.
func tabNumber(host string, tabs []model.Workspace, id string) string {
	i := slices.IndexFunc(tabs, func(w model.Workspace) bool { return w.ID == id })
	switch {
	case i < 0:
		return ""
	case host == "":
		return strconv.Itoa(i + 1)
	}
	return host + ":" + strconv.Itoa(i+1)
}

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
		hi := sidebar.HostInfo{Name: h.name, Up: up(h), Note: l.Note, SSH: h.ssh, Latency: l.RTT, Retry: l.Retry, Since: l.Since}
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

// Monitor answers from the backend of the host that owns Monitor tab ws.
func (m *multi) Monitor(ws string) (proto.MonitorSample, time.Time, bool) {
	if h := m.of(ws); h != nil {
		return h.b.Monitor(ws)
	}
	return proto.MonitorSample{}, time.Time{}, false
}

// Favicon asks the host of the session the window shows: the sidebar
// draws that session's repos.
func (m *multi) Favicon(dir string) (proto.FaviconResult, bool) {
	m.mu.Lock()
	cur := m.cur
	m.mu.Unlock()
	return cur.b.Favicon(dir)
}

// FaviconOn implements app.HostIconer: host's daemon looks dir up, the
// window's for "".
func (m *multi) FaviconOn(host, dir string) (proto.FaviconResult, bool) {
	if host == "" {
		return m.Favicon(dir)
	}
	if h := m.byName(host); h != nil {
		return h.b.Favicon(dir)
	}
	return proto.FaviconResult{}, false
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
