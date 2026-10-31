package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestQueueScheduleCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	next := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	st := model.State{Sessions: []model.Session{{ID: "s"}}, Tasks: []model.Task{{ID: "q", SessionID: "s", NextStart: next, Repeat: 24 * time.Hour, Cmd: []string{"codex", "nightly"}}}}
	if err := Save(path, st); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || len(got.Tasks) != 1 || !got.Tasks[0].NextStart.Equal(next) || got.Tasks[0].Repeat != 24*time.Hour {
		t.Fatalf("round trip: %+v, %v", got.Tasks, err)
	}
	// Existing version-8 files have neither scheduling field.
	old := `{"format_version":8,"state":{"Sessions":[{"ID":"s"}],"Tasks":[{"ID":"q","SessionID":"s","Cmd":["codex","old task"]}]}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	if err != nil || len(got.Tasks) != 1 || !got.Tasks[0].NextStart.IsZero() || got.Tasks[0].Repeat != 0 {
		t.Fatalf("old state: %+v, %v", got.Tasks, err)
	}
}
