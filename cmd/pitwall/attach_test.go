package main

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// A new window opens on the named session, raising its window if it has
// one; without a name, on the most recent session no window shows.
func TestGUITarget(t *testing.T) {
	now := time.Now()
	st := model.State{Sessions: []model.Session{
		{ID: "a", Name: "swift-otter", UsedAt: now.Add(-time.Hour)},
		{ID: "b", Name: "calm-heron", UsedAt: now, Windows: 1},
	}}
	for _, tc := range []struct {
		name, want string
		raise      bool
	}{{"", "a", false}, {"swift-otter", "a", false}, {"calm-heron", "b", true}, {"absent", "", false}} {
		if s, raise := guiTarget(st, tc.name); s.ID != tc.want || raise != tc.raise {
			t.Errorf("%q: %s raise %v, want %s raise %v", tc.name, s.ID, raise, tc.want, tc.raise)
		}
	}
}
