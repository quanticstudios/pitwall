package model

import (
	"slices"
	"testing"
)

func TestTopOrder(t *testing.T) {
	s := State{
		Projects: []Project{{ID: "g1"}, {ID: "g2"}},
		Workspaces: []Workspace{
			{ID: "a", ProjectID: "g1"}, {ID: "u1"}, {ID: "b", ProjectID: "g1"},
			{ID: "u2"}, {ID: "orphan", ProjectID: "gone"}, {ID: "c", ProjectID: "g2"},
		},
	}
	// No Order: the old implied order, ungrouped tabs before groups.
	if got := s.TopOrder(); !slices.Equal(got, []string{"u1", "u2", "orphan", "g1", "g2"}) {
		t.Fatalf("implied: %v", got)
	}
	// Stale: unknown, repeated and grouped IDs drop out; missing ones follow.
	s.Order = []string{"g2", "zzz", "u2", "a", "g2"}
	if got := s.TopOrder(); !slices.Equal(got, []string{"g2", "u2", "u1", "orphan", "g1"}) {
		t.Fatalf("stale: %v", got)
	}
	var ids []string
	for _, w := range s.Ordered() {
		ids = append(ids, w.ID)
	}
	if !slices.Equal(ids, []string{"c", "u2", "u1", "orphan", "a", "b"}) {
		t.Fatalf("Ordered: %v", ids)
	}
	s.PlaceTop("g1", "g2")
	s.PlaceTopAfter("u1", "g1")
	s.PlaceTop("orphan", "")
	if got := s.TopOrder(); !slices.Equal(got, []string{"g1", "u1", "g2", "u2", "orphan"}) {
		t.Fatalf("placed: %v", got)
	}
}
