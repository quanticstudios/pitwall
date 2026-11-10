package phone

import (
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// Fleet is the paired machines with a client each, and the tab list each
// sent last, merged into the one State the sidebar draws: each machine's
// sessions in turn, in the order the machines were paired. Every id in
// that State has its machine's id in front (NS), as two machines can use
// the same ids; Tab gives back the tab with the ids its machine knows, for
// calls to that machine.
type Fleet struct {
	Machines []Machine
	clients  map[string]*Client
	tabs     map[string][]remote.Tab
	owner    map[string]string     // a session, group, tab or pane id of State to its machine's id
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
	f.st, f.owner, f.tab = model.State{}, map[string]string{}, map[string]remote.Tab{}
	for _, m := range f.Machines {
		tabs := f.tabs[m.ID]
		part := stateOf(m.ID, tabs)
		for _, s := range part.Sessions {
			f.owner[s.ID] = m.ID
		}
		for _, p := range part.Projects {
			f.owner[p.ID] = m.ID
		}
		for _, t := range tabs {
			for _, id := range []string{NS(m.ID, t.ID), NS(m.ID, t.Pane)} {
				f.owner[id], f.tab[id] = m.ID, t
			}
		}
		f.st.Sessions = append(f.st.Sessions, part.Sessions...)
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

// Owner is the machine that owns a State tab, pane, session or group id,
// and whether one does.
func (f *Fleet) Owner(id string) (Machine, bool) {
	for _, m := range f.Machines {
		if m.ID == f.owner[id] {
			return m, true
		}
	}
	return Machine{}, false
}

// Client is the client of the machine with that id, nil for none.
func (f *Fleet) Client(machine string) *Client { return f.clients[machine] }

// ClientOf is the client of the machine that owns id, nil for none.
func (f *Fleet) ClientOf(id string) *Client { return f.clients[f.owner[id]] }

// TabsOf is the tab list machine sent last.
func (f *Fleet) TabsOf(machine string) []remote.Tab { return f.tabs[machine] }
