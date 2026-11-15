package control

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Caller is who asks: Session names the session to act on ("" for none
// named), Pane is the pane the request comes from ("" outside a pane).
// The CLI fills them from -s and $PITWALL_PANE.
type Caller struct {
	Session string
	Pane    string
}

// CurrentSession is the session tab commands act on: the one c names
// (exactly, else by unique prefix), else the session of c's pane, else
// the most recently used one.
func CurrentSession(state model.State, c Caller) (model.Session, error) {
	if c.Session != "" {
		return ResolveSession(state, c.Session)
	}
	if c.Pane != "" {
		for _, p := range state.Panes {
			if p.ID == c.Pane {
				if s := state.Session(state.SessionOf(p.WorkspaceID)); s != nil {
					return *s, nil
				}
			}
		}
	}
	if s := state.Recent(); s != nil {
		return *s, nil
	}
	return model.Session{}, errors.New("no session is open")
}

// ResolveSession finds a session by exact name, then by unique prefix.
func ResolveSession(state model.State, name string) (model.Session, error) {
	if s := state.SessionNamed(name); s != nil {
		return *s, nil
	}
	var found []model.Session
	for _, s := range state.Sessions {
		if strings.HasPrefix(s.Name, name) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return model.Session{}, fmt.Errorf("no session matches %q (see pitwall session ls)", name)
	case 1:
		return found[0], nil
	}
	var names []string
	for _, s := range found {
		names = append(names, s.Name)
	}
	return model.Session{}, fmt.Errorf("ambiguous session %q; matches %s", name, strings.Join(names, ", "))
}

// ResolveTab finds a tab by @ and its id or one of its panes' ids, in any
// session, or a tab of session (an id, as from CurrentSession) by its id,
// then by its number in pitwall ls ("3" or "#3"), then by exact title,
// then by a unique title prefix. With no name it is the tab of c's pane,
// in whichever session.
func ResolveTab(state model.State, c Caller, session, name string) (model.Workspace, error) {
	if name == "" {
		for _, p := range state.Panes {
			if c.Pane != "" && p.ID == c.Pane {
				for _, w := range state.Workspaces {
					if w.ID == p.WorkspaceID {
						return w, nil
					}
				}
			}
		}
		if c.Pane != "" {
			return model.Workspace{}, fmt.Errorf("pane %q has no tab in daemon state", c.Pane)
		}
		return model.Workspace{}, errors.New("specify a tab name outside a pitwall pane (see pitwall ls)")
	}
	if id, ok := strings.CutPrefix(name, "@"); ok {
		if i := slices.IndexFunc(state.Workspaces, func(w model.Workspace) bool { return w.ID == id }); i >= 0 {
			return state.Workspaces[i], nil
		}
		w, _, err := ResolvePane(state, c, session, name)
		return w, err
	}
	tabs := state.Numbered(session)
	if i := slices.IndexFunc(tabs, func(w model.Workspace) bool { return w.ID == name }); i >= 0 {
		return tabs[i], nil
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(name, "#")); err == nil {
		if n < 1 || n > len(tabs) {
			return model.Workspace{}, fmt.Errorf("no tab #%d (see pitwall ls)", n)
		}
		return tabs[n-1], nil
	}
	var exact, prefixed []int
	for i, w := range tabs {
		switch t := Title(w); {
		case t == name:
			exact = append(exact, i)
		case strings.HasPrefix(t, name):
			prefixed = append(prefixed, i)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = prefixed
	}
	switch len(matches) {
	case 0:
		return model.Workspace{}, fmt.Errorf("no tab matches %q (see pitwall ls)", name)
	case 1:
		return tabs[matches[0]], nil
	}
	var nums []string
	for _, i := range matches {
		nums = append(nums, "#"+strconv.Itoa(i+1))
	}
	return model.Workspace{}, fmt.Errorf("ambiguous tab %q; matches %s", name, strings.Join(nums, ", "))
}

// ResolvePane finds a pane by address: a tab, as ResolveTab names it, for
// its main pane, TAB.N for its Nth pane in layout order, as in "3.2",
// "#3.2" or "review.2", or @ and its id in any session, which stays valid
// while tabs move. A tab whose whole title is the address wins over
// reading N off its end.
func ResolvePane(state model.State, c Caller, session, addr string) (model.Workspace, model.Pane, error) {
	if id, ok := strings.CutPrefix(addr, "@"); ok {
		i := slices.IndexFunc(state.Panes, func(p model.Pane) bool { return p.ID == id })
		if i < 0 {
			return model.Workspace{}, model.Pane{}, fmt.Errorf("no pane %s: it closed (see pitwall ls --json)", addr)
		}
		p := state.Panes[i]
		w := slices.IndexFunc(state.Workspaces, func(w model.Workspace) bool { return w.ID == p.WorkspaceID })
		if w < 0 {
			return model.Workspace{}, model.Pane{}, fmt.Errorf("pane %s has no tab", addr)
		}
		return state.Workspaces[w], p, nil
	}
	tab, n := addr, -1
	if i := strings.LastIndexByte(addr, '.'); i > 0 && !slices.ContainsFunc(state.Numbered(session), func(w model.Workspace) bool { return Title(w) == addr }) {
		if k, err := strconv.Atoi(addr[i+1:]); err == nil {
			tab, n = addr[:i], k
		}
	}
	w, err := ResolveTab(state, c, session, tab)
	if err != nil {
		return w, model.Pane{}, err
	}
	panes := TabPanes(state, w)
	if n < 0 {
		p := MainPane(panes)
		if p == nil {
			return w, model.Pane{}, fmt.Errorf("tab %s has no pane", Title(w))
		}
		return w, *p, nil
	}
	if n < 1 || n > len(panes) {
		return w, model.Pane{}, fmt.Errorf("tab %s has no pane %d; it has %d (see pitwall ls --json)", Title(w), n, len(panes))
	}
	return w, panes[n-1], nil
}

// Title is what the sidebar shows for a tab: its chosen name, else its
// label.
func Title(w model.Workspace) string {
	if w.NameSet {
		return w.Name
	}
	return cmp.Or(w.Label, w.Name)
}

// ChildTabs is the set of tabs c's pane's tab opened (their Parent), for
// --children.
func ChildTabs(state model.State, c Caller) (map[string]bool, error) {
	i := slices.IndexFunc(state.Panes, func(p model.Pane) bool { return p.ID == c.Pane })
	if c.Pane == "" || i < 0 {
		return nil, errors.New("--children must run inside a pitwall pane (PITWALL_PANE is not set)")
	}
	out := map[string]bool{}
	for _, w := range state.Workspaces {
		if w.Parent != "" && w.Parent == state.Panes[i].WorkspaceID {
			out[w.ID] = true
		}
	}
	return out, nil
}
