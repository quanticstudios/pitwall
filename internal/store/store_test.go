package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		Workspaces: []model.Workspace{{ID: "workspace", ProjectID: "project", Name: "store", NameSet: true, Label: "make", Branch: "track/store", Path: "/repo/store", RepoRoot: "/repo", Detached: true, UpdatedAt: now,
			Tabs: []model.Tab{{ID: "t1", Name: "build", Title: "make", Layout: &layout.Node{Dir: layout.Horizontal, Ratios: []float64{0.4, 0.6}, Children: []*layout.Node{
				{Pane: "p1"}, {Dir: layout.Vertical, Ratios: []float64{0.5, 0.5}, Children: []*layout.Node{{Pane: "p2"}, {Pane: "p3"}}},
			}}}},
			ActiveTab: "t1",
		}},
		Panes: []model.Pane{
			{ID: "p1", WorkspaceID: "workspace", Cmd: []string{"claude", "--model", "sonnet"}, Cwd: "/repo/store", Title: "Claude", Provider: model.ProviderClaude, SessionID: "session", Prompt: "fix the store"},
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
	for _, data := range []string{"{", "null", "{}", `{"format_version":1}`, `{"format_version":5,"state":{}}`, `{"format_version":1,"state":{}} {}`} {
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
	type testCase struct {
		name string
		pane model.Pane
		want []string
	}
	tests := []testCase{
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
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		want := []string{string(provider), "--resume", "new"}
		if provider == model.ProviderCodex {
			want = []string{"codex", "resume", "new"}
		}
		for _, cmd := range [][]string{
			{"sh", "-c", string(provider) + "; rm -rf build; make deploy"},
			{"sh", "-lc", string(provider) + "; rm -rf build; make deploy"},
			{"/bin/bash", "-lc", string(provider) + " --model old; make deploy"},
			{"zsh", "-c", string(provider)},
			{"fish", "-c", string(provider)},
			{"env", "MODEL=old", string(provider), "--model", "old"},
			{"/usr/bin/env", string(provider), "--model", "old"},
			{"node", "/opt/bin/" + string(provider), "--model", "old"},
			{"custom", "-c", string(provider)},
			{"claude-wrapper", "--model", "old"},
			{"codex-wrapper", "--model", "old"},
		} {
			tests = append(tests, testCase{string(provider) + " via " + strings.Join(cmd, " "), model.Pane{Provider: provider, SessionID: "new", Cmd: cmd}, want})
		}
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

func TestLoadMigratesVersion1Worktrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v1 := `{"format_version":1,"state":{
		"Projects":[{"ID":"p","Root":"/r","Kind":"git"},{"ID":"g","Kind":"group"}],
		"Workspaces":[
			{"ID":"made","ProjectID":"p","Path":"/r/.worktrees/feat"},
			{"ID":"main","ProjectID":"p","Path":"/r"},
			{"ID":"grouped","ProjectID":"g","Path":"/r/.worktrees/x"}]}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"made": "/r", "main": "", "grouped": ""}
	for _, w := range s.Workspaces {
		if w.WorktreeRoot != want[w.ID] {
			t.Errorf("%s: WorktreeRoot = %q, want %q", w.ID, w.WorktreeRoot, want[w.ID])
		}
	}
	// Saved as the current version, an unowned worktree stays unowned on the next load.
	s.Workspaces[0].WorktreeRoot = ""
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if s, err = Load(path); err != nil {
		t.Fatal(err)
	}
	if s.Workspaces[0].WorktreeRoot != "" {
		t.Fatal("version 2 load re-inferred worktree ownership")
	}
}

func TestLoadMigratesVersion2Tabs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v2 := `{"format_version":2,"state":{"Workspaces":[
		{"ID":"a","Archived":true,"Layout":{"Dir":1,"Ratios":[0.5,0.5],"Children":[{"Pane":"p1"},{"Pane":"p2"}]}},
		{"ID":"b"}]}}`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := s.Workspaces[0], s.Workspaces[1]
	if !a.Detached || b.Detached || len(a.Tabs) != 1 || len(b.Tabs) != 1 {
		t.Fatalf("migrated %+v %+v", a, b)
	}
	if a.ActiveTab != a.Tabs[0].ID || a.Tabs[0].ID == "" || a.Tabs[0].ID == b.Tabs[0].ID || b.ActiveTab != b.Tabs[0].ID {
		t.Fatalf("tab ids: %+v %+v", a, b)
	}
	if got := layout.Panes(a.Tabs[0].Layout); !reflect.DeepEqual(got, []string{"p1", "p2"}) || a.Tabs[0].Layout.Dir != layout.Vertical || b.Tabs[0].Layout != nil {
		t.Fatalf("layouts: %v %+v", got, b.Tabs[0])
	}
}

func TestLoadMigratesVersion3NameSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v3 := `{"format_version":3,"state":{"Workspaces":[
		{"ID":"a","Name":"rustic-swan"},{"ID":"b","Name":"swift-otter-104"},{"ID":"c","Name":"workspace-2"},
		{"ID":"d","Name":"repo","Path":"/src/repo"},{"ID":"e","Name":"fix-auth"},{"ID":"f","Name":"swan-rustic"}],
		"Panes":[{"ID":"p","Title":"cap"}]}}`
	if err := os.WriteFile(path, []byte(v3), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, w := range s.Workspaces {
		got[w.Name] = w.NameSet
		if w.Label != "" {
			t.Fatalf("label %q from a version 3 file", w.Label)
		}
	}
	want := map[string]bool{"rustic-swan": false, "swift-otter-104": false, "workspace-2": false, "repo": false, "fix-auth": true, "swan-rustic": true}
	if !reflect.DeepEqual(got, want) || s.Panes[0].Prompt != "" {
		t.Fatalf("NameSet = %v, want %v; panes %+v", got, want, s.Panes)
	}
}
