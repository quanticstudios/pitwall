package main

// The joins of several daemons' states, shared by the window's multi and
// the CLI's remotes.

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// The waits between reconnect tries double from retryMin up to retryMax.
var retryMin, retryMax = 500 * time.Millisecond, 30 * time.Second

// foldIn is s with the sessions in to, each a part of a shared session
// here, folded into that session: their groups, tabs, closed tabs and tasks move to it,
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
	// why: a finished worker's closed tab is its parent's result, in the
	// parent's session.
	s.Closed = slices.Clone(s.Closed)
	for i := range s.Closed {
		if id := to[s.Closed[i].Tab.SessionID]; id != "" {
			s.Closed[i].Tab.SessionID = id
		}
	}
	return s
}

// eachID calls f with every id in s a request can name: its sessions,
// groups, tabs and their splits, panes, chats and tasks, and its closed
// tabs with the ids their tab and pane had, by which a Reparent or a read
// of a finished worker's reply reaches them.
func eachID(s model.State, f func(id string)) {
	own := func(id string) {
		if id != "" {
			f(id)
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
		own(x.Tab.ID)
		own(x.Pane.ID)
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
