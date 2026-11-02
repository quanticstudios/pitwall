package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestInboxStateCompatibility keeps a tab's settle and snooze across a
// save, and leaves them out of the file for a tab with neither, so files
// stay readable by a pitwall from before the inbox.
func TestInboxStateCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	st := model.State{Sessions: []model.Session{{ID: "s"}}, Workspaces: []model.Workspace{
		{ID: "a", SessionID: "s", Settled: at, SnoozedUntil: at.Add(time.Hour), SnoozedAt: at},
		{ID: "b", SessionID: "s"},
	}}
	if err := Save(path, st); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := got.Workspaces[0], got.Workspaces[1]
	if !a.Settled.Equal(at) || !a.SnoozedUntil.Equal(at.Add(time.Hour)) || !a.SnoozedAt.Equal(at) || !b.Settled.IsZero() || !b.SnoozedUntil.IsZero() {
		t.Fatalf("round trip %+v %+v", a, b)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"Settled"`); n != 1 {
		t.Errorf("Settled written %d times, want once: a zero time stays out", n)
	}
}
