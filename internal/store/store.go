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
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// formatVersion 7 added sessions, which own the tabs, groups and order;
// formatVersion 6 added State.Order and dropped generated tab names;
// formatVersion 5 made each tab a workspace of its own; version 4 added Workspace.NameSet and Label and Pane.Prompt; version
// 3 moved Workspace.Layout into Tabs and renamed Archived to Detached;
// version 2 added Workspace.WorktreeRoot. Older files are migrated once on
// load.
const formatVersion = 7

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
	// Windows can't fsync a directory (Access is denied); NTFS journals the rename.
	if runtime.GOOS == "windows" {
		return nil
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
		// No order yet: the session's TopOrder implies the old one,
		// ungrouped tabs, then groups.
		for i := range saved.State.Workspaces {
			if w := &saved.State.Workspaces[i]; !w.NameSet {
				w.Name = ""
			}
		}
	}
	if saved.FormatVersion < 7 {
		if err := migrateSessions(data, saved.FormatVersion, saved.State); err != nil {
			return model.State{}, err
		}
	}
	return *saved.State, nil
}

// migrateSessions puts every tab and group of an older file into one
// session named "main", which takes over the file's top-level order.
// State.Order left model.State, so it is read from the raw file.
func migrateSessions(data []byte, version int, s *model.State) error {
	if len(s.Workspaces) == 0 && len(s.Projects) == 0 {
		return nil
	}
	var old struct {
		State struct{ Order []string }
	}
	if version >= 6 {
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("load state: %w", err)
		}
	}
	main := model.Session{ID: newID(), Name: "main", Order: old.State.Order}
	for _, w := range s.Workspaces {
		if w.UpdatedAt.After(main.UsedAt) {
			main.UsedAt = w.UpdatedAt
		}
	}
	for i := range s.Workspaces {
		s.Workspaces[i].SessionID = main.ID
	}
	for i := range s.Projects {
		s.Projects[i].SessionID = main.ID
	}
	s.Sessions = []model.Session{main}
	return nil
}

// RestoreCmd is the argv that brings a pane back: a resumed agent session
// when one is known, otherwise the original command. A resumed agent keeps
// the permission mode its hooks last reported (Pane.AgentMode) unless its
// command already sets its permissions.
// Adapted from tuios (MIT): internal/session/agent_resume.go
func RestoreCmd(p model.Pane) []string {
	if p.SessionID == "" || (p.Provider != model.ProviderClaude && p.Provider != model.ProviderCodex && p.Provider != model.ProviderPi) {
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
		cmd = claudeFlags(cmd, args)
		// "default" is passed too: settings may make bypass the default
		// mode, which the user had left.
		if !hasFlag(cmd[1:], "--dangerously-skip-permissions", "--permission-mode") {
			switch p.AgentMode {
			case "bypassPermissions":
				cmd = append(cmd, "--dangerously-skip-permissions")
			case "default", "acceptEdits", "plan", "dontAsk":
				cmd = append(cmd, "--permission-mode", p.AgentMode)
			}
		}
		return append(cmd, "--resume", p.SessionID)
	}
	if p.Provider == model.ProviderPi {
		return append(piFlags(cmd, args), "--session", p.SessionID)
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
			if !attached && (i+1 >= len(args) || args[i+1] == "--") {
				break // the value flag would take the session id
			}
			cmd = append(cmd, args[i])
			if !attached {
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
	// Codex reports Claude's mode names. Only the full bypass has one codex
	// flag; the others depend on its config, which the command already
	// carries.
	if kept := cmd[2:]; p.AgentMode == "bypassPermissions" && !hasFlag(kept, "--dangerously-bypass-approvals-and-sandbox", "-s", "--sandbox", "-a", "--ask-for-approval") &&
		!slices.ContainsFunc(kept, func(a string) bool { return len(a) > 2 && (a[:2] == "-s" || a[:2] == "-a") }) {
		cmd = append(cmd, "--dangerously-bypass-approvals-and-sandbox")
	}
	return append(cmd, p.SessionID)
}

// claudeOpt is one option of `claude --help`: the values it takes, '1' one,
// '+' one or more, '?' an optional one, 0 none, and whether a resumed
// session keeps it.
type claudeOpt struct {
	values byte
	keep   bool
}

// claudeOptions are the options of `claude --help` (2.1.289). A resume drops
// session selectors, print mode and its options, and one-off actions such as
// --worktree and --bg.
var claudeOptions = map[string]claudeOpt{
	"--add-dir": {'+', true}, "--agent": {'1', true}, "--agents": {'1', true},
	"--allow-dangerously-skip-permissions": {0, true}, "--allowedTools": {'+', true}, "--allowed-tools": {'+', true},
	"--append-system-prompt": {'1', true}, "--autocompact": {'1', true}, "--ax-screen-reader": {0, true},
	"--bare": {0, true}, "--betas": {'+', true}, "--brief": {0, true}, "--chrome": {0, true},
	"--dangerously-skip-permissions": {0, true}, "-d": {'?', true}, "--debug": {'?', true}, "--debug-file": {'1', true},
	"--disable-slash-commands": {0, true}, "--disallowedTools": {'+', true}, "--disallowed-tools": {'+', true},
	"--effort": {'1', true}, "--exclude-dynamic-system-prompt-sections": {0, true}, "--fallback-model": {'1', true},
	"--ide": {0, true}, "--mcp-config": {'+', true}, "--model": {'1', true}, "--no-chrome": {0, true},
	"--permission-mode": {'1', true}, "--plugin-dir": {'1', true}, "--plugin-url": {'1', true},
	"--remote-control": {'?', true}, "--remote-control-session-name-prefix": {'1', true}, "--restricted": {0, true},
	"--safe-mode": {0, true}, "--setting-sources": {'1', true}, "--settings": {'1', true},
	"--strict-mcp-config": {0, true}, "--system-prompt": {'1', true}, "--system-prompt-snapshot": {'1', true},
	"--tools": {'+', true}, "--verbose": {0, true},

	"--bg": {}, "--background": {}, "--cloud": {'?', false}, "-c": {}, "--continue": {}, "--desktop": {},
	"--environment": {'1', false}, "--file": {'+', false}, "--fork-session": {}, "--forward-subagent-text": {},
	"--from-pr": {'?', false}, "-h": {}, "--help": {}, "--include-hook-events": {}, "--include-partial-messages": {},
	"--input-format": {'1', false}, "--json-schema": {'1', false}, "--max-budget-usd": {'1', false},
	"-n": {'1', false}, "--name": {'1', false}, "--no-session-persistence": {}, "--output-format": {'1', false},
	"--permission-prompts": {'1', false}, "-p": {}, "--print": {}, "--prompt-suggestions": {'?', false},
	"--replay-user-messages": {}, "-r": {'?', false}, "--resume": {'?', false}, "--session-id": {'1', false},
	"--teleport": {'?', false}, "--tmux": {}, "-v": {}, "--version": {}, "-w": {'?', false}, "--worktree": {'?', false},
}

// claudeFlags appends to cmd the options of claude's args that a resumed
// session keeps, with their values, parsed as claude's commander parser
// does. Prompts, dropped options and options claude does not document go;
// so does a value option missing its value, which would take --resume.
func claudeFlags(cmd, args []string) []string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break // a prompt follows, and after it flags would be prompt text
		}
		flag, _, attached := strings.Cut(args[i], "=")
		opt, known := claudeOptions[flag]
		if !known {
			continue // a prompt, or an option claude does not document
		}
		start := i
		switch opt.values {
		case '1', '+':
			if !attached {
				if i+1 >= len(args) || args[i+1] == "--" {
					continue
				}
				i++
			}
			for opt.values == '+' && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		case '?':
			if !attached && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		}
		if opt.keep {
			cmd = append(cmd, args[start:i+1]...)
		}
	}
	return cmd
}

// hasFlag reports whether the options of args, before any "--", include
// one of flags, alone or with an attached value.
func hasFlag(args []string, flags ...string) bool {
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[:i]
	}
	return slices.ContainsFunc(args, func(a string) bool {
		f, _, _ := strings.Cut(a, "=")
		return slices.Contains(flags, f)
	})
}

// piFlags appends to cmd the options of pi's args that a resumed session
// keeps, from pi's CLI docs. Session selectors, prompts, print and output
// modes, and options pi does not document (an extension's flags, whose
// values cannot be told from prompts) are dropped.
func piFlags(cmd, args []string) []string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		flag, _, attached := strings.Cut(args[i], "=")
		switch flag {
		case "--provider", "--model", "--api-key", "--thinking", "--models",
			"--session-dir", "-t", "--tools", "-xt", "--exclude-tools",
			"-e", "--extension", "--skill", "--prompt-template", "--theme",
			"--use-theme", "--system-prompt", "--append-system-prompt", "--tui-mode":
			// why: a value flag missing its value would take the --session that follows.
			if !attached && (i+1 >= len(args) || args[i+1] == "--") {
				break
			}
			cmd = append(cmd, args[i])
			if !attached {
				i++
				cmd = append(cmd, args[i])
			}
		case "-nbt", "--no-builtin-tools", "-nt", "--no-tools", "-ne", "--no-extensions",
			"-ns", "--no-skills", "-np", "--no-prompt-templates", "--no-themes",
			"-nc", "--no-context-files", "--verbose", "-a", "--approve",
			"-na", "--no-approve", "--offline":
			cmd = append(cmd, args[i])
		case "--session", "--session-id", "--fork", "-n", "--name", "--mode":
			if !attached && i+1 < len(args) {
				i++
			}
		}
	}
	return cmd
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
