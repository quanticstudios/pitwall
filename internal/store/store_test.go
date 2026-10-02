package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	if got, want := Path(), filepath.Join(root, "pitwall", "state.json"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", root)
	if got, want := Path(), filepath.Join(root, ".local", "state", "pitwall", "state.json"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestSaveLoad(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	want := model.State{
		Version:  42,
		Projects: []model.Project{{ID: "project", Name: "pitwall", Root: "/repo", Kind: model.ProjectGit, Color: "blue", Icon: "git"}},
		Workspaces: []model.Workspace{{ID: "workspace", ProjectID: "project", Name: "store", Branch: "track/store", Path: "/repo/store", Archived: true, UpdatedAt: now,
			Layout: &layout.Node{Dir: layout.Horizontal, Ratios: []float64{0.4, 0.6}, Children: []*layout.Node{
				{Pane: "p1"}, {Dir: layout.Vertical, Ratios: []float64{0.5, 0.5}, Children: []*layout.Node{{Pane: "p2"}, {Pane: "p3"}}},
			}},
		}},
		Panes: []model.Pane{
			{ID: "p1", WorkspaceID: "workspace", Cmd: []string{"claude", "--model", "sonnet"}, Cwd: "/repo/store", Title: "Claude", Provider: model.ProviderClaude, SessionID: "session"},
			{ID: "p2", WorkspaceID: "workspace", Cmd: []string{"false"}, Exited: true, ExitCode: 1, Provider: model.ProviderTerminal},
			{ID: "p3", WorkspaceID: "workspace"},
		},
		Activities: []model.Activity{{PaneID: "p1", WorkspaceID: "workspace", Provider: model.ProviderClaude, SessionID: "session", State: model.StateWorking, Detail: "saving", UpdatedAt: now}},
		Stats:      map[string]model.BranchStats{"workspace": {Additions: 10, Deletions: 2, MergeStatus: model.MergeClean, Ahead: 1, Behind: 3}},
	}
	// Overwrite an existing snapshot as well as creating the first one.
	for _, state := range []model.State{{}, want} {
		if err := Save(Path(), state); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil || saved.FormatVersion != formatVersion {
		t.Fatalf("format version: %#v, %v", saved, err)
	}
	if !bytes.Contains(data, []byte("\n  \"format_version\"")) {
		t.Fatal("snapshot is not indented")
	}
	entries, err := os.ReadDir(filepath.Dir(Path()))
	if err != nil || len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("unexpected files after save: %v, %v", entries, err)
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(Path()): 0700, Path(): 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("permissions for %s: %v, %v", path, info, err)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	got, err := Load(Path())
	if err != nil || !reflect.DeepEqual(got, model.State{}) {
		t.Fatalf("missing file = %#v, %v", got, err)
	}
}

func TestLoadCorrupt(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(Path()), 0700); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"{", "null", "{}", `{"format_version":1}`, `{"format_version":2,"state":{}}`, `{"format_version":1,"state":{}} {}`} {
		t.Run(data, func(t *testing.T) {
			if err := os.WriteFile(Path(), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(Path()); err == nil {
				t.Fatal("corrupt file returned no error")
			}
			got, err := os.ReadFile(Path())
			if err != nil || string(got) != data {
				t.Fatalf("corrupt file changed: %q, %v", got, err)
			}
		})
	}
}

func TestSaveFailureCleansTemp(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(Path(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Save(Path(), model.State{}); err == nil {
		t.Fatal("rename onto a directory returned no error")
	}
	entries, err := os.ReadDir(filepath.Dir(Path()))
	if err != nil || len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("unexpected files after failed save: %v, %v", entries, err)
	}
}

func TestRestoreCmd(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tests := []struct {
		name string
		pane model.Pane
		want []string
	}{
		{"claude resume", model.Pane{Provider: model.ProviderClaude, SessionID: "new", Cmd: []string{"/bin/claude", "--resume", "old", "--model", "sonnet", "-c"}}, []string{"/bin/claude", "--model", "sonnet", "--resume", "new"}},
		{"claude short resume", model.Pane{Provider: model.ProviderClaude, SessionID: "new", Cmd: []string{"claude", "-r", "old", "--continue", "--verbose"}}, []string{"claude", "--verbose", "--resume", "new"}},
		{"claude attached", model.Pane{Provider: model.ProviderClaude, SessionID: "new", Cmd: []string{"claude", "--resume=old", "--model=sonnet"}}, []string{"claude", "--model=sonnet", "--resume", "new"}},
		{"claude optional resume", model.Pane{Provider: model.ProviderClaude, SessionID: "new", Cmd: []string{"claude", "-r", "--model", "sonnet"}}, []string{"claude", "--model", "sonnet", "--resume", "new"}},
		{"claude empty", model.Pane{Provider: model.ProviderClaude, SessionID: "new"}, []string{"claude", "--resume", "new"}},
		{"codex flags", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"/bin/codex", "-m", "gpt-5", "-c", "model_reasoning_effort=high", "--profile=work", "--search", "original prompt"}}, []string{"/bin/codex", "resume", "-m", "gpt-5", "-c", "model_reasoning_effort=high", "--profile=work", "--search", "new"}},
		{"codex resumed", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"codex", "--config", "x=true", "resume", "old", "--last", "--all", "--model=gpt-5"}}, []string{"codex", "resume", "--config", "x=true", "--model=gpt-5", "new"}},
		{"codex short attached", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"codex", "-mgpt-5", "-cx=true"}}, []string{"codex", "resume", "-mgpt-5", "-cx=true", "new"}},
		{"codex empty", model.Pane{Provider: model.ProviderCodex, SessionID: "new"}, []string{"codex", "resume", "new"}},
		{"codex prompt delimiter", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"codex", "-m", "gpt-5", "--", "--model=prompt"}}, []string{"codex", "resume", "-m", "gpt-5", "new"}},
		{"codex images", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"codex", "--image", "one.png", "two.png", "--model", "gpt-5"}}, []string{"codex", "resume", "--model", "gpt-5", "new"}},
		{"terminal", model.Pane{Provider: model.ProviderTerminal, SessionID: "new", Cmd: []string{"sh", "-l"}}, []string{"sh", "-l"}},
		{"no session", model.Pane{Provider: model.ProviderClaude, Cmd: []string{"claude", "--continue"}}, []string{"claude", "--continue"}},
		{"unknown provider", model.Pane{SessionID: "new", Cmd: []string{"custom"}}, []string{"custom"}},
		{"empty", model.Pane{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]string(nil), tt.pane.Cmd...)
			got := RestoreCmd(tt.pane)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RestoreCmd() = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(tt.pane.Cmd, before) {
				t.Fatal("RestoreCmd modified the original command")
			}
		})
	}
}
