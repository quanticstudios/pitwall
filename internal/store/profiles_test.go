package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestProfileStateCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := model.State{Sessions: []model.Session{{ID: "s"}}, Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Profile: "work", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "p"}}}, ActiveTab: "t"}}, Panes: []model.Pane{{ID: "p", WorkspaceID: "w", Env: []string{"CODEX_HOME=/work/codex", "PITWALL_PROFILE=work"}}}, Tasks: []model.Task{{ID: "q", SessionID: "s", Profile: "work", Cmd: []string{"codex", "fix it"}}}}
	if err := Save(path, st); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.Workspaces[0].Profile != "work" || got.Tasks[0].Profile != "work" || got.Panes[0].Env[0] != "CODEX_HOME=/work/codex" {
		t.Fatalf("profile round trip %+v %v", got, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Existing format-8 files have no Profile key. No format bump is needed.
	data = bytes.ReplaceAll(data, []byte(`"Profile": "work",`), nil)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	if err != nil || got.Workspaces[0].Profile != "" || got.Tasks[0].Profile != "" {
		t.Fatalf("old state %+v %v", got, err)
	}
}
