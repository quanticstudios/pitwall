package main

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/store"
)

// TestRunSnapshots checks the list names each snapshot with its tabs, and a
// restore with no daemon running puts it back and keeps the state it replaced.
func TestRunSnapshots(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("PITWALL_SOCKET", "")
	path := store.Path()
	two := model.State{Sessions: []model.Session{{ID: "s", Name: "main"}}, Workspaces: []model.Workspace{{ID: "a", SessionID: "s"}, {ID: "b", SessionID: "s"}}}
	if err := store.Save(path, two); err != nil {
		t.Fatal(err)
	}
	two.Workspaces = two.Workspaces[:1]
	if err := store.Save(path, two); err != nil {
		t.Fatal(err)
	}
	names := store.ListSnapshots(path)
	var out strings.Builder
	if err := runSnapshots(nil, &out); err != nil || len(names) != 1 || !strings.Contains(out.String(), names[0]+"  2 tabs") {
		t.Fatalf("list %q, %v; snapshots %v", out.String(), err, names)
	}
	if err := runSnapshots([]string{"restore", "state-missing.json"}, &out); err == nil {
		t.Fatal("restored a snapshot that does not exist")
	}
	if err := runSnapshots([]string{"restore", names[0]}, &out); err != nil {
		t.Fatal(err)
	}
	if s, err := store.Load(path); err != nil || len(s.Workspaces) != 2 {
		t.Fatalf("restored state has %d tabs, %v", len(s.Workspaces), err)
	}
	if n := len(store.ListSnapshots(path)); n != 2 {
		t.Fatalf("%d snapshots after the restore, want 2: the replaced state kept", n)
	}
}
