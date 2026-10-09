package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	want := filepath.Join(root, ".local", "state", "pitwall", "state.json")
	if runtime.GOOS == "windows" {
		cache, _ := os.UserCacheDir()
		want = filepath.Join(cache, "pitwall", "state.json")
	}
	if got := Path(); got != want {
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
			{ID: "p2", WorkspaceID: "workspace", Cmd: []string{"false"}, Exited: true, ExitCode: 1, Held: true, Provider: model.ProviderTerminal},
			{ID: "p3", WorkspaceID: "workspace"},
			{ID: "p4", WorkspaceID: "workspace", Cmd: []string{"make"}, Exited: true, ExitUnknown: true, Held: true},
		},
		Activities: []model.Activity{{PaneID: "p1", WorkspaceID: "workspace", Provider: model.ProviderClaude, SessionID: "session", State: model.StateWorking, Detail: "saving", UpdatedAt: now}},
		Stats:      map[string]model.BranchStats{"workspace": {Additions: 10, Deletions: 2, MergeStatus: model.MergeClean, Ahead: 1, Behind: 3}},
		Tasks:      []model.Task{{ID: "task", SessionID: "s", GroupID: "project", Dir: "/repo", Worktree: "fix", Base: "main", Cmd: []string{"codex", "fix it"}}},
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
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != mode) {
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
	for _, data := range []string{"{", "null", "{}", `{"format_version":1}`, `{"format_version":9,"state":{}}`, `{"format_version":1,"state":{}} {}`} {
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
		{"claude prompt", model.Pane{Provider: model.ProviderClaude, SessionID: "new", Cmd: []string{"claude", "fix the tests", "--model", "opus", "and this"}}, []string{"claude", "--model", "opus", "--resume", "new"}},
		{"claude dangling model", model.Pane{Provider: model.ProviderClaude, SessionID: "new", Cmd: []string{"claude", "--verbose", "--model"}}, []string{"claude", "--verbose", "--resume", "new"}},
		{"claude dangling before prompt", model.Pane{Provider: model.ProviderClaude, SessionID: "new", Cmd: []string{"claude", "--effort", "--", "fix it"}}, []string{"claude", "--resume", "new"}},
		{"claude kept flags", model.Pane{Provider: model.ProviderClaude, SessionID: "new", Cmd: []string{"claude", "--add-dir", "../a", "../b", "--settings=s.json", "-d", "api", "--effort", "high", "--allowed-tools", "Bash(git *)", "Edit", "--bare", "--system-prompt", "-be terse"}},
			[]string{"claude", "--add-dir", "../a", "../b", "--settings=s.json", "-d", "api", "--effort", "high", "--allowed-tools", "Bash(git *)", "Edit", "--bare", "--system-prompt", "-be terse", "--resume", "new"}},
		{"claude dropped flags", model.Pane{Provider: model.ProviderClaude, SessionID: "new", Cmd: []string{"claude", "-p", "--output-format", "json", "-w", "feature", "--session-id", "x", "--fork-session", "--bg", "--name", "n", "--plugin-flag", "v", "--model", "opus"}}, []string{"claude", "--model", "opus", "--resume", "new"}},
		{"claude mode not duplicated", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "plan", Cmd: []string{"claude", "--permission-mode", "acceptEdits", "fix it"}}, []string{"claude", "--permission-mode", "acceptEdits", "--resume", "new"}},
		{"claude bypass not duplicated", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "bypassPermissions", Cmd: []string{"claude", "--dangerously-skip-permissions", "fix it"}}, []string{"claude", "--dangerously-skip-permissions", "--resume", "new"}},
		{"claude dangling mode", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "plan", Cmd: []string{"claude", "--permission-mode"}}, []string{"claude", "--permission-mode", "plan", "--resume", "new"}},
		{"codex flags", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"/bin/codex", "-m", "gpt-5", "-c", "model_reasoning_effort=high", "--profile=work", "--search", "original prompt"}}, []string{"/bin/codex", "resume", "-m", "gpt-5", "-c", "model_reasoning_effort=high", "--profile=work", "--search", "new"}},
		{"codex resumed", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"codex", "--config", "x=true", "resume", "old", "--last", "--all", "--model=gpt-5"}}, []string{"codex", "resume", "--config", "x=true", "--model=gpt-5", "new"}},
		{"codex short attached", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"codex", "-mgpt-5", "-cx=true"}}, []string{"codex", "resume", "-mgpt-5", "-cx=true", "new"}},
		{"codex empty", model.Pane{Provider: model.ProviderCodex, SessionID: "new"}, []string{"codex", "resume", "new"}},
		{"codex prompt delimiter", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"codex", "-m", "gpt-5", "--", "--model=prompt"}}, []string{"codex", "resume", "-m", "gpt-5", "new"}},
		{"codex images", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"codex", "--image", "one.png", "two.png", "--model", "gpt-5"}}, []string{"codex", "resume", "--model", "gpt-5", "new"}},
		{"claude bypass", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "bypassPermissions"}, []string{"claude", "--dangerously-skip-permissions", "--resume", "new"}},
		{"claude accept edits", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "acceptEdits", Cmd: []string{"claude", "--model", "x"}}, []string{"claude", "--model", "x", "--permission-mode", "acceptEdits", "--resume", "new"}},
		{"claude auto mode", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "auto", Cmd: []string{"claude"}}, []string{"claude", "--permission-mode", "auto", "--resume", "new"}},
		{"claude plan", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "plan"}, []string{"claude", "--permission-mode", "plan", "--resume", "new"}},
		{"claude default mode", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "default"}, []string{"claude", "--permission-mode", "default", "--resume", "new"}},
		{"claude default mode in command", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "default", Cmd: []string{"claude", "--permission-mode", "plan"}}, []string{"claude", "--permission-mode", "plan", "--resume", "new"}},
		{"claude mode in prompt", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "plan", Cmd: []string{"claude", "--", "--permission-mode=default"}}, []string{"claude", "--permission-mode", "plan", "--resume", "new"}},
		{"codex default mode", model.Pane{Provider: model.ProviderCodex, SessionID: "new", AgentMode: "default"}, []string{"codex", "resume", "new"}},
		{"codex approval in prompt", model.Pane{Provider: model.ProviderCodex, SessionID: "new", AgentMode: "bypassPermissions", Cmd: []string{"codex", "--", "--ask-for-approval=never"}}, []string{"codex", "resume", "--dangerously-bypass-approvals-and-sandbox", "new"}},
		{"codex dropped approval flag", model.Pane{Provider: model.ProviderCodex, SessionID: "new", AgentMode: "bypassPermissions", Cmd: []string{"codex", "--search", "-a"}}, []string{"codex", "resume", "--search", "--dangerously-bypass-approvals-and-sandbox", "new"}},
		{"claude mode in command", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "bypassPermissions", Cmd: []string{"claude", "--dangerously-skip-permissions"}}, []string{"claude", "--dangerously-skip-permissions", "--resume", "new"}},
		{"claude other mode in command", model.Pane{Provider: model.ProviderClaude, SessionID: "new", AgentMode: "plan", Cmd: []string{"claude", "--permission-mode=acceptEdits"}}, []string{"claude", "--permission-mode=acceptEdits", "--resume", "new"}},
		{"codex bypass", model.Pane{Provider: model.ProviderCodex, SessionID: "new", AgentMode: "bypassPermissions"}, []string{"codex", "resume", "--dangerously-bypass-approvals-and-sandbox", "new"}},
		{"codex bypass in command", model.Pane{Provider: model.ProviderCodex, SessionID: "new", AgentMode: "bypassPermissions", Cmd: []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}}, []string{"codex", "resume", "--dangerously-bypass-approvals-and-sandbox", "new"}},
		{"codex sandbox in command", model.Pane{Provider: model.ProviderCodex, SessionID: "new", AgentMode: "bypassPermissions", Cmd: []string{"codex", "-sread-only"}}, []string{"codex", "resume", "-sread-only", "new"}},
		{"codex plan", model.Pane{Provider: model.ProviderCodex, SessionID: "new", AgentMode: "plan"}, []string{"codex", "resume", "new"}},
		{"pi mode", model.Pane{Provider: model.ProviderPi, SessionID: "new", AgentMode: "bypassPermissions"}, []string{"pi", "--session", "new"}},
		{"terminal", model.Pane{Provider: model.ProviderTerminal, SessionID: "new", Cmd: []string{"sh", "-l"}}, []string{"sh", "-l"}},
		{"pi flags", model.Pane{Provider: model.ProviderPi, SessionID: "new", Cmd: []string{"/bin/pi", "--model", "sonnet:high", "-e", "./x.ts", "--no-skills", "--thinking=low", "-c", "--session", "old", "--name", "n", "-p", "fix it", "--plugin-flag", "--", "-x"}},
			[]string{"/bin/pi", "--model", "sonnet:high", "-e", "./x.ts", "--no-skills", "--thinking=low", "--session", "new"}},
		{"pi dangling value", model.Pane{Provider: model.ProviderPi, SessionID: "new", Cmd: []string{"pi", "--offline", "--model"}}, []string{"pi", "--offline", "--session", "new"}},
		{"pi dangling before prompt end", model.Pane{Provider: model.ProviderPi, SessionID: "new", Cmd: []string{"pi", "--thinking", "high", "--model", "--", "x"}}, []string{"pi", "--thinking", "high", "--session", "new"}},
		{"codex dangling value", model.Pane{Provider: model.ProviderCodex, SessionID: "new", Cmd: []string{"codex", "--search", "-m"}}, []string{"codex", "resume", "--search", "new"}},
		{"pi wrapper", model.Pane{Provider: model.ProviderPi, SessionID: "new", Cmd: []string{"mise", "x", "--", "pi"}}, []string{"pi", "--session", "new"}},
		{"pi no session", model.Pane{Provider: model.ProviderPi, Cmd: []string{"pi", "--no-session"}}, []string{"pi", "--no-session"}},
		{"no session", model.Pane{Provider: model.ProviderClaude, Cmd: []string{"claude", "--continue"}}, []string{"claude", "--continue"}},
		{"gemini flags", model.Pane{Provider: model.ProviderGemini, SessionID: "new", Cmd: []string{"/bin/gemini", "-m", "gemini-3-pro", "--yolo", "--include-directories", "../a", "../b", "-r", "old", "-p", "fix it", "-o", "json", "--approval-mode=auto_edit", "query"}},
			[]string{"/bin/gemini", "-m", "gemini-3-pro", "--yolo", "--include-directories", "../a", "../b", "--approval-mode=auto_edit", "--resume", "new"}},
		{"gemini empty", model.Pane{Provider: model.ProviderGemini, SessionID: "new"}, []string{"gemini", "--resume", "new"}},
		{"opencode flags", model.Pane{Provider: model.ProviderOpenCode, SessionID: "ses_new", Cmd: []string{"opencode", "--agent", "plan", "-m", "anthropic/x", "-c", "--auto", "--prompt", "fix it", "-s", "ses_old", "--port", "4096"}},
			[]string{"opencode", "--agent", "plan", "--auto", "--port", "4096", "--session", "ses_new"}},
		{"cursor", model.Pane{Provider: model.ProviderCursor, SessionID: "c1", Cmd: []string{"/home/u/.local/bin/agent", "--model", "x", "fix it"}}, []string{"/home/u/.local/bin/agent", "--resume", "c1"}},
		{"cursor empty", model.Pane{Provider: model.ProviderCursor, SessionID: "c1"}, []string{"cursor-agent", "--resume", "c1"}},
		{"aider has no resume", model.Pane{Provider: model.ProviderAider, SessionID: "x", Cmd: []string{"aider"}}, []string{"aider"}},
		{"unknown provider", model.Pane{SessionID: "new", Cmd: []string{"custom"}}, []string{"custom"}},
		{"empty", model.Pane{}, nil},
	}
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderPi} {
		want := []string{string(provider), "--resume", "new"}
		switch provider {
		case model.ProviderCodex:
			want = []string{"codex", "resume", "new"}
		case model.ProviderPi:
			want = []string{"pi", "--session", "new"}
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
			{"ID":"made","ProjectID":"p","Path":"/r/.worktrees/feat","Layout":{"Pane":"p1"}},
			{"ID":"main","ProjectID":"p","Path":"/r","Layout":{"Pane":"p2"}},
			{"ID":"grouped","ProjectID":"g","Path":"/r/.worktrees/x","Layout":{"Pane":"p3"}}]}}`
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
	// b had no panes, so the version 5 split drops it.
	if len(s.Workspaces) != 1 {
		t.Fatalf("migrated %+v", s.Workspaces)
	}
	a := s.Workspaces[0]
	if !a.Detached || len(a.Tabs) != 1 || a.ActiveTab != a.Tabs[0].ID || a.Tabs[0].ID == "" {
		t.Fatalf("migrated %+v", a)
	}
	if got := layout.Panes(a.Tabs[0].Layout); !reflect.DeepEqual(got, []string{"p1", "p2"}) || a.Tabs[0].Layout.Dir != layout.Vertical {
		t.Fatalf("layout: %v", got)
	}
}

func TestLoadMigratesVersion3NameSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v3 := `{"format_version":3,"state":{"Workspaces":[
		{"ID":"a","Name":"rustic-swan","Tabs":[{"ID":"t","Layout":{"Pane":"p"}}]},
		{"ID":"b","Name":"swift-otter-104","Tabs":[{"ID":"t","Layout":{"Pane":"x"}}]},
		{"ID":"c","Name":"workspace-2","Tabs":[{"ID":"t","Layout":{"Pane":"x"}}]},
		{"ID":"d","Name":"repo","Path":"/src/repo","Tabs":[{"ID":"t","Layout":{"Pane":"x"}}]},
		{"ID":"e","Name":"fix-auth","Tabs":[{"ID":"t","Layout":{"Pane":"x"}}]},
		{"ID":"f","Name":"swan-rustic","Tabs":[{"ID":"t","Layout":{"Pane":"x"}}]}],
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
		got[w.ID] = w.NameSet
		if w.Label != "" {
			t.Fatalf("label %q from a version 3 file", w.Label)
		}
	}
	want := map[string]bool{"a": false, "b": false, "c": false, "d": false, "e": true, "f": true}
	if !reflect.DeepEqual(got, want) || s.Panes[0].Prompt != "" {
		t.Fatalf("NameSet = %v, want %v; panes %+v", got, want, s.Panes)
	}
}

// A version 4 workspace with three tabs becomes three workspaces in its
// place, in its group; tab names become chosen names and a workspace without
// tabs goes.
func TestLoadSplitsVersion4Tabs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v4 := `{"format_version":4,"state":{"Workspaces":[
		{"ID":"before","Name":"brave-otter","Tabs":[{"ID":"t0","Layout":{"Pane":"p0"}}],"ActiveTab":"t0"},
		{"ID":"w","ProjectID":"g","Name":"swift-otter","Path":"/src","WorktreeRoot":"/r","Detached":true,"ActiveTab":"t3","Tabs":[
			{"ID":"t1","Layout":{"Children":[{"Pane":"p1"},{"Pane":"p2"}]}},
			{"ID":"t2","Name":"build","Layout":{"Pane":"p3"}},
			{"ID":"t3","Name":"brave-otter","Layout":{"Pane":"p4"}}]},
		{"ID":"empty","Name":"calm-cat"},
		{"ID":"after","Name":"fix-auth","NameSet":true,"Tabs":[{"ID":"t5","Name":"logs","Layout":{"Pane":"p5"}}],"ActiveTab":"t5"}],
		"Panes":[{"ID":"p0","WorkspaceID":"before"},{"ID":"p1","WorkspaceID":"w"},{"ID":"p2","WorkspaceID":"w"},
			{"ID":"p3","WorkspaceID":"w"},{"ID":"p4","WorkspaceID":"w"},{"ID":"p5","WorkspaceID":"after"},{"ID":"orphan","WorkspaceID":"empty"}]}}`
	if err := os.WriteFile(path, []byte(v4), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range s.Workspaces {
		names = append(names, w.Name)
		if len(w.Tabs) != 1 || w.ActiveTab != w.Tabs[0].ID || w.Tabs[0].Name != "" {
			t.Fatalf("tabs of %+v", w)
		}
	}
	// Generated names are cleared by the version 6 migration.
	if len(s.Workspaces) != 5 || names[0] != "" || names[1] != "" || names[2] != "build" ||
		names[3] != "brave-otter-2" || names[4] != "logs" {
		t.Fatalf("names %v", names)
	}
	first, build, third := s.Workspaces[1], s.Workspaces[2], s.Workspaces[3]
	if first.ID != "w" || first.NameSet || first.WorktreeRoot != "/r" || first.Tabs[0].ID != "t1" {
		t.Fatalf("first %+v", first)
	}
	for _, w := range []model.Workspace{build, third} {
		if w.ID == "w" || w.ID == "" || !w.NameSet || w.ProjectID != "g" || w.Path != "/src" || !w.Detached || w.WorktreeRoot != "" {
			t.Fatalf("split %+v", w)
		}
	}
	if build.ID == third.ID || build.Tabs[0].ID != "t2" || third.Tabs[0].ID != "t3" || !s.Workspaces[4].NameSet {
		t.Fatalf("split ids %+v %+v", build, third)
	}
	owner := map[string]string{}
	for _, p := range s.Panes {
		owner[p.ID] = p.WorkspaceID
	}
	want := map[string]string{"p0": "before", "p1": "w", "p2": "w", "p3": build.ID, "p4": third.ID, "p5": "after"}
	if !reflect.DeepEqual(owner, want) {
		t.Fatalf("pane owners %v, want %v", owner, want)
	}
}

// A workspace past its first tab with no tab name gets a fresh generated name.
func TestSplitTabsGeneratesNames(t *testing.T) {
	s := &model.State{Workspaces: []model.Workspace{{ID: "w", Name: "swift-otter", Tabs: []model.Tab{
		{ID: "a", Layout: layout.Leaf("p1")}, {ID: "b", Layout: layout.Leaf("p2")}}}}}
	splitTabs(s)
	if len(s.Workspaces) != 2 || s.Workspaces[0].Name != "swift-otter" {
		t.Fatalf("%+v", s.Workspaces)
	}
	if w := s.Workspaces[1]; w.NameSet || w.Name == "swift-otter" || !model.IsSessionName(w.Name) {
		t.Fatalf("second %+v", w)
	}
}

// A version 5 file gets State.Order from the old implied order (ungrouped
// tabs, then groups) and loses its generated names; chosen ones stay.
func TestLoadMigratesVersion5Order(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v5 := `{"format_version":5,"state":{"Projects":[{"ID":"g1"},{"ID":"g2"}],"Workspaces":[
		{"ID":"a","ProjectID":"g2","Name":"swift-otter"},
		{"ID":"u1","Name":"brave-otter"},
		{"ID":"u2","Name":"api","NameSet":true}]}}`
	if err := os.WriteFile(path, []byte(v5), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.TopOrder(s.Sessions[0].ID); !reflect.DeepEqual(got, []string{"u1", "u2", "g1", "g2"}) {
		t.Fatalf("order %v", got)
	}
	if s.Workspaces[0].Name != "" || s.Workspaces[1].Name != "" || s.Workspaces[2].Name != "api" || !s.Workspaces[2].NameSet {
		t.Fatalf("names %+v", s.Workspaces)
	}
}

// A version 6 file puts every tab and group into one session named "main",
// which keeps the file's order and was last used when its newest tab was.
func TestLoadMigratesVersion6Sessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v6 := `{"format_version":6,"state":{"Order":["g1","u1"],"Projects":[{"ID":"g1"}],"Workspaces":[
		{"ID":"a","ProjectID":"g1","UpdatedAt":"2026-01-02T00:00:00Z"},
		{"ID":"u1","UpdatedAt":"2026-01-03T00:00:00Z"}]}}`
	if err := os.WriteFile(path, []byte(v6), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sessions) != 1 || s.Sessions[0].Name != "main" || s.Sessions[0].ID == "" {
		t.Fatalf("sessions %+v", s.Sessions)
	}
	main := s.Sessions[0]
	if !reflect.DeepEqual(main.Order, []string{"g1", "u1"}) || main.UsedAt.Day() != 3 {
		t.Fatalf("main %+v", main)
	}
	if s.Projects[0].SessionID != main.ID || s.Workspaces[0].SessionID != main.ID || s.Workspaces[1].SessionID != main.ID {
		t.Fatalf("not adopted: %+v %+v", s.Projects, s.Workspaces)
	}
	// Saved and loaded again, nothing changes.
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil || !reflect.DeepEqual(again.Sessions, s.Sessions) {
		t.Fatalf("round trip %+v, %v", again.Sessions, err)
	}

	// An empty older file gets no session; the first window makes one.
	if err := os.WriteFile(path, []byte(`{"format_version":6,"state":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Load(path); err != nil || len(s.Sessions) != 0 {
		t.Fatalf("empty: %+v %v", s.Sessions, err)
	}
}

// A version 7 file marks held the panes `pitwall new -- cmd` opened: those
// with a command and no agent session to resume. A version 8 file is taken
// as saved.
func TestLoadMigratesVersion7Held(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	panes := `[
		{"ID":"shell"},
		{"ID":"make","Cmd":["make"]},
		{"ID":"done","Cmd":["false"],"Exited":true,"ExitCode":1},
		{"ID":"claude","Cmd":["claude","fix it"],"Provider":"claude","SessionID":"s"},
		{"ID":"unseen","Cmd":["codex","fix it"]},
		{"ID":"terminal","Cmd":["make"],"Provider":"terminal","SessionID":"s"}]`
	for version, want := range map[int][]string{7: {"make", "done", "unseen", "terminal"}, 8: nil} {
		data := fmt.Sprintf(`{"format_version":%d,"state":{"Panes":%s}}`, version, panes)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		var held []string
		for _, p := range s.Panes {
			if p.Held {
				held = append(held, p.ID)
			}
		}
		if !reflect.DeepEqual(held, want) {
			t.Fatalf("version %d: held %v, want %v", version, held, want)
		}
	}
}

// badFiles is the names of the files Open set aside next to path.
func badFiles(t *testing.T, path string) []string {
	t.Helper()
	names, err := filepath.Glob(path + ".bad-*")
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// TestOpenSetsAside checks a file Load refuses is moved aside with its bytes,
// never over an earlier one, and the daemon gets an empty state with a
// notice that says why and where the file is, under ~ in the home directory.
func TestOpenSetsAside(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	path := filepath.Join(home, "state.json")
	full := model.State{Sessions: []model.Session{{ID: "s", Name: "main"}}, Workspaces: []model.Workspace{{ID: "w", SessionID: "s"}}}
	if err := Save(path, full); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, data, why string }{
		{"corrupt", "{", "damaged"},
		{"truncated", string(saved[:len(saved)/2]), "damaged"},
		{"newer", `{"format_version":99,"state":{}}`, "newer pitwall"},
		{"again in the same second", "{", "damaged"},
	}
	for i, c := range cases {
		if err := os.WriteFile(path, []byte(c.data), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Open(path)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		bad := badFiles(t, path)
		if len(bad) != i+1 {
			t.Fatalf("%s: set-aside files %v, want %d", c.name, bad, i+1)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s: state.json still there: %v", c.name, err)
		}
		kept := ""
		for _, b := range bad {
			if strings.Contains(s.Notice, " ~"+string(filepath.Separator)+filepath.Base(b)+".") {
				kept = b
			}
		}
		if got, err := os.ReadFile(kept); err != nil || string(got) != c.data {
			t.Fatalf("%s: kept %q = %q, %v; notice %q", c.name, kept, got, err, s.Notice)
		}
		if !strings.Contains(s.Notice, c.why) || len(s.Workspaces) != 0 {
			t.Fatalf("%s: state %#v", c.name, s)
		}
	}
}

// TestLoadKeepsPrev checks a migration copies the original to state.json.prev,
// replacing an earlier one, and a current file writes none.
func TestLoadKeepsPrev(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path+".prev", []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	v7 := `{"format_version":7,"state":{"Sessions":[{"ID":"s","Name":"main"}],"Workspaces":[{"ID":"w","SessionID":"s"}]}}`
	if err := os.WriteFile(path, []byte(v7), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path + ".prev"); err != nil || string(got) != v7 {
		t.Fatalf("prev = %q, %v", got, err)
	}
	if err := os.Remove(path + ".prev"); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".prev"); !os.IsNotExist(err) {
		t.Fatalf("current file wrote a prev: %v", err)
	}
}

// TestOpenRestores checks a set-aside file that loads now, as one a newer
// pitwall saved does after an update, joins the state once, and one that
// still does not stays.
func TestOpenRestores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := model.State{Sessions: []model.Session{{ID: "s1", Name: "main"}}, Workspaces: []model.Workspace{{ID: "w1", SessionID: "s1"}}, Panes: []model.Pane{{ID: "p1", WorkspaceID: "w1"}}}
	old := model.State{Sessions: []model.Session{{ID: "s2", Name: "main"}}, Workspaces: []model.Workspace{{ID: "w2", SessionID: "s2"}, {ID: "w3", SessionID: "s2"}}, Panes: []model.Pane{{ID: "p2", WorkspaceID: "w2"}, {ID: "p3", WorkspaceID: "w3"}}}
	if err := Save(path+".bad-20261001-120000", old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".bad-20261002-120000", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, now); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Workspaces) != 3 || len(s.Panes) != 3 || len(s.Sessions) != 2 || s.Sessions[1].Name != "main-2" {
		t.Fatalf("merged state %#v", s)
	}
	if !strings.Contains(s.Notice, "Restored 2 saved tabs") {
		t.Fatalf("notice %q", s.Notice)
	}
	if bad := badFiles(t, path); len(bad) != 1 || !strings.HasSuffix(bad[0], "20261002-120000") {
		t.Fatalf("set-aside files left %v", bad)
	}
	if _, err := os.Stat(path + ".restored-20261001-120000"); err != nil {
		t.Fatal(err)
	}
	// The merged state is on disk, so the next start restores nothing.
	s, err = Open(path)
	if err != nil || len(s.Workspaces) != 3 || s.Notice != "" {
		t.Fatalf("second open: %#v, %v", s, err)
	}
	// A file already merged before a crash adds nothing again.
	if n := merge(&s, old); n != 0 || len(s.Workspaces) != 3 {
		t.Fatalf("merge again added %d", n)
	}
}
