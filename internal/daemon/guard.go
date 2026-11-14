package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
)

// errUnguarded marks a guarded pane that did not start because the guard
// could not hold in it.
var errUnguarded = errors.New("guarded tab not started")

// guardGate refuses a pane whose environment turns the guard on when the
// guard would not run in it: its program is not Claude Code or Codex, or
// pitwall's PreToolUse hook is missing from the config dir that program
// reads. env is what the pane gets on top of the daemon's own environment,
// so a later entry wins, as it does for the process.
func guardGate(cmd, env []string, cwd string) error {
	get := func(k string) string {
		for i := len(env) - 1; i >= 0; i-- {
			name, v, ok := strings.Cut(env[i], "=")
			if ok && (name == k || runtime.GOOS == "windows" && strings.EqualFold(name, k)) {
				return v
			}
		}
		return os.Getenv(k)
	}
	if get(guard.Env) == "" {
		return nil // the hook guards only while it is set, to anything
	}
	program, provider := "a shell", model.Provider("")
	if len(cmd) > 0 {
		program, provider = filepath.Base(cmd[0]), agent.Identify("", cmd[0])
	}
	homeKey := "HOME"
	if runtime.GOOS == "windows" {
		homeKey = "USERPROFILE"
	}
	home := get(homeKey)
	var key, dir string
	switch provider {
	case model.ProviderClaude:
		key, dir = "CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")
		if get("CLAUDE_CODE_SIMPLE") != "" {
			return fmt.Errorf("%w: CLAUDE_CODE_SIMPLE skips Claude Code's hooks; unset it to use the guard", errUnguarded)
		}
		if err := guardClaudeArgs(cmd[1:]); err != nil {
			return fmt.Errorf("%w: %v", errUnguarded, err)
		}
	case model.ProviderCodex:
		key, dir = "CODEX_HOME", filepath.Join(home, ".codex")
	default:
		return fmt.Errorf("%w: the guard covers Claude Code and Codex, not %s", errUnguarded, program)
	}
	if v := get(key); v != "" {
		dir = v
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	if err := agent.GuardHooked(provider, dir); err != nil {
		return fmt.Errorf("%w: %v; run %s=%s pitwall hooks install", errUnguarded, err, key, dir)
	}
	return nil
}

// GuardHooked checks user settings. Refuse launch options that skip those
// settings or can replace them, rather than validating a file Claude will
// ignore. Stop at -- so a literal prompt is not treated as an option.
func guardClaudeArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		flag, value, inline := strings.Cut(args[i], "=")
		switch flag {
		case "--bare", "--safe-mode", "--settings":
			return fmt.Errorf("cannot verify Claude Code's guard with %s; remove it to use the guard", flag)
		case "--setting-sources":
			if !inline && i+1 < len(args) {
				i++
				value = args[i]
			}
			user := false
			for _, source := range strings.Split(value, ",") {
				user = user || strings.TrimSpace(source) == "user"
			}
			if !user {
				return fmt.Errorf("--setting-sources must include user to load pitwall's guard hook")
			}
		}
	}
	return nil
}
