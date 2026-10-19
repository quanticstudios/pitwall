package model

import (
	"slices"
	"testing"
)

func TestTopOrder(t *testing.T) {
	s := State{
		Sessions: []Session{{ID: "s"}, {ID: "other"}},
		Projects: []Project{{ID: "g1", SessionID: "s"}, {ID: "x", SessionID: "other"}, {ID: "g2", SessionID: "s"}},
		Workspaces: []Workspace{
			{ID: "a", SessionID: "s", ProjectID: "g1"}, {ID: "u1", SessionID: "s"}, {ID: "b", SessionID: "s", ProjectID: "g1"},
			{ID: "y", SessionID: "other"}, {ID: "u2", SessionID: "s"}, {ID: "orphan", SessionID: "s", ProjectID: "gone"},
			{ID: "c", SessionID: "s", ProjectID: "g2"},
		},
	}
	// No Order: the old implied order, ungrouped tabs before groups.
	if got := s.TopOrder("s"); !slices.Equal(got, []string{"u1", "u2", "orphan", "g1", "g2"}) {
		t.Fatalf("implied: %v", got)
	}
	// Stale: unknown, repeated and grouped IDs drop out; missing ones follow.
	s.Sessions[0].Order = []string{"g2", "zzz", "u2", "a", "g2"}
	if got := s.TopOrder("s"); !slices.Equal(got, []string{"g2", "u2", "u1", "orphan", "g1"}) {
		t.Fatalf("stale: %v", got)
	}
	var ids []string
	for _, w := range s.Ordered("s") {
		ids = append(ids, w.ID)
	}
	if !slices.Equal(ids, []string{"c", "u2", "u1", "orphan", "a", "b"}) {
		t.Fatalf("Ordered: %v", ids)
	}
	s.PlaceTop("g1", "g2")
	s.PlaceTopAfter("u1", "g1")
	s.PlaceTop("orphan", "")
	if got := s.TopOrder("s"); !slices.Equal(got, []string{"g1", "u1", "g2", "u2", "orphan"}) {
		t.Fatalf("placed: %v", got)
	}
	// Another session's order is its own and untouched.
	if got := s.TopOrder("other"); !slices.Equal(got, []string{"y", "x"}) || s.Sessions[1].Order != nil {
		t.Fatalf("other session: %v %v", got, s.Sessions[1].Order)
	}
	v := s.View("other")
	if len(v.Workspaces) != 1 || len(v.Projects) != 1 || len(v.Sessions) != 2 {
		t.Fatalf("view: %+v", v)
	}
}

// A worker follows its top ancestor in its group, a worker's worker too;
// one whose parent is gone or in another group stands on its own.
func TestOrderedWorkers(t *testing.T) {
	s := State{
		Sessions: []Session{{ID: "s"}},
		Projects: []Project{{ID: "g", SessionID: "s"}},
		Workspaces: []Workspace{
			{ID: "orch", SessionID: "s", ProjectID: "g"}, {ID: "other", SessionID: "s", ProjectID: "g"},
			{ID: "w1", SessionID: "s", ProjectID: "g", Parent: "orch"}, {ID: "w1a", SessionID: "s", ProjectID: "g", Parent: "w1"},
			{ID: "lost", SessionID: "s", ProjectID: "g", Parent: "closed"}, {ID: "away", SessionID: "s", Parent: "orch"},
			{ID: "w2", SessionID: "s", ProjectID: "g", Parent: "orch"},
		},
	}
	var ids []string
	for _, w := range s.Ordered("s") {
		ids = append(ids, w.ID)
	}
	if want := []string{"away", "orch", "w1", "w1a", "w2", "other", "lost"}; !slices.Equal(ids, want) {
		t.Fatalf("Ordered: %v, want %v", ids, want)
	}
	if r := s.Root(s.Workspaces[3]); r != "orch" {
		t.Fatalf("Root of a worker's worker: %q, want orch", r)
	}
}
