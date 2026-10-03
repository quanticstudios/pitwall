// Package store saves daemon state to disk and plans how to bring panes back
// after a restart.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// formatVersion 6 added State.Order and dropped generated tab names;
// formatVersion 5 made each tab a workspace of its own; version 4 added Workspace.NameSet and Label and Pane.Prompt; version
// 3 moved Workspace.Layout into Tabs and renamed Archived to Detached;
// version 2 added Workspace.WorktreeRoot. Older files are migrated once on
// load.
const formatVersion = 6

type snapshot struct {
	FormatVersion int          `json:"format_version"`
	State         *model.State `json:"state"`
}

// Path is state.json in config.StateDir.
func Path() string { return filepath.Join(config.StateDir(), "state.json") }

// Save writes atomically (temp file + rename).
func Save(path string, s model.State) error {
	data, err := json.MarshalIndent(snapshot{formatVersion, &s}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Load returns an empty state and nil when the file does not exist.
func Load(path string) (model.State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return model.State{}, nil
	}
	if err != nil {
		return model.State{}, err
	}
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return model.State{}, fmt.Errorf("load state: %w", err)
	}
	if saved.FormatVersion < 1 || saved.FormatVersion > formatVersion {
		return model.State{}, fmt.Errorf("unsupported state format version %d", saved.FormatVersion)
	}
	if saved.State == nil {
		return model.State{}, errors.New("load state: missing state")
	}
	if saved.FormatVersion == 1 {
		migrateWorktrees(saved.State)
	}
	if saved.FormatVersion < 3 {
		if err := migrateTabs(data, saved.State); err != nil {
			return model.State{}, err
		}
	}
	if saved.FormatVersion < 4 {
		migrateNameSet(saved.State)
	}
	if saved.FormatVersion < 5 {
		splitTabs(saved.State)
	}
	if saved.FormatVersion < 6 {
		saved.State.Order = saved.State.TopOrder() // ungrouped tabs, then groups
		for i := range saved.State.Workspaces {
			if w := &saved.State.Workspaces[i]; !w.NameSet {
				w.Name = ""
			}
		}
	}
	return *saved.State, nil
}

// RestoreCmd is the argv that brings a pane back: a resumed agent session
// when one is known, otherwise the original command.
// Adapted from tuios (MIT): internal/session/agent_resume.go
func RestoreCmd(p model.Pane) []string {
	if p.SessionID == "" || (p.Provider != model.ProviderClaude && p.Provider != model.ProviderCodex) {
		return p.Cmd
	}
	binary := string(p.Provider)
	var args []string
	// Hooks identify the agent inside a pane, not the command that launched it.
	// Reusing a shell or wrapper can execute the original script again.
	if len(p.Cmd) > 0 && filepath.Base(p.Cmd[0]) == binary {
		binary, args = p.Cmd[0], p.Cmd[1:]
	}
	cmd := []string{binary}
	if p.Provider == model.ProviderClaude {
		for i := 0; i < len(args); i++ {
			flag, _, attached := strings.Cut(args[i], "=")
			switch flag {
			case "--resume", "-r":
				if !attached && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
				}
			case "--continue", "-c":
			default:
				cmd = append(cmd, args[i])
			}
		}
		return append(cmd, "--resume", p.SessionID)
	}
	cmd = append(cmd, "resume")
	// codex resume accepts these options from codex --help. Positional
	// prompts and old resume selectors must not select a different session.
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		flag, _, attached := strings.Cut(args[i], "=")
		switch flag {
		case "-c", "--config", "-m", "--model", "-p", "--profile",
			"-s", "--sandbox", "-a", "--ask-for-approval", "-C", "--cd",
			"--add-dir", "--enable", "--disable", "--local-provider",
			"--remote", "--remote-auth-token-env":
			cmd = append(cmd, args[i])
			if !attached && i+1 < len(args) {
				i++
				cmd = append(cmd, args[i])
			}
		case "--oss", "--approve-for-me", "--dangerously-bypass-approvals-and-sandbox",
			"--dangerously-bypass-hook-trust", "--search", "--no-alt-screen",
			"--no-daemon", "--strict-config", "--worktree":
			cmd = append(cmd, args[i])
		case "-i", "--image":
			// Images belong to the initial prompt, not the restored session.
			if !attached {
				for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
				}
			}
		default:
			// Clap also accepts attached short-option values, such as -mMODEL.
			if len(args[i]) > 2 && args[i][0] == '-' && strings.IndexByte("cmpsaC", args[i][1]) >= 0 {
				cmd = append(cmd, args[i])
			}
		}
	}
	return append(cmd, p.SessionID)
}

// migrateWorktrees marks the worktrees a version 1 daemon created. Every
// version 1 workspace belonged to the project it was made in, and the only
// ones under <root>/.worktrees/ came from AddWorktree. From version 2 on the
// daemon records ownership itself, so this never runs again: a hand-made
// worktree grouped in later stays unowned across restarts.
func migrateWorktrees(s *model.State) {
	roots := map[string]string{}
	for _, p := range s.Projects {
		if p.Kind == model.ProjectGit {
			roots[p.ID] = p.Root
		}
	}
	for i, w := range s.Workspaces {
		if root, ok := roots[w.ProjectID]; ok && filepath.Dir(w.Path) == filepath.Join(root, ".worktrees") {
			s.Workspaces[i].WorktreeRoot = root
		}
	}
}

// migrateTabs moves each version 2 workspace's Layout into a first tab and
// turns Archived into Detached. Those fields left model.Workspace, so they
// are read from the raw file.
func migrateTabs(data []byte, s *model.State) error {
	var old struct {
		State struct {
			Workspaces []struct {
				Layout   *layout.Node
				Archived bool
			}
		}
	}
	if err := json.Unmarshal(data, &old); err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	for i := range s.Workspaces {
		w := &s.Workspaces[i]
		o := old.State.Workspaces[i]
		w.Tabs = []model.Tab{{ID: newID(), Layout: o.Layout}}
		w.ActiveTab, w.Detached = w.Tabs[0].ID, o.Archived
	}
	return nil
}

// migrateNameSet marks every name pitwall did not generate as chosen. Older
// files did not record it; generated names are adjective-noun pairs,
// "workspace-N" from NewWorkspace, or a project's folder name.
func migrateNameSet(s *model.State) {
	for i := range s.Workspaces {
		w := &s.Workspaces[i]
		n, numbered := strings.CutPrefix(w.Name, "workspace-")
		if numbered {
			_, err := strconv.Atoi(n)
			numbered = err == nil
		}
		w.NameSet = !model.IsSessionName(w.Name) && !numbered && w.Name != filepath.Base(w.Path)
	}
}

// splitTabs makes each tab of an older workspace a workspace of its own, in
// the workspace's place and group, because the user now sees one workspace
// as one tab. The first keeps the workspace's ID and name, the others get new
// IDs and generated names; a tab's Name becomes its workspace's chosen name.
// Tabs without panes go, and so do workspaces left without tabs and panes in
// no tab.
func splitTabs(s *model.State) {
	taken := map[string]bool{}
	for _, w := range s.Workspaces {
		taken[w.Name] = true
	}
	owner := map[string]string{} // pane: workspace
	var out []model.Workspace
	for _, w := range s.Workspaces {
		first := true
		for _, t := range w.Tabs {
			if t.Layout == nil {
				continue
			}
			nw := w
			if !first {
				nw.ID, nw.NameSet, nw.WorktreeRoot = newID(), false, ""
				for try := 0; nw.Name == w.Name || taken[nw.Name]; try++ {
					nw.Name = model.SessionName(mrand.IntN, try)
				}
			}
			if t.Name != "" && t.Name != nw.Name {
				nw.Name, nw.NameSet = t.Name, true
				for n := 2; taken[nw.Name]; n++ {
					nw.Name = t.Name + "-" + strconv.Itoa(n)
				}
			}
			taken[nw.Name], first = true, false
			t.Name = ""
			nw.Tabs, nw.ActiveTab = []model.Tab{t}, t.ID
			for _, p := range layout.Panes(t.Layout) {
				owner[p] = nw.ID
			}
			out = append(out, nw)
		}
	}
	s.Workspaces = out
	var panes []model.Pane
	for _, p := range s.Panes {
		if id, ok := owner[p.ID]; ok {
			p.WorkspaceID = id
			panes = append(panes, p)
		}
	}
	s.Panes = panes
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
