package phone

import (
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// Fleet is the paired machines with a client each, and the tab list each
// sent last, merged into the one State the sidebar draws: the sessions in
// the order the machines were paired and then the order each lists them.
// Sessions of one name on several machines are one session, its items
// each machine's in turn, as the window's sidebar draws a shared session.
// Every tab, pane and group id in that State has its machine's id in
// front (NS), as two machines can use the same ids; Tab gives back the
// tab with the ids its machine knows, for calls to that machine.
type Fleet struct {
	Machines []Machine
	clients  map[string]*Client
	tabs     map[string][]remote.Tab
	owner    map[string]string     // a group, tab or pane id of State to its machine's id; a session's first machine
	on       map[string][]string   // a session id of State to its machines' ids, in Machines order
	tab      map[string]remote.Tab // a tab or pane id of State to the tab as its machine sent it
	st       model.State
}

// NewFleet is a Fleet over ms with a client for each.
func NewFleet(ms []Machine) *Fleet {
	f := &Fleet{Machines: ms, clients: map[string]*Client{}, tabs: map[string][]remote.Tab{}}
	for _, m := range ms {
		f.clients[m.ID] = NewClient(m.Creds)
	}
	f.merge()
	return f
}

// Set takes machine's latest tab list.
func (f *Fleet) Set(machine string, tabs []remote.Tab) {
	f.tabs[machine] = tabs
	f.merge()
}

func (f *Fleet) merge() {
	f.st, f.owner, f.on, f.tab = model.State{}, map[string]string{}, map[string][]string{}, map[string]remote.Tab{}
	for _, m := range f.Machines {
		tabs := f.tabs[m.ID]
		part := stateOf(m.ID, tabs)
		for _, s := range part.Sessions {
			f.on[s.ID] = append(f.on[s.ID], m.ID)
			if ss := f.st.Session(s.ID); ss != nil {
				ss.Order = append(ss.Order, s.Order...)
				continue
			}
			f.owner[s.ID] = m.ID
			f.st.Sessions = append(f.st.Sessions, s)
		}
		for _, p := range part.Projects {
			f.owner[p.ID] = m.ID
		}
		for _, t := range tabs {
			for _, id := range []string{NS(m.ID, t.ID), NS(m.ID, t.Pane)} {
				f.owner[id], f.tab[id] = m.ID, t
			}
		}
		f.st.Projects = append(f.st.Projects, part.Projects...)
		f.st.Workspaces = append(f.st.Workspaces, part.Workspaces...)
		f.st.Panes = append(f.st.Panes, part.Panes...)
		f.st.Activities = append(f.st.Activities, part.Activities...)
	}
}

// State is every machine's tabs as one State.
func (f *Fleet) State() model.State { return f.st }

// Tab is the machine with State's tab or pane id, and that tab with the
// ids the machine knows.
func (f *Fleet) Tab(id string) (Machine, remote.Tab, bool) {
	m, ok := f.Owner(id)
	t, found := f.tab[id]
	return m, t, ok && found
}

// Owner is the machine that owns a State tab, pane or group id, or the
// first machine of a session id (MachinesOf has them all), and whether
// one does.
func (f *Fleet) Owner(id string) (Machine, bool) {
	for _, m := range f.Machines {
		if m.ID == f.owner[id] {
			return m, true
		}
	}
	return Machine{}, false
}

// MachinesOf is the machines with tabs in State's session id, in
// Machines order.
func (f *Fleet) MachinesOf(session string) []Machine {
	var out []Machine
	for _, m := range f.Machines {
		if slices.Contains(f.on[session], m.ID) {
			out = append(out, m)
		}
	}
	return out
}

// Client is the client of the machine with that id, nil for none.
func (f *Fleet) Client(machine string) *Client { return f.clients[machine] }

// ClientOf is the client of the machine that owns id, nil for none.
func (f *Fleet) ClientOf(id string) *Client { return f.clients[f.owner[id]] }

// TabsOf is the tab list machine sent last.
func (f *Fleet) TabsOf(machine string) []remote.Tab { return f.tabs[machine] }
