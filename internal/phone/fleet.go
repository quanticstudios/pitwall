package phone

import (
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// Fleet is the paired machines with a client each, and the tab list each
// sent last, merged into the one State the sidebar draws: each machine's
// sessions in turn, in the order the machines were paired. A call about a
// tab goes to the machine that owns it.
type Fleet struct {
	Machines []Machine
	clients  map[string]*Client
	tabs     map[string][]remote.Tab
	owner    map[string]string // a tab, pane, session or group id to its machine's id
	st       model.State
	all      []remote.Tab
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
	f.st, f.all, f.owner = model.State{}, nil, map[string]string{}
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
			f.owner[t.ID], f.owner[t.Pane] = m.ID, m.ID
		}
		f.all = append(f.all, tabs...)
		f.st.Sessions = append(f.st.Sessions, part.Sessions...)
		f.st.Projects = append(f.st.Projects, part.Projects...)
		f.st.Workspaces = append(f.st.Workspaces, part.Workspaces...)
		f.st.Panes = append(f.st.Panes, part.Panes...)
		f.st.Activities = append(f.st.Activities, part.Activities...)
	}
}

// State is every machine's tabs as one State.
func (f *Fleet) State() model.State { return f.st }

// Tabs is every machine's tabs, in State's order.
func (f *Fleet) Tabs() []remote.Tab { return f.all }

// Owner is the machine that owns a tab, pane, session or group id, and
// whether one does.
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
