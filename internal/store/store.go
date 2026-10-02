// Package store saves daemon state to disk and plans how to bring panes back
// after a restart.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// formatVersion 2 added Workspace.WorktreeRoot; version 1 files are
// migrated once on load.
const formatVersion = 2

type snapshot struct {
	FormatVersion int          `json:"format_version"`
	State         *model.State `json:"state"`
}

// Path is $XDG_STATE_HOME/pitwall/state.json.
func Path() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "pitwall", "state.json")
}

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
	if saved.FormatVersion != 1 && saved.FormatVersion != formatVersion {
		return model.State{}, fmt.Errorf("unsupported state format version %d", saved.FormatVersion)
	}
	if saved.State == nil {
		return model.State{}, errors.New("load state: missing state")
	}
	if saved.FormatVersion == 1 {
		migrateWorktrees(saved.State)
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
